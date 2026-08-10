package training

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultFabricPollInterval = 100 * time.Millisecond

type Config struct {
	Rank, WorldSize                                                      int
	Pod, Node, Job, Coordinator, ControlFile, CheckpointFile             string
	StepInterval, CheckpointInterval, RequestTimeout, CoordinatorTimeout time.Duration
	FabricPollInterval                                                   time.Duration
	TotalSteps                                                           int64
	MetricsPort, CoordinatorPort                                         int
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func envInt(key string, fallback int) int {
	v, _ := strconv.Atoi(os.Getenv(key))
	if v == 0 {
		return fallback
	}
	return v
}
func envIntOptional(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
func RankFromPodName(name string) (int, error) {
	parts := strings.Split(strings.TrimSpace(name), "-")
	if len(parts) == 0 {
		return 0, fmt.Errorf("empty pod name")
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("pod name %q has no ordinal", name)
	}
	return n, nil
}
func LoadConfig() (Config, error) {
	pod := env("POD_NAME", "training-0")
	rank, rankSet := envIntOptional("RANK")
	var err error
	if !rankSet {
		rank, err = RankFromPodName(pod)
		if err != nil {
			return Config{}, err
		}
	}
	world := envInt("WORLD_SIZE", 1)
	if world < 1 || rank < 0 || rank >= world {
		return Config{}, fmt.Errorf("invalid rank/world size: %d/%d", rank, world)
	}
	coord := env("COORDINATOR_ADDR", "training-0.training:8080")
	if rank == 0 && os.Getenv("COORDINATOR_ADDR") == "" {
		coord = "127.0.0.1:8080"
	}
	// This is only the in-process control-file polling interval. Kubernetes
	// projected-volume refresh remains the upper bound for ConfigMap updates;
	// 100ms keeps recovery responsive once the new projection is visible while
	// remaining negligible for this synthetic worker.
	return Config{Rank: rank, WorldSize: world, Pod: pod, Node: env("NODE_NAME", "unknown"), Job: env("JOB_NAME", "gpu-lab-training"), Coordinator: coord, ControlFile: env("CONTROL_FILE", "/etc/gpu-lab-training/control.json"), CheckpointFile: env("CHECKPOINT_FILE", "/var/lib/gpu-lab-training/checkpoint.json"), StepInterval: envDuration("STEP_INTERVAL", 500*time.Millisecond), CheckpointInterval: envDuration("CHECKPOINT_INTERVAL", 5*time.Second), RequestTimeout: envDuration("REQUEST_TIMEOUT", 5*time.Second), CoordinatorTimeout: envDuration("COORDINATOR_TIMEOUT", 10*time.Second), FabricPollInterval: envDuration("FABRIC_POLL_INTERVAL", defaultFabricPollInterval), TotalSteps: int64(envInt("TOTAL_STEPS", 0)), MetricsPort: envInt("METRICS_PORT", 9401), CoordinatorPort: envInt("COORDINATOR_PORT", 8080)}, nil
}

type Worker struct {
	Config      Config
	Metrics     *Metrics
	client      *http.Client
	readControl func() (Control, error)
	sleep       func(context.Context, time.Duration) error
}

func NewWorker(c Config) *Worker {
	w := &Worker{Config: c, Metrics: &Metrics{}, client: &http.Client{Timeout: c.RequestTimeout}}
	w.readControl = func() (Control, error) { return ReadControl(c.ControlFile) }
	w.sleep = sleepContext
	return w
}

func (w *Worker) Run(ctx context.Context) error {
	cp, err := LoadCheckpoint(w.Config.CheckpointFile)
	if err != nil {
		return err
	}
	cp.Restarts++
	if err := SaveCheckpoint(w.Config.CheckpointFile, cp); err != nil {
		return err
	}
	w.Metrics.SetRestarts(cp.Restarts)
	w.Metrics.SetStep(cp.Step)
	w.Metrics.SetSamples(cp.Step * 32)
	w.Metrics.SetCheckpoint(cp.Step)
	w.Metrics.SetUp(true)
	w.Metrics.SetFabricMode("normal")
	w.Metrics.SetFabricActive(false)
	w.Metrics.SetFabricDelay(0)
	defer w.Metrics.SetUp(false)
	if w.Config.Rank == 0 {
		coord, err := NewCoordinator(w.Config.WorldSize, w.Config.CoordinatorTimeout)
		if err != nil {
			return err
		}
		go func() { _ = http.ListenAndServe(fmt.Sprintf(":%d", w.Config.CoordinatorPort), coord.Handler()) }()
	}
	start := cp.Step + 1
	lastCheckpoint := time.Now()
	ticker := time.NewTicker(w.Config.StepInterval)
	defer ticker.Stop()
	for step := start; w.Config.TotalSteps == 0 || step <= w.Config.TotalSteps; step++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		control, controlErr := w.readCurrentControl()
		if controlErr != nil {
			fmt.Fprintf(os.Stderr, "control ignored: %v\n", controlErr)
			control = Control{FabricMode: "normal"}
		}
		mode, targeted, delay := w.applyFabricControl(control)
		started := time.Now()
		// A collective cannot make progress while any rank has lost its fabric
		// link. Keep every rank alive at the same step until the control plane
		// clears the incident; only the targeted rank reports fabric errors.
		if mode == "down" {
			control, err = w.waitForFabricRecovery(ctx, control)
			if err != nil {
				return err
			}
			mode, targeted, delay = w.applyFabricControl(control)
		}
		if targeted && mode == "errors" {
			w.Metrics.AddFabricError()
		}
		if targeted && mode == "retries" {
			w.Metrics.AddFabricRetry()
		}
		if targeted && delay > 0 && mode != "down" {
			if err := w.wait(delay, ctx); err != nil {
				return err
			}
		}
		if control.Straggler != nil && control.Straggler.Rank == w.Config.Rank {
			w.Metrics.SetStraggler(true)
			if err := w.wait(control.Straggler.Delay, ctx); err != nil {
				return err
			}
		} else {
			w.Metrics.SetStraggler(false)
		}
		gradient := deterministicGradient(step, w.Config.Rank)
		requestCtx, cancel := context.WithTimeout(ctx, w.Config.RequestTimeout*8+time.Second)
		avg, err := ReduceWithRetry(requestCtx, w.client, w.Config.Coordinator, reduceRequest{Step: step, Rank: w.Config.Rank, Gradient: gradient}, w.Config.RequestTimeout)
		cancel()
		w.Metrics.SetAllreduce(time.Since(started).Seconds())
		if err != nil {
			w.Metrics.AddError()
			return err
		}
		loss := deterministicLoss(step, avg)
		w.Metrics.SetStep(step)
		w.Metrics.SetLoss(loss)
		w.Metrics.AddSamples(32)
		cp.Step = step
		if time.Since(lastCheckpoint) >= w.Config.CheckpointInterval || (w.Config.TotalSteps > 0 && step == w.Config.TotalSteps) {
			if err := SaveCheckpoint(w.Config.CheckpointFile, cp); err != nil {
				return err
			}
			w.Metrics.SetCheckpoint(cp.Step)
			lastCheckpoint = time.Now()
		}
		if control.Crash != nil && control.Crash.Enabled && control.Crash.Rank == w.Config.Rank {
			id := control.CrashID()
			if id != "" && !cp.AppliedCrashGenerations[id] {
				cp.AppliedCrashGenerations[id] = true
				if err := SaveCheckpoint(w.Config.CheckpointFile, cp); err != nil {
					return err
				}
				return fmt.Errorf("crash injection applied: %s", id)
			}
		}
	}
	return nil
}

func (w *Worker) readCurrentControl() (Control, error) {
	if w.readControl != nil {
		return w.readControl()
	}
	return ReadControl(w.Config.ControlFile)
}

func (w *Worker) applyFabricControl(control Control) (string, bool, time.Duration) {
	mode := control.EffectiveFabricMode()
	targeted := FabricTargetMatches(control.FabricTargetNode, w.Config.Node)
	delay := control.FabricDelay
	if targeted && delay <= 0 {
		delay = defaultFabricDelay(mode)
	}
	active := targeted && mode != "normal"
	w.Metrics.SetFabricMode(mode)
	w.Metrics.SetFabricActive(active)
	if active && delay > 0 {
		w.Metrics.SetFabricDelay(delay.Seconds())
	} else {
		w.Metrics.SetFabricDelay(0)
	}
	return mode, targeted, delay
}

func (w *Worker) waitForFabricRecovery(ctx context.Context, control Control) (Control, error) {
	interval := w.Config.FabricPollInterval
	if interval <= 0 {
		interval = defaultFabricPollInterval
	}
	for {
		mode, targeted, _ := w.applyFabricControl(control)
		if mode != "down" {
			return control, nil
		}
		// Keep the process and readiness probe alive while the collective is
		// blocked. Only the disconnected rank owns the synthetic error.
		if targeted {
			w.Metrics.AddFabricError()
			w.Metrics.AddError()
		}
		if err := w.wait(interval, ctx); err != nil {
			return Control{}, err
		}
		next, err := w.readCurrentControl()
		if err != nil {
			continue
		}
		control = next
	}
}

func (w *Worker) wait(delay time.Duration, ctx context.Context) error {
	if delay <= 0 {
		return nil
	}
	if w.sleep != nil {
		return w.sleep(ctx, delay)
	}
	return sleepContext(ctx, delay)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultFabricDelay(mode string) time.Duration {
	switch mode {
	case "degraded":
		return 1500 * time.Millisecond
	case "retries":
		return time.Second
	case "congestion":
		return 800 * time.Millisecond
	default:
		return 0
	}
}

func deterministicGradient(step int64, rank int) float64 {
	return float64((step*31+int64(rank)*17)%1000) / 1000
}
func deterministicLoss(step int64, average float64) float64 {
	return 1/(1+float64(step)/100) + average/100
}
