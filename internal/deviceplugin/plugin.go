package deviceplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const SocketDirectory = "/var/lib/kubelet/device-plugins"

const (
	defaultPollInterval = time.Second
	defaultRetryInitial = time.Second
	defaultRetryMaximum = 30 * time.Second
)

var errRestartServer = errors.New("device plugin socket lifecycle changed")

type socketIdentity struct {
	info os.FileInfo
}

func (identity socketIdentity) sameAs(other socketIdentity) bool {
	// Linux may recycle an unlinked socket's inode immediately. Inode/device
	// equality alone can mistake a new server generation for the old socket.
	return identity.info != nil && other.info != nil && os.SameFile(identity.info, other.info) && identity.info.ModTime().Equal(other.info.ModTime())
}

type Plugin struct {
	pluginapi.UnimplementedDevicePluginServer
	ResourceName string
	NodeName     string
	GPUCount     int

	socketDirectory string
	pollInterval    time.Duration
	retryInitial    time.Duration
	retryMaximum    time.Duration
}

type streamTrackingServer struct {
	pluginapi.DevicePluginServer
	active atomic.Int32
	closed chan struct{}
}

func New(resourceName, nodeName string, gpuCount int) *Plugin {
	if resourceName == "" {
		resourceName = "nvidia.com/gpu"
	}
	if nodeName == "" {
		nodeName = "unknown-node"
	}
	if gpuCount <= 0 {
		gpuCount = 8
	}
	return &Plugin{
		ResourceName:    resourceName,
		NodeName:        nodeName,
		GPUCount:        gpuCount,
		socketDirectory: SocketDirectory,
	}
}

func (p *Plugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *Plugin) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices()}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

func (p *Plugin) Allocate(_ context.Context, request *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	responses := make([]*pluginapi.ContainerAllocateResponse, len(request.ContainerRequests))
	for i, container := range request.ContainerRequests {
		responses[i] = &pluginapi.ContainerAllocateResponse{Envs: map[string]string{
			"GPU_LAB_ALLOCATED_IDS": strings.Join(container.DevicesIds, ","),
			"GPU_LAB_RESOURCE_NAME": p.ResourceName,
		}}
	}
	return &pluginapi.AllocateResponse{ContainerResponses: responses}, nil
}

func (p *Plugin) devices() []*pluginapi.Device {
	devices := make([]*pluginapi.Device, 0, p.GPUCount)
	for i := 0; i < p.GPUCount; i++ {
		devices = append(devices, &pluginapi.Device{
			ID:     fmt.Sprintf("gpu-%s-%02d", p.NodeName, i),
			Health: pluginapi.Healthy,
		})
	}
	return devices
}

func (p *Plugin) Serve(ctx context.Context) error {
	directory := p.socketDir()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	endpoint := filepath.Join(directory, "gpu-lab.sock")

	for ctx.Err() == nil {
		listener, identity, err := p.listen(endpoint)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("listen on device plugin socket: %w", err)
		}

		server := grpc.NewServer()
		tracker := &streamTrackingServer{DevicePluginServer: p, closed: make(chan struct{}, 1)}
		pluginapi.RegisterDevicePluginServer(server, tracker)
		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()

		lifecycleErr := p.registrationLoop(ctx, endpoint, identity, tracker.closed, serveErr)
		server.Stop()
		_ = listener.Close()
		p.removeSocketIfOwned(endpoint, identity)

		if ctx.Err() != nil {
			return nil
		}
		if !errors.Is(lifecycleErr, errRestartServer) {
			return lifecycleErr
		}
	}
	return nil
}

func (p *Plugin) listen(endpoint string) (net.Listener, socketIdentity, error) {
	if info, err := os.Lstat(endpoint); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, socketIdentity{}, fmt.Errorf("existing endpoint %q is not a socket", endpoint)
		}
		if err := os.Remove(endpoint); err != nil {
			return nil, socketIdentity{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, socketIdentity{}, err
	}

	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, socketIdentity{}, err
	}
	identity, err := identifySocket(endpoint)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, socketIdentity{}, err
	}
	return listener, identity, nil
}

func (p *Plugin) registrationLoop(ctx context.Context, endpoint string, pluginIdentity socketIdentity, streamClosed <-chan struct{}, serveErr <-chan error) error {
	pollInterval := p.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	retryDelay := p.retryInitial
	if retryDelay <= 0 {
		retryDelay = defaultRetryInitial
	}
	retryMaximum := p.retryMaximum
	if retryMaximum <= 0 {
		retryMaximum = defaultRetryMaximum
	}
	if retryDelay > retryMaximum {
		retryDelay = retryMaximum
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastKubelet socketIdentity
	var sawKubelet, kubeletWasMissing, registered bool
	nextAttempt := time.Now()
	for {
		if ctx.Err() != nil {
			return nil
		}
		currentPlugin, err := identifySocket(endpoint)
		if err != nil || !currentPlugin.sameAs(pluginIdentity) {
			return errRestartServer
		}

		kubeletSocket := filepath.Join(p.socketDir(), "kubelet.sock")
		currentKubelet, err := identifySocket(kubeletSocket)
		if err != nil {
			if sawKubelet {
				kubeletWasMissing = true
			}
			registered = false
		} else {
			if sawKubelet && (kubeletWasMissing || !currentKubelet.sameAs(lastKubelet)) {
				return errRestartServer
			}
			lastKubelet = currentKubelet
			sawKubelet = true
			kubeletWasMissing = false
			if !registered && !time.Now().Before(nextAttempt) {
				if err := p.register(ctx, endpoint, kubeletSocket); err == nil {
					registered = true
					retryDelay = p.retryInitial
					if retryDelay <= 0 {
						retryDelay = defaultRetryInitial
					}
					if retryDelay > retryMaximum {
						retryDelay = retryMaximum
					}
				} else {
					nextAttempt = time.Now().Add(retryDelay)
					if retryDelay >= retryMaximum/2 {
						retryDelay = retryMaximum
					} else {
						retryDelay *= 2
					}
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case err := <-serveErr:
			if err == nil {
				return errors.New("device plugin gRPC server stopped unexpectedly")
			}
			return fmt.Errorf("serve device plugin gRPC server: %w", err)
		case <-streamClosed:
			if registered {
				registered = false
				nextAttempt = time.Now()
			}
		case <-ticker.C:
		}
	}
}

func (s *streamTrackingServer) ListAndWatch(request *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.active.Add(1)
	defer func() {
		if s.active.Add(-1) == 0 {
			select {
			case s.closed <- struct{}{}:
			default:
			}
		}
	}()
	return s.DevicePluginServer.ListAndWatch(request, stream)
}

func (p *Plugin) register(ctx context.Context, endpoint, kubeletSocket string) error {
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, kubeletSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", kubeletSocket)
		}),
		grpc.WithBlock(),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pluginapi.NewRegistrationClient(conn)
	registerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = client.Register(registerCtx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     filepath.Base(endpoint),
		ResourceName: p.ResourceName,
		Options:      &pluginapi.DevicePluginOptions{},
	})
	return err
}

func (p *Plugin) socketDir() string {
	if p.socketDirectory != "" {
		return p.socketDirectory
	}
	return SocketDirectory
}

func identifySocket(path string) (socketIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return socketIdentity{}, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return socketIdentity{}, fmt.Errorf("%q is not a Unix socket", path)
	}
	return socketIdentity{info: info}, nil
}

func (p *Plugin) removeSocketIfOwned(path string, identity socketIdentity) {
	current, err := identifySocket(path)
	if err == nil && current.sameAs(identity) {
		_ = os.Remove(path)
	}
}
