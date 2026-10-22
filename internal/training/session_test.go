package training

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJoinFailsClosedOnInvalidProtocol(t *testing.T) {
	for _, body := range []string{`{}`, `{"session":"s","step":-1}`, `{"session":"s","step":11}`, `invalid`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := joinWithRetry(ctx, server.Client(), strings.TrimPrefix(server.URL, "http://"), joinRequest{Rank: 0, Step: 10, Incarnation: "worker"}, time.Second)
			if err == nil || ctx.Err() != nil {
				t.Fatalf("invalid protocol was retried until deadline or accepted: %v", err)
			}
		})
	}
}

func TestJoinWaitsForLegacyCoordinatorUpgrade(t *testing.T) {
	coordinator, _ := NewCoordinator(1, time.Second)
	var upgraded atomic.Bool
	legacyAttempt := make(chan int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upgraded.Load() {
			coordinator.Handler().ServeHTTP(w, r)
			return
		}
		var legacy reduceRequest
		_ = json.NewDecoder(r.Body).Decode(&legacy)
		select {
		case legacyAttempt <- legacy.Step:
		default:
		}
		http.Error(w, "invalid allreduce request", http.StatusBadRequest)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		joined, err := joinWithRetry(ctx, server.Client(), strings.TrimPrefix(server.URL, "http://"), joinRequest{Rank: 0, Step: 7, Incarnation: "new-worker"}, time.Second)
		if err == nil && (joined.Session == "" || joined.Step != 7) {
			err = fmt.Errorf("invalid upgrade rendezvous: %+v", joined)
		}
		result <- err
	}()
	select {
	case step := <-legacyAttempt:
		if step != 0 {
			t.Fatalf("join was interpreted as a legacy gradient at step %d", step)
		}
	case <-ctx.Done():
		t.Fatal("legacy coordinator was not contacted")
	}
	upgraded.Store(true)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
