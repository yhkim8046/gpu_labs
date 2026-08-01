package scenario

import (
	"encoding/json"
	"testing"
)

func TestBuiltinScenarios(t *testing.T) {
	names, err := ListBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exporter-down", "gpu-util-high", "normal", "scheduling-failure", "vram-pressure", "xid-79"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], want[i])
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
}
