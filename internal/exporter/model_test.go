package exporter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestScenarioSelectorOnlyAffectsMatchingSyntheticNode(t *testing.T) {
	selected, err := scenario.LoadBuiltin("gpu-util-high")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Targets.Selector = map[string]string{"gpu.lab/node-id": "gpu-node-01"}

	target := NewModel("gpu-node-01", 1)
	other := NewModel("gpu-node-02", 1)
	target.Apply(selected, "42")
	other.Apply(selected, "42")

	if got := target.Snapshot(); got.Scenario != selected.Name() || got.Generation != "42" || got.Readings[0].UtilizationPercent != 95 {
		t.Fatalf("target snapshot = %+v, want selected scenario at generation 42", got)
	}
	if got := other.Snapshot(); got.Scenario != "normal" || got.Generation != "42" || got.Readings[0].UtilizationPercent != defaultUtilization {
		t.Fatalf("non-target snapshot = %+v, want normal telemetry at generation 42", got)
	}
	if !strings.Contains(other.Metrics(), `gpu_lab_scenario_info{node="gpu-node-02",scenario="normal"} 1`) {
		t.Fatal("non-target scenario info was not normal")
	}
}

func TestScenarioSelectorSupportsKubernetesHostname(t *testing.T) {
	selected, err := scenario.LoadBuiltin("gpu-util-high")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Targets.Selector = map[string]string{"kubernetes.io/hostname": "gpu-node-02"}

	first := NewModel("gpu-node-01", 1)
	second := NewModel("gpu-node-02", 1)
	first.Apply(selected, "43")
	second.Apply(selected, "43")

	if got := first.Snapshot(); got.Scenario != "normal" || got.Readings[0].UtilizationPercent != defaultUtilization {
		t.Fatalf("hostname non-target snapshot = %+v, want normal telemetry", got)
	}
	if got := second.Snapshot(); got.Scenario != selected.Name() || got.Readings[0].UtilizationPercent != 95 {
		t.Fatalf("hostname target snapshot = %+v, want selected telemetry", got)
	}
}

func TestScenarioGPUIndicesOnlyAffectSelectedDevices(t *testing.T) {
	selected, err := scenario.LoadBuiltin("gpu-util-high")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Targets.GPUIndices = []int{1, 3}

	m := NewModel("gpu-node-01", 4)
	m.Apply(selected, "47")
	got := m.Snapshot()
	for index, reading := range got.Readings {
		want := defaultUtilization
		if index == 1 || index == 3 {
			want = 95
		}
		if reading.UtilizationPercent != want {
			t.Errorf("GPU %d utilization = %v, want %v", index, reading.UtilizationPercent, want)
		}
	}
}

func TestAllocatedCountUsesSelectedGPUIndices(t *testing.T) {
	selected, err := scenario.LoadBuiltin("gpu-allocated-idle")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Targets.GPUIndices = []int{2, 3}

	m := NewModel("gpu-lab-worker", 4)
	m.Apply(selected, "48")
	got := m.Snapshot()
	for index, reading := range got.Readings {
		want := 0
		if index == 2 {
			want = 1
		}
		if reading.Allocated != want {
			t.Errorf("GPU %d allocated = %d, want %d", index, reading.Allocated, want)
		}
	}
}

func TestScenarioSelectorLimitsExporterFault(t *testing.T) {
	down, err := scenario.LoadBuiltin("exporter-down")
	if err != nil {
		t.Fatal(err)
	}
	down.Spec.Targets.Selector = map[string]string{"gpu.lab/node-id": "gpu-node-01"}

	target := NewModel("gpu-node-01", 1)
	other := NewModel("gpu-node-02", 1)
	target.Apply(down, "46")
	other.Apply(down, "46")

	if !target.IsUnavailable() {
		t.Fatal("matching node did not enter exporter fault mode")
	}
	if other.IsUnavailable() {
		t.Fatal("non-target node entered exporter fault mode")
	}
	if got := other.Snapshot(); got.Scenario != "normal" || got.Generation != "46" || got.ExporterFault != "" {
		t.Fatalf("non-target fault snapshot = %+v, want normal generation 46", got)
	}
}

