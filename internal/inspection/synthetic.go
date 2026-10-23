package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/stateclient"
)

const MaxPrometheusSampleAge = 2 * time.Minute

type fieldValue struct {
	Value  float64
	Raw    string
	Status Status
	Detail string
}

type gpuTelemetry struct {
	Node       string
	GPU        string
	Fields     map[string]fieldValue
	Ages       map[string]fieldValue
	Duplicates map[string]bool
}

type metricDefinition struct {
	Name       string
	Prometheus string
}

var inspectionMetrics = []metricDefinition{
	{Name: "health", Prometheus: "gpu_lab_gpu_health"},
	{Name: "xid", Prometheus: "gpu_lab_gpu_xid_code"},
	{Name: "ecc", Prometheus: "gpu_lab_gpu_ecc_dbe_total"},
	{Name: "temperature", Prometheus: "gpu_lab_gpu_temperature_celsius"},
	{Name: "utilization", Prometheus: "gpu_lab_gpu_utilization_percent"},
	{Name: "memory_used", Prometheus: "gpu_lab_gpu_memory_used_bytes"},
	{Name: "memory_total", Prometheus: "gpu_lab_gpu_memory_total_bytes"},
}

func inspectSynthetic(ctx context.Context, requested Mode, evidence syntheticEvidence, d Dependencies) (Report, error) {
	report := NewReport(ModeSynthetic, requested, evidence.Source)
	report.Add("GPU Lab evidence", StatusPass, evidence.Source, "positive synthetic-environment evidence found")
	var readings []gpuTelemetry
	var err error
	var snapshotNote string
	if evidence.StateURL != "" {
		readings, err = readStateEndpoint(ctx, d, evidence.StateURL)
		snapshotNote = "state endpoint retrieval-time snapshot at " + d.Now().UTC().Format(time.RFC3339) + "; field sample age is not supplied by the endpoint"
	} else {
		readings, err = readPrometheus(ctx, d, evidence.Kubeconfig)
		snapshotNote = "Prometheus sample ages use time()-timestamp(metric), not the Prometheus evaluation timestamp; maximum accepted age is " + MaxPrometheusSampleAge.String()
	}
	if err != nil {
		report.Add("GPU telemetry collection", StatusUnavailable, "", err.Error())
		return report, fmt.Errorf("collect synthetic GPU telemetry: %w", err)
	}
	if len(readings) == 0 {
		report.Add("GPU telemetry collection", StatusUnavailable, "", "source returned no identifiable GPU readings")
		return report, errors.New("synthetic GPU telemetry contains no identifiable GPU readings")
	}
	report.Add("snapshot timing", StatusPass, "", snapshotNote)
	sort.Slice(readings, func(i, j int) bool {
		if readings[i].Node == readings[j].Node {
			return readings[i].GPU < readings[j].GPU
		}
		return readings[i].Node < readings[j].Node
	})
	for _, reading := range readings {
		addSyntheticGPUChecks(&report, reading, evidence.StateURL == "")
	}
	return report, nil
}

type stateReading struct {
	GPU                string          `json:"gpu"`
	Health             json.RawMessage `json:"health"`
	XIDCode            json.RawMessage `json:"xid_code"`
	ECCDbeTotal        json.RawMessage `json:"ecc_dbe_total"`
	TemperatureCelsius json.RawMessage `json:"temperature_celsius"`
	UtilizationPercent json.RawMessage `json:"utilization_percent"`
	MemoryUsedBytes    json.RawMessage `json:"memory_used_bytes"`
	MemoryTotalBytes   json.RawMessage `json:"memory_total_bytes"`
}

