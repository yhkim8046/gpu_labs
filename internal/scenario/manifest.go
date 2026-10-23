package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type configMapManifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   objectMeta        `json:"metadata"`
	Data       map[string]string `json:"data"`
}

type objectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ConfigMapJSON renders the scenario ConfigMap manifest without a start
// timestamp, preserving the pre-timeline wire format.
func ConfigMapJSON(s Scenario, generation string) ([]byte, error) {
	return configMapJSON(s, generation, "")
}

// ConfigMapJSONAt includes the common scenario start timestamp used by every
// exporter to derive timeline phases. UTC RFC3339Nano keeps the value stable
// across nodes and preserves sub-second precision for short test timelines.
func ConfigMapJSONAt(s Scenario, generation string, startedAt time.Time) ([]byte, error) {
	if startedAt.IsZero() {
		return nil, errors.New("startedAt must not be zero")
	}
	return configMapJSON(s, generation, startedAt.UTC().Format(time.RFC3339Nano))
}

func configMapJSON(s Scenario, generation, startedAt string) ([]byte, error) {
	yamlData, err := Marshal(s)
	if err != nil {
		return nil, err
	}
	data := map[string]string{
		ScenarioDataKey:   string(yamlData),
		GenerationDataKey: generation,
	}
	if startedAt != "" {
		data[StartedAtDataKey] = startedAt
	}
	manifest := configMapManifest{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Metadata: objectMeta{
			Name:      ConfigName,
			Namespace: Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":    "gpu-lab",
				"app.kubernetes.io/managed-by": "gpu-lab",
			},
		},
		Data: data,
	}
	return json.MarshalIndent(manifest, "", "  ")
}

// PendingWorkloadJSON synthesizes an unschedulable GPU pod that exercises the
// scheduling-failure surface for the scenario.
func PendingWorkloadJSON(s Scenario, gpuCount int) ([]byte, error) {
	if gpuCount <= 0 {
		return nil, errors.New("gpuCount must be positive")
	}
	return GPUWorkloadJSON(s, ScenarioAction{
		Type:     "create_gpu_workload",
		Name:     "gpu-lab-scheduling-failure",
		GPUCount: gpuCount,
	})
}

// GPUWorkloadJSON renders the synthetic pause-container pod for a
// create_gpu_workload action.
func GPUWorkloadJSON(s Scenario, action ScenarioAction) ([]byte, error) {
	if action.Type != "create_gpu_workload" {
		return nil, fmt.Errorf("action type must be create_gpu_workload, got %q", action.Type)
	}
	if !namePattern.MatchString(action.Name) {
		return nil, fmt.Errorf("workload name %q is not a DNS-compatible lowercase name", action.Name)
	}
	if action.GPUCount <= 0 {
		return nil, errors.New("gpu_count must be positive")
	}
	resourceCount := fmt.Sprint(action.GPUCount)
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      action.Name,
			"namespace": "gpu-lab-demo",
			"labels": map[string]string{
				"app.kubernetes.io/part-of":    "gpu-lab",
				"app.kubernetes.io/managed-by": "gpu-lab",
				"gpu-lab/scenario":             s.Name(),
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []any{map[string]any{
				"name":  "pause",
				"image": "registry.k8s.io/pause:3.9",
				"resources": map[string]any{
					"requests": map[string]string{"nvidia.com/gpu": resourceCount},
					"limits":   map[string]string{"nvidia.com/gpu": resourceCount},
				},
			}},
		},
	}
	if len(action.NodeSelector) > 0 {
		manifest["spec"].(map[string]any)["nodeSelector"] = action.NodeSelector
	}
	return json.MarshalIndent(manifest, "", "  ")
}

// PrettyJSON re-indents compact JSON, returning the input unchanged when it
// is not valid JSON.
func PrettyJSON(data []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return string(data)
	}
	return out.String()
}
