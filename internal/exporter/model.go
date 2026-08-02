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
	DefaultGPUCount      = 8
	DefaultMemoryTotal   = int64(16 * 1024 * 1024 * 1024)
	defaultUtilization   = 15.0
	defaultMemoryPercent = 20.0
	defaultTemperature   = 45.0
	defaultPower         = 80.0
)

type Reading struct {
	GPU                string  `json:"gpu"`
	UtilizationPercent float64 `json:"utilization_percent"`
	MemoryUsedBytes    int64   `json:"memory_used_bytes"`
	MemoryTotalBytes   int64   `json:"memory_total_bytes"`
	TemperatureCelsius float64 `json:"temperature_celsius"`
	PowerWatts         float64 `json:"power_watts"`
	XIDCode            int     `json:"xid_code"`
	Health             int     `json:"health"`
}

type StateSnapshot struct {
	Node          string    `json:"node"`
	Scenario      string    `json:"scenario"`
	Generation    string    `json:"generation"`
	ExporterFault string    `json:"exporter_fault,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
	Readings      []Reading `json:"readings"`
}

type Model struct {
	mu         sync.RWMutex
	nodeName   string
	gpuCount   int
	scenario   scenario.Scenario
	generation string
	updatedAt  time.Time
	readings   []Reading
	fault      string
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
	m := &Model{nodeName: nodeName, gpuCount: gpuCount}
	m.Apply(normal, "0")
	return m
}

func (m *Model) Apply(s scenario.Scenario, generation string) {
	readings := make([]Reading, 0, m.gpuCount)
	metrics := s.Spec.Metrics
	for i := 0; i < m.gpuCount; i++ {
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
		readings = append(readings, reading)
	}
	fault := ""
	if _, ok := scenario.HasAction(s, "exporter_fault"); ok {
		fault = "unavailable"
	}
	m.mu.Lock()
	m.scenario = s
	m.generation = generation
	m.updatedAt = time.Now().UTC()
	m.readings = readings
	m.fault = fault
	m.mu.Unlock()
}

func (m *Model) IsUnavailable() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fault == "unavailable"
}

func (m *Model) Snapshot() StateSnapshot {
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
	}
}

func (m *Model) StateJSON() ([]byte, error) {
	return json.MarshalIndent(m.Snapshot(), "", "  ")
}

func (m *Model) Metrics() string {
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
	writeMetricHelp(&b, "gpu_lab_exporter_up", "Synthetic dcgm-exporter projection availability.", "gauge")
	writeMetricHelp(&b, "gpu_lab_scenario_info", "Active gpu-lab scenario.", "gauge")
	writeMetricHelp(&b, "gpu_lab_scenario_generation", "Applied scenario generation.", "gauge")
	for _, reading := range m.readings {
		labels := fmt.Sprintf(`node="%s",gpu="%s"`, escapeLabel(m.nodeName), escapeLabel(reading.GPU))
		fmt.Fprintf(&b, "gpu_lab_gpu_utilization_percent{%s} %s\n", labels, floatString(reading.UtilizationPercent))
		fmt.Fprintf(&b, "gpu_lab_gpu_memory_used_bytes{%s} %d\n", labels, reading.MemoryUsedBytes)
		fmt.Fprintf(&b, "gpu_lab_gpu_memory_total_bytes{%s} %d\n", labels, reading.MemoryTotalBytes)
		fmt.Fprintf(&b, "gpu_lab_gpu_temperature_celsius{%s} %s\n", labels, floatString(reading.TemperatureCelsius))
		fmt.Fprintf(&b, "gpu_lab_gpu_power_watts{%s} %s\n", labels, floatString(reading.PowerWatts))
		fmt.Fprintf(&b, "gpu_lab_gpu_xid_code{%s} %d\n", labels, reading.XIDCode)
		fmt.Fprintf(&b, "gpu_lab_gpu_health{%s} %d\n", labels, reading.Health)
	}
	fmt.Fprintf(&b, "gpu_lab_exporter_up{node=\"%s\"} 1\n", escapeLabel(m.nodeName))
	fmt.Fprintf(&b, "gpu_lab_scenario_info{node=\"%s\",scenario=\"%s\"} 1\n", escapeLabel(m.nodeName), escapeLabel(m.scenario.Name()))
	fmt.Fprintf(&b, "gpu_lab_scenario_generation{node=\"%s\"} %s\n", escapeLabel(m.nodeName), floatString(parseGeneration(m.generation)))
	return b.String()
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
