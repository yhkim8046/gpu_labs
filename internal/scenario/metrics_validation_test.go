package scenario

import (
	"fmt"
	"testing"
)

func TestFabricMetricValidation(t *testing.T) {
	base := "apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: fabric-test\nspec:\n  metrics:\n    %s\n"
	cases := []string{
		"ib_port_up: 2",
		"ib_state: BROKEN",
		"ib_physical_state: BROKEN",
		"ib_link_rate_gbps: 0",
		"ib_link_rate_gbps: -1",
		"ib_tx_bytes_total: -1",
		"ib_rx_bytes_total: -1",
		"ib_symbol_errors_total: -1",
		"ib_link_error_recovery_total: -1",
		"ib_link_downed_total: -1",
		"ib_xmit_discards_total: -1",
		"ib_xmit_wait_total: -1",
		"rdma_retries_total: -1",
		"rdma_timeouts_total: -1",
		"fabric_delay_seconds: -0.1",
	}
	for _, metric := range cases {
		if _, err := Parse([]byte(fmt.Sprintf(base, metric))); err == nil {
			t.Fatalf("Parse() accepted invalid fabric metric %q", metric)
		}
	}

	withGPUIndex := fmt.Sprintf("apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: fabric-gpu-target\nspec:\n  targets:\n    gpu_indices: [0]\n  metrics:\n    ib_port_up: 0\n")
	if _, err := Parse([]byte(withGPUIndex)); err == nil {
		t.Fatal("Parse() accepted node/port fabric override with gpu_indices")
	}
}
