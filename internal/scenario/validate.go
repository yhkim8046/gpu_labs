package scenario

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Validate enforces the full scenario contract. Validation is ordered so that
// document identity is checked first, then targets, timeline, metrics, the
// target/metric compatibility matrix, and finally actions.
func (s Scenario) Validate() error {
	if err := s.validateIdentity(); err != nil {
		return err
	}
	var scenarioDuration time.Duration
	if s.Spec.Duration != "" {
		duration, err := time.ParseDuration(s.Spec.Duration)
		if err != nil {
			return fmt.Errorf("spec.duration: %w", err)
		}
		if duration <= 0 {
			return errors.New("spec.duration must be positive")
		}
		scenarioDuration = duration
	}
	if err := s.Spec.Targets.validate(); err != nil {
		return err
	}
	if s.Spec.Timeline != nil {
		if err := s.validateTimeline(scenarioDuration); err != nil {
			return err
		}
	}
	if err := s.Spec.Metrics.validate(); err != nil {
		return err
	}
	if err := validateMetricTargets(s.Spec.Targets, s.Spec.Metrics); err != nil {
		return err
	}
	return s.validateActions()
}

func (s Scenario) validateIdentity() error {
	if s.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q, got %q", APIVersion, s.APIVersion)
	}
	if s.Kind != Kind {
		return fmt.Errorf("kind must be %q, got %q", Kind, s.Kind)
	}
	if !namePattern.MatchString(s.Metadata.Name) {
		return fmt.Errorf("metadata.name %q is not a DNS-compatible lowercase name", s.Metadata.Name)
	}
	return nil
}

func (t TargetSpec) validate() error {
	for key, value := range t.Selector {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return errors.New("spec.targets.selector keys and values cannot be empty")
		}
	}
	seenGPUIndices := make(map[int]struct{}, len(t.GPUIndices))
	for _, index := range t.GPUIndices {
		if index < 0 {
			return errors.New("spec.targets.gpu_indices cannot contain a negative index")
		}
		if _, exists := seenGPUIndices[index]; exists {
			return fmt.Errorf("spec.targets.gpu_indices contains duplicate index %d", index)
		}
		seenGPUIndices[index] = struct{}{}
	}
	return nil
}

// validateMetricTargets enforces the node-wide versus GPU-index safety matrix:
// capacity and fabric overrides are node/port scoped and cannot be paired
// with an individual GPU-index target. It is shared by top-level metrics and
// every timeline phase so both paths report identical errors.
func validateMetricTargets(targets TargetSpec, metrics MetricOverrides) error {
	if len(targets.GPUIndices) == 0 {
		return nil
	}
	if metrics.GPUCapacity != nil || metrics.GPUAllocatable != nil {
		return errors.New("node capacity metrics cannot be combined with spec.targets.gpu_indices")
	}
	if metrics.HasFabricOverrides() {
		return errors.New("fabric metrics cannot be combined with spec.targets.gpu_indices")
	}
	return nil
}

func (s Scenario) validateActions() error {
	for i, action := range s.Spec.Actions {
		switch action.Type {
		case "exporter_fault":
			if len(s.Spec.Targets.GPUIndices) > 0 {
				return fmt.Errorf("spec.actions[%d] exporter_fault cannot target individual GPUs", i)
			}
			if action.Mode != "unavailable" {
				return fmt.Errorf("spec.actions[%d].mode must be unavailable", i)
			}
		case "create_pending_workload":
			if action.GPUCount <= 0 {
				return fmt.Errorf("spec.actions[%d].gpu_count must be positive", i)
			}
		case "create_gpu_workload":
			if !namePattern.MatchString(action.Name) {
				return fmt.Errorf("spec.actions[%d].name %q is not a DNS-compatible lowercase name", i, action.Name)
			}
			if action.GPUCount <= 0 {
				return fmt.Errorf("spec.actions[%d].gpu_count must be positive", i)
			}
			for key, value := range action.NodeSelector {
				if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
					return fmt.Errorf("spec.actions[%d].node_selector keys and values cannot be empty", i)
				}
			}
		default:
			return fmt.Errorf("spec.actions[%d].type %q is unsupported", i, action.Type)
		}
	}
	return nil
}
