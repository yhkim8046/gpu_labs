package exporter

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

const (
	DefaultGPUCount       = 8
	DefaultMemoryTotal    = int64(16 * 1024 * 1024 * 1024)
	DefaultHCA            = "mlx5_0"
	DefaultFabricPort     = 1
	DefaultLinkLayer      = "InfiniBand"
	DefaultIBLinkRateGbps = 200.0
	defaultUtilization    = 15.0
	defaultMemoryPercent  = 20.0
	defaultTemperature    = 45.0
	defaultPower          = 80.0
)

type Reading struct {
	GPU                 string  `json:"gpu"`
	UtilizationPercent  float64 `json:"utilization_percent"`
	MemoryUsedBytes     int64   `json:"memory_used_bytes"`
	MemoryTotalBytes    int64   `json:"memory_total_bytes"`
	TemperatureCelsius  float64 `json:"temperature_celsius"`
	PowerWatts          float64 `json:"power_watts"`
	XIDCode             int     `json:"xid_code"`
	Health              int     `json:"health"`
	ECCDbeTotal         int64   `json:"ecc_dbe_total"`
	PowerViolationTotal int64   `json:"power_violation_total"`
	PCIeReplayTotal     int64   `json:"pcie_replay_total"`
	ThrottleActive      int     `json:"throttle_active"`
	ThrottleReason      string  `json:"throttle_reason"`
	Allocated           int     `json:"allocated"`
}

// FabricReading is the deterministic synthetic InfiniBand/RDMA view exported
// by every fake worker node. It intentionally models one HCA and one port so
// that operational exercises can use the same node/port labels as a real
// fabric exporter without requiring an InfiniBand device.
type FabricReading struct {
	Synthetic              bool    `json:"synthetic"`
	HCA                    string  `json:"hca"`
	Port                   int     `json:"port"`
	LinkLayer              string  `json:"link_layer"`
	PortUp                 int     `json:"port_up"`
	State                  string  `json:"state"`
	PhysicalState          string  `json:"physical_state"`
	LinkRateGbps           float64 `json:"link_rate_gbps"`
	TxBytesTotal           int64   `json:"tx_bytes_total"`
	RxBytesTotal           int64   `json:"rx_bytes_total"`
	SymbolErrorsTotal      int64   `json:"symbol_errors_total"`
	LinkErrorRecoveryTotal int64   `json:"link_error_recovery_total"`
	LinkDownedTotal        int64   `json:"link_downed_total"`
	XmitDiscardsTotal      int64   `json:"xmit_discards_total"`
	XmitWaitTotal          int64   `json:"xmit_wait_total"`
	RDMARetriesTotal       int64   `json:"rdma_retries_total"`
	RDMATimeoutsTotal      int64   `json:"rdma_timeouts_total"`
	FabricDelaySeconds     float64 `json:"fabric_delay_seconds"`
}

