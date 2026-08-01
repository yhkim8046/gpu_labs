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
