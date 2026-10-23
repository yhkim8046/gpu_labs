package stateclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/testutil"
)

type snapshot struct {
	Node string `json:"node"`
}

func TestValidateURLAcceptsAbsoluteEndpoint(t *testing.T) {
	parsed, err := ValidateURL("http://gpu-lab.test/api/v1/state")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "gpu-lab.test" {
		t.Fatalf("parsed host = %q", parsed.Host)
	}
}

func TestValidateURLRejectsRelativeEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "/api/v1/state", "gpu-lab.test/api/v1/state", "://broken"} {
		_, err := ValidateURL(endpoint)
		if err == nil {
			t.Fatalf("ValidateURL(%q) accepted a non-absolute endpoint", endpoint)
		}
		var fetchErr *Error
		if !errors.As(err, &fetchErr) || fetchErr.Kind != InvalidURL {
			t.Fatalf("ValidateURL(%q) error = %v, want InvalidURL", endpoint, err)
		}
		if got, want := err.Error(), "invalid GPU_LAB_STATE_URL \""+endpoint+"\""; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	}
}

func TestFetchSuccessDecodesSnapshot(t *testing.T) {
	client := testutil.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"node":"gpu-lab-worker"}`)
	}))
	var got snapshot
	if err := Fetch(context.Background(), client, "http://gpu-lab.test/api/v1/state", &got); err != nil {
		t.Fatal(err)
	}
	if got.Node != "gpu-lab-worker" {
		t.Fatalf("decoded snapshot = %#v", got)
	}
}

func TestFetchReportsNon2xxStatus(t *testing.T) {
	client := testutil.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	err := Fetch(context.Background(), client, "http://gpu-lab.test/api/v1/state", &snapshot{})
	var fetchErr *Error
	if !errors.As(err, &fetchErr) || fetchErr.Kind != Status {
		t.Fatalf("error = %v, want Status kind", err)
	}
	if fetchErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode = %d, want 503", fetchErr.StatusCode)
	}
	if !strings.HasPrefix(err.Error(), "state endpoint returned ") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestFetchReportsTransportError(t *testing.T) {
	sentinel := errors.New("dial failed")
	client := &http.Client{Transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	})}
	err := Fetch(context.Background(), client, "http://gpu-lab.test/api/v1/state", &snapshot{})
	var fetchErr *Error
	if !errors.As(err, &fetchErr) || fetchErr.Kind != Transport {
		t.Fatalf("error = %v, want Transport kind", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("transport error not wrapped: %v", err)
	}
}

func TestFetchReportsDecodeError(t *testing.T) {
	client := testutil.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	err := Fetch(context.Background(), client, "http://gpu-lab.test/api/v1/state", &snapshot{})
	var fetchErr *Error
	if !errors.As(err, &fetchErr) || fetchErr.Kind != Decode {
		t.Fatalf("error = %v, want Decode kind", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("decode error not wrapped: %v", err)
	}
}

func TestFetchValidatesURLBeforeTransport(t *testing.T) {
	client := testutil.HandlerClient(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("Fetch reached the transport with an invalid URL")
	}))
	err := Fetch(context.Background(), client, "/not-absolute", &snapshot{})
	var fetchErr *Error
	if !errors.As(err, &fetchErr) || fetchErr.Kind != InvalidURL {
		t.Fatalf("error = %v, want InvalidURL kind", err)
	}
}

func TestDefaultClientHasFiveSecondTimeout(t *testing.T) {
	if got, want := DefaultClient().Timeout, DefaultTimeout; got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
}