type StateSnapshot struct {
	Node          string        `json:"node"`
	Scenario      string        `json:"scenario"`
	Generation    string        `json:"generation"`
	ExporterFault string        `json:"exporter_fault,omitempty"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Readings      []Reading     `json:"readings"`
	Fabric        FabricReading `json:"fabric"`
}

type Model struct {
	mu         sync.RWMutex
	nodeName   string
	gpuCount   int
	normal     scenario.Scenario
	scenario   scenario.Scenario
	generation string
	updatedAt  time.Time
	readings   []Reading
	fault      string
	expiresAt  time.Time
	fabric     FabricReading
	now        func() time.Time
}

func NewModel(nodeName string, gpuCount int) *Model {
	if strings.TrimSpace(nodeName) == "" {
		nodeName = "unknown-node"
	}
	if gpuCount <= 0 {
		gpuCount = DefaultGPUCount
	}
	normal, err := scenario.LoadBuiltin("normal")
	if err != nil {
		normal = scenario.Scenario{APIVersion: scenario.APIVersion, Kind: scenario.Kind, Metadata: scenario.Metadata{Name: "normal"}}
	}
	m := &Model{nodeName: nodeName, gpuCount: gpuCount, normal: normal, now: time.Now}
	m.Apply(normal, "0")
	return m
}

func (m *Model) Apply(s scenario.Scenario, generation string) {
	appliedAt := m.currentTime()
	matched := m.selectorMatches(s.Spec.Targets.Selector)
	active := m.normalScenario()
	if matched {
		active = s
	}
	readings := m.buildReadings(active)
	fabric := m.buildFabricReading(active)
	fault := exporterFault(active)
	var expiresAt time.Time
	if matched && strings.TrimSpace(s.Spec.Duration) != "" {
		if duration, err := time.ParseDuration(s.Spec.Duration); err == nil {
			expiresAt = appliedAt.Add(duration)
		}
	}
	m.mu.Lock()
	m.scenario = active
	m.generation = generation
	m.updatedAt = appliedAt.UTC()
	m.readings = readings
	m.fault = fault
	m.expiresAt = expiresAt
	m.fabric = fabric
	m.mu.Unlock()
}

func (m *Model) IsUnavailable() bool {
	m.refreshExpired()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fault == "unavailable"
}

func (m *Model) Snapshot() StateSnapshot {
	m.refreshExpired()
	m.mu.RLock()
	defer m.mu.RUnlock()
	readings := append([]Reading(nil), m.readings...)
	return StateSnapshot{
		Node:          m.nodeName,
		Scenario:      m.scenario.Name(),
		Generation:    m.generation,
		ExporterFault: m.fault,
		UpdatedAt:     m.updatedAt,
		Readings:      readings,
		Fabric:        m.fabric,
	}
}

func (m *Model) StateJSON() ([]byte, error) {
	return json.MarshalIndent(m.Snapshot(), "", "  ")
}

func (m *Model) Metrics() string {
	m.refreshExpired()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var b strings.Builder
	writeMetricHelp(&b, "gpu_lab_gpu_utilization_percent", "Synthetic GPU utilization percent.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_memory_used_bytes", "Synthetic GPU memory used in bytes.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_memory_total_bytes", "Synthetic GPU memory total in bytes.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_temperature_celsius", "Synthetic GPU temperature in Celsius.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_power_watts", "Synthetic GPU power draw in watts.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_xid_code", "Synthetic current GPU XID code; zero means none.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_health", "Synthetic GPU health, one for healthy and zero for unhealthy.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_health_status", "Synthetic DCGM-style GPU health status: 0 PASS, 10 WARN, 20 FAIL.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_ecc_dbe_total", "Synthetic cumulative uncorrectable double-bit ECC errors.", "counter")
	writeMetricHelp(&b, "gpu_lab_gpu_power_violation_total", "Synthetic cumulative power-limit violation events.", "counter")
	writeMetricHelp(&b, "gpu_lab_gpu_pcie_replay_total", "Synthetic cumulative PCIe replay events.", "counter")
	writeMetricHelp(&b, "gpu_lab_gpu_throttle_active", "Synthetic GPU clock throttling state by reason.", "gauge")
	writeMetricHelp(&b, "gpu_lab_gpu_allocated", "Synthetic GPU allocation state; one means reserved by a workload.", "gauge")
	writeMetricHelp(&b, "gpu_lab_node_gpu_capacity", "Synthetic node GPU capacity.", "gauge")
	writeMetricHelp(&b, "gpu_lab_node_gpu_allocatable", "Synthetic node GPU allocatable count.", "gauge")
	writeMetricHelp(&b, "gpu_lab_exporter_up", "Synthetic dcgm-exporter projection availability.", "gauge")
	writeMetricHelp(&b, "gpu_lab_scenario_info", "Active gpu-lab scenario.", "gauge")
	writeMetricHelp(&b, "gpu_lab_scenario_generation", "Applied scenario generation.", "gauge")
	writeMetricHelp(&b, "gpu_lab_ib_port_up", "Synthetic InfiniBand HCA port availability; one means the port is up.", "gauge")
	writeMetricHelp(&b, "gpu_lab_ib_port_state", "Synthetic InfiniBand HCA port state; the state and physical_state labels describe the port.", "gauge")
	writeMetricHelp(&b, "gpu_lab_ib_link_rate_gbps", "Synthetic InfiniBand link rate in gigabits per second.", "gauge")
	writeMetricHelp(&b, "gpu_lab_ib_tx_bytes_total", "Synthetic cumulative InfiniBand transmit bytes.", "counter")
	writeMetricHelp(&b, "gpu_lab_ib_rx_bytes_total", "Synthetic cumulative InfiniBand receive bytes.", "counter")
	writeMetricHelp(&b, "gpu_lab_ib_symbol_errors_total", "Synthetic cumulative InfiniBand symbol errors.", "counter")
	writeMetricHelp(&b, "gpu_lab_ib_link_error_recovery_total", "Synthetic cumulative InfiniBand link error recovery events.", "counter")
	writeMetricHelp(&b, "gpu_lab_ib_link_downed_total", "Synthetic cumulative InfiniBand link-down events.", "counter")
	writeMetricHelp(&b, "gpu_lab_ib_xmit_discards_total", "Synthetic cumulative InfiniBand transmit discards.", "counter")
	writeMetricHelp(&b, "gpu_lab_ib_xmit_wait_total", "Synthetic cumulative InfiniBand transmit wait events.", "counter")
	writeMetricHelp(&b, "gpu_lab_rdma_retries_total", "Synthetic cumulative RDMA retry events.", "counter")
	writeMetricHelp(&b, "gpu_lab_rdma_timeouts_total", "Synthetic cumulative RDMA timeout events.", "counter")
	writeMetricHelp(&b, "gpu_lab_fabric_delay_seconds", "Synthetic fabric delay applied to the distributed-training coupling contract, in seconds.", "gauge")
	for _, reading := range m.readings {
		labels := fmt.Sprintf(`node="%s",gpu="%s"`, escapeLabel(m.nodeName), escapeLabel(reading.GPU))
		fmt.Fprintf(&b, "gpu_lab_gpu_utilization_percent{%s} %s\n", labels, floatString(reading.UtilizationPercent))
		fmt.Fprintf(&b, "gpu_lab_gpu_memory_used_bytes{%s} %d\n", labels, reading.MemoryUsedBytes)
		fmt.Fprintf(&b, "gpu_lab_gpu_memory_total_bytes{%s} %d\n", labels, reading.MemoryTotalBytes)
		fmt.Fprintf(&b, "gpu_lab_gpu_temperature_celsius{%s} %s\n", labels, floatString(reading.TemperatureCelsius))
		fmt.Fprintf(&b, "gpu_lab_gpu_power_watts{%s} %s\n", labels, floatString(reading.PowerWatts))
		fmt.Fprintf(&b, "gpu_lab_gpu_xid_code{%s} %d\n", labels, reading.XIDCode)
		fmt.Fprintf(&b, "gpu_lab_gpu_health{%s} %d\n", labels, reading.Health)
		healthStatus := 0
		if reading.Health == 0 {
			healthStatus = 20
		}
		fmt.Fprintf(&b, "gpu_lab_gpu_health_status{%s} %d\n", labels, healthStatus)
		fmt.Fprintf(&b, "gpu_lab_gpu_ecc_dbe_total{%s} %d\n", labels, reading.ECCDbeTotal)
		fmt.Fprintf(&b, "gpu_lab_gpu_power_violation_total{%s} %d\n", labels, reading.PowerViolationTotal)
		fmt.Fprintf(&b, "gpu_lab_gpu_pcie_replay_total{%s} %d\n", labels, reading.PCIeReplayTotal)
		throttleReason := reading.ThrottleReason
		if throttleReason == "" {
			throttleReason = "none"
		}
		throttleLabels := fmt.Sprintf(`%s,reason="%s"`, labels, escapeLabel(throttleReason))
		fmt.Fprintf(&b, "gpu_lab_gpu_throttle_active{%s} %d\n", throttleLabels, reading.ThrottleActive)
		fmt.Fprintf(&b, "gpu_lab_gpu_allocated{%s} %d\n", labels, reading.Allocated)
	}
	capacity := scenarioDefaultGPUCount(m.scenario.Spec.Metrics.GPUCapacity, DefaultGPUCount)
	allocatable := scenarioDefaultGPUCount(m.scenario.Spec.Metrics.GPUAllocatable, capacity)
	nodeLabels := fmt.Sprintf(`node="%s"`, escapeLabel(m.nodeName))
	fmt.Fprintf(&b, "gpu_lab_node_gpu_capacity{%s} %d\n", nodeLabels, capacity)
	fmt.Fprintf(&b, "gpu_lab_node_gpu_allocatable{%s} %d\n", nodeLabels, allocatable)
	fmt.Fprintf(&b, "gpu_lab_exporter_up{node=\"%s\"} 1\n", escapeLabel(m.nodeName))
	fmt.Fprintf(&b, "gpu_lab_scenario_info{node=\"%s\",scenario=\"%s\"} 1\n", escapeLabel(m.nodeName), escapeLabel(m.scenario.Name()))
	fmt.Fprintf(&b, "gpu_lab_scenario_generation{node=\"%s\"} %s\n", escapeLabel(m.nodeName), floatString(parseGeneration(m.generation)))
	fabricLabels := fmt.Sprintf(`node="%s",hca="%s",port="%d",link_layer="%s"`,
		escapeLabel(m.nodeName), escapeLabel(m.fabric.HCA), m.fabric.Port, escapeLabel(m.fabric.LinkLayer))
	fabricStateLabels := fmt.Sprintf(`%s,state="%s",physical_state="%s"`, fabricLabels,
		escapeLabel(m.fabric.State), escapeLabel(m.fabric.PhysicalState))
	fmt.Fprintf(&b, "gpu_lab_ib_port_up{%s} %d\n", fabricLabels, m.fabric.PortUp)
	fmt.Fprintf(&b, "gpu_lab_ib_port_state{%s} 1\n", fabricStateLabels)
	fmt.Fprintf(&b, "gpu_lab_ib_link_rate_gbps{%s} %s\n", fabricLabels, floatString(m.fabric.LinkRateGbps))
	fmt.Fprintf(&b, "gpu_lab_ib_tx_bytes_total{%s} %d\n", fabricLabels, m.fabric.TxBytesTotal)
	fmt.Fprintf(&b, "gpu_lab_ib_rx_bytes_total{%s} %d\n", fabricLabels, m.fabric.RxBytesTotal)
	fmt.Fprintf(&b, "gpu_lab_ib_symbol_errors_total{%s} %d\n", fabricLabels, m.fabric.SymbolErrorsTotal)
	fmt.Fprintf(&b, "gpu_lab_ib_link_error_recovery_total{%s} %d\n", fabricLabels, m.fabric.LinkErrorRecoveryTotal)
	fmt.Fprintf(&b, "gpu_lab_ib_link_downed_total{%s} %d\n", fabricLabels, m.fabric.LinkDownedTotal)
	fmt.Fprintf(&b, "gpu_lab_ib_xmit_discards_total{%s} %d\n", fabricLabels, m.fabric.XmitDiscardsTotal)
	fmt.Fprintf(&b, "gpu_lab_ib_xmit_wait_total{%s} %d\n", fabricLabels, m.fabric.XmitWaitTotal)
	fmt.Fprintf(&b, "gpu_lab_rdma_retries_total{%s} %d\n", fabricLabels, m.fabric.RDMARetriesTotal)
	fmt.Fprintf(&b, "gpu_lab_rdma_timeouts_total{%s} %d\n", fabricLabels, m.fabric.RDMATimeoutsTotal)
	fmt.Fprintf(&b, "gpu_lab_fabric_delay_seconds{%s} %s\n", fabricLabels, floatString(m.fabric.FabricDelaySeconds))
	return b.String()
}

func (m *Model) buildReadings(s scenario.Scenario) []Reading {
	readings := make([]Reading, 0, m.gpuCount)
	allocationCount := 0
	if s.Spec.Metrics.GPUAllocatedCount != nil {
		allocationCount = *s.Spec.Metrics.GPUAllocatedCount
		if action, ok := scenario.HasAction(s, "create_gpu_workload"); ok && !m.selectorMatches(action.NodeSelector) {
			allocationCount = 0
		}
	}
	allocated := 0
	for i := 0; i < m.gpuCount; i++ {
		targeted := gpuIndexMatches(i, s.Spec.Targets.GPUIndices)
		metrics := m.normal.Spec.Metrics
		if targeted {
			metrics = s.Spec.Metrics
		}
		total := DefaultMemoryTotal
		if metrics.GPUMemoryTotalBytes != nil {
			total = *metrics.GPUMemoryTotalBytes
		}
		memoryPercent := defaultMemoryPercent
		if metrics.GPUMemoryUsedPercent != nil {
			memoryPercent = *metrics.GPUMemoryUsedPercent
		}
		reading := Reading{
			GPU:                fmt.Sprintf("%s-%02d", m.nodeName, i),
			UtilizationPercent: defaultUtilization,
			MemoryUsedBytes:    int64(float64(total) * memoryPercent / 100),
			MemoryTotalBytes:   total,
			TemperatureCelsius: defaultTemperature,
			PowerWatts:         defaultPower,
			XIDCode:            0,
			Health:             1,
			ThrottleReason:     "none",
		}
		if metrics.GPUUtilizationPercent != nil {
			reading.UtilizationPercent = *metrics.GPUUtilizationPercent
		}
		if metrics.TemperatureCelsius != nil {
			reading.TemperatureCelsius = *metrics.TemperatureCelsius
		}
		if metrics.PowerWatts != nil {
			reading.PowerWatts = *metrics.PowerWatts
		}
		if metrics.XIDCode != nil {
			reading.XIDCode = *metrics.XIDCode
		}
		if metrics.Health != nil {
			reading.Health = *metrics.Health
		}
		if metrics.ECCDbeTotal != nil {
			reading.ECCDbeTotal = *metrics.ECCDbeTotal
		}
		if metrics.PowerViolationTotal != nil {
			reading.PowerViolationTotal = *metrics.PowerViolationTotal
		}
		if metrics.PCIeReplayTotal != nil {
			reading.PCIeReplayTotal = *metrics.PCIeReplayTotal
		}
		if metrics.ThrottleActive != nil {
			reading.ThrottleActive = *metrics.ThrottleActive
		}
		if metrics.ThrottleReason != "" {
			reading.ThrottleReason = metrics.ThrottleReason
		}
		if targeted && allocated < allocationCount {
			reading.Allocated = 1
			allocated++
		}
		readings = append(readings, reading)
	}
	return readings
}

func (m *Model) buildFabricReading(s scenario.Scenario) FabricReading {
	metrics := s.Spec.Metrics
	reading := FabricReading{
		Synthetic:     true,
		HCA:           DefaultHCA,
		Port:          DefaultFabricPort,
		LinkLayer:     DefaultLinkLayer,
		PortUp:        1,
		State:         "ACTIVE",
		PhysicalState: "LINK_UP",
		LinkRateGbps:  DefaultIBLinkRateGbps,
	}
	if metrics.IBPortUp != nil {
		reading.PortUp = *metrics.IBPortUp
	}
	if metrics.IBState != "" {
		reading.State = metrics.IBState
	}
	if metrics.IBPhysicalState != "" {
		reading.PhysicalState = metrics.IBPhysicalState
	}
	if metrics.IBLinkRateGbps != nil {
		reading.LinkRateGbps = *metrics.IBLinkRateGbps
	}
	if metrics.IBTxBytesTotal != nil {
		reading.TxBytesTotal = *metrics.IBTxBytesTotal
	}
	if metrics.IBRxBytesTotal != nil {
		reading.RxBytesTotal = *metrics.IBRxBytesTotal
	}
	if metrics.IBSymbolErrorsTotal != nil {
		reading.SymbolErrorsTotal = *metrics.IBSymbolErrorsTotal
	}
	if metrics.IBLinkErrorRecoveryTotal != nil {
		reading.LinkErrorRecoveryTotal = *metrics.IBLinkErrorRecoveryTotal
	}
	if metrics.IBLinkDownedTotal != nil {
		reading.LinkDownedTotal = *metrics.IBLinkDownedTotal
	}
	if metrics.IBXmitDiscardsTotal != nil {
		reading.XmitDiscardsTotal = *metrics.IBXmitDiscardsTotal
	}
	if metrics.IBXmitWaitTotal != nil {
		reading.XmitWaitTotal = *metrics.IBXmitWaitTotal
	}
	if metrics.RDMARetriesTotal != nil {
		reading.RDMARetriesTotal = *metrics.RDMARetriesTotal
	}
	if metrics.RDMATimeoutsTotal != nil {
		reading.RDMATimeoutsTotal = *metrics.RDMATimeoutsTotal
	}
	if metrics.FabricDelaySeconds != nil {
		reading.FabricDelaySeconds = *metrics.FabricDelaySeconds
	}
	return reading
}

func gpuIndexMatches(index int, targets []int) bool {
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		if target == index {
			return true
		}
	}
	return false
}

func (m *Model) refreshExpired() {
	now := m.currentTime()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.expiresAt.IsZero() || now.Before(m.expiresAt) {
		return
	}
	normal := m.normalScenario()
	m.scenario = normal
	m.readings = m.buildReadings(normal)
	m.fabric = m.buildFabricReading(normal)
	m.fault = ""
	m.expiresAt = time.Time{}
	m.updatedAt = now.UTC()
}

func (m *Model) currentTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *Model) normalScenario() scenario.Scenario {
	if m.normal.Metadata.Name != "" {
		return m.normal
	}
	normal, err := scenario.LoadBuiltin("normal")
	if err != nil {
		return scenario.Scenario{APIVersion: scenario.APIVersion, Kind: scenario.Kind, Metadata: scenario.Metadata{Name: "normal"}}
	}
	return normal
}

func (m *Model) selectorMatches(selector map[string]string) bool {
	labels := map[string]string{
		"gpu.lab/type":           "fake",
		"gpu.lab/node-id":        syntheticNodeID(m.nodeName),
		"kubernetes.io/hostname": m.nodeName,
	}
	for key, expected := range selector {
		if labels[key] != expected {
			return false
		}
	}
	return true
}

func exporterFault(s scenario.Scenario) string {
	if _, ok := scenario.HasAction(s, "exporter_fault"); ok {
		return "unavailable"
	}
	return ""
}

func scenarioDefaultGPUCount(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func syntheticNodeID(nodeName string) string {
	if strings.HasPrefix(nodeName, "gpu-node-") {
		return nodeName
	}
	if nodeName == "gpu-lab-worker" {
		return "gpu-node-01"
	}
	if strings.HasPrefix(nodeName, "gpu-lab-worker") {
		suffix := strings.TrimPrefix(nodeName, "gpu-lab-worker")
		if suffix == "2" {
			return "gpu-node-02"
		}
		if suffix == "3" {
			return "gpu-node-03"
		}
	}
	return nodeName
}

func writeMetricHelp(b *strings.Builder, name, help, metricType string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func parseGeneration(value string) float64 {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func floatString(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func escapeLabel(value string) string {
	return strings.NewReplacer("\\", `\\`, "\n", `\n`, `\"`, `\"`).Replace(value)
}
