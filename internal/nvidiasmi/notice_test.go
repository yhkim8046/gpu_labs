package nvidiasmi

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func TestSyntheticNoticeUsesStderrAndAllowsNilWriter(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := runner.Runner{Stdout: &stdout, Stderr: &stderr}
	if err := run(context.Background(), r, []string{"--help"}, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "GPU Lab — unofficial") || !strings.Contains(stderr.String(), "mode=synthetic") {
		t.Fatalf("stderr notice = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") || strings.Contains(stdout.String(), "GPU Lab — unofficial") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if err := run(context.Background(), runner.Runner{Stdout: &stdout}, []string{"--help"}, &stdout, nil); err != nil {
		t.Fatalf("nil stderr writer panicked or failed: %v", err)
	}
}
