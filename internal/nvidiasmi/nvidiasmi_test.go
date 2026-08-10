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

func TestRunQueryCommonFieldsCSV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"node":"gpu-node-01","readings":[{"gpu":"gpu-node-01-00","utilization_percent":15,"memory_used_bytes":4294967296,"memory_total_bytes":17179869184,"temperature_celsius":45,"power_watts":80,"xid_code":0,"health":1,"ecc_dbe_total":0,"throttle_active":1,"allocated":0}]}`)
	}))
	defer server.Close()
	t.Setenv("GPU_LAB_STATE_URL", server.URL)

	var stdout strings.Builder
	args := []string{
		"--query-gpu=driver_version,pci.bus_id,serial,memory.free,utilization.memory,fan.speed,clocks.current.graphics,display_active,display_mode,persistence_mode,mig.mode.current",
		"--format=csv,noheader,nounits",
	}
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), args, &stdout); err != nil {
		t.Fatal(err)
	}

	identity := newGPU("gpu-node-01", "gpu-node-01-00")
	want := strings.Join([]string{
		syntheticDriverVersion,
		"00000000:19:00.0",
		serial(*identity),
		"12288",
		"25",
		syntheticUnavailable,
		syntheticUnavailable,
		"Disabled",
		"Disabled",
		"Enabled",
		"Disabled",
	}, ", ")
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestQueryValueDerivedMemorySemantics(t *testing.T) {
	gpu := GPU{
		Node:             "gpu-node-01",
		ID:               "gpu-node-01-00",
		MemoryUsedBytes:  4 * 1024 * 1024,
		MemoryTotalBytes: 16 * 1024 * 1024,
	}

	tests := []struct {
		name   string
		field  string
		noUnit bool
		want   string
	}{
		{name: "free with units", field: "memory.free", want: "12 MiB"},
		{name: "free without units", field: "memory.free", noUnit: true, want: "12"},
		{name: "memory utilization with units", field: "utilization.memory", want: "25 %"},
		{name: "memory utilization without units", field: "utilization.memory", noUnit: true, want: "25"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := queryValue(gpu, test.field, test.noUnit); got != test.want {
				t.Fatalf("queryValue(%q, noUnits=%t) = %q, want %q", test.field, test.noUnit, got, test.want)
			}
		})
	}

	gpu.MemoryUsedBytes = 20 * 1024 * 1024
	if got := queryValue(gpu, "memory.free", true); got != "0" {
		t.Fatalf("saturated memory.free = %q, want 0", got)
	}
	if got := queryValue(gpu, "utilization.memory", true); got != "100" {
		t.Fatalf("saturated utilization.memory = %q, want 100", got)
	}

	gpu.MemoryTotalBytes = 0
	if got := queryValue(gpu, "memory.free", false); got != syntheticUnavailable {
		t.Fatalf("missing memory.free = %q, want %q", got, syntheticUnavailable)
	}
	if got := queryValue(gpu, "utilization.memory", false); got != syntheticUnavailable {
		t.Fatalf("missing utilization.memory = %q, want %q", got, syntheticUnavailable)
	}
}

func TestSyntheticIdentityQueryFieldsAreStable(t *testing.T) {
	first := newGPU("gpu-node-01", "gpu-node-01-00")
	second := newGPU("gpu-node-01", "gpu-node-01-01")
	second.Index = 1
	otherNode := newGPU("gpu-node-02", "gpu-node-02-00")

	if got, want := queryValue(*first, "driver_version", true), syntheticDriverVersion; got != want {
		t.Fatalf("driver_version = %q, want %q", got, want)
	}
	if got, want := queryValue(*first, "pci.bus_id", true), "00000000:19:00.0"; got != want {
		t.Fatalf("first pci.bus_id = %q, want %q", got, want)
	}
	if got, want := queryValue(*second, "pci.bus_id", true), "00000000:3B:00.0"; got != want {
		t.Fatalf("second pci.bus_id = %q, want %q", got, want)
	}

	firstSerial := queryValue(*first, "serial", true)
	if !strings.HasPrefix(firstSerial, syntheticSerialPrefix) {
		t.Fatalf("serial = %q, want synthetic prefix %q", firstSerial, syntheticSerialPrefix)
	}
	if got := queryValue(*first, "serial", true); got != firstSerial {
		t.Fatalf("serial changed between reads: first=%q second=%q", firstSerial, got)
	}
	if got := queryValue(*otherNode, "serial", true); got == firstSerial {
		t.Fatalf("serial collision across synthetic GPUs: %q", got)
	}
}

func TestQueryValueUnavailableAndHeadlessSemantics(t *testing.T) {
	gpu := *newGPU("gpu-node-01", "gpu-node-01-00")
	for _, field := range []string{"fan.speed", "clocks.current.graphics"} {
		if got := queryValue(gpu, field, false); got != syntheticUnavailable {
			t.Errorf("queryValue(%q) = %q, want %q", field, got, syntheticUnavailable)
		}
	}
	for _, field := range []string{"display_active", "display_mode", "mig.mode.current"} {
		if got := queryValue(gpu, field, false); got != "Disabled" {
			t.Errorf("queryValue(%q) = %q, want Disabled", field, got)
		}
	}
	if got := queryValue(gpu, "persistence_mode", false); got != "Enabled" {
		t.Fatalf("persistence_mode = %q, want Enabled", got)
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
	if _, err := parseArgs([]string{"--query-gpu=temperature.memory"}); err == nil {
		t.Fatal("parseArgs() succeeded for unsupported field")
	}
}

func TestParseArgsAcceptsCommonFields(t *testing.T) {
	parsed, err := parseArgs([]string{"--query-gpu=driver_version,pci.bus_id,serial,memory.free,utilization.memory,fan.speed,clocks.current.graphics,display_active"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.query) != 8 {
		t.Fatalf("parsed query fields = %#v, want 8 fields", parsed.query)
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
	for _, field := range []string{"driver_version", "pci.bus_id", "serial", "memory.free", "utilization.memory", "fan.speed", "clocks.current.graphics", "display_active"} {
		if !strings.Contains(stdout.String(), field) {
			t.Fatalf("help output = %q, missing supported field %q", stdout.String(), field)
		}
	}
}
