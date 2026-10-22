package training

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const sessionChangedHeader = "X-Gpu-Lab-Session-Changed"
const trainingProtocolHeader = "X-Gpu-Lab-Training-Protocol"

var errSessionChanged = errors.New("training session changed; rejoin from checkpoint")

type joinRequest struct {
	Rank        int    `json:"rank"`
	Step        int64  `json:"checkpoint_step"`
	Incarnation string `json:"incarnation"`
}

type joinResponse struct {
	Session string `json:"session"`
	Step    int64  `json:"step"`
}

type trainingSession struct {
	id      string
	members map[int]joinRequest
	ready   chan struct{}
	changed chan struct{}
	started bool
	step    int64
}

// Every process joins before reducing. A restarted rank creates a new session;
// all peers then rendezvous at their lowest available checkpoint. This also
// handles coordinator restarts, when the completed-round cache is gone.
func (c *Coordinator) join(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(trainingProtocolHeader, "2")
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req joinRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Rank < 0 || req.Rank >= c.world || req.Step < 0 || req.Incarnation == "" {
		http.Error(w, "invalid join request", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	if c.session == nil || (c.session.members[req.Rank].Incarnation != "" && c.session.members[req.Rank].Incarnation != req.Incarnation) {
		if c.session != nil {
			close(c.session.changed)
		}
		c.session = &trainingSession{id: rand.Text(), members: map[int]joinRequest{}, ready: make(chan struct{}), changed: make(chan struct{})}
		c.rounds = map[int64]*reduceRound{}
		c.completed = map[int64]float64{}
	}
	session := c.session
	if _, exists := session.members[req.Rank]; !exists {
		session.members[req.Rank] = req
	}
	if !session.started && len(session.members) == c.world {
		session.step = req.Step
		for _, member := range session.members {
			if member.Step < session.step {
				session.step = member.Step
			}
		}
		session.started = true
		close(session.ready)
	}
	c.mu.Unlock()
	select {
	case <-session.changed:
		writeSessionChanged(w)
	case <-session.ready:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(joinResponse{Session: session.id, Step: session.step})
	case <-r.Context().Done():
		return
	}
}

func writeSessionChanged(w http.ResponseWriter) {
	w.Header().Set(sessionChangedHeader, "1")
	http.Error(w, errSessionChanged.Error(), http.StatusConflict)
}

func joinWithRetry(ctx context.Context, client *http.Client, addr string, req joinRequest, timeout time.Duration) (joinResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return joinResponse{}, err
	}
	for {
		attempt, cancel := context.WithTimeout(ctx, timeout)
		request, err := http.NewRequestWithContext(attempt, http.MethodPost, "http://"+addr+"/join", bytes.NewReader(data))
		if err != nil {
			cancel()
			return joinResponse{}, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err == nil {
			var joined joinResponse
			permanent := false
			if response.StatusCode == http.StatusOK {
				err = json.NewDecoder(response.Body).Decode(&joined)
				if err == nil && (joined.Session == "" || joined.Step < 0 || joined.Step > req.Step) {
					err = fmt.Errorf("invalid join response: session=%q step=%d", joined.Session, joined.Step)
				}
				permanent = err != nil
			} else {
				err = fmt.Errorf("join coordinator returned %s", response.Status)
				// Old coordinators routed every POST to /allreduce. They reject
				// checkpoint_step rather than treating a join as a gradient.
				// Wait for rank 0 to roll forward while local readiness lets the
				// StatefulSet continue replacing the other ranks.
				permanent = response.Header.Get(trainingProtocolHeader) == "2" && response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusConflict
			}
			_ = response.Body.Close()
			cancel()
			if err == nil {
				return joined, nil
			}
			if permanent {
				return joinResponse{}, err
			}
		} else {
			cancel()
		}
		// Stay alive through slow Pod startup and coordinator replacement.
		// The worker's parent context, rather than a short retry budget, owns
		// the lifetime; per-attempt timeouts still bound every request.
		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return joinResponse{}, err
		}
	}
}
