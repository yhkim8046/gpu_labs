// Package stateclient provides the shared HTTP access to GPU Lab's synthetic
// state endpoint used by the compatibility commands. It owns absolute-URL
// validation of GPU_LAB_STATE_URL, context-aware GET requests, non-2xx
// handling, and JSON decoding. Interpreting and domain-validating the decoded
// snapshot remains the responsibility of each calling package.
package stateclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// DefaultTimeout is the timeout applied to state requests when the caller
// does not configure an explicit HTTP client.
const DefaultTimeout = 5 * time.Second

// DefaultClient returns the standard HTTP client for state requests.
func DefaultClient() *http.Client {
	return &http.Client{Timeout: DefaultTimeout}
}

// Kind identifies which stage of a state fetch failed.
type Kind int

const (
	// InvalidURL means the configured endpoint is not an absolute URL.
	InvalidURL Kind = iota
	// Request means the HTTP request could not be created.
	Request
	// Transport means the GET failed before a response arrived.
	Transport
	// Status means the endpoint answered with a non-2xx status.
	Status
	// Decode means the response body was not valid JSON for the snapshot.
	Decode
)

// Error reports a state fetch failure without committing to a command-specific
// message. Callers translate the kind into their own compatibility wording so
// user-facing error prefixes stay stable.
type Error struct {
	Kind       Kind
	Endpoint   string
	Err        error
	StatusCode int
	Status     string
}

func (e *Error) Error() string {
	switch e.Kind {
	case InvalidURL:
		return fmt.Sprintf("invalid GPU_LAB_STATE_URL %q", e.Endpoint)
	case Request:
		return fmt.Sprintf("create state request: %v", e.Err)
	case Transport:
		return fmt.Sprintf("query synthetic state: %v", e.Err)
	case Status:
		return fmt.Sprintf("state endpoint returned %s", e.Status)
	case Decode:
		return fmt.Sprintf("decode synthetic state: %v", e.Err)
	default:
		return "state request failed"
	}
}

// Unwrap exposes the underlying transport, request, or decode failure.
func (e *Error) Unwrap() error { return e.Err }

// ValidateURL returns the parsed endpoint when endpoint is an absolute URL
// with both scheme and host; otherwise it returns an *Error with kind
// InvalidURL.
func ValidateURL(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &Error{Kind: InvalidURL, Endpoint: endpoint, Err: err}
	}
	return parsed, nil
}

// Fetch validates endpoint, performs a context-aware HTTP GET with client,
// rejects non-2xx responses, and JSON-decodes the body into target. Every
// failure is reported as *Error so each caller can preserve its own message
// prefixes. A nil client falls back to DefaultClient.
func Fetch(ctx context.Context, client *http.Client, endpoint string, target any) error {
	if _, err := ValidateURL(endpoint); err != nil {
		return err
	}
	if client == nil {
		client = DefaultClient()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &Error{Kind: Request, Endpoint: endpoint, Err: err}
	}
	response, err := client.Do(request)
	if err != nil {
		return &Error{Kind: Transport, Endpoint: endpoint, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return &Error{Kind: Status, Endpoint: endpoint, StatusCode: response.StatusCode, Status: response.Status}
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return &Error{Kind: Decode, Endpoint: endpoint, Err: err}
	}
	return nil
}