func TestScenarioDurationExpiresFromApplyAndKeepsGeneration(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	m := NewModel("gpu-node-01", 1)
	m.now = func() time.Time { return now }
	selected, err := scenario.LoadBuiltin("xid-79")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Duration = "10s"

	m.Apply(selected, "44")
	if got := m.Snapshot(); got.Scenario != selected.Name() || got.Readings[0].XIDCode != 79 {
		t.Fatalf("active snapshot = %+v, want XID scenario", got)
	}

	now = now.Add(9 * time.Second)
	if got := m.Snapshot(); got.Scenario != selected.Name() {
		t.Fatalf("scenario expired early at %+v", got)
	}

	now = now.Add(time.Second)
	got := m.Snapshot()
	if got.Scenario != "normal" || got.Generation != "44" || got.Readings[0].XIDCode != 0 || got.Readings[0].Health != 1 {
		t.Fatalf("expired snapshot = %+v, want normal telemetry with generation 44", got)
	}
	if strings.Contains(m.Metrics(), "gpu_lab_gpu_xid_code{node=\"gpu-node-01\",gpu=\"gpu-node-01-00\"} 79") {
		t.Fatal("expired XID telemetry remained exposed")
	}
}

func TestBaselineFabricReadingAndMetrics(t *testing.T) {
	m := NewModel("gpu-node-01", 1)
	snapshot := m.Snapshot()
	if !snapshot.Fabric.Synthetic || snapshot.Fabric.HCA != DefaultHCA || snapshot.Fabric.Port != DefaultFabricPort || snapshot.Fabric.LinkLayer != DefaultLinkLayer {
		t.Fatalf("baseline fabric identity = %+v", snapshot.Fabric)
	}
	if snapshot.Fabric.PortUp != 1 || snapshot.Fabric.State != "ACTIVE" || snapshot.Fabric.PhysicalState != "LINK_UP" || snapshot.Fabric.LinkRateGbps != DefaultIBLinkRateGbps {
		t.Fatalf("baseline fabric state = %+v", snapshot.Fabric)
	}
	if snapshot.Fabric.SymbolErrorsTotal != 0 || snapshot.Fabric.RDMARetriesTotal != 0 || snapshot.Fabric.FabricDelaySeconds != 0 {
		t.Fatalf("baseline fabric counters = %+v", snapshot.Fabric)
	}
	state, err := m.StateJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded StateSnapshot
	if err := json.Unmarshal(state, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Fabric.Synthetic || decoded.Fabric.HCA != DefaultHCA {
		t.Fatalf("state JSON omitted synthetic fabric reading: %s", state)
	}

	metrics := m.Metrics()
	for _, expected := range []string{
		`# HELP gpu_lab_ib_port_up Synthetic InfiniBand HCA port availability; one means the port is up.`,
		`# TYPE gpu_lab_ib_port_state gauge`,
		`gpu_lab_ib_port_up{node="gpu-node-01",hca="mlx5_0",port="1",link_layer="InfiniBand"} 1`,
		`gpu_lab_ib_port_state{node="gpu-node-01",hca="mlx5_0",port="1",link_layer="InfiniBand",state="ACTIVE",physical_state="LINK_UP"} 1`,
		`gpu_lab_ib_link_rate_gbps{node="gpu-node-01",hca="mlx5_0",port="1",link_layer="InfiniBand"} 200`,
		`gpu_lab_rdma_retries_total{node="gpu-node-01",hca="mlx5_0",port="1",link_layer="InfiniBand"} 0`,
		`gpu_lab_fabric_delay_seconds{node="gpu-node-01",hca="mlx5_0",port="1",link_layer="InfiniBand"} 0`,
	} {
		if !strings.Contains(metrics, expected) {
			t.Fatalf("baseline metrics missing %q", expected)
		}
	}
}

func TestTargetedFabricScenariosOnlyAffectSelectedNode(t *testing.T) {
	cases := []struct {
		name       string
		targetNode string
		metric     string
	}{
		{name: "ib-link-down", targetNode: "gpu-node-02", metric: `gpu_lab_ib_port_up{node="gpu-node-02",hca="mlx5_0",port="1",link_layer="InfiniBand"} 0`},
		{name: "ib-rate-degraded", targetNode: "gpu-node-03", metric: `gpu_lab_ib_link_rate_gbps{node="gpu-node-03",hca="mlx5_0",port="1",link_layer="InfiniBand"} 25`},
		{name: "ib-symbol-errors", targetNode: "gpu-node-01", metric: `gpu_lab_ib_symbol_errors_total{node="gpu-node-01",hca="mlx5_0",port="1",link_layer="InfiniBand"} 128`},
		{name: "rdma-retry-storm", targetNode: "gpu-node-02", metric: `gpu_lab_rdma_retries_total{node="gpu-node-02",hca="mlx5_0",port="1",link_layer="InfiniBand"} 250`},
		{name: "ib-congestion", targetNode: "gpu-node-03", metric: `gpu_lab_ib_xmit_wait_total{node="gpu-node-03",hca="mlx5_0",port="1",link_layer="InfiniBand"} 640`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			selected, err := scenario.LoadBuiltin(testCase.name)
			if err != nil {
				t.Fatal(err)
			}
			target := NewModel(testCase.targetNode, 1)
			otherNode := "gpu-node-01"
			if otherNode == testCase.targetNode {
				otherNode = "gpu-node-02"
			}
			other := NewModel(otherNode, 1)
			target.Apply(selected, "fabric-1")
			other.Apply(selected, "fabric-1")
			if !strings.Contains(target.Metrics(), testCase.metric) {
				t.Fatalf("target metrics missing %q", testCase.metric)
			}
			if other.Snapshot().Scenario != "normal" || other.Snapshot().Fabric.PortUp != 1 || other.Snapshot().Fabric.LinkRateGbps != DefaultIBLinkRateGbps || other.Snapshot().Fabric.FabricDelaySeconds != 0 {
				t.Fatalf("non-target fabric changed: %+v", other.Snapshot())
			}
		})
	}
}

