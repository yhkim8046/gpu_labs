package scenario

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestBuiltinScenarios(t *testing.T) {
	names, err := ListBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ecc-double-bit", "exporter-down", "gpu-allocated-idle", "gpu-capacity-mismatch", "gpu-fragmentation", "gpu-idle", "gpu-util-high", "ib-congestion", "ib-link-down", "ib-rate-degraded", "ib-symbol-errors", "node-selector-mismatch", "normal", "pcie-replay", "power-throttle", "rdma-retry-storm", "scheduling-failure", "thermal-throttling", "vram-pressure", "xid-48", "xid-79"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], want[i])
		}
		if _, err := LoadBuiltin(names[i]); err != nil {
			t.Fatalf("scenario %q failed to parse: %v", names[i], err)
		}
	}
}

func TestScenarioValidation(t *testing.T) {
	s, err := LoadBuiltin("xid-79")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "xid-79" || s.Spec.Metrics.XIDCode == nil || *s.Spec.Metrics.XIDCode != 79 {
		t.Fatalf("unexpected scenario: %#v", s)
	}
	if _, err := Parse([]byte(`apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata:
  name: bad
spec:
  metrics:
    gpu_utilization_percent: 101`)); err == nil {
		t.Fatal("expected invalid metric range")
	}
}

func TestScenarioGPUIndexTargets(t *testing.T) {
	valid := []byte(`apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata:
  name: one-gpu-hot
spec:
  duration: 30s
  targets:
    selector:
      gpu.lab/node-id: gpu-node-01
    gpu_indices: [1, 3]
  metrics:
    temperature_celsius: 91
`)
	parsed, err := Parse(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Spec.Targets.GPUIndices; len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("gpu_indices = %v, want [1 3]", got)
	}

	invalid := []string{
		"gpu_indices: [-1]",
		"gpu_indices: [1, 1]",
	}
	for _, targets := range invalid {
		data := []byte("apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: bad-target\nspec:\n  targets:\n    " + targets + "\n")
		if _, err := Parse(data); err == nil {
			t.Fatalf("Parse() accepted invalid target %q", targets)
		}
	}

	for _, duration := range []string{"0s", "-1s"} {
		data := []byte("apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: bad-duration\nspec:\n  duration: " + duration + "\n")
		if _, err := Parse(data); err == nil {
			t.Fatalf("Parse() accepted invalid duration %q", duration)
		}
	}

	emptySelector := []byte("apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: bad-selector\nspec:\n  targets:\n    selector:\n      gpu.lab/node-id: ''\n")
	if _, err := Parse(emptySelector); err == nil {
		t.Fatal("Parse() accepted an empty target selector value")
	}
}

func TestScenarioRejectsNodeWideStateForGPUIndexTarget(t *testing.T) {
	capacity := 4
	selected, err := LoadBuiltin("gpu-util-high")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Targets.GPUIndices = []int{0}
	selected.Spec.Metrics.GPUCapacity = &capacity
	if err := selected.Validate(); err == nil {
		t.Fatal("Validate() accepted node capacity with an individual GPU target")
	}

	down, err := LoadBuiltin("exporter-down")
	if err != nil {
		t.Fatal(err)
	}
	down.Spec.Targets.GPUIndices = []int{0}
	if err := down.Validate(); err == nil {
		t.Fatal("Validate() accepted exporter fault with an individual GPU target")
	}
}

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

func TestFabricMetricFieldsParseFromJSON(t *testing.T) {
	data := []byte(`{"apiVersion":"gpu-lab.io/v1alpha1","kind":"Scenario","metadata":{"name":"fabric-json"},"spec":{"metrics":{"ib_port_up":0,"ib_state":"DOWN","ib_physical_state":"DISABLED","ib_link_rate_gbps":25,"ib_tx_bytes_total":10,"ib_rx_bytes_total":20,"ib_symbol_errors_total":3,"ib_link_error_recovery_total":4,"ib_link_downed_total":5,"ib_xmit_discards_total":6,"ib_xmit_wait_total":7,"rdma_retries_total":8,"rdma_timeouts_total":9,"fabric_delay_seconds":1.5}}}`)
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	metrics := parsed.Spec.Metrics
	if metrics.IBPortUp == nil || *metrics.IBPortUp != 0 || metrics.IBState != "DOWN" || metrics.IBPhysicalState != "DISABLED" {
		t.Fatalf("parsed fabric state = %+v", metrics)
	}
	if metrics.IBLinkRateGbps == nil || *metrics.IBLinkRateGbps != 25 || metrics.FabricDelaySeconds == nil || *metrics.FabricDelaySeconds != 1.5 {
		t.Fatalf("parsed fabric gauges = %+v", metrics)
	}
	if metrics.RDMARetriesTotal == nil || *metrics.RDMARetriesTotal != 8 || metrics.RDMATimeoutsTotal == nil || *metrics.RDMATimeoutsTotal != 9 {
		t.Fatalf("parsed RDMA counters = %+v", metrics)
	}
}

