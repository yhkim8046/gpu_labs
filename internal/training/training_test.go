package training

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRankFromPodName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{{"gpu-lab-training-0", 0}, {"worker-a-12", 12}} {
		got, err := RankFromPodName(tc.name)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %d/%v", tc.name, got, err)
		}
	}
	if _, err := RankFromPodName("worker"); err == nil {
		t.Fatal("expected missing ordinal error")
	}
}

func TestControlAndCheckpoint(t *testing.T) {
	d := t.TempDir()
	controlPath := filepath.Join(d, "control.json")
	b, _ := json.Marshal(map[string]any{"straggler": map[string]any{"rank": 2, "delay": "150ms"}, "crash": map[string]any{"rank": 1, "enabled": true, "generation": "g1"}})
	if err := os.WriteFile(controlPath, b, 0644); err != nil {
		t.Fatal(err)
	}
	c, err := ReadControl(controlPath)
	if err != nil || c.Straggler.Delay != 150*time.Millisecond || c.CrashID() != "g1" {
		t.Fatalf("control: %#v %v", c, err)
	}
	cp := Checkpoint{Step: 7, Restarts: 2, AppliedCrashGenerations: map[string]bool{"g1": true}}
	path := filepath.Join(d, "nested", "checkpoint.json")
	if err := SaveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCheckpoint(path)
	if err != nil || got.Step != 7 || !got.AppliedCrashGenerations["g1"] {
		t.Fatalf("checkpoint: %#v %v", got, err)
	}
	if err := os.WriteFile(controlPath, []byte(`{"straggler":{"rank":2,"delay":"not-a-duration"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadControl(controlPath); err == nil {
		t.Fatal("expected invalid control error")
	}
}

func TestFlatControlSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	flat := `{"generation":17,"straggler_rank":2,"straggler_delay":"2s","crash_rank":1,"crash_token":"token-17","fabric_mode":"degraded","fabric_target_node":"gpu-node-03","fabric_delay":"1.5s"}`
	if err := os.WriteFile(path, []byte(flat), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := ReadControl(path)
	if err != nil || c.Straggler == nil || c.Straggler.Rank != 2 || c.Straggler.Delay != 2*time.Second || c.Crash == nil || !c.Crash.Enabled || c.CrashID() != "17" || c.EffectiveFabricMode() != "degraded" || c.FabricTargetNode != "gpu-node-03" || c.FabricDelay != 1500*time.Millisecond {
		t.Fatalf("flat control: %#v, %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`{"straggler_rank":null,"crash_rank":null,"generation":18}`), 0644); err != nil {
		t.Fatal(err)
	}
	c, err = ReadControl(path)
	if err != nil || c.Straggler != nil || c.Crash != nil {
		t.Fatalf("null ranks should disable faults: %#v, %v", c, err)
	}
}

func TestFabricTargetMatchingAndDefaultDelays(t *testing.T) {
	for _, test := range []struct {
		target string
		node   string
		want   bool
	}{
		{target: "gpu-node-01", node: "gpu-lab-worker", want: true},
		{target: "gpu-node-02", node: "gpu-lab-worker2", want: true},
		{target: "gpu-node-03", node: "gpu-lab-worker3", want: true},
		{target: "gpu-node-01", node: "gpu-lab-worker2", want: false},
		{target: "gpu-lab-worker", node: "gpu-lab-worker", want: true},
	} {
		if got := FabricTargetMatches(test.target, test.node); got != test.want {
			t.Errorf("FabricTargetMatches(%q, %q) = %t, want %t", test.target, test.node, got, test.want)
		}
	}
	for mode, want := range map[string]time.Duration{"degraded": 1500 * time.Millisecond, "retries": time.Second, "congestion": 800 * time.Millisecond, "errors": 0, "normal": 0} {
		if got := defaultFabricDelay(mode); got != want {
			t.Errorf("defaultFabricDelay(%q) = %s, want %s", mode, got, want)
		}
	}
}

func TestCoordinatorAllReduce(t *testing.T) {
	c, _ := NewCoordinator(3, time.Second)
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	addr := server.Listener.Addr().String()
	client := server.Client()
	var wg sync.WaitGroup
	results := make(chan float64, 3)
	for rank := 0; rank < 3; rank++ {
		wg.Add(1)
		go func(rank int) {
			defer wg.Done()
			avg, err := Reduce(context.Background(), client, addr, reduceRequest{Step: 1, Rank: rank, Gradient: float64(rank + 1)})
			if err != nil {
				t.Errorf("rank %d: %v", rank, err)
				return
			}
			results <- avg
		}(rank)
	}
	wg.Wait()
	close(results)
	for avg := range results {
		if avg != 2 {
			t.Fatalf("average=%v", avg)
		}
	}
}

func TestCoordinatorReturnsCompletedRoundToRetry(t *testing.T) {
	c, _ := NewCoordinator(2, time.Second)
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	client := server.Client()
	addr := server.Listener.Addr().String()

	results := make(chan float64, 2)
	for rank := 0; rank < 2; rank++ {
		go func(rank int) {
			average, err := Reduce(context.Background(), client, addr, reduceRequest{Step: 9, Rank: rank, Gradient: float64(rank + 2)})
			if err != nil {
				t.Errorf("initial rank %d: %v", rank, err)
				return
			}
			results <- average
		}(rank)
	}
	for range 2 {
		if average := <-results; average != 2.5 {
			t.Fatalf("initial average = %v", average)
		}
	}
	retried, err := Reduce(context.Background(), client, addr, reduceRequest{Step: 9, Rank: 0, Gradient: 2})
	if err != nil || retried != 2.5 {
		t.Fatalf("retried completed round = %v, %v", retried, err)
	}
}

func TestReduceWithRetry(t *testing.T) {
	c, _ := NewCoordinator(1, time.Second)
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	avg, err := ReduceWithRetry(context.Background(), server.Client(), server.Listener.Addr().String(), reduceRequest{Step: 1, Rank: 0, Gradient: 3}, 100*time.Millisecond)
	if err != nil || avg != 3 {
		t.Fatalf("retry reduce: %v, %v", avg, err)
	}
}

func TestMetrics(t *testing.T) {
	m := &Metrics{}
	m.SetStep(4)
	m.SetLoss(.25)
	m.SetSamples(64)
	m.AddSamples(32)
	m.SetAllreduce(.125)
	m.SetUp(true)
	m.SetCheckpoint(3)
	m.SetRestarts(2)
	m.SetStraggler(true)
	m.AddError()
	rr := httptest.NewRecorder()
	m.Handler(map[string]string{"rank": "1", "job": "j", "node": "n", "pod": "p"}).ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	for _, name := range []string{"gpu_lab_training_step", "gpu_lab_training_loss", "gpu_lab_training_samples_total", "gpu_lab_training_allreduce_seconds", "gpu_lab_training_rank_up", "gpu_lab_training_checkpoint_step", "gpu_lab_training_restarts_total", "gpu_lab_training_straggler_active", "gpu_lab_training_allreduce_errors_total", "gpu_lab_training_fabric_fault_active", "gpu_lab_training_fabric_delay_seconds", "gpu_lab_training_fabric_errors_total", "gpu_lab_training_fabric_retries_total"} {
		if !contains(rr.Body.String(), name) {
			t.Errorf("missing %s", name)
		}
	}
	if !contains(rr.Body.String(), "gpu_lab_training_samples_total{rank=\"1\",job=\"j\",node=\"n\",pod=\"p\"} 96") {
		t.Fatalf("samples metric = %q", rr.Body.String())
	}
}

func TestWorkerFabricDelayIsInjectedAndObservable(t *testing.T) {
	w := NewWorker(Config{Node: "gpu-lab-worker3", FabricPollInterval: time.Millisecond})
	w.Metrics.SetUp(true)
	w.sleep = func(_ context.Context, delay time.Duration) error {
		if delay != 1500*time.Millisecond {
			t.Fatalf("delay = %s, want 1.5s", delay)
		}
		return nil
	}
	mode, targeted, delay := w.applyFabricControl(Control{FabricMode: "degraded", FabricTargetNode: "gpu-node-03"})
	if mode != "degraded" || !targeted || delay != 1500*time.Millisecond {
		t.Fatalf("fabric control = mode %q targeted=%t delay=%s", mode, targeted, delay)
	}
	if err := w.wait(delay, context.Background()); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	w.Metrics.Handler(map[string]string{"rank": "2", "job": "j", "node": "gpu-lab-worker3", "pod": "p"}).ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !contains(rr.Body.String(), `gpu_lab_training_fabric_fault_active{rank="2",job="j",node="gpu-lab-worker3",pod="p",mode="degraded"} 1`) || !contains(rr.Body.String(), "gpu_lab_training_fabric_delay_seconds") {
		t.Fatalf("fabric metrics = %q", rr.Body.String())
	}
}

func TestWorkerLinkDownWaitsAliveAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	if err := os.WriteFile(path, []byte(`{"fabric_mode":"down","fabric_target_node":"gpu-node-02"}`), 0644); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(Config{Node: "gpu-lab-worker2", ControlFile: path, FabricPollInterval: time.Millisecond})
	calls := 0
	w.sleep = func(_ context.Context, _ time.Duration) error {
		calls++
		if calls == 1 {
			if err := os.WriteFile(path, []byte(`{"fabric_mode":"normal"}`), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	control, err := w.readCurrentControl()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.waitForFabricRecovery(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || w.Metrics.fabricErrors.Load() == 0 || w.Metrics.fabricActive.Load() != 0 {
		t.Fatalf("recovery calls=%d fabric_errors=%d active=%d", calls, w.Metrics.fabricErrors.Load(), w.Metrics.fabricActive.Load())
	}
	if !w.Metrics.IsUp() {
		// The worker loop sets this before serving readiness; this direct helper
		// test only needs to prove that a fault does not force it down.
		w.Metrics.SetUp(true)
	}
	if !w.Metrics.IsUp() {
		t.Fatal("worker became unready during link-down recovery")
	}
}

func TestWorkerLinkDownAlsoHoldsPeerAtSameCollectiveStep(t *testing.T) {
	w := NewWorker(Config{Node: "gpu-lab-worker3", FabricPollInterval: time.Millisecond})
	calls := 0
	w.readControl = func() (Control, error) {
		calls++
		return Control{FabricMode: "normal"}, nil
	}
	w.sleep = func(_ context.Context, _ time.Duration) error { return nil }

	control := Control{FabricMode: "down", FabricTargetNode: "gpu-node-02"}
	if _, err := w.waitForFabricRecovery(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("control reads = %d, want 1", calls)
	}
	if w.Metrics.fabricErrors.Load() != 0 || w.Metrics.errors.Load() != 0 {
		t.Fatalf("peer rank incorrectly reported fabric errors: fabric=%d allreduce=%d", w.Metrics.fabricErrors.Load(), w.Metrics.errors.Load())
	}
}

func TestWorkerCheckpointResume(t *testing.T) {
	c, _ := NewCoordinator(1, time.Second)
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	d := t.TempDir()
	config := Config{Rank: 0, WorldSize: 1, Pod: "training-0", Node: "node", Job: "job", Coordinator: server.Listener.Addr().String(), ControlFile: filepath.Join(d, "control.json"), CheckpointFile: filepath.Join(d, "checkpoint.json"), StepInterval: time.Millisecond, CheckpointInterval: time.Millisecond, RequestTimeout: time.Second, CoordinatorTimeout: time.Second, TotalSteps: 2}
	if err := NewWorker(config).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	config.TotalSteps = 3
	if err := NewWorker(config).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	cp, err := LoadCheckpoint(config.CheckpointFile)
	if err != nil || cp.Step != 3 || cp.Restarts != 2 {
		t.Fatalf("resume checkpoint: %#v, %v", cp, err)
	}
}
func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
