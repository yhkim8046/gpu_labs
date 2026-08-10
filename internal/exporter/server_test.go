package exporter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

func TestHTTPFaultMode(t *testing.T) {
	m := NewModel("gpu-node-01", 1)
	s := NewServer(m, "")
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("normal status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	down, err := scenario.LoadBuiltin("exporter-down")
	if err != nil {
		t.Fatal(err)
	}
	m.Apply(down, "2")
	response, err = http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("fault status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
}

func TestHTTPFaultModeRecoversAfterDuration(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	m := NewModel("gpu-node-01", 1)
	m.now = func() time.Time { return now }
	down, err := scenario.LoadBuiltin("exporter-down")
	if err != nil {
		t.Fatal(err)
	}
	down.Spec.Duration = "5s"
	m.Apply(down, "45")

	server := httptest.NewServer(NewServer(m, "").Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("active fault status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	_ = response.Body.Close()

	now = now.Add(5 * time.Second)
	response, err = http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("recovered status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), `gpu_lab_scenario_info{node="gpu-node-01",scenario="normal"} 1`) {
		t.Fatal("recovered exporter did not expose normal scenario telemetry")
	}
	if m.IsUnavailable() {
		t.Fatal("exporter fault did not recover after duration")
	}
	if got := m.Snapshot(); got.Generation != "45" {
		t.Fatalf("recovered generation = %q, want 45", got.Generation)
	}
}