func TestFabricScenarioDurationExpiresToBaseline(t *testing.T) {
	now := time.Unix(300, 0).UTC()
	m := NewModel("gpu-node-02", 1)
	m.now = func() time.Time { return now }
	selected, err := scenario.LoadBuiltin("ib-link-down")
	if err != nil {
		t.Fatal(err)
	}
	selected.Spec.Duration = "5s"
	m.Apply(selected, "fabric-2")
	if got := m.Snapshot().Fabric; got.PortUp != 0 || got.State != "DOWN" || got.PhysicalState != "DISABLED" || got.LinkDownedTotal != 1 {
		t.Fatalf("active fabric scenario = %+v", got)
	}
	now = now.Add(5 * time.Second)
	got := m.Snapshot()
	if got.Scenario != "normal" || got.Fabric.PortUp != 1 || got.Fabric.State != "ACTIVE" || got.Fabric.PhysicalState != "LINK_UP" || got.Fabric.LinkDownedTotal != 0 || got.Fabric.FabricDelaySeconds != 0 {
		t.Fatalf("expired fabric scenario = %+v", got)
	}
	if strings.Contains(m.Metrics(), `state="DOWN",physical_state="DISABLED`) {
		t.Fatal("expired fabric state remained exposed")
	}
}

func TestFabricMetricLabelsEscapeNodeNames(t *testing.T) {
	node := "gpu-node-\\\"quoted\nnode"
	m := NewModel(node, 1)
	metrics := m.Metrics()
	expected := `gpu_lab_ib_port_up{node="` + escapeLabel(node) + `",hca="mlx5_0",port="1",link_layer="InfiniBand"} 1`
	if !strings.Contains(metrics, expected) {
		t.Fatalf("fabric labels were not escaped in Prometheus exposition: %s", metrics)
	}
}
