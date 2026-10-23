package scenario

import (
	"regexp"
	"strings"
	"time"
)

var namePattern = regexp.MustCompile("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")

var (
	ibStates         = []string{"ACTIVE", "DOWN", "INIT", "ARMED"}
	ibPhysicalStates = []string{"LINK_UP", "DISABLED", "POLLING"}
)

type Scenario struct {
	APIVersion string       `yaml:"apiVersion" json:"apiVersion"`
	Kind       string       `yaml:"kind" json:"kind"`
	Metadata   Metadata     `yaml:"metadata" json:"metadata"`
	Spec       ScenarioSpec `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

type ScenarioSpec struct {
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Duration    string           `yaml:"duration,omitempty" json:"duration,omitempty"`
	Targets     TargetSpec       `yaml:"targets,omitempty" json:"targets,omitempty"`
	Timeline    []TimelineStep   `yaml:"timeline,omitempty" json:"timeline,omitempty"`
	Metrics     MetricOverrides  `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Actions     []ScenarioAction `yaml:"actions,omitempty" json:"actions,omitempty"`
}

// TimelineStep is one absolute offset in a staged scenario. Metrics in every
// step are an independent override of the normal baseline, rather than a merge
// with the preceding step, so omitted fields cannot leak across transitions.
type TimelineStep struct {
	Phase   string          `yaml:"phase" json:"phase"`
	At      string          `yaml:"at" json:"at"`
	Metrics MetricOverrides `yaml:"metrics,omitempty" json:"metrics,omitempty"`
}

type TargetSpec struct {
	Selector   map[string]string `yaml:"selector,omitempty" json:"selector,omitempty"`
	GPUIndices []int             `yaml:"gpu_indices,omitempty" json:"gpu_indices,omitempty"`
}

type MetricOverrides struct {
	GPUUtilizationPercent    *float64 `yaml:"gpu_utilization_percent,omitempty" json:"gpu_utilization_percent,omitempty"`
	GPUMemoryUsedPercent     *float64 `yaml:"gpu_memory_used_percent,omitempty" json:"gpu_memory_used_percent,omitempty"`
	GPUMemoryTotalBytes      *int64   `yaml:"gpu_memory_total_bytes,omitempty" json:"gpu_memory_total_bytes,omitempty"`
	TemperatureCelsius       *float64 `yaml:"temperature_celsius,omitempty" json:"temperature_celsius,omitempty"`
	PowerWatts               *float64 `yaml:"power_watts,omitempty" json:"power_watts,omitempty"`
	XIDCode                  *int     `yaml:"xid_code,omitempty" json:"xid_code,omitempty"`
	Health                   *int     `yaml:"health,omitempty" json:"health,omitempty"`
	ECCDbeTotal              *int64   `yaml:"ecc_dbe_total,omitempty" json:"ecc_dbe_total,omitempty"`
	PowerViolationTotal      *int64   `yaml:"power_violation_total,omitempty" json:"power_violation_total,omitempty"`
	PCIeReplayTotal          *int64   `yaml:"pcie_replay_total,omitempty" json:"pcie_replay_total,omitempty"`
	ThrottleActive           *int     `yaml:"throttle_active,omitempty" json:"throttle_active,omitempty"`
	ThrottleReason           string   `yaml:"throttle_reason,omitempty" json:"throttle_reason,omitempty"`
	GPUAllocatedCount        *int     `yaml:"gpu_allocated_count,omitempty" json:"gpu_allocated_count,omitempty"`
	GPUCapacity              *int     `yaml:"gpu_capacity,omitempty" json:"gpu_capacity,omitempty"`
	GPUAllocatable           *int     `yaml:"gpu_allocatable,omitempty" json:"gpu_allocatable,omitempty"`
	IBPortUp                 *int     `yaml:"ib_port_up,omitempty" json:"ib_port_up,omitempty"`
	IBState                  string   `yaml:"ib_state,omitempty" json:"ib_state,omitempty"`
	IBPhysicalState          string   `yaml:"ib_physical_state,omitempty" json:"ib_physical_state,omitempty"`
	IBLinkRateGbps           *float64 `yaml:"ib_link_rate_gbps,omitempty" json:"ib_link_rate_gbps,omitempty"`
	IBTxBytesTotal           *int64   `yaml:"ib_tx_bytes_total,omitempty" json:"ib_tx_bytes_total,omitempty"`
	IBRxBytesTotal           *int64   `yaml:"ib_rx_bytes_total,omitempty" json:"ib_rx_bytes_total,omitempty"`
	IBSymbolErrorsTotal      *int64   `yaml:"ib_symbol_errors_total,omitempty" json:"ib_symbol_errors_total,omitempty"`
	IBLinkErrorRecoveryTotal *int64   `yaml:"ib_link_error_recovery_total,omitempty" json:"ib_link_error_recovery_total,omitempty"`
	IBLinkDownedTotal        *int64   `yaml:"ib_link_downed_total,omitempty" json:"ib_link_downed_total,omitempty"`
	IBXmitDiscardsTotal      *int64   `yaml:"ib_xmit_discards_total,omitempty" json:"ib_xmit_discards_total,omitempty"`
	IBXmitWaitTotal          *int64   `yaml:"ib_xmit_wait_total,omitempty" json:"ib_xmit_wait_total,omitempty"`
	RDMARetriesTotal         *int64   `yaml:"rdma_retries_total,omitempty" json:"rdma_retries_total,omitempty"`
	RDMATimeoutsTotal        *int64   `yaml:"rdma_timeouts_total,omitempty" json:"rdma_timeouts_total,omitempty"`
	FabricDelaySeconds       *float64 `yaml:"fabric_delay_seconds,omitempty" json:"fabric_delay_seconds,omitempty"`
}

// HasFabricOverrides reports whether a scenario changes the synthetic HCA/port
// view. Fabric readings are node/port scoped and cannot be combined with a
// GPU-index target.
func (m MetricOverrides) HasFabricOverrides() bool {
	return m.IBPortUp != nil ||
		strings.TrimSpace(m.IBState) != "" ||
		strings.TrimSpace(m.IBPhysicalState) != "" ||
		m.IBLinkRateGbps != nil ||
		m.IBTxBytesTotal != nil ||
		m.IBRxBytesTotal != nil ||
		m.IBSymbolErrorsTotal != nil ||
		m.IBLinkErrorRecoveryTotal != nil ||
		m.IBLinkDownedTotal != nil ||
		m.IBXmitDiscardsTotal != nil ||
		m.IBXmitWaitTotal != nil ||
		m.RDMARetriesTotal != nil ||
		m.RDMATimeoutsTotal != nil ||
		m.FabricDelaySeconds != nil
}

func (m MetricOverrides) empty() bool {
	return m == (MetricOverrides{})
}

type ScenarioAction struct {
	Type         string            `yaml:"type" json:"type"`
	Name         string            `yaml:"name,omitempty" json:"name,omitempty"`
	Mode         string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	GPUCount     int               `yaml:"gpu_count,omitempty" json:"gpu_count,omitempty"`
	NodeSelector map[string]string `yaml:"node_selector,omitempty" json:"node_selector,omitempty"`
	WaitForReady bool              `yaml:"wait_for_ready,omitempty" json:"wait_for_ready,omitempty"`
}

func (s Scenario) Name() string {
	return s.Metadata.Name
}

// PhaseAt is the Scenario convenience form of the package-level helper.
func (s Scenario) PhaseAt(elapsed time.Duration) (TimelineStep, int, bool) {
	return PhaseAt(s.Spec.Timeline, elapsed)
}
