package nvidiasmi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func TestRunSummaryFromStateEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"node":"gpu-node-01","readings":[{"gpu":"gpu-node-01-00","utilization_percent":95,"memory_used_bytes":16106127360,"memory_total_bytes":17179869184,"temperature_celsius":82,"power_watts":240,"xid_code":79,"health":0,"ecc_dbe_total":0,"throttle_active":1,"allocated":1}]}`)
	}))
	defer server.Close()
	t.Setenv("GPU_LAB_STATE_URL", server.URL)

	var stdout strings.Builder
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), nil, &stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, expected := range []string{"NVIDIA-SMI 550.163.01", "NVIDIA H200", "82C", "95%", "79", "15360MiB"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output = %q, missing %q", output, expected)
		}
	}
	for _, expected := range []string{"Persistence-M", "Bus-Id", "Memory-Usage", "GPU-Util  Compute M.", "Processes:", "No running processes found"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output = %q, missing nvidia-smi section %q", output, expected)
		}
	}
}

func TestRunQueryCSV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"node":"gpu-node-01","readings":[{"gpu":"gpu-node-01-00","utilization_percent":15,"memory_used_bytes":3355443200,"memory_total_bytes":17179869184,"temperature_celsius":45,"power_watts":80,"xid_code":0,"health":1,"ecc_dbe_total":0,"throttle_active":0,"allocated":0}]}`)
	}))
	defer server.Close()
	t.Setenv("GPU_LAB_STATE_URL", server.URL)

	var stdout strings.Builder
	args := []string{"--query-gpu=temperature.gpu,memory.used,utilization.gpu", "--format=csv,noheader,nounits"}
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), args, &stdout); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(stdout.String()), "45, 3200, 15"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunSummaryFromPrometheusViaKubectl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX syntax")
	}
	t.Setenv("GPU_LAB_STATE_URL", "")
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	response := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"gpu_lab_gpu_utilization_percent","node":"gpu-node-01","gpu":"gpu-node-01-00"},"value":["0","15"]},{"metric":{"__name__":"gpu_lab_gpu_memory_used_bytes","node":"gpu-node-01","gpu":"gpu-node-01-00"},"value":["0","3355443200"]},{"metric":{"__name__":"gpu_lab_gpu_memory_total_bytes","node":"gpu-node-01","gpu":"gpu-node-01-00"},"value":["0","17179869184"]}]}}`
	script := "#!/bin/sh\nprintf '%s\\n' '" + response + "'\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout strings.Builder
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), nil, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "3200MiB /") || !strings.Contains(stdout.String(), "16384MiB") {
		t.Fatalf("output = %q, want Prometheus-derived memory", stdout.String())
	}
}

func TestRunSummaryGroupsSyntheticNodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX syntax")
	}
	t.Setenv("GPU_LAB_STATE_URL", "")
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	response := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"gpu_lab_gpu_utilization_percent","node":"gpu-node-01","gpu":"gpu-node-01-00"},"value":["0","15"]},{"metric":{"__name__":"gpu_lab_gpu_utilization_percent","node":"gpu-node-02","gpu":"gpu-node-02-00"},"value":["0","25"]}]}}`
	script := "#!/bin/sh\nprintf '%s\\n' '" + response + "'\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout strings.Builder
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), nil, &stdout); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if got := strings.Count(output, "GPU Lab node:"); got != 2 {
		t.Fatalf("node block count = %d, want 2; output = %q", got, output)
	}
	for _, node := range []string{"gpu-node-01", "gpu-node-02"} {
		if !strings.Contains(output, "GPU Lab node: "+node) {
			t.Fatalf("output = %q, missing node %q", output, node)
		}
	}
}

func TestParseArgsNodeSelector(t *testing.T) {
	parsed, err := parseArgs([]string{"--node", "gpu-node-02", "--list-gpus"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.node != "gpu-node-02" || !parsed.list {
		t.Fatalf("parsed = %#v, want node selector and list flag", parsed)
	}
	if _, err := parseArgs([]string{"--node="}); err == nil {
		t.Fatal("parseArgs() accepted an empty node selector")
	}
}

func TestParseArgsRejectsUnsupportedField(t *testing.T) {
	if _, err := parseArgs([]string{"--query-gpu=fan.speed"}); err == nil {
		t.Fatal("parseArgs() succeeded for unsupported field")
	}
}

func TestRunHelpDoesNotQueryCluster(t *testing.T) {
	var stdout strings.Builder
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), []string{"--help"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "nvidia-smi") {
		t.Fatalf("help output = %q", stdout.String())
	}
}
