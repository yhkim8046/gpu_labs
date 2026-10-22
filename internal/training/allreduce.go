package training

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type reduceRequest struct {
	Step     int64   `json:"step"`
	Rank     int     `json:"rank"`
	Gradient float64 `json:"gradient"`
	Session  string  `json:"session,omitempty"`
}
type reduceResponse struct {
	Average float64 `json:"average"`
}
type reduceRound struct {
	values  map[int]float64
	average float64
	done    chan struct{}
}

type Coordinator struct {
	world     int
	timeout   time.Duration
	mu        sync.Mutex
	rounds    map[int64]*reduceRound
	completed map[int64]float64
	session   *trainingSession
}

func NewCoordinator(world int, timeout time.Duration) (*Coordinator, error) {
	if world < 1 {
		return nil, fmt.Errorf("world size must be positive")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Coordinator{world: world, timeout: timeout, rounds: map[int64]*reduceRound{}, completed: map[int64]float64{}}, nil
}
func (c *Coordinator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/allreduce", c.handle)
	mux.HandleFunc("/join", c.join)
	return mux
}
func (c *Coordinator) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var req reduceRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Step < 1 || req.Rank < 0 || req.Rank >= c.world {
		http.Error(w, "invalid allreduce request", 400)
		return
	}
	c.mu.Lock()
	var changed <-chan struct{}
	if c.session != nil {
		if req.Session != c.session.id || !c.session.started {
			c.mu.Unlock()
			writeSessionChanged(w)
			return
		}
		changed = c.session.changed
	} else if req.Session != "" {
		c.mu.Unlock()
		writeSessionChanged(w)
		return
	}
	if average, ok := c.completed[req.Step]; ok {
		c.mu.Unlock()
		writeReduceResponse(w, average)
		return
	}
	round := c.rounds[req.Step]
	if round == nil {
		if len(c.rounds) >= 128 {
			c.mu.Unlock()
			http.Error(w, "too many pending rounds", http.StatusServiceUnavailable)
			return
		}
		round = &reduceRound{values: map[int]float64{}, done: make(chan struct{})}
		c.rounds[req.Step] = round
	}
	if previous, ok := round.values[req.Rank]; ok && previous != req.Gradient {
		c.mu.Unlock()
		http.Error(w, "rank changed its contribution", http.StatusBadRequest)
		return
	}
	round.values[req.Rank] = req.Gradient
	if len(round.values) == c.world {
		var sum float64
		for _, v := range round.values {
			sum += v
		}
		round.average = sum / float64(c.world)
		c.completed[req.Step] = round.average
		delete(c.completed, req.Step-128)
		close(round.done)
		delete(c.rounds, req.Step)
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), c.timeout)
	defer cancel()
	select {
	case <-round.done:
		writeReduceResponse(w, round.average)
	case <-changed:
		writeSessionChanged(w)
	case <-ctx.Done():
		// A timeout belongs to this HTTP attempt. Contributions remain valid
		// for idempotent retries and must not strand other ranks in the round.
		http.Error(w, "allreduce timed out", 504)
	}
}

func writeReduceResponse(w http.ResponseWriter, average float64) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reduceResponse{Average: average})
}

func Reduce(ctx context.Context, client *http.Client, addr string, req reduceRequest) (float64, error) {
	b, _ := json.Marshal(req)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/allreduce", bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict && resp.Header.Get(sessionChangedHeader) == "1" {
		return 0, errSessionChanged
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("allreduce coordinator returned %s", resp.Status)
	}
	var out reduceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Average, nil
}

// ReduceWithRetry tolerates pod startup and short coordinator disruptions. A
// single attempt still has its own request timeout; the caller controls the
// overall retry budget through ctx.
func ReduceWithRetry(ctx context.Context, client *http.Client, addr string, req reduceRequest, requestTimeout time.Duration) (float64, error) {
	if requestTimeout <= 0 {
		requestTimeout = time.Second
	}
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		avg, err := Reduce(attemptCtx, client, addr, req)
		cancel()
		if err == nil {
			return avg, nil
		}
		if errors.Is(err, errSessionChanged) {
			return 0, err
		}
		last = err
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("allreduce failed after retries: %w", last)
}
