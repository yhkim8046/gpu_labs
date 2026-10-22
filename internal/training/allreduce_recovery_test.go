package training

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCoordinatorCanceledAttemptPreservesPeer(t *testing.T) {
	c, err := NewCoordinator(3, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	finishedCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Handler().ServeHTTP(w, r)
		if r.Context().Err() != nil {
			select {
			case finishedCanceled <- struct{}{}:
			default:
			}
		}
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	attempt, stopAttempt := context.WithCancel(ctx)
	defer stopAttempt()
	type result struct {
		average float64
		err     error
	}
	canceled := make(chan result, 1)
	peer := make(chan result, 1)
	go func() {
		avg, err := Reduce(attempt, http.DefaultClient, addr, reduceRequest{Step: 1, Rank: 0, Gradient: 2})
		canceled <- result{avg, err}
	}()
	go func() {
		avg, err := Reduce(ctx, http.DefaultClient, addr, reduceRequest{Step: 1, Rank: 1, Gradient: 4})
		peer <- result{avg, err}
	}()
	deadline := time.After(time.Second)
	for {
		c.mu.Lock()
		round := c.rounds[1]
		ready := round != nil && len(round.values) == 2
		c.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("initial contributions did not arrive")
		case <-time.After(time.Millisecond):
		}
	}
	stopAttempt()
	if got := <-canceled; got.err == nil {
		t.Fatal("canceled request succeeded without the final rank")
	}
	// Wait for the canceled handler to finish before the retry arrives.
	select {
	case <-finishedCanceled:
	case <-ctx.Done():
		t.Fatal("canceled handler did not finish")
	}
	retry := make(chan result, 1)
	go func() {
		avg, err := Reduce(ctx, http.DefaultClient, addr, reduceRequest{Step: 1, Rank: 0, Gradient: 2})
		retry <- result{avg, err}
	}()
	last, err := Reduce(ctx, http.DefaultClient, addr, reduceRequest{Step: 1, Rank: 2, Gradient: 6})
	if err != nil || last != 4 {
		t.Fatalf("last rank: average=%v error=%v", last, err)
	}
	for _, ch := range []<-chan result{peer, retry} {
		got := <-ch
		if got.err != nil || got.average != 4 {
			t.Fatalf("collective did not survive a canceled attempt: average=%v error=%v", got.average, got.err)
		}
	}
}
