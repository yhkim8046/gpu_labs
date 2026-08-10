package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/monitoring"
)

func TestHelmPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX syntax")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	helm := filepath.Join(dir, "helm")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GPU_LAB_TEST_ARGS\"\n"
	if err := os.WriteFile(helm, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	oldArgs := os.Getenv("GPU_LAB_TEST_ARGS")
	defer func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("GPU_LAB_TEST_ARGS", oldArgs)
	}()
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	_ = os.Setenv("GPU_LAB_TEST_ARGS", argsFile)
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"helm", "repo", "add", "demo", "https://example.invalid/charts"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"repo", "add", "demo", "https://example.invalid/charts"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.HasPrefix(got, "gpu-lab ") {
		t.Fatalf("version output = %q, want gpu-lab prefix", got)
	}
}

func TestParseCreateArgs(t *testing.T) {
	options, err := parseCreateArgs([]string{"--local"})
	if err != nil {
		t.Fatal(err)
	}
	if options.imageSource != cluster.ImageSourceLocal {
		t.Fatalf("image source = %q, want %q", options.imageSource, cluster.ImageSourceLocal)
	}

	options, err = parseCreateArgs([]string{"--registry", "--image", "ghcr.io/example/gpu-lab-runtime:1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if options.imageSource != cluster.ImageSourceRegistry {
		t.Fatalf("image source = %q, want %q", options.imageSource, cluster.ImageSourceRegistry)
	}
	if options.image != "ghcr.io/example/gpu-lab-runtime:1.0.0" {
		t.Fatalf("image = %q, want explicit image", options.image)
	}

	options, err = parseCreateArgs([]string{"--all"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.installAll {
		t.Fatal("installAll = false, want true")
	}
}

func TestHelmCatalog(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"helm", "catalog"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"nvidia-device-plugin", "dcgm-exporter", "monitoring"} {
		if !strings.Contains(stdout.String(), component) {
			t.Fatalf("catalog output = %q, missing %q", stdout.String(), component)
		}
	}
}

func TestParseCreateArgsRejectsConflictingSources(t *testing.T) {
	if _, err := parseCreateArgs([]string{"--local", "--registry"}); err == nil {
		t.Fatal("parseCreateArgs() succeeded for conflicting sources")
	}
}

func TestDashboardPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX syntax")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GPU_LAB_TEST_ARGS\"\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	oldArgs := os.Getenv("GPU_LAB_TEST_ARGS")
	defer func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("GPU_LAB_TEST_ARGS", oldArgs)
	}()
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	_ = os.Setenv("GPU_LAB_TEST_ARGS", argsFile)
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"dashboard", "--port", "3200"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"--context", "gpu-lab", "-n", "gpu-lab-monitoring", "port-forward", "svc/gpu-lab-monitoring-grafana", "3200:80"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if !strings.Contains(stdout.String(), "http://127.0.0.1:3200") {
		t.Fatalf("stdout = %q, want dashboard URL", stdout.String())
	}
}

func TestInspectScenario(t *testing.T) {
	var stdout bytes.Buffer
	if err := inspectScenario([]string{"xid-79"}, &stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, expected := range []string{"scenario: xid-79", "xid_code=79", "health=0"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output = %q, missing %q", output, expected)
		}
	}
}

func TestInspectScenarioShowsGPUIndexTargets(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "targeted.yaml")
	data := []byte(`apiVersion: gpu-lab.io/v1alpha1
kind: Scenario
metadata:
  name: targeted
spec:
  duration: 30s
  targets:
    selector:
      gpu.lab/node-id: gpu-node-01
    gpu_indices: [3, 1]
  metrics:
    gpu_utilization_percent: 90
`)
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := inspectScenario([]string{"targeted", "--file", filename}, &stdout); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"duration: 30s", "gpu.lab/node-id=gpu-node-01", "gpu_indices=1,3"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output = %q, missing %q", stdout.String(), expected)
		}
	}
}