func readStateEndpoint(ctx context.Context, d Dependencies, endpoint string) ([]gpuTelemetry, error) {
	var state struct {
		Node     string         `json:"node"`
		Readings []stateReading `json:"readings"`
	}
	if err := stateclient.Fetch(ctx, d.HTTPClient, endpoint, &state); err != nil {
		return nil, err
	}
	readings := make([]gpuTelemetry, 0, len(state.Readings))
	for _, item := range state.Readings {
		if strings.TrimSpace(item.GPU) == "" {
			continue
		}
		telemetry := gpuTelemetry{Node: strings.TrimSpace(state.Node), GPU: strings.TrimSpace(item.GPU), Fields: map[string]fieldValue{}, Ages: map[string]fieldValue{}, Duplicates: map[string]bool{}}
		for field, raw := range map[string]json.RawMessage{
			"health": item.Health, "xid": item.XIDCode, "ecc": item.ECCDbeTotal,
			"temperature": item.TemperatureCelsius, "utilization": item.UtilizationPercent,
			"memory_used": item.MemoryUsedBytes, "memory_total": item.MemoryTotalBytes,
		} {
			telemetry.Fields[field] = parseJSONNumber(raw)
		}
		readings = append(readings, telemetry)
	}
	return readings, nil
}

func parseJSONNumber(raw json.RawMessage) fieldValue {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return fieldValue{Status: StatusUnavailable, Detail: "field was not reported"}
	}
	var value float64
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return fieldValue{Status: StatusUnavailable, Detail: "field is invalid"}
		}
		text = strings.TrimSpace(text)
		if strings.EqualFold(text, "n/a") || strings.EqualFold(text, "not supported") {
			return fieldValue{Raw: text, Status: StatusUnsupported, Detail: "source reports this field as unsupported"}
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil || !finite(parsed) {
			return fieldValue{Raw: text, Status: StatusUnavailable, Detail: "field is not a finite valid number"}
		}
		value = parsed
	} else {
		if err := json.Unmarshal(raw, &value); err != nil || !finite(value) {
			return fieldValue{Raw: strings.TrimSpace(string(raw)), Status: StatusUnavailable, Detail: "field is not a finite valid number"}
		}
	}
	return fieldValue{Value: value, Raw: strings.TrimSpace(string(raw)), Status: StatusPass}
}

func readPrometheus(ctx context.Context, d Dependencies, kubeconfig string) ([]gpuTelemetry, error) {
	query := prometheusInspectionQuery()
	path := "/api/v1/namespaces/gpu-lab-monitoring/services/gpu-lab-monitoring-kube-pr-prometheus:9090/proxy/api/v1/query?query=" + url.QueryEscape(query)
	stdout, _, err := d.RunCommand(ctx, "kubectl", "--kubeconfig", kubeconfig, "--context", labContext, "get", "--raw", path)
	if err != nil {
		return nil, fmt.Errorf("query Prometheus through dedicated gpu-lab kubeconfig: %w", err)
	}
	return parsePrometheusInspection(stdout)
}

func prometheusInspectionQuery() string {
	parts := make([]string, 0, len(inspectionMetrics)*2)
	for _, metric := range inspectionMetrics {
		parts = append(parts,
			`label_replace(`+metric.Prometheus+`, "inspection_field", "`+metric.Name+`", "__name__", ".*")`,
			`label_replace(time() - timestamp(`+metric.Prometheus+`), "inspection_field", "`+metric.Name+`_age", "__name__", ".*")`,
		)
	}
	return strings.Join(parts, " or ")
}

