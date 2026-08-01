package deviceplugin

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const SocketDirectory = "/var/lib/kubelet/device-plugins"

type Plugin struct {
	pluginapi.UnimplementedDevicePluginServer
	ResourceName string
	NodeName     string
	GPUCount     int
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
	return &Plugin{ResourceName: resourceName, NodeName: nodeName, GPUCount: gpuCount}
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
	if err := os.MkdirAll(SocketDirectory, 0o755); err != nil {
		return err
	}
	endpoint := filepath.Join(SocketDirectory, "gpu-lab.sock")
	_ = os.Remove(endpoint)
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return fmt.Errorf("listen on device plugin socket: %w", err)
	}
	server := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(server, p)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	go p.registrationLoop(ctx, endpoint)
	select {
	case <-ctx.Done():
		server.GracefulStop()
		_ = os.Remove(endpoint)
		return nil
	case err := <-serveErr:
		return err
	}
}

func (p *Plugin) registrationLoop(ctx context.Context, endpoint string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := p.register(ctx, endpoint); err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Plugin) register(ctx context.Context, endpoint string) error {
	kubeletSocket := filepath.Join(SocketDirectory, "kubelet.sock")
	if _, err := os.Stat(kubeletSocket); err != nil {
		return err
	}
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
