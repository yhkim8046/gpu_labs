// Package testutil provides small helpers shared by GPU Lab tests.
package testutil

import (
	"net/http"
	"net/http/httptest"
)

// RoundTripFunc adapts a function to an http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// HandlerClient returns an HTTP client that serves requests directly through
// handler without opening a network listener.
func HandlerClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		serverRequest := request.Clone(request.Context())
		serverRequest.RequestURI = request.URL.RequestURI()
		if serverRequest.Host == "" {
			serverRequest.Host = request.URL.Host
		}
		if serverRequest.Body != nil {
			defer serverRequest.Body.Close()
		}

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, serverRequest)
		response := recorder.Result()
		response.Request = request
		return response, nil
	})}
}
