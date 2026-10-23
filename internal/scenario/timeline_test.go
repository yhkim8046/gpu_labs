package scenario

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const validTimelineScenarioYAML = `apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata:
  name: thermal-escalation
spec:
  description: baseline to warning to critical to recovery
  duration: 3m
  targets:
    selector:
      gpu.lab/node-id: gpu-node-01
    gpu_indices: [1]
  timeline:
    - phase: baseline
      at: 0s
      metrics: {}
    - phase: warning
      at: 30s
      metrics:
        gpu_utilization_percent: 92
        temperature_celsius: 84
    - phase: critical
      at: 1m30s
      metrics:
        gpu_utilization_percent: 62
        temperature_celsius: 96
        health: 0
    - phase: recovery
      at: 2m
      metrics: {}
`

func TestTimelineScenarioParsesAndRoundTripsYAMLAndJSON(t *testing.T) {
	parsed, err := Parse([]byte(validTimelineScenarioYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Spec.Timeline) != 4 || parsed.Spec.Timeline[2].Phase != "critical" || parsed.Spec.Timeline[2].At != "1m30s" {
		t.Fatalf("timeline = %#v", parsed.Spec.Timeline)
	}
	if parsed.Spec.Timeline[1].Metrics.TemperatureCelsius == nil || *parsed.Spec.Timeline[1].Metrics.TemperatureCelsius != 84 {
		t.Fatalf("warning metrics = %#v", parsed.Spec.Timeline[1].Metrics)
	}

	yamlData, err := Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, err := Parse(yamlData)
	if err != nil {
		t.Fatalf("strict parser rejected marshaled YAML: %v", err)
	}
	if fromYAML.Spec.Timeline[3].Phase != "recovery" {
		t.Fatalf("YAML round trip = %#v", fromYAML.Spec.Timeline)
	}

	jsonData, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := Parse(jsonData)
	if err != nil {
		t.Fatalf("strict parser rejected compatible JSON: %v", err)
	}
	if fromJSON.Spec.Timeline[1].Phase != "warning" || fromJSON.Spec.Targets.GPUIndices[0] != 1 {
		t.Fatalf("JSON round trip = %#v", fromJSON)
	}
}

func TestTimelineValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Scenario)
		want   string
	}{
		{
			name: "exactly four steps",
			mutate: func(s *Scenario) {
				s.Spec.Timeline = s.Spec.Timeline[:3]
			},
			want: "exactly 4 steps",
		},
		{
			name: "fixed phase order",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[1].Phase = "critical"
			},
			want: `phase must be "warning"`,
		},
		{
			name: "baseline starts at zero",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[0].At = "1s"
			},
			want: "timeline[0].at must be 0s",
		},
		{
			name: "valid offsets",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[2].At = "eventually"
			},
			want: "timeline[2].at",
		},
		{
			name: "strictly increasing offsets",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[2].At = "30s"
			},
			want: "must be later",
		},
		{
			name: "duration after recovery",
			mutate: func(s *Scenario) {
				s.Spec.Duration = "2m"
			},
			want: "duration must be later",
		},
		{
			name: "baseline metrics empty",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[0].Metrics.TemperatureCelsius = float64Pointer(45)
			},
			want: "must be empty for baseline",
		},
		{
			name: "warning metrics present",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[1].Metrics = MetricOverrides{}
			},
			want: "must not be empty for warning",
		},
		{
			name: "critical metrics present",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[2].Metrics = MetricOverrides{}
			},
			want: "must not be empty for critical",
		},
		{
			name: "recovery metrics empty",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[3].Metrics.Health = intPointer(1)
			},
			want: "must be empty for recovery",
		},
		{
			name: "top level metrics conflict",
			mutate: func(s *Scenario) {
				s.Spec.Metrics.TemperatureCelsius = float64Pointer(80)
			},
			want: "spec.metrics cannot be combined",
		},
		{
			name: "top level actions conflict",
			mutate: func(s *Scenario) {
				s.Spec.Actions = []ScenarioAction{{Type: "exporter_fault", Mode: "unavailable"}}
			},
			want: "spec.actions cannot be combined",
		},
		{
			name: "fabric overrides unsupported",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[1].Metrics.IBPortUp = intPointer(0)
			},
			want: "InfiniBand/RDMA fabric overrides",
		},
		{
			name: "target safety rechecked",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[1].Metrics.GPUCapacity = intPointer(8)
			},
			want: "node capacity metrics cannot be combined",
		},
		{
			name: "phase metric ranges rechecked",
			mutate: func(s *Scenario) {
				s.Spec.Timeline[1].Metrics.GPUUtilizationPercent = float64Pointer(101)
			},
			want: "gpu_utilization_percent must be between 0 and 100",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, err := Parse([]byte(validTimelineScenarioYAML))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&selected)
			if err := selected.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPhaseAt(t *testing.T) {
	selected, err := Parse([]byte(validTimelineScenarioYAML))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		elapsed time.Duration
		phase   string
		index   int
	}{
		{elapsed: -time.Second, phase: "baseline", index: 0},
		{elapsed: 0, phase: "baseline", index: 0},
		{elapsed: 29*time.Second + 999*time.Millisecond, phase: "baseline", index: 0},
		{elapsed: 30 * time.Second, phase: "warning", index: 1},
		{elapsed: 90 * time.Second, phase: "critical", index: 2},
		{elapsed: 2 * time.Minute, phase: "recovery", index: 3},
		{elapsed: 10 * time.Minute, phase: "recovery", index: 3},
	}
	for _, test := range tests {
		step, index, ok := selected.PhaseAt(test.elapsed)
		if !ok || step.Phase != test.phase || index != test.index {
			t.Fatalf("PhaseAt(%s) = (%+v, %d, %t), want phase=%s index=%d", test.elapsed, step, index, ok, test.phase, test.index)
		}
	}
	if _, _, ok := PhaseAt(nil, 0); ok {
		t.Fatal("PhaseAt() accepted an empty timeline")
	}
	malformed := append([]TimelineStep(nil), selected.Spec.Timeline...)
	malformed[2].At = "later"
	if _, _, ok := PhaseAt(malformed, 2*time.Minute); ok {
		t.Fatal("PhaseAt() accepted a malformed timeline")
	}
}