func TestConfigMapAndPendingWorkloadJSON(t *testing.T) {
	s, err := LoadBuiltin("scheduling-failure")
	if err != nil {
		t.Fatal(err)
	}
	configMap, err := ConfigMapJSON(s, "42")
	if err != nil {
		t.Fatal(err)
	}
	var cm map[string]any
	if err := json.Unmarshal(configMap, &cm); err != nil {
		t.Fatal(err)
	}
	if cm["kind"] != "ConfigMap" {
		t.Fatalf("kind = %v", cm["kind"])
	}
	workload, err := PendingWorkloadJSON(s, 9)
	if err != nil {
		t.Fatal(err)
	}
	var pod map[string]any
	if err := json.Unmarshal(workload, &pod); err != nil {
		t.Fatal(err)
	}
	spec := pod["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)
	limits := container["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["nvidia.com/gpu"] != "9" {
		t.Fatalf("GPU limit = %v", limits["nvidia.com/gpu"])
	}
	requests := container["resources"].(map[string]any)["requests"].(map[string]any)
	if requests["nvidia.com/gpu"] != "9" {
		t.Fatalf("GPU request = %v", requests["nvidia.com/gpu"])
	}
}

func TestGPUWorkloadJSON(t *testing.T) {
	s, err := LoadBuiltin("node-selector-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	action, ok := HasAction(s, "create_gpu_workload")
	if !ok {
		t.Fatal("create_gpu_workload action missing")
	}
	data, err := GPUWorkloadJSON(s, action)
	if err != nil {
		t.Fatal(err)
	}
	var pod map[string]any
	if err := json.Unmarshal(data, &pod); err != nil {
		t.Fatal(err)
	}
	metadata := pod["metadata"].(map[string]any)
	if metadata["name"] != "gpu-lab-node-selector-mismatch" {
		t.Fatalf("name = %v", metadata["name"])
	}
	spec := pod["spec"].(map[string]any)
	selector := spec["nodeSelector"].(map[string]any)
	if selector["gpu.lab/node-id"] != "gpu-node-99" {
		t.Fatalf("node selector = %v", selector)
	}
}

func TestIncidentScenarioContracts(t *testing.T) {
	cases := map[string]func(MetricOverrides) bool{
		"ecc-double-bit": func(m MetricOverrides) bool { return m.ECCDbeTotal != nil && *m.ECCDbeTotal > 0 },
		"power-throttle": func(m MetricOverrides) bool {
			return m.ThrottleActive != nil && *m.ThrottleActive == 1 && m.PowerViolationTotal != nil
		},
		"pcie-replay":        func(m MetricOverrides) bool { return m.PCIeReplayTotal != nil && *m.PCIeReplayTotal > 0 },
		"gpu-allocated-idle": func(m MetricOverrides) bool { return m.GPUAllocatedCount != nil && *m.GPUAllocatedCount > 0 },
		"gpu-capacity-mismatch": func(m MetricOverrides) bool {
			return m.GPUCapacity != nil && m.GPUAllocatable != nil && *m.GPUCapacity != *m.GPUAllocatable
		},
		"ib-link-down": func(m MetricOverrides) bool {
			return m.IBPortUp != nil && *m.IBPortUp == 0 && m.IBState == "DOWN" && m.IBPhysicalState == "DISABLED" && m.IBLinkDownedTotal != nil && *m.IBLinkDownedTotal > 0
		},
		"ib-rate-degraded": func(m MetricOverrides) bool {
			return m.IBLinkRateGbps != nil && *m.IBLinkRateGbps == 25 && m.FabricDelaySeconds != nil && *m.FabricDelaySeconds == 1.5
		},
		"ib-symbol-errors": func(m MetricOverrides) bool {
			return m.IBSymbolErrorsTotal != nil && *m.IBSymbolErrorsTotal > 0 && m.IBLinkErrorRecoveryTotal != nil && *m.IBLinkErrorRecoveryTotal > 0
		},
		"rdma-retry-storm": func(m MetricOverrides) bool {
			return m.RDMARetriesTotal != nil && *m.RDMARetriesTotal > 0 && m.RDMATimeoutsTotal != nil && *m.RDMATimeoutsTotal > 0 && m.FabricDelaySeconds != nil && *m.FabricDelaySeconds == 1
		},
		"ib-congestion": func(m MetricOverrides) bool {
			return m.IBXmitWaitTotal != nil && *m.IBXmitWaitTotal > 0 && m.IBXmitDiscardsTotal != nil && *m.IBXmitDiscardsTotal > 0 && m.FabricDelaySeconds != nil && *m.FabricDelaySeconds == 0.8
		},
	}
	for name, valid := range cases {
		selected, err := LoadBuiltin(name)
		if err != nil {
			t.Fatal(err)
		}
		if !valid(selected.Spec.Metrics) {
			t.Fatalf("scenario %q does not contain its expected metric contract", name)
		}
	}
}