func parsePrometheusInspection(data []byte) ([]gpuTelemetry, error) {
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Prometheus inspection response: %w", err)
	}
	if response.Status != "success" {
		if response.Error != "" {
			return nil, fmt.Errorf("Prometheus returned %q: %s", response.Status, response.Error)
		}
		return nil, fmt.Errorf("Prometheus returned %q", response.Status)
	}
	if response.Data.ResultType != "vector" {
		return nil, fmt.Errorf("Prometheus inspection query returned %q instead of a vector", response.Data.ResultType)
	}
	byGPU := map[string]*gpuTelemetry{}
	for _, sample := range response.Data.Result {
		node, gpu, field := sample.Metric["node"], sample.Metric["gpu"], sample.Metric["inspection_field"]
		if node == "" || gpu == "" || field == "" {
			continue
		}
		key := node + "\x00" + gpu
		reading := byGPU[key]
		if reading == nil {
			reading = &gpuTelemetry{Node: node, GPU: gpu, Fields: map[string]fieldValue{}, Ages: map[string]fieldValue{}, Duplicates: map[string]bool{}}
			byGPU[key] = reading
		}
		if len(sample.Value) != 2 {
			reading.Duplicates[field] = true
			continue
		}
		valueText, err := promScalar(sample.Value[1])
		if err != nil {
			reading.Duplicates[field] = true
			continue
		}
		value := parsePromNumber(valueText)
		destination := reading.Fields
		fieldName := field
		if strings.HasSuffix(field, "_age") {
			destination = reading.Ages
			fieldName = strings.TrimSuffix(field, "_age")
		}
		if _, exists := destination[fieldName]; exists {
			reading.Duplicates[field] = true
			continue
		}
		destination[fieldName] = value
	}
	readings := make([]gpuTelemetry, 0, len(byGPU))
	for _, reading := range byGPU {
		readings = append(readings, *reading)
	}
	return readings, nil
}

func promScalar(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}
	return "", errors.New("Prometheus sample value is not a scalar")
}

func parsePromNumber(text string) fieldValue {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || !finite(value) {
		return fieldValue{Raw: text, Status: StatusUnavailable, Detail: "sample is not a finite valid number"}
	}
	return fieldValue{Value: value, Raw: text, Status: StatusPass}
}

func addSyntheticGPUChecks(report *Report, reading gpuTelemetry, prometheus bool) {
	identity := reading.GPU
	if reading.Node != "" {
		identity = reading.Node + "/" + identity
	}
	report.Add(identity+" synthetic identity", StatusPass, identity, "GPU Lab telemetry label; this does not identify physical hardware")

	health, healthStatus, healthDetail := telemetryField(reading, "health", prometheus)
	switch {
	case healthStatus != StatusPass:
		report.Add(identity+" health", healthStatus, health.Raw, healthDetail)
	case health.Value == 1:
		report.Add(identity+" health", StatusPass, health.Raw, "GPU Lab synthetic health metric reports healthy; not hardware health proof"+freshSuffix(prometheus, reading, "health"))
	case health.Value == 0:
		report.Add(identity+" health", StatusFail, health.Raw, "GPU Lab synthetic health metric reports unhealthy"+freshSuffix(prometheus, reading, "health"))
	default:
		report.Add(identity+" health", StatusUnavailable, health.Raw, "health value must be 0 or 1")
	}

	xid, xidStatus, xidDetail := telemetryField(reading, "xid", prometheus)
	if xidStatus != StatusPass {
		report.Add(identity+" Xid", xidStatus, xid.Raw, xidDetail)
	} else if xid.Value < 0 || math.Trunc(xid.Value) != xid.Value {
		report.Add(identity+" Xid", StatusUnavailable, xid.Raw, "Xid value is not a non-negative integer")
	} else if xid.Value == 0 {
		report.Add(identity+" Xid", StatusPass, xid.Raw, "no current synthetic Xid code reported"+freshSuffix(prometheus, reading, "xid"))
	} else {
		report.Add(identity+" Xid", StatusFail, xid.Raw, "synthetic source reports a current Xid code"+freshSuffix(prometheus, reading, "xid"))
	}

	ecc, eccStatus, eccDetail := telemetryField(reading, "ecc", prometheus)
	if eccStatus != StatusPass {
		report.Add(identity+" aggregate uncorrected ECC (lifetime history)", eccStatus, ecc.Raw, eccDetail)
	} else if ecc.Value < 0 || math.Trunc(ecc.Value) != ecc.Value {
		report.Add(identity+" aggregate uncorrected ECC (lifetime history)", StatusUnavailable, ecc.Raw, "ECC count is not a non-negative integer")
	} else if ecc.Value == 0 {
		report.Add(identity+" aggregate uncorrected ECC (lifetime history)", StatusPass, ecc.Raw, "cumulative counter is zero; this is not a current-fault test"+freshSuffix(prometheus, reading, "ecc"))
	} else {
		report.Add(identity+" aggregate uncorrected ECC (lifetime history)", StatusWarn, ecc.Raw, "cumulative counter records history and does not establish a current fault"+freshSuffix(prometheus, reading, "ecc"))
	}

	temperature, tempStatus, tempDetail := telemetryField(reading, "temperature", prometheus)
	if tempStatus != StatusPass {
		report.Add(identity+" temperature", tempStatus, temperature.Raw, tempDetail)
	} else {
		report.Add(identity+" temperature", StatusPass, temperature.Raw+" °C", "display only; no generic temperature threshold applied"+freshSuffix(prometheus, reading, "temperature"))
	}

	utilization, utilizationStatus, utilizationDetail := telemetryField(reading, "utilization", prometheus)
	if utilizationStatus != StatusPass {
		report.Add(identity+" utilization", utilizationStatus, utilization.Raw, utilizationDetail)
	} else if utilization.Value < 0 || utilization.Value > 100 {
		report.Add(identity+" utilization", StatusUnavailable, utilization.Raw, "value is outside the valid 0–100 percent range")
	} else {
		report.Add(identity+" utilization", StatusPass, utilization.Raw+"%", "synthetic utilization reading"+freshSuffix(prometheus, reading, "utilization"))
	}

	used, usedStatus, usedDetail := telemetryField(reading, "memory_used", prometheus)
	total, totalStatus, totalDetail := telemetryField(reading, "memory_total", prometheus)
	if usedStatus != StatusPass || totalStatus != StatusPass {
		report.Add(identity+" memory used / total", combineMissingStatus(usedStatus, totalStatus), used.Raw+" / "+total.Raw+" bytes", firstReason(usedDetail, totalDetail))
	} else if used.Value < 0 || total.Value <= 0 || used.Value > total.Value {
		report.Add(identity+" memory used / total", StatusUnavailable, used.Raw+" / "+total.Raw+" bytes", "memory values are invalid or inconsistent")
	} else {
		report.Add(identity+" memory used / total", StatusPass, used.Raw+" / "+total.Raw+" bytes", "synthetic memory reading"+freshSuffix(prometheus, reading, "memory_used"))
	}
}

