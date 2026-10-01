package scenario

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

func validIBState(value string) bool {
	for _, allowed := range ibStates {
		if value == allowed {
			return true
		}
	}
	return false
}

func validIBPhysicalState(value string) bool {
	for _, allowed := range ibPhysicalStates {
		if value == allowed {
			return true
		}
	}
	return false
}

func validateNonNegative(name string, value *int64) error {
	if value != nil && *value < 0 {
		return fmt.Errorf("metrics.%s cannot be negative", name)
	}
	return nil
}

// validate enforces the value ranges of every supported override field. It is
// called for top-level metrics and for each timeline phase, so phase errors
// carry the same messages as single-stage scenarios.
func (m MetricOverrides) validate() error {
	if m.GPUUtilizationPercent != nil && (*m.GPUUtilizationPercent < 0 || *m.GPUUtilizationPercent > 100) {
		return errors.New("metrics.gpu_utilization_percent must be between 0 and 100")
	}
	if m.GPUMemoryUsedPercent != nil && (*m.GPUMemoryUsedPercent < 0 || *m.GPUMemoryUsedPercent > 100) {
		return errors.New("metrics.gpu_memory_used_percent must be between 0 and 100")
	}
	if m.GPUMemoryTotalBytes != nil && *m.GPUMemoryTotalBytes <= 0 {
		return errors.New("metrics.gpu_memory_total_bytes must be positive")
	}
	if m.TemperatureCelsius != nil && *m.TemperatureCelsius < 0 {
		return errors.New("metrics.temperature_celsius cannot be negative")
	}
	if m.PowerWatts != nil && *m.PowerWatts < 0 {
		return errors.New("metrics.power_watts cannot be negative")
	}
	if m.XIDCode != nil && *m.XIDCode < 0 {
		return errors.New("metrics.xid_code cannot be negative")
	}
	if m.Health != nil && *m.Health != 0 && *m.Health != 1 {
		return errors.New("metrics.health must be 0 or 1")
	}
	if m.ECCDbeTotal != nil && *m.ECCDbeTotal < 0 {
		return errors.New("metrics.ecc_dbe_total cannot be negative")
	}
	if m.PowerViolationTotal != nil && *m.PowerViolationTotal < 0 {
		return errors.New("metrics.power_violation_total cannot be negative")
	}
	if m.PCIeReplayTotal != nil && *m.PCIeReplayTotal < 0 {
		return errors.New("metrics.pcie_replay_total cannot be negative")
	}
	if m.ThrottleActive != nil && *m.ThrottleActive != 0 && *m.ThrottleActive != 1 {
		return errors.New("metrics.throttle_active must be 0 or 1")
	}
	if m.GPUAllocatedCount != nil && *m.GPUAllocatedCount < 0 {
		return errors.New("metrics.gpu_allocated_count cannot be negative")
	}
	if m.GPUCapacity != nil && *m.GPUCapacity <= 0 {
		return errors.New("metrics.gpu_capacity must be positive")
	}
	if m.GPUAllocatable != nil && *m.GPUAllocatable < 0 {
		return errors.New("metrics.gpu_allocatable cannot be negative")
	}
	if m.IBPortUp != nil && *m.IBPortUp != 0 && *m.IBPortUp != 1 {
		return errors.New("metrics.ib_port_up must be 0 or 1")
	}
	if m.IBState != "" && !validIBState(m.IBState) {
		return fmt.Errorf("metrics.ib_state must be one of %s", strings.Join(ibStates, ", "))
	}
	if m.IBPhysicalState != "" && !validIBPhysicalState(m.IBPhysicalState) {
		return fmt.Errorf("metrics.ib_physical_state must be one of %s", strings.Join(ibPhysicalStates, ", "))
	}
	if m.IBLinkRateGbps != nil && (math.IsNaN(*m.IBLinkRateGbps) || math.IsInf(*m.IBLinkRateGbps, 0) || *m.IBLinkRateGbps <= 0) {
		return errors.New("metrics.ib_link_rate_gbps must be positive")
	}
	for _, counter := range []struct {
		name  string
		value *int64
	}{
		{"ib_tx_bytes_total", m.IBTxBytesTotal},
		{"ib_rx_bytes_total", m.IBRxBytesTotal},
		{"ib_symbol_errors_total", m.IBSymbolErrorsTotal},
		{"ib_link_error_recovery_total", m.IBLinkErrorRecoveryTotal},
		{"ib_link_downed_total", m.IBLinkDownedTotal},
		{"ib_xmit_discards_total", m.IBXmitDiscardsTotal},
		{"ib_xmit_wait_total", m.IBXmitWaitTotal},
		{"rdma_retries_total", m.RDMARetriesTotal},
		{"rdma_timeouts_total", m.RDMATimeoutsTotal},
	} {
		if err := validateNonNegative(counter.name, counter.value); err != nil {
			return err
		}
	}
	if m.FabricDelaySeconds != nil && (math.IsNaN(*m.FabricDelaySeconds) || math.IsInf(*m.FabricDelaySeconds, 0) || *m.FabricDelaySeconds < 0) {
		return errors.New("metrics.fabric_delay_seconds cannot be negative")
	}
	return nil
}
