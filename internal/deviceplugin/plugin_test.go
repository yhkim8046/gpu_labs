package deviceplugin

import (
	"context"
	"testing"

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
