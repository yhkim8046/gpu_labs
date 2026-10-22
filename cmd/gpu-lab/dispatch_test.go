package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/inspection"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func TestGlobalModePrecedenceAndSuffixPreservation(t *testing.T) {
	mode, remaining, err := parseGlobalMode([]string{"--mode=synthetic", "nvidia-smi", "--query-gpu=name", "--mode", "vendor-value"}, "invalid-env")
	if err != nil {
		t.Fatal(err)
	}
	if mode != inspection.ModeSynthetic {
		t.Fatalf("mode = %q", mode)
	}
	want := []string{"nvidia-smi", "--query-gpu=name", "--mode", "vendor-value"}
	if strings.Join(remaining, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("suffix changed: %#v", remaining)
	}

	mode, remaining, err = parseGlobalMode([]string{"inspect", "--mode", "real", "--json"}, "invalid-env")
	if err != nil || mode != inspection.ModeAuto || !hasInspectModeSelector(remaining[1:]) {
		t.Fatalf("inspect override did not supersede invalid env: mode=%q args=%#v err=%v", mode, remaining, err)
	}
	if _, _, err := parseGlobalMode([]string{"status"}, "invalid-env"); err == nil {
		t.Fatal("invalid GPU_LAB_MODE without a CLI selector was accepted")
	}
}

func TestInspectHelpAndOptionValidation(t *testing.T) {
	var stdout bytes.Buffer
	if err := inspectCommand(context.Background(), []string{"--help"}, inspection.ModeAuto, &stdout); err != nil {
		t.Fatalf("inspect --help returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Usage: gpu inspect") {
		t.Fatalf("help output = %q", stdout.String())
	}
	for _, args := range [][]string{{"--json", "--json"}, {"--mode", "nope"}, {"--timeout", "soon"}, {"--json=true"}} {
		if err := inspectCommand(context.Background(), args, inspection.ModeAuto, &bytes.Buffer{}); err == nil {
			t.Errorf("inspect args %#v unexpectedly succeeded", args)
		}
	}
}

func TestInspectJSONRemainsValidOnCollectionErrorAndOverridesInvalidEnv(t *testing.T) {
	t.Setenv("GPU_LAB_MODE", "invalid-env")
	t.Setenv("GPU_LAB_NVIDIA_SMI", "relative-nvidia-smi")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"inspect", "--mode=real", "--json"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected local tool validation failure")
	}
	var report inspection.Report
	if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", decodeErr, stdout.String())
	}
	if report.Mode != inspection.ModeReal || !report.Unofficial || !strings.Contains(report.Disclaimer, "not affiliated with or endorsed by NVIDIA") {
		t.Fatalf("report metadata = %#v", report)
	}
	if stderr.Len() != 0 {
		t.Fatalf("collection failure polluted stderr before main error handling: %q", stderr.String())
	}
}

func TestSyntheticNativeCSVKeepsNoticeOffStdout(t *testing.T) {
	var state strings.Builder
	state.WriteString(`{"node":"gpu-lab-worker1","readings":[`)
	for i := 0; i < 24; i++ {
		if i > 0 {
			state.WriteByte(',')
		}
		fmt.Fprintf(&state, `{"gpu":"gpu-%02d","temperature_celsius":42,"memory_used_bytes":1024,"memory_total_bytes":8192,"utilization_percent":7,"health":1,"xid_code":0,"ecc_dbe_total":0}`, i)
	}
	state.WriteString(`]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, state.String()) }))
	defer server.Close()
	t.Setenv("GPU_LAB_STATE_URL", server.URL)
	t.Setenv("GPU_LAB_NVIDIA_SMI", "")
	var stdout, stderr bytes.Buffer
	r := runner.New(&stdout, &stderr)
	args := []string{"--query-gpu=temperature.gpu,memory.used,utilization.gpu", "--format=csv,noheader,nounits"}
	if err := nativeCommand(context.Background(), r, "nvidia-smi", args, inspection.ModeSynthetic, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(rows) != 24 {
		t.Fatalf("CSV rows = %d, stdout=%q", len(rows), stdout.String())
	}
	for _, row := range rows {
		if strings.Count(row, ",") != 2 {
			t.Errorf("CSV row has unexpected shape: %q", row)
			break
		}
	}
	if !strings.Contains(stderr.String(), "GPU Lab — unofficial") || !strings.Contains(stderr.String(), "mode=synthetic") {
		t.Fatalf("stderr notice = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "unofficial") || strings.Contains(stdout.String(), "mode=synthetic") {
		t.Fatalf("notice contaminated CSV stdout: %q", stdout.String())
	}
}

func TestNativeSelectionFailureStillMarksStderr(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	r := runner.New(&stdout, &stderr)
	err := nativeCommand(context.Background(), r, "dcgmi", []string{"discovery", "-l"}, inspection.ModeReal, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unavailable real dcgmi error")
	}
	if !strings.Contains(stderr.String(), "GPU Lab — unofficial") || !strings.Contains(stderr.String(), "mode=real") {
		t.Fatalf("failure notice = %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("selection error wrote to stdout: %q", stdout.String())
	}
}

func TestMainDispatchKeepsVendorSuffixAfterCommandUntouched(t *testing.T) {
	args := []string{"--mode", "real", "nvidia-smi", "--query-gpu=temperature.gpu,memory.used", "--format=csv,noheader,nounits"}
	mode, remaining, err := parseGlobalMode(args, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if mode != inspection.ModeReal {
		t.Fatalf("global selector = %q", mode)
	}
	want := args[2:]
	if strings.Join(remaining, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("native command suffix = %#v, want %#v", remaining, want)
	}
}

func TestDedicatedKubeconfigPathIsNotWrittenByInspectModeParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kube", "gpu-lab.config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspectCommand(context.Background(), []string{"--help"}, inspection.ModeAuto, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatal("inspect option parsing changed the kubeconfig")
	}
}
