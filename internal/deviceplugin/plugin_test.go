package deviceplugin

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestAllocateReturnsOnlyLabMetadata(t *testing.T) {
	p := New("nvidia.com/gpu", "gpu-node-01", 8)
	response, err := p.Allocate(context.Background(), &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIds: []string{"gpu-0"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ContainerResponses) != 1 {
		t.Fatalf("responses = %d", len(response.ContainerResponses))
	}
	container := response.ContainerResponses[0]
	if container.Envs["GPU_LAB_ALLOCATED_IDS"] != "gpu-0" {
		t.Fatalf("allocated ids = %q", container.Envs["GPU_LAB_ALLOCATED_IDS"])
	}
	if len(container.Devices) != 0 || len(container.Mounts) != 0 {
		t.Fatal("fake plugin must not expose real devices or mounts")
	}
}

type fakeKubelet struct {
	pluginapi.UnimplementedRegistrationServer
	directory      string
	listener       net.Listener
	server         *grpc.Server
	failures       int32
	openStreams    bool
	streamDelay    time.Duration
	registrations  atomic.Int32
	streamStarts   atomic.Int32
	activeStreams  atomic.Int32
	maximumStreams atomic.Int32
	done           chan struct{}
	stopOnce       sync.Once
	clientMu       sync.Mutex
	clientCancels  []context.CancelFunc
}

func startFakeKubelet(t *testing.T, directory string, failures int32, openStreams bool) *fakeKubelet {
	t.Helper()
	socket := filepath.Join(directory, "kubelet.sock")
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen for fake kubelet: %v", err)
	}
	kubelet := &fakeKubelet{
		directory:   directory,
		listener:    listener,
		server:      grpc.NewServer(),
		failures:    failures,
		openStreams: openStreams,
		done:        make(chan struct{}),
	}
	pluginapi.RegisterRegistrationServer(kubelet.server, kubelet)
	go func() { _ = kubelet.server.Serve(listener) }()
	t.Cleanup(kubelet.stop)
	return kubelet
}

func (k *fakeKubelet) Register(_ context.Context, request *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	call := k.registrations.Add(1)
	if call <= k.failures {
		return nil, status.Error(codes.Unavailable, "temporary fake kubelet registration failure")
	}
	if k.openStreams {
		k.streamStarts.Add(1)
		go k.openPluginStream(filepath.Join(k.directory, request.Endpoint))
	}
	return &pluginapi.Empty{}, nil
}

func (k *fakeKubelet) openPluginStream(endpoint string) {
	if k.streamDelay > 0 {
		timer := time.NewTimer(k.streamDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-k.done:
			return
		}
	}
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	defer cancelDial()
	conn, err := grpc.DialContext(dialCtx, endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", endpoint)
		}),
		grpc.WithBlock(),
	)
	if err != nil {
		return
	}
	defer conn.Close()
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	k.clientMu.Lock()
	k.clientCancels = append(k.clientCancels, cancelStream)
	k.clientMu.Unlock()
	stream, err := pluginapi.NewDevicePluginClient(conn).ListAndWatch(streamCtx, &pluginapi.Empty{})
	if err != nil {
		return
	}
	if _, err := stream.Recv(); err != nil {
		return
	}
	active := k.activeStreams.Add(1)
	for current := k.maximumStreams.Load(); active > current; current = k.maximumStreams.Load() {
		if k.maximumStreams.CompareAndSwap(current, active) {
			break
		}
	}
	defer k.activeStreams.Add(-1)
	_, _ = stream.Recv() // Keep reading so a server-side stop is observed promptly.
}

func (k *fakeKubelet) stop() {
	k.stopOnce.Do(func() {
		close(k.done)
		k.clientMu.Lock()
		for _, cancel := range k.clientCancels {
			cancel()
		}
		k.clientMu.Unlock()
		k.server.Stop()
		_ = k.listener.Close()
		_ = os.Remove(filepath.Join(k.directory, "kubelet.sock"))
	})
}

