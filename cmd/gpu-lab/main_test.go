package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	if err := run(context.Background(), []string{"helm", "install", "demo", "repo/chart", "--namespace", "lab"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"install", "demo", "repo/chart", "--namespace", "lab"}
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
