package scenario

import (
	"errors"
	"fmt"
	"time"
)

// PhaseAt returns the active timeline step at elapsed time. A negative elapsed
// duration is treated as the start of the scenario, which makes callers robust
// to small clock skew around a shared started_at timestamp. The boolean is
// false for an empty or malformed timeline.
func PhaseAt(timeline []TimelineStep, elapsed time.Duration) (TimelineStep, int, bool) {
	if len(timeline) == 0 {
		return TimelineStep{}, -1, false
	}
	offsets := make([]time.Duration, len(timeline))
	for i, step := range timeline {
		offset, err := time.ParseDuration(step.At)
		if err != nil {
			return TimelineStep{}, -1, false
		}
		offsets[i] = offset
	}
	if elapsed < 0 {
		elapsed = 0
	}
	active := -1
	for i, offset := range offsets {
		if elapsed < offset {
			break
		}
		active = i
	}
	if active < 0 {
		return TimelineStep{}, -1, false
	}
	return timeline[active], active, true
}

// validateTimeline enforces the fixed four-phase staged-scenario contract.
// Every phase reuses the shared metric-range validation (MetricOverrides.validate)
// and the shared target compatibility rules (validateMetricTargets) rather than
// recursing through Scenario.Validate, so phase errors carry the same messages
// as single-stage scenarios without re-checking the document envelope.
func (s Scenario) validateTimeline(scenarioDuration time.Duration) error {
	if len(s.Spec.Timeline) != 4 {
		return fmt.Errorf("spec.timeline must contain exactly 4 steps, got %d", len(s.Spec.Timeline))
	}
	if !s.Spec.Metrics.empty() {
		return errors.New("spec.metrics cannot be combined with spec.timeline")
	}
	if len(s.Spec.Actions) > 0 {
		return errors.New("spec.actions cannot be combined with spec.timeline")
	}

	expectedPhases := [...]string{"baseline", "warning", "critical", "recovery"}
	var previousOffset time.Duration
	for i, step := range s.Spec.Timeline {
		if step.Phase != expectedPhases[i] {
			return fmt.Errorf("spec.timeline[%d].phase must be %q, got %q", i, expectedPhases[i], step.Phase)
		}
		offset, err := time.ParseDuration(step.At)
		if err != nil {
			return fmt.Errorf("spec.timeline[%d].at: %w", i, err)
		}
		if i == 0 {
			if offset != 0 {
				return errors.New("spec.timeline[0].at must be 0s")
			}
		} else if offset <= previousOffset {
			return fmt.Errorf("spec.timeline[%d].at must be later than spec.timeline[%d].at", i, i-1)
		}
		if err := validateTimelineStepMetrics(s.Spec.Targets, i, step); err != nil {
			return err
		}
		previousOffset = offset
	}
	if scenarioDuration > 0 && scenarioDuration <= previousOffset {
		return errors.New("spec.duration must be later than the recovery timeline offset")
	}
	return nil
}

// validateTimelineStepMetrics checks one phase: per-phase emptiness rules, the
// fabric override ban, and the same metric ranges and target compatibility rules
// that a single-stage scenario enforces. The rules are called explicitly rather
// than by re-entering Scenario.Validate, so the document envelope, duration,
// and actions are never rechecked or allowed to recurse.
func validateTimelineStepMetrics(targets TargetSpec, index int, step TimelineStep) error {
	switch step.Phase {
	case "baseline", "recovery":
		if !step.Metrics.empty() {
			return fmt.Errorf("spec.timeline[%d].metrics must be empty for %s phase", index, step.Phase)
		}
	case "warning", "critical":
		if step.Metrics.empty() {
			return fmt.Errorf("spec.timeline[%d].metrics must not be empty for %s phase", index, step.Phase)
		}
	}
	if step.Metrics.HasFabricOverrides() {
		return fmt.Errorf("spec.timeline[%d].metrics cannot contain InfiniBand/RDMA fabric overrides", index)
	}
	if err := step.Metrics.validate(); err != nil {
		return fmt.Errorf("spec.timeline[%d]: %w", index, err)
	}
	if err := validateMetricTargets(targets, step.Metrics); err != nil {
		return fmt.Errorf("spec.timeline[%d]: %w", index, err)
	}
	return nil
}