func (k *fakeKubelet) disconnectStreams() {
	k.clientMu.Lock()
	cancels := append([]context.CancelFunc(nil), k.clientCancels...)
	k.clientMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func startTestPlugin(t *testing.T, directory string) (*Plugin, context.CancelFunc, <-chan error) {
	t.Helper()
	plugin := New("nvidia.com/gpu", "test-node", 2)
	plugin.socketDirectory = directory
	plugin.pollInterval = 5 * time.Millisecond
	plugin.retryInitial = 10 * time.Millisecond
	plugin.retryMaximum = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- plugin.Serve(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("device plugin did not stop during cleanup")
		}
	})
	waitFor(t, time.Second, func() bool {
		_, err := os.Lstat(filepath.Join(directory, "gpu-lab.sock"))
		return err == nil
	}, "plugin socket to appear")
	return plugin, cancel, done
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func shortSocketDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "dp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func TestServeRegistersOnceWhileKubeletAndSocketsStayStable(t *testing.T) {
	directory := shortSocketDirectory(t)
	kubelet := startFakeKubelet(t, directory, 0, true)
	_, _, _ = startTestPlugin(t, directory)
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 1 }, "one ListAndWatch stream")

	// This spans dozens of lifecycle polling cycles at the test poll interval.
	time.Sleep(250 * time.Millisecond)
	if got := kubelet.registrations.Load(); got != 1 {
		t.Fatalf("registration calls = %d, want 1 while sockets remain stable", got)
	}
	if got := kubelet.streamStarts.Load(); got != 1 {
		t.Fatalf("ListAndWatch starts = %d, want 1", got)
	}
	if got := kubelet.activeStreams.Load(); got != 1 {
		t.Fatalf("active ListAndWatch streams = %d, want 1", got)
	}
	if got := kubelet.maximumStreams.Load(); got != 1 {
		t.Fatalf("maximum concurrent ListAndWatch streams = %d, want 1", got)
	}
}

func TestServeDoesNotReregisterBeforeDelayedFirstListAndWatch(t *testing.T) {
	directory := shortSocketDirectory(t)
	kubelet := startFakeKubelet(t, directory, 0, true)
	kubelet.streamDelay = 200 * time.Millisecond
	_, _, _ = startTestPlugin(t, directory)
	waitFor(t, time.Second, func() bool { return kubelet.registrations.Load() == 1 }, "successful registration before delayed stream")

	time.Sleep(80 * time.Millisecond)
	if got := kubelet.registrations.Load(); got != 1 {
		t.Fatalf("registration calls before first stream = %d, want 1", got)
	}
	if got := kubelet.activeStreams.Load(); got != 0 {
		t.Fatalf("active streams before configured delay elapsed = %d, want 0", got)
	}
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 1 }, "delayed first ListAndWatch stream")
	time.Sleep(100 * time.Millisecond)
	if got := kubelet.registrations.Load(); got != 1 {
		t.Fatalf("registration calls after delayed stream starts = %d, want 1", got)
	}
	if got := kubelet.streamStarts.Load(); got != 1 {
		t.Fatalf("ListAndWatch starts = %d, want 1", got)
	}
}

func TestServeReregistersAfterListAndWatchDisconnect(t *testing.T) {
	directory := shortSocketDirectory(t)
	kubelet := startFakeKubelet(t, directory, 0, true)
	_, _, _ = startTestPlugin(t, directory)
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 1 }, "initial ListAndWatch stream")

	kubelet.disconnectStreams()
	waitFor(t, time.Second, func() bool {
		return kubelet.registrations.Load() == 2 && kubelet.activeStreams.Load() == 1
	}, "re-registration after the stream disconnects")
	time.Sleep(100 * time.Millisecond)
	if got := kubelet.registrations.Load(); got != 2 {
		t.Fatalf("registrations after stream recovery = %d, want exactly 2", got)
	}
	if got := kubelet.maximumStreams.Load(); got != 1 {
		t.Fatalf("maximum concurrent streams = %d, want 1", got)
	}
}

