package nvidiasmi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

// promVectorResponse renders a Prometheus query API vector response from
// (node, gpu, value) samples using the same label shape the lab exporter
// emits (metric.node + metric.gpu).
type promSample struct {
	Name  string
	Node  string
	GPU   string
	Value string
}

func promVectorResponse(samples []promSample) string {
	type metric map[string]string
	type result struct {
		Metric metric    `json:"metric"`
		Value  [2]string `json:"value"`
	}
	type data struct {
		ResultType string   `json:"resultType"`
		Result     []result `json:"result"`
	}
	response := struct {
		Status string `json:"status"`
		Data   data   `json:"data"`
	}{Status: "success", Data: data{ResultType: "vector"}}
	for _, sample := range samples {
		response.Data.Result = append(response.Data.Result, result{
			Metric: metric{"__name__": sample.Name, "node": sample.Node, "gpu": sample.GPU},
			Value:  [2]string{"0", sample.Value},
		})
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestCourseTopologyThreeNodesEightGPUsEach is the topology regression pin
// for the course default verified in deploy/kind/cluster.yaml: three GPU
// workers (gpu-node-01/02/03) x eight GPUs each, i.e. 24 GPU rows in total.
// nvidia-smi must group exactly three node blocks and print one row per GPU
// without duplicating devices across nodes.
func TestCourseTopologyThreeNodesEightGPUsEach(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX syntax")
	}
	const (
		nodeCount = 3
		gpuCount  = 8
	)
	t.Setenv("GPU_LAB_STATE_URL", "")

	names := []string{
		"gpu_lab_gpu_utilization_percent",
		"gpu_lab_gpu_memory_used_bytes",
		"gpu_lab_gpu_memory_total_bytes",
	}
	values := map[string]string{
		"gpu_lab_gpu_utilization_percent": "15",
		"gpu_lab_gpu_memory_used_bytes":   "3355443200",
		"gpu_lab_gpu_memory_total_bytes":  "17179869184",
	}
	var samples []promSample
	for nodeIndex := 1; nodeIndex <= nodeCount; nodeIndex++ {
		node := fmt.Sprintf("gpu-node-%02d", nodeIndex)
		for gpuIndex := 0; gpuIndex < gpuCount; gpuIndex++ {
			gpu := fmt.Sprintf("%s-%02d", node, gpuIndex)
			for _, name := range names {
				samples = append(samples, promSample{Name: name, Node: node, GPU: gpu, Value: values[name]})
			}
		}
	}
	dir := t.TempDir()
	responseFile := filepath.Join(dir, "response.json")
	if err := os.WriteFile(responseFile, []byte(promVectorResponse(samples)), 0o644); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\ncat \"" + responseFile + "\"\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout strings.Builder
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), nil, &stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()

	if got := strings.Count(output, "GPU Lab node:"); got != nodeCount {
		t.Fatalf("node block count = %d, want %d\n%s", got, nodeCount, output)
	}

	// Split into per-node blocks and pin exactly gpuCount device rows each.
	// Each device renders exactly one summary row carrying the synthetic
	// product name, so the per-block count is the device count.
	totalRows := 0
	blocks := strings.Split(output, "GPU Lab node: ")
	if len(blocks) != nodeCount+1 {
		t.Fatalf("unexpected block split %d", len(blocks))
	}
	for nodeIndex := 1; nodeIndex <= nodeCount; nodeIndex++ {
		node := fmt.Sprintf("gpu-node-%02d", nodeIndex)
		block := blocks[nodeIndex]
		if !strings.HasPrefix(block, node+"\n") {
			t.Fatalf("block %d starts with %q, want node %q", nodeIndex, block, node)
		}
		rows := strings.Count(block, syntheticGPUName+"    ")
		if rows != gpuCount {
			t.Fatalf("node %s device rows = %d, want %d\n%s", node, rows, gpuCount, block)
		}
		totalRows += rows
	}
	if totalRows != nodeCount*gpuCount {
		t.Fatalf("device rows total = %d, want %d", totalRows, nodeCount*gpuCount)
	}
}

// TestNodeSelectorLimitsOutputToSingleNode exercises the real --node
// selection path: with the same three-node synthetic fixture, Run with
// --node gpu-node-02 must print exactly one node block for gpu-node-02 with
// eight device rows and no rows from the other nodes.
func TestNodeSelectorLimitsOutputToSingleNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX syntax")
	}
	t.Setenv("GPU_LAB_STATE_URL", "")

	values := map[string]string{
		"gpu_lab_gpu_utilization_percent": "15",
		"gpu_lab_gpu_memory_used_bytes":   "3355443200",
		"gpu_lab_gpu_memory_total_bytes":  "17179869184",
	}
	var samples []promSample
	for _, node := range []string{"gpu-node-01", "gpu-node-02", "gpu-node-03"} {
		for gpuIndex := 0; gpuIndex < 8; gpuIndex++ {
			gpu := fmt.Sprintf("%s-%02d", node, gpuIndex)
			for _, name := range []string{
				"gpu_lab_gpu_utilization_percent",
				"gpu_lab_gpu_memory_used_bytes",
				"gpu_lab_gpu_memory_total_bytes",
			} {
				samples = append(samples, promSample{Name: name, Node: node, GPU: gpu, Value: values[name]})
			}
		}
	}
	dir := t.TempDir()
	responseFile := filepath.Join(dir, "response.json")
	if err := os.WriteFile(responseFile, []byte(promVectorResponse(samples)), 0o644); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\ncat \"" + responseFile + "\"\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout strings.Builder
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), []string{"--node", "gpu-node-02"}, &stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()

	if got := strings.Count(output, "GPU Lab node:"); got != 1 {
		t.Fatalf("node block count = %d, want 1\n%s", got, output)
	}
	if !strings.Contains(output, "GPU Lab node: gpu-node-02\n") {
		t.Fatalf("selected node block missing\n%s", output)
	}
	for _, other := range []string{"gpu-node-01", "gpu-node-03"} {
		if strings.Contains(output, "GPU Lab node: "+other) {
			t.Fatalf("output leaked non-selected node %q\n%s", other, output)
		}
	}
	block := output[strings.Index(output, "GPU Lab node: "):]
	rows := strings.Count(block, syntheticGPUName+"    ")
	if rows != 8 {
		t.Fatalf("selected node device rows = %d, want 8\n%s", rows, block)
	}
}