func telemetryField(reading gpuTelemetry, name string, prometheus bool) (fieldValue, Status, string) {
	if reading.Duplicates[name] {
		return fieldValue{Status: StatusUnavailable}, StatusUnavailable, "multiple or malformed samples make this field ambiguous"
	}
	value, found := reading.Fields[name]
	if !found {
		return fieldValue{Status: StatusUnavailable}, StatusUnavailable, "field was not reported"
	}
	if value.Status != StatusPass {
		return value, value.Status, value.Detail
	}
	if !finite(value.Value) {
		return value, StatusUnavailable, "field is not finite"
	}
	if !prometheus {
		return value, StatusPass, ""
	}
	if reading.Duplicates[name+"_age"] {
		return value, StatusUnavailable, "sample age is ambiguous"
	}
	age, found := reading.Ages[name]
	if !found || age.Status != StatusPass || !finite(age.Value) {
		return value, StatusUnavailable, "Prometheus sample freshness is unavailable"
	}
	if age.Value < 0 || age.Value > MaxPrometheusSampleAge.Seconds() {
		return value, StatusUnavailable, fmt.Sprintf("Prometheus sample is stale or has invalid age (%.3fs)", age.Value)
	}
	return value, StatusPass, ""
}

func freshSuffix(prometheus bool, reading gpuTelemetry, field string) string {
	if !prometheus {
		return "; value is from the retrieval-time state snapshot"
	}
	age := reading.Ages[field]
	return fmt.Sprintf("; fresh Prometheus sample (%.3fs old)", age.Value)
}

func firstReason(reasons ...string) string {
	for _, reason := range reasons {
		if reason != "" {
			return reason
		}
	}
	return "one or more memory fields are unavailable"
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
