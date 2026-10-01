package scenario

import (
	"strings"
	"testing"
)

func TestBuiltinScenarios(t *testing.T) {
	names, err := ListBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ecc-double-bit", "exporter-down", "gpu-allocated-idle", "gpu-capacity-mismatch", "gpu-fragmentation", "gpu-idle", "gpu-util-high", "ib-congestion", "ib-link-down", "ib-rate-degraded", "ib-symbol-errors", "node-selector-mismatch", "normal", "pcie-replay", "power-throttle", "rdma-retry-storm", "scheduling-failure", "thermal-escalation", "thermal-throttling", "vram-pressure", "xid-48", "xid-79"}
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

func TestParseRejectsUnknownYAMLAndJSONFields(t *testing.T) {
	cases := map[string]string{
		"unknown YAML spec field": `apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata: {name: typo}
spec:
  timline: []
`,
		"unknown YAML timeline field": `apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata: {name: typo}
spec:
  timeline:
    - phaze: baseline
      at: 0s
`,
		"unknown YAML metric field": `apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata: {name: typo}
spec:
  metrics:
    temperatur_celsius: 80
`,
		"unknown JSON field": `{"apiVersion":"gpu-lab.io/v1alpha1","kind":"Scenario","metadata":{"name":"typo"},"spec":{"unexpected":true}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "field") {
				t.Fatalf("Parse() error = %v, want strict unknown-field error", err)
			}
		})
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
