package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	builtins "github.com/gpu-lab/gpu-lab/scenarios"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "gpu-lab.io/v1alpha1"
	Kind       = "Scenario"
	Namespace  = "gpu-lab-system"
	ConfigName = "gpu-lab-scenario"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

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
	Metrics     MetricOverrides  `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Actions     []ScenarioAction `yaml:"actions,omitempty" json:"actions,omitempty"`
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

func (s Scenario) Validate() error {
	if s.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q, got %q", APIVersion, s.APIVersion)
	}
	if s.Kind != Kind {
		return fmt.Errorf("kind must be %q, got %q", Kind, s.Kind)
	}
	if !namePattern.MatchString(s.Metadata.Name) {
		return fmt.Errorf("metadata.name %q is not a DNS-compatible lowercase name", s.Metadata.Name)
	}
	if s.Spec.Duration != "" {
		duration, err := time.ParseDuration(s.Spec.Duration)
		if err != nil {
			return fmt.Errorf("spec.duration: %w", err)
		}
		if duration <= 0 {
			return errors.New("spec.duration must be positive")
		}
	}
	seenGPUIndices := make(map[int]struct{}, len(s.Spec.Targets.GPUIndices))
	for key, value := range s.Spec.Targets.Selector {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return errors.New("spec.targets.selector keys and values cannot be empty")
		}
	}
	for _, index := range s.Spec.Targets.GPUIndices {
		if index < 0 {
			return errors.New("spec.targets.gpu_indices cannot contain a negative index")
		}
		if _, exists := seenGPUIndices[index]; exists {
			return fmt.Errorf("spec.targets.gpu_indices contains duplicate index %d", index)
		}
		seenGPUIndices[index] = struct{}{}
	}
	m := s.Spec.Metrics
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
	if err := validateNonNegative("ib_tx_bytes_total", m.IBTxBytesTotal); err != nil {
		return err
	}
	if err := validateNonNegative("ib_rx_bytes_total", m.IBRxBytesTotal); err != nil {
		return err
	}
	if err := validateNonNegative("ib_symbol_errors_total", m.IBSymbolErrorsTotal); err != nil {
		return err
	}
	if err := validateNonNegative("ib_link_error_recovery_total", m.IBLinkErrorRecoveryTotal); err != nil {
		return err
	}
	if err := validateNonNegative("ib_link_downed_total", m.IBLinkDownedTotal); err != nil {
		return err
	}
	if err := validateNonNegative("ib_xmit_discards_total", m.IBXmitDiscardsTotal); err != nil {
		return err
	}
	if err := validateNonNegative("ib_xmit_wait_total", m.IBXmitWaitTotal); err != nil {
		return err
	}
	if err := validateNonNegative("rdma_retries_total", m.RDMARetriesTotal); err != nil {
		return err
	}
	if err := validateNonNegative("rdma_timeouts_total", m.RDMATimeoutsTotal); err != nil {
		return err
	}
	if m.FabricDelaySeconds != nil && (math.IsNaN(*m.FabricDelaySeconds) || math.IsInf(*m.FabricDelaySeconds, 0) || *m.FabricDelaySeconds < 0) {
		return errors.New("metrics.fabric_delay_seconds cannot be negative")
	}
	if len(s.Spec.Targets.GPUIndices) > 0 {
		if m.GPUCapacity != nil || m.GPUAllocatable != nil {
			return errors.New("node capacity metrics cannot be combined with spec.targets.gpu_indices")
		}
		if m.HasFabricOverrides() {
			return errors.New("fabric metrics cannot be combined with spec.targets.gpu_indices")
		}
	}
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

func Parse(data []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Scenario{}, err
	}
	if err := s.Validate(); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

func LoadBuiltin(name string) (Scenario, error) {
	data, err := fs.ReadFile(builtins.FS, name+".yaml")
	if err != nil {
		return Scenario{}, fmt.Errorf("builtin scenario %q: %w", name, err)
	}
	return Parse(data)
}

func LoadFile(filename string) (Scenario, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Scenario{}, err
	}
	return Parse(data)
}

func ListBuiltin() ([]string, error) {
	entries, err := fs.ReadDir(builtins.FS, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".yaml" {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}

func Marshal(s Scenario) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(s)
}

type configMapManifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   objectMeta        `json:"metadata"`
	Data       map[string]string `json:"data"`
}

type objectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

func ConfigMapJSON(s Scenario, generation string) ([]byte, error) {
	yamlData, err := Marshal(s)
	if err != nil {
		return nil, err
	}
	manifest := configMapManifest{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Metadata: objectMeta{
			Name:      ConfigName,
			Namespace: Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":    "gpu-lab",
				"app.kubernetes.io/managed-by": "gpu-lab",
			},
		},
		Data: map[string]string{
			"scenario.yaml": string(yamlData),
			"generation":    generation,
		},
	}
	return json.MarshalIndent(manifest, "", "  ")
}

func PendingWorkloadJSON(s Scenario, gpuCount int) ([]byte, error) {
	if gpuCount <= 0 {
		return nil, errors.New("gpuCount must be positive")
	}
	return GPUWorkloadJSON(s, ScenarioAction{
		Type:     "create_gpu_workload",
		Name:     "gpu-lab-scheduling-failure",
		GPUCount: gpuCount,
	})
}

func GPUWorkloadJSON(s Scenario, action ScenarioAction) ([]byte, error) {
	if action.Type != "create_gpu_workload" {
		return nil, fmt.Errorf("action type must be create_gpu_workload, got %q", action.Type)
	}
	if !namePattern.MatchString(action.Name) {
		return nil, fmt.Errorf("workload name %q is not a DNS-compatible lowercase name", action.Name)
	}
	if action.GPUCount <= 0 {
		return nil, errors.New("gpu_count must be positive")
	}
	resourceCount := fmt.Sprint(action.GPUCount)
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      action.Name,
			"namespace": "gpu-lab-demo",
			"labels": map[string]string{
				"app.kubernetes.io/part-of":    "gpu-lab",
				"app.kubernetes.io/managed-by": "gpu-lab",
				"gpu-lab/scenario":             s.Name(),
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []any{map[string]any{
				"name":  "pause",
				"image": "registry.k8s.io/pause:3.9",
				"resources": map[string]any{
					"requests": map[string]string{"nvidia.com/gpu": resourceCount},
					"limits":   map[string]string{"nvidia.com/gpu": resourceCount},
				},
			}},
		},
	}
	if len(action.NodeSelector) > 0 {
		manifest["spec"].(map[string]any)["nodeSelector"] = action.NodeSelector
	}
	return json.MarshalIndent(manifest, "", "  ")
}

func HasAction(s Scenario, actionType string) (ScenarioAction, bool) {
	for _, action := range s.Spec.Actions {
		if action.Type == actionType {
			return action, true
		}
	}
	return ScenarioAction{}, false
}

func PrettyJSON(data []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return string(data)
	}
	return out.String()
}
