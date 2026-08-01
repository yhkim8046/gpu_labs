package exporter

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
