package trainingjob

import (
	"encoding/json"
	"testing"
	"time"
)

func TestManifestShape(t *testing.T) {
	rank := 1
	raw, err := Manifest(Options{Workers: 3, Image: "gpu-lab:test", Namespace: "lab"}, ControlState{Generation: 7, CrashRank: &rank, CrashToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Items      []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if list.APIVersion != "v1" || list.Kind != "List" {
		t.Fatalf("manifest type = %s %s, want v1 List", list.APIVersion, list.Kind)
	}
	resources := list.Items
	if len(resources) != 4 {
		t.Fatalf("resources = %d, want 4", len(resources))
	}
	var sts map[string]any
	for _, resource := range resources {
		if resource["kind"] == "StatefulSet" {
			sts = resource
		}
	}
	if sts == nil {
		t.Fatal("StatefulSet missing")
	}
	spec := sts["spec"].(map[string]any)
	if spec["serviceName"] != Name || spec["podManagementPolicy"] != "Parallel" {
		t.Fatalf("statefulset spec = %#v", spec)
	}
	if spec["replicas"].(float64) != 3 {
		t.Fatalf("replicas = %v", spec["replicas"])
	}
	template := spec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	container := podSpec["containers"].([]any)[0].(map[string]any)
	if container["image"] != "gpu-lab:test" {
		t.Fatalf("image = %v", container["image"])
	}
	if container["command"].([]any)[0] != "/usr/local/bin/training-worker" {
		t.Fatalf("command = %#v", container["command"])
	}
	limits := container["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["nvidia.com/gpu"] != "1" {
		t.Fatalf("GPU limit = %v", limits["nvidia.com/gpu"])
	}
	volumes := podSpec["volumes"].([]any)
	if len(volumes) != 2 {
		t.Fatalf("volumes = %d", len(volumes))
	}
	spread := podSpec["topologySpreadConstraints"].([]any)
	if len(spread) != 1 || spread[0].(map[string]any)["topologyKey"] != "kubernetes.io/hostname" {
		t.Fatal("hostname topology spread constraint is missing")
	}
}

func TestControlJSONRoundTrip(t *testing.T) {
	rank := 2
	want := ControlState{Generation: 11, StragglerRank: &rank, StragglerDelay: "2s", CrashToken: "x", FabricMode: "degraded", FabricTargetNode: "gpu-node-03", FabricDelay: "1.5s"}
	raw, err := want.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got ControlState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Generation != want.Generation || got.StragglerRank == nil || *got.StragglerRank != rank || got.StragglerDelay != want.StragglerDelay || got.CrashToken != want.CrashToken || got.FabricMode != want.FabricMode || got.FabricTargetNode != want.FabricTargetNode || got.FabricDelay != want.FabricDelay {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestFabricControlForScenario(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		target string
		delay  string
	}{
		{name: "normal", mode: "normal"},
		{name: "xid-79", mode: "normal"},
		{name: "ib-link-down", mode: "down", target: "gpu-node-02"},
		{name: "ib-rate-degraded", mode: "degraded", target: "gpu-node-03", delay: "1.5s"},
		{name: "ib-symbol-errors", mode: "errors", target: "gpu-node-01"},
		{name: "rdma-retry-storm", mode: "retries", target: "gpu-node-02", delay: "1s"},
		{name: "ib-congestion", mode: "congestion", target: "gpu-node-03", delay: "800ms"},
	}
	for _, test := range tests {
		got := FabricControlForScenario(test.name)
		if got.Mode != test.mode || got.TargetNode != test.target || got.Delay != test.delay {
			t.Errorf("%s = %#v, want mode=%q target=%q delay=%q", test.name, got, test.mode, test.target, test.delay)
		}
	}
}

func TestApplyFabricControlPreservesExistingTrainingFaultsAndNormalizes(t *testing.T) {
	rank := 1
	state := ControlState{Generation: 4, StragglerRank: &rank, StragglerDelay: "2s", CrashRank: &rank, CrashToken: "crash-4"}
	ApplyFabricControl(&state, FabricControl{Mode: "DEGRADED", TargetNode: "gpu-node-03", Delay: "1.5s"})
	if state.Generation != 4 || state.StragglerRank == nil || state.CrashRank == nil || state.FabricMode != "degraded" || state.FabricTargetNode != "gpu-node-03" || state.FabricDelay != "1.5s" {
		t.Fatalf("applied state = %#v", state)
	}
	ApplyFabricControl(&state, FabricControl{Mode: "normal", TargetNode: "gpu-node-02", Delay: "1s"})
	if state.FabricMode != "normal" || state.FabricTargetNode != "" || state.FabricDelay != "" || state.StragglerRank == nil || state.CrashRank == nil {
		t.Fatalf("neutral state = %#v", state)
	}
}

func TestOptionsRejectInvalidValues(t *testing.T) {
	if _, err := Manifest(Options{Workers: 0, Timeout: -time.Second}, ControlState{}); err == nil {
		t.Fatal("expected invalid timeout")
	}
	if _, err := Manifest(Options{Workers: -1}, ControlState{}); err == nil {
		t.Fatal("expected invalid workers")
	}
}
