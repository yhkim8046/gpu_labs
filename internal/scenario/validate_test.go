package scenario

import (
	"testing"
)

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

func float64Pointer(value float64) *float64 { return &value }

func intPointer(value int) *int { return &value }