func TestServeRetriesFailedRegistrationWithBoundedBackoff(t *testing.T) {
	directory := shortSocketDirectory(t)
	kubelet := startFakeKubelet(t, directory, 3, true)
	_, _, _ = startTestPlugin(t, directory)
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 1 }, "registration to succeed after failures")
	time.Sleep(100 * time.Millisecond)
	if got := kubelet.registrations.Load(); got != 4 {
		t.Fatalf("registration calls = %d, want 4 (three failures then one success)", got)
	}
	if got := kubelet.streamStarts.Load(); got != 1 {
		t.Fatalf("ListAndWatch starts = %d, want 1 after registration succeeds", got)
	}
}

func TestServeRecoversFromMissingAndReplacedSockets(t *testing.T) {
	directory := shortSocketDirectory(t)
	_, _, _ = startTestPlugin(t, directory)
	pluginSocket := filepath.Join(directory, "gpu-lab.sock")
	time.Sleep(30 * time.Millisecond)
	if _, err := os.Lstat(filepath.Join(directory, "kubelet.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubelet socket should remain absent before startup, stat error = %v", err)
	}

	kubelet := startFakeKubelet(t, directory, 0, true)
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 1 }, "registration after kubelet socket appears")
	oldPluginIdentity, err := identifySocket(pluginSocket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pluginSocket); err != nil {
		t.Fatalf("delete plugin socket: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		identity, err := identifySocket(pluginSocket)
		return err == nil && !identity.sameAs(oldPluginIdentity) && kubelet.registrations.Load() >= 2 && kubelet.activeStreams.Load() == 1
	}, "plugin listener recreation and re-registration after socket deletion")
	time.Sleep(100 * time.Millisecond)
	if got := kubelet.registrations.Load(); got != 2 {
		t.Fatalf("registrations after plugin socket recovery = %d, want exactly 2", got)
	}

	oldPluginIdentity, err = identifySocket(pluginSocket)
	if err != nil {
		t.Fatal(err)
	}
	kubelet.stop()
	time.Sleep(30 * time.Millisecond) // Let the plugin observe the absent kubelet socket.
	replacement := startFakeKubelet(t, directory, 0, true)
	waitFor(t, time.Second, func() bool {
		return replacement.registrations.Load() >= 1 && replacement.activeStreams.Load() == 1
	}, "re-registration after kubelet socket replacement")
	identity, err := identifySocket(pluginSocket)
	if err != nil {
		t.Fatal(err)
	}
	if identity.sameAs(oldPluginIdentity) {
		t.Fatal("plugin server generation was not recreated after kubelet socket replacement")
	}
	time.Sleep(100 * time.Millisecond)
	if got := replacement.registrations.Load(); got != 1 {
		t.Fatalf("registrations after kubelet socket replacement = %d, want exactly 1", got)
	}
}

func TestSocketIdentityIncludesModificationTime(t *testing.T) {
	socket := filepath.Join(shortSocketDirectory(t), "identity.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	before, err := identifySocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	changed := before.info.ModTime().Add(time.Second)
	if err := os.Chtimes(socket, changed, changed); err != nil {
		t.Fatal(err)
	}
	after, err := identifySocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before.info, after.info) {
		t.Fatal("test must retain the same device/inode")
	}
	if before.sameAs(after) {
		t.Fatal("a new timestamp was mistaken for the previous socket generation")
	}
}

func TestServeCancellationStopsActiveListAndWatchPromptly(t *testing.T) {
	directory := shortSocketDirectory(t)
	kubelet := startFakeKubelet(t, directory, 0, true)
	_, cancel, done := startTestPlugin(t, directory)
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 1 }, "active ListAndWatch stream")

	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error on cancellation: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Serve did not stop promptly with an active ListAndWatch stream")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown took %s, want at most 500ms", elapsed)
	}
	waitFor(t, time.Second, func() bool { return kubelet.activeStreams.Load() == 0 }, "ListAndWatch stream to close during shutdown")
}
