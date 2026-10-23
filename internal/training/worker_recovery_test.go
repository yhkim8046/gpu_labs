package training

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recoveryWorkerConfig(t *testing.T, rank int, address string, step int64) Config {
	t.Helper()
	dir := t.TempDir()
	control := filepath.Join(dir, "control.json")
	if err := os.WriteFile(control, []byte(`{"fabric_mode":"normal"}`), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(dir, "checkpoint.json")
	if err := SaveCheckpoint(checkpoint, Checkpoint{Step: step}); err != nil {
		t.Fatal(err)
	}
	return Config{Rank: rank, WorldSize: 3, Coordinator: address, Pod: fmt.Sprintf("training-%d", rank), Node: fmt.Sprintf("node-%d", rank), ControlFile: control, CheckpointFile: checkpoint, StepInterval: 2 * time.Millisecond, CheckpointInterval: 5 * time.Millisecond, RequestTimeout: 100 * time.Millisecond, CoordinatorTimeout: time.Second}
}

func runRecoveryWorker(ctx context.Context, worker *Worker) <-chan error {
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	return done
}

func TestWorkersResumeUnequalCheckpoints(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make([]<-chan error, 3)
	configs := make([]Config, 3)
	for rank, checkpoint := range []int64{2, 5, 7} {
		configs[rank] = recoveryWorkerConfig(t, rank, listener.Addr().String(), checkpoint)
		configs[rank].TotalSteps = 12
		worker := NewWorker(configs[rank])
		if rank == 0 {
			worker.listen = func(string, string) (net.Listener, error) { return listener, nil }
		}
		results[rank] = runRecoveryWorker(ctx, worker)
		if rank < 2 {
			for !worker.IsReady() {
				select {
				case <-ctx.Done():
					t.Fatal("local readiness waited for all collective peers")
				case <-time.After(time.Millisecond):
				}
			}
			if worker.Metrics.IsUp() {
				t.Fatal("rank_up succeeded before the final peer joined")
			}
		}
	}
	for rank, result := range results {
		if err := <-result; err != nil {
			t.Fatalf("rank %d: %v", rank, err)
		}
		checkpoint, err := LoadCheckpoint(configs[rank].CheckpointFile)
		if err != nil || checkpoint.Step != 12 {
			t.Fatalf("rank %d failed to converge: checkpoint=%+v error=%v", rank, checkpoint, err)
		}
	}
}

func TestWorkersRecoverAfterRankRestart(t *testing.T) {
	for _, restartedRank := range []int{0, 1} {
		t.Run(fmt.Sprintf("rank-%d", restartedRank), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			address := listener.Addr().String()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			workers := make([]*Worker, 3)
			stops := make([]context.CancelFunc, 3)
			results := make([]<-chan error, 3)
			for rank := range workers {
				workers[rank] = NewWorker(recoveryWorkerConfig(t, rank, address, 0))
				if rank == 0 {
					workers[rank].listen = func(string, string) (net.Listener, error) { return listener, nil }
				}
				var rankCtx context.Context
				rankCtx, stops[rank] = context.WithCancel(ctx)
				results[rank] = runRecoveryWorker(rankCtx, workers[rank])
			}
			waitProgress := func(after int64) {
				t.Helper()
				for {
					ready := true
					for rank, worker := range workers {
						select {
						case err := <-results[rank]:
							t.Fatalf("rank %d exited during recovery: %v", rank, err)
						default:
						}
						if !worker.Metrics.IsUp() || worker.Metrics.step.Load() <= after {
							ready = false
						}
					}
					if ready {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatalf("ranks did not make progress past step %d", after)
					case <-time.After(2 * time.Millisecond):
					}
				}
			}
			waitProgress(8)
			before := workers[0].Metrics.step.Load()
			stops[restartedRank]()
			if err := <-results[restartedRank]; err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			// Simulate a checkpoint older than the healthy ranks' current step.
			config := workers[restartedRank].Config
			checkpoint, err := LoadCheckpoint(config.CheckpointFile)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint.Step = 2
			if err := SaveCheckpoint(config.CheckpointFile, checkpoint); err != nil {
				t.Fatal(err)
			}
			if restartedRank == 0 {
				listener, err = net.Listen("tcp", address)
				if err != nil {
					t.Fatal(err)
				}
			}
			workers[restartedRank] = NewWorker(config)
			if restartedRank == 0 {
				workers[0].listen = func(string, string) (net.Listener, error) { return listener, nil }
			}
			results[restartedRank] = runRecoveryWorker(ctx, workers[restartedRank])
			waitProgress(before + 3)
			cancel()
			for rank, result := range results {
				if err := <-result; err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("rank %d: %v", rank, err)
				}
			}
			checkpoint, err = LoadCheckpoint(config.CheckpointFile)
			if err != nil || checkpoint.Restarts != 2 || checkpoint.Step <= before {
				t.Fatalf("restarted rank did not recover: checkpoint=%+v error=%v", checkpoint, err)
			}
		})
	}
}
