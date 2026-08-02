package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
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

func TestParseMetricsArgs(t *testing.T) {
	options, err := parseMetricsArgs([]string{"--query", "max(gpu_lab_gpu_xid_code)", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if options.query != "max(gpu_lab_gpu_xid_code)" || !options.json {
		t.Fatalf("options = %#v, want query and json", options)
	}
}