func TestParseMetricsArgs(t *testing.T) {
	options, err := parseMetricsArgs([]string{"--query", "max(gpu_lab_gpu_xid_code)", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if options.query != "max(gpu_lab_gpu_xid_code)" || !options.json {
		t.Fatalf("options = %#v, want query and json", options)
	}
}

func TestParseTrainingRunArgs(t *testing.T) {
	o, err := parseTrainingRunArgs([]string{"--workers", "3", "--image", "example/training:v1", "--namespace", "lab", "--wait", "--timeout", "45s"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Workers != 3 || o.Image != "example/training:v1" || o.Namespace != "lab" || !o.Wait || o.Timeout != 45*time.Second {
		t.Fatalf("options = %#v", o)
	}
}

func TestParseTrainingRunArgsRejectsBadValues(t *testing.T) {
	for _, args := range [][]string{{"--workers", "0"}, {"--timeout", "nope"}, {"--unknown"}} {
		if _, err := parseTrainingRunArgs(args); err == nil {
			t.Fatalf("parseTrainingRunArgs(%v) succeeded", args)
		}
	}
}

func TestParseTrainingLogsArgs(t *testing.T) {
	ns, rank, follow, err := parseTrainingLogsArgs([]string{"--rank", "2", "--follow", "--namespace", "lab"})
	if err != nil {
		t.Fatal(err)
	}
	if ns != "lab" || rank != 2 || !follow {
		t.Fatalf("got namespace=%q rank=%d follow=%t", ns, rank, follow)
	}
	if _, _, _, err := parseTrainingLogsArgs([]string{"--rank", "-1"}); err == nil {
		t.Fatal("negative rank accepted")
	}
}

func TestTrainingMetricSnapshot(t *testing.T) {
	metrics := `gpu_lab_training_step{rank="1"} 42
gpu_lab_training_loss{rank="1"} 0.125
gpu_lab_training_allreduce_seconds{rank="1"} 0.03
gpu_lab_training_restarts_total{rank="1"} 2
gpu_lab_training_allreduce_errors_total{rank="1"} 1
`
	got := trainingMetricSnapshot(metrics)
	if got["gpu_lab_training_step"] != "42" || got["gpu_lab_training_loss"] != "0.125" || got["gpu_lab_training_checkpoint_step"] != "n/a" {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestParseIBStatusArgs(t *testing.T) {
	o, err := parseIBStatusArgs([]string{"--node", "gpu-node-02", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if o.node != "gpu-node-02" || !o.json {
		t.Fatalf("options = %#v", o)
	}
	for _, args := range [][]string{{"--node"}, {"--node", "bad node"}, {"--unknown"}} {
		if _, err := parseIBStatusArgs(args); err == nil {
			t.Fatalf("parseIBStatusArgs(%v) accepted invalid input", args)
		}
	}
}

func TestJoinIBSamplesSortsAndFilters(t *testing.T) {
	results := map[string]monitoring.QueryResult{
		"gpu_lab_ib_port_up": {Samples: []monitoring.Sample{
			{Metric: map[string]string{"node": "gpu-node-02", "hca": "mlx5_1", "port": "2"}, Value: 1},
			{Metric: map[string]string{"node": "gpu-node-01", "hca": "mlx5_0", "port": "1"}, Value: 1},
		}},
		"gpu_lab_ib_port_state": {Samples: []monitoring.Sample{
			{Metric: map[string]string{"node": "gpu-node-02", "hca": "mlx5_1", "port": "2", "link_layer": "InfiniBand", "state": "ACTIVE", "physical_state": "LINK_UP"}, Value: 1},
			{Metric: map[string]string{"node": "gpu-node-01", "hca": "mlx5_0", "port": "1", "link_layer": "InfiniBand", "state": "DOWN", "physical_state": "POLLING"}, Value: 0},
		}},
		"gpu_lab_ib_link_rate_gbps":  {Samples: []monitoring.Sample{{Metric: map[string]string{"node": "gpu-node-01", "hca": "mlx5_0", "port": "1"}, Value: 200}}},
		"gpu_lab_ib_tx_bytes_total":  {Samples: []monitoring.Sample{{Metric: map[string]string{"node": "gpu-node-01", "hca": "mlx5_0", "port": "1"}, Value: 10}}},
		"gpu_lab_ib_rx_bytes_total":  {Samples: []monitoring.Sample{{Metric: map[string]string{"node": "gpu-node-01", "hca": "mlx5_0", "port": "1"}, Value: 20}}},
		"gpu_lab_rdma_retries_total": {Samples: []monitoring.Sample{{Metric: map[string]string{"node": "gpu-node-01", "hca": "mlx5_0", "port": "1"}, Value: 3}}},
	}
	rows := joinIBSamples(results, "")
	if len(rows) != 2 || rows[0].Node != "gpu-node-01" || rows[1].Node != "gpu-node-02" {
		t.Fatalf("rows = %#v, want stable node ordering", rows)
	}
	if rows[0].State != "DOWN" || rows[0].PhysicalState != "POLLING" || rows[0].RateGbps != 200 || rows[0].RDMARetries != 3 {
		t.Fatalf("joined row = %#v", rows[0])
	}
	filtered := joinIBSamples(results, "gpu-node-02")
	if len(filtered) != 1 || filtered[0].HCA != "mlx5_1" {
		t.Fatalf("filtered rows = %#v", filtered)
	}
}

func TestIBStatusJSONIsValid(t *testing.T) {
	var out bytes.Buffer
	rows := []ibPortStatus{{Node: "gpu-node-01", HCA: "mlx5_0", Port: "1", State: "ACTIVE", PhysicalState: "LINK_UP", Up: 1}}
	if err := writeJSON(&out, rows); err != nil {
		t.Fatal(err)
	}
	var decoded []ibPortStatus
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].State != "ACTIVE" || decoded[0].Up != 1 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestQueryIBStatusFailsClearlyWithoutFabricSamples(t *testing.T) {
	_, err := queryIBStatus(context.Background(), fakePrometheusQuerier{}, "")
	if err == nil || !strings.Contains(err.Error(), "no InfiniBand/RDMA fabric samples") {
		t.Fatalf("error = %v, want clear no-samples error", err)
	}
}

type fakePrometheusQuerier struct {
	results map[string]monitoring.QueryResult
}

func (f fakePrometheusQuerier) Query(_ context.Context, metric string) (monitoring.QueryResult, error) {
	return f.results[metric], nil
}
