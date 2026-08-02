package exporter

import (
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

func TestNormalMetricsHaveOneReadingPerGPU(t *testing.T) {
	m := NewModel("gpu-node-01", 2)
	metrics := m.Metrics()
	if got := strings.Count(metrics, "gpu_lab_gpu_utilization_percent{"); got != 2 {
		t.Fatalf("utilization series = %d, want 2", got)
	}
	if !strings.Contains(metrics, `gpu_lab_scenario_info{node="gpu-node-01",scenario="normal"} 1`) {
		t.Fatal("normal scenario info metric missing")
	}
}

func TestXIDScenarioAndExporterFault(t *testing.T) {
	m := NewModel("gpu-node-02", 1)
	xid, err := scenario.LoadBuiltin("xid-79")
	if err != nil {
		t.Fatal(err)
	}
	m.Apply(xid, "7")
	if !strings.Contains(m.Metrics(), "gpu_lab_gpu_xid_code{node=\"gpu-node-02\",gpu=\"gpu-node-02-00\"} 79") {
		t.Fatal("XID metric missing")
	}
	down, err := scenario.LoadBuiltin("exporter-down")
	if err != nil {
		t.Fatal(err)
	}
	m.Apply(down, "8")
	if !m.IsUnavailable() {
		t.Fatal("exporter fault was not applied")
	}
}

func TestMonitoringIncidentMetrics(t *testing.T) {
	m := NewModel("gpu-node-01", 2)
	selected, err := scenario.LoadBuiltin("power-throttle")
	if err != nil {
		t.Fatal(err)
	}
	m.Apply(selected, "9")
	metrics := m.Metrics()
	for _, expected := range []string{
		"gpu_lab_gpu_power_violation_total{node=\"gpu-node-01\",gpu=\"gpu-node-01-00\"} 25",
		"gpu_lab_gpu_throttle_active{node=\"gpu-node-01\",gpu=\"gpu-node-01-00\",reason=\"power_cap\"} 1",
		"gpu_lab_node_gpu_capacity{node=\"gpu-node-01\"} 8",
		"gpu_lab_node_gpu_allocatable{node=\"gpu-node-01\"} 8",
	} {
		if !strings.Contains(metrics, expected) {
			t.Fatalf("metrics missing %q", expected)
		}
	}

	selected, err = scenario.LoadBuiltin("gpu-capacity-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	m.Apply(selected, "10")
	metrics = m.Metrics()
	if !strings.Contains(metrics, "gpu_lab_node_gpu_allocatable{node=\"gpu-node-01\"} 4") {
		t.Fatal("capacity mismatch metric missing")
	}
}

func TestAllocatedIdleTargetsOnlySelectedNode(t *testing.T) {
	selected, err := scenario.LoadBuiltin("gpu-allocated-idle")
	if err != nil {
		t.Fatal(err)
	}
	primary := NewModel("gpu-lab-worker", 2)
	primary.Apply(selected, "11")
	if !strings.Contains(primary.Metrics(), `gpu_lab_gpu_allocated{node="gpu-lab-worker",gpu="gpu-lab-worker-00"} 1`) {
		t.Fatal("selected worker did not expose allocated GPU")
	}
	secondary := NewModel("gpu-lab-worker2", 2)
	secondary.Apply(selected, "11")
	if !strings.Contains(secondary.Metrics(), `gpu_lab_gpu_allocated{node="gpu-lab-worker2",gpu="gpu-lab-worker2-00"} 0`) {
		t.Fatal("non-selected worker exposed an allocated GPU")
	}
}
