package exporter

import (
	"strings"
	"testing"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

func TestTimelineUsesSharedStartAcrossExporterRestarts(t *testing.T) {
	selected, err := scenario.LoadBuiltin("thermal-escalation")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	now := started
	model := NewModel("gpu-lab-worker", 2)
	model.now = func() time.Time { return now }
	model.ApplyAt(selected, "101", started)
	for _, tc := range []struct {
		elapsed                         time.Duration
		phase                           string
		temperature, utilization, power float64
		throttled                       int
	}{
		{-time.Second, "baseline", 45, 15, 80, 0},
		{0, "baseline", 45, 15, 80, 0},
		{30*time.Second - time.Nanosecond, "baseline", 45, 15, 80, 0},
		{30 * time.Second, "warning", 82, 88, 210, 0},
		{time.Minute, "critical", 96, 62, 245, 1},
		{2 * time.Minute, "recovery", 45, 15, 80, 0},
	} {
		now = started.Add(tc.elapsed)
		state := model.Snapshot()
		if state.Phase != tc.phase || state.Generation != "101" || !state.StartedAt.Equal(started) {
			t.Fatalf("at %s: %+v", tc.elapsed, state)
		}
		for _, gpu := range state.Readings {
			if gpu.TemperatureCelsius != tc.temperature || gpu.UtilizationPercent != tc.utilization || gpu.PowerWatts != tc.power || gpu.ThrottleActive != tc.throttled {
				t.Fatalf("at %s: incorrect phase reading %+v", tc.elapsed, gpu)
			}
		}
		if !strings.Contains(model.Metrics(), `phase="`+tc.phase+`",generation="101"} 1`) {
			t.Fatalf("at %s: missing phase metric", tc.elapsed)
		}
	}
	now = started.Add(75 * time.Second)
	restarted := NewModel("gpu-lab-worker2", 2)
	restarted.now = func() time.Time { return now }
	restarted.ApplyAt(selected, "101", started)
	if state := restarted.Snapshot(); state.Phase != "critical" || state.Readings[0].TemperatureCelsius != 96 {
		t.Fatalf("restarted exporter lost shared timeline: %+v", state)
	}
	now = started.Add(3 * time.Minute)
	if state := restarted.Snapshot(); state.Scenario != "normal" || state.Phase != "" || state.Readings[0].ThrottleActive != 0 {
		t.Fatalf("timeline did not expire: %+v", state)
	}
}

func TestTimelineHonorsNodeAndGPUSelection(t *testing.T) {
	selected, _ := scenario.LoadBuiltin("thermal-escalation")
	selected.Spec.Targets.Selector = map[string]string{"gpu.lab/node-id": "gpu-node-01"}
	selected.Spec.Targets.GPUIndices = []int{1}
	started := time.Unix(1_000, 0)
	for _, node := range []string{"gpu-lab-worker", "gpu-lab-worker2"} {
		model := NewModel(node, 2)
		model.now = func() time.Time { return started.Add(time.Minute) }
		model.ApplyAt(selected, "102", started)
		state := model.Snapshot()
		for index, gpu := range state.Readings {
			want := 45.0
			if node == "gpu-lab-worker" && index == 1 {
				want = 96
			}
			if gpu.TemperatureCelsius != want {
				t.Fatalf("node=%s GPU=%d: temperature=%v want=%v", node, index, gpu.TemperatureCelsius, want)
			}
		}
	}
}

func TestTimelineConfigRejectsMissingOrInvalidStart(t *testing.T) {
	selected, _ := scenario.LoadBuiltin("thermal-escalation")
	data, _ := scenario.Marshal(selected)
	model := NewModel("gpu-lab-worker", 1)
	for _, value := range []string{"", "invalid"} {
		_, err := applyScenarioConfig(model, map[string]string{scenario.ScenarioDataKey: string(data), scenario.StartedAtDataKey: value}, "103")
		if err == nil || model.Snapshot().Scenario != "normal" {
			t.Fatalf("invalid timestamp %q mutated state or succeeded: %v", value, err)
		}
	}
	started := time.Now().Add(-time.Minute)
	if _, err := applyScenarioConfig(model, map[string]string{scenario.ScenarioDataKey: string(data), scenario.StartedAtDataKey: started.Format(time.RFC3339Nano)}, "104"); err != nil {
		t.Fatal(err)
	}
	if state := model.Snapshot(); state.Phase != "critical" || state.Generation != "104" {
		t.Fatalf("ConfigMap timeline was not applied: %+v", state)
	}
}
