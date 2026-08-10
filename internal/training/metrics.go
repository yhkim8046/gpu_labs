package training

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

type Metrics struct {
	step, samples, checkpoint, restarts, errors atomic.Int64
	fabricErrors, fabricRetries                 atomic.Int64
	loss, allreduce, fabricDelay                atomic.Uint64
	up, straggler, fabricActive, fabricMode     atomic.Int64
}

func (m *Metrics) IsUp() bool { return m.up.Load() == 1 }

func (m *Metrics) Handler(labels map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		l := fmt.Sprintf(`rank="%s",job="%s",node="%s",pod="%s"`, labels["rank"], labels["job"], labels["node"], labels["pod"])
		line := func(name, value string) { fmt.Fprintf(w, "%s{%s} %s\n", name, l, value) }
		line("gpu_lab_training_step", strconv.FormatInt(m.step.Load(), 10))
		line("gpu_lab_training_loss", fmt.Sprintf("%g", float64from(m.loss.Load())))
		line("gpu_lab_training_samples_total", strconv.FormatInt(m.samples.Load(), 10))
		line("gpu_lab_training_allreduce_seconds", fmt.Sprintf("%g", float64from(m.allreduce.Load())))
		line("gpu_lab_training_rank_up", strconv.FormatInt(m.up.Load(), 10))
		line("gpu_lab_training_checkpoint_step", strconv.FormatInt(m.checkpoint.Load(), 10))
		line("gpu_lab_training_restarts_total", strconv.FormatInt(m.restarts.Load(), 10))
		line("gpu_lab_training_straggler_active", strconv.FormatInt(m.straggler.Load(), 10))
		line("gpu_lab_training_allreduce_errors_total", strconv.FormatInt(m.errors.Load(), 10))
		fabricLabels := fmt.Sprintf(`%s,mode="%s"`, l, escapeMetricLabel(fabricModeName(m.fabricMode.Load())))
		fmt.Fprintf(w, "gpu_lab_training_fabric_fault_active{%s} %s\n", fabricLabels, strconv.FormatInt(m.fabricActive.Load(), 10))
		fmt.Fprintf(w, "gpu_lab_training_fabric_delay_seconds{%s} %g\n", l, float64from(m.fabricDelay.Load()))
		fmt.Fprintf(w, "gpu_lab_training_fabric_errors_total{%s} %s\n", l, strconv.FormatInt(m.fabricErrors.Load(), 10))
		fmt.Fprintf(w, "gpu_lab_training_fabric_retries_total{%s} %s\n", l, strconv.FormatInt(m.fabricRetries.Load(), 10))
	})
}
func float64from(v uint64) float64        { return float64(v) / 1e6 }
func (m *Metrics) SetStep(v int64)        { m.step.Store(v) }
func (m *Metrics) SetLoss(v float64)      { m.loss.Store(uint64(v * 1e6)) }
func (m *Metrics) SetSamples(v int64)     { m.samples.Store(v) }
func (m *Metrics) AddSamples(v int64)     { m.samples.Add(v) }
func (m *Metrics) SetAllreduce(v float64) { m.allreduce.Store(uint64(v * 1e6)) }
func (m *Metrics) SetUp(v bool) {
	if v {
		m.up.Store(1)
	} else {
		m.up.Store(0)
	}
}
func (m *Metrics) SetCheckpoint(v int64) { m.checkpoint.Store(v) }
func (m *Metrics) SetRestarts(v int64)   { m.restarts.Store(v) }
func (m *Metrics) SetStraggler(v bool) {
	if v {
		m.straggler.Store(1)
	} else {
		m.straggler.Store(0)
	}
}
func (m *Metrics) AddError() { m.errors.Add(1) }

func (m *Metrics) SetFabricMode(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "down":
		m.fabricMode.Store(1)
	case "degraded":
		m.fabricMode.Store(2)
	case "errors":
		m.fabricMode.Store(3)
	case "retries":
		m.fabricMode.Store(4)
	case "congestion":
		m.fabricMode.Store(5)
	default:
		m.fabricMode.Store(0)
	}
}

func (m *Metrics) SetFabricActive(active bool) {
	if active {
		m.fabricActive.Store(1)
	} else {
		m.fabricActive.Store(0)
	}
}

func (m *Metrics) SetFabricDelay(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	m.fabricDelay.Store(uint64(seconds * 1e6))
}

func (m *Metrics) AddFabricError() { m.fabricErrors.Add(1) }

func (m *Metrics) AddFabricRetry() { m.fabricRetries.Add(1) }

func fabricModeName(value int64) string {
	switch value {
	case 1:
		return "down"
	case 2:
		return "degraded"
	case 3:
		return "errors"
	case 4:
		return "retries"
	case 5:
		return "congestion"
	default:
		return "normal"
	}
}

func escapeMetricLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}
