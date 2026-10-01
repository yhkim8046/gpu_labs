package scenario

import (
	"encoding/json"
	"testing"
	"time"
)

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
	if data := cm["data"].(map[string]any); data[StartedAtDataKey] != nil {
		t.Fatalf("legacy ConfigMapJSON() unexpectedly set %s: %v", StartedAtDataKey, data[StartedAtDataKey])
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

func TestConfigMapJSONAtIncludesUTCStartTimestamp(t *testing.T) {
	selected, err := Parse([]byte(validTimelineScenarioYAML))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, time.August, 13, 10, 11, 12, 123456789, time.FixedZone("KST", 9*60*60))
	configMap, err := ConfigMapJSONAt(selected, "timeline-42", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	var cm map[string]any
	if err := json.Unmarshal(configMap, &cm); err != nil {
		t.Fatal(err)
	}
	data := cm["data"].(map[string]any)
	if got, want := data[StartedAtDataKey], startedAt.UTC().Format(time.RFC3339Nano); got != want {
		t.Fatalf("%s = %v, want %q", StartedAtDataKey, got, want)
	}
	if data[GenerationDataKey] != "timeline-42" {
		t.Fatalf("generation = %v", data[GenerationDataKey])
	}
	if _, err := Parse([]byte(data[ScenarioDataKey].(string))); err != nil {
		t.Fatalf("embedded timeline scenario did not parse: %v", err)
	}
	if _, err := ConfigMapJSONAt(selected, "timeline-43", time.Time{}); err == nil {
		t.Fatal("ConfigMapJSONAt() accepted a zero start timestamp")
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
