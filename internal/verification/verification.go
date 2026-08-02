package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

const (
	KubeContext     = "gpu-lab"
	DemoNamespace   = "gpu-lab-demo"
	SystemNS        = "gpu-lab-system"
	MonitoringNS    = "gpu-lab-monitoring"
	PrometheusSvc   = "gpu-lab-monitoring-kube-pr-prometheus:9090"
	ExporterService = "dcgm-exporter"
)

type Check struct {
	Name   string
	Detail string
	Passed bool
}

type Report struct {
	Scenario string
	Checks   []Check
}

func (r Report) Passed() bool {
	if len(r.Checks) == 0 {
		return false
	}
	for _, check := range r.Checks {
		if !check.Passed {
			return false
		}
	}
	return true
}

type Verifier struct {
	Runner       runner.Runner
	Timeout      time.Duration
	PollInterval time.Duration
}

func New(r runner.Runner) Verifier {
	return Verifier{
		Runner:       r,
		Timeout:      45 * time.Second,
		PollInterval: 2 * time.Second,
	}
}

func (v Verifier) Verify(ctx context.Context, selected scenario.Scenario) Report {
	report := Report{Scenario: selected.Name()}
	active, err := v.activeScenario(ctx)
	if err != nil {
		report.Checks = append(report.Checks, Check{
			Name:   "active scenario",
			Detail: err.Error(),
		})
		return report
	}
	if active != selected.Name() {
		report.Checks = append(report.Checks, Check{
			Name:   "active scenario",
			Detail: fmt.Sprintf("cluster has %q, expected %q; run gpu-lab scenario run %s first", active, selected.Name(), selected.Name()),
		})
		return report
	}
	report.Checks = append(report.Checks, Check{
		Name:   "active scenario",
		Detail: fmt.Sprintf("ConfigMap is set to %q", active),
		Passed: true,
	})

	expectedTargets := 3.0
	if selected.Name() == "exporter-down" {
		report.Checks = append(report.Checks, v.queryCheck(ctx, "exporter targets", `sum(up{service="`+ExporterService+`"})`, func(value float64) bool {
			return value == 0
		}, "all dcgm-exporter targets are down"))
	} else {
		report.Checks = append(report.Checks, v.queryCheck(ctx, "exporter targets", `sum(up{service="`+ExporterService+`"})`, func(value float64) bool {
			return value == expectedTargets
		}, "all 3 dcgm-exporter targets are up"))
	}

	switch selected.Name() {
	case "normal":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "baseline utilization", "avg(gpu_lab_gpu_utilization_percent)", func(value float64) bool { return value == 15 }, "average utilization is 15%"),
			v.queryCheck(ctx, "baseline temperature", "max(gpu_lab_gpu_temperature_celsius)", func(value float64) bool { return value == 45 }, "maximum temperature is 45°C"),
			v.queryCheck(ctx, "baseline XID", "max(gpu_lab_gpu_xid_code)", func(value float64) bool { return value == 0 }, "no XID is reported"),
			v.queryCheck(ctx, "baseline health", "min(gpu_lab_gpu_health)", func(value float64) bool { return value == 1 }, "all GPUs report healthy"),
			v.noScenarioPodsCheck(ctx),
		)
	case "gpu-util-high":
		report.Checks = append(report.Checks, v.queryCheck(ctx, "GPU utilization", "avg(gpu_lab_gpu_utilization_percent)", func(value float64) bool { return value >= 80 }, "average utilization is at least 80%"))
	case "vram-pressure":
		report.Checks = append(report.Checks, v.queryCheck(ctx, "VRAM pressure", "max(gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes)", func(value float64) bool { return value >= 0.9 }, "at least one GPU uses 90% or more VRAM"))
	case "thermal-throttling":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "thermal threshold", "max(gpu_lab_gpu_temperature_celsius)", func(value float64) bool { return value > 90 }, "temperature is above 90°C"),
			v.queryCheck(ctx, "throttled utilization", "avg(gpu_lab_gpu_utilization_percent)", func(value float64) bool { return value < 80 }, "utilization is below the high-utilization threshold"),
		)
	case "xid-48":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "XID 48", "max(gpu_lab_gpu_xid_code)", func(value float64) bool { return value == 48 }, "XID 48 is reported"),
			v.queryCheck(ctx, "GPU health", "min(gpu_lab_gpu_health)", func(value float64) bool { return value == 0 }, "at least one GPU reports unhealthy"),
		)
	case "xid-79":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "XID 79", "max(gpu_lab_gpu_xid_code)", func(value float64) bool { return value == 79 }, "XID 79 is reported"),
			v.queryCheck(ctx, "GPU health", "min(gpu_lab_gpu_health)", func(value float64) bool { return value == 0 }, "at least one GPU reports unhealthy"),
		)
	case "ecc-double-bit":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "double-bit ECC", "max(gpu_lab_gpu_ecc_dbe_total)", func(value float64) bool { return value > 0 }, "uncorrectable ECC counter is non-zero"),
			v.queryCheck(ctx, "XID 48", "max(gpu_lab_gpu_xid_code)", func(value float64) bool { return value == 48 }, "XID 48 is reported"),
			v.queryCheck(ctx, "GPU health", "min(gpu_lab_gpu_health)", func(value float64) bool { return value == 0 }, "at least one GPU reports unhealthy"),
		)
	case "power-throttle":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "power violation", "max(gpu_lab_gpu_power_violation_total)", func(value float64) bool { return value > 0 }, "power violation counter is non-zero"),
			v.queryCheck(ctx, "power throttle", `max(gpu_lab_gpu_throttle_active{reason="power_cap"})`, func(value float64) bool { return value == 1 }, "power-cap throttle is active"),
			v.queryCheck(ctx, "throttled utilization", "avg(gpu_lab_gpu_utilization_percent)", func(value float64) bool { return value < 80 }, "utilization is below the high-utilization threshold"),
		)
	case "pcie-replay":
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "PCIe replay", "max(gpu_lab_gpu_pcie_replay_total)", func(value float64) bool { return value > 0 }, "PCIe replay counter is non-zero"),
			v.queryCheck(ctx, "GPU health", "min(gpu_lab_gpu_health)", func(value float64) bool { return value == 1 }, "GPU health remains healthy while the link signal is investigated"),
		)
	case "gpu-idle":
		report.Checks = append(report.Checks,
			v.podPhaseCheck(ctx, "gpu-lab-idle-workload", "Running"),
			v.queryCheck(ctx, "GPU allocation", "max(gpu_lab_gpu_allocated)", func(value float64) bool { return value == 1 }, "at least one GPU is allocated"),
			v.queryCheck(ctx, "idle utilization", "avg(gpu_lab_gpu_utilization_percent)", func(value float64) bool { return value <= 5 }, "average utilization is 5% or lower"),
		)
	case "gpu-allocated-idle":
		report.Checks = append(report.Checks,
			v.podPhaseCheck(ctx, "gpu-lab-allocated-idle-workload", "Running"),
			v.queryCheck(ctx, "GPU allocation", "max(gpu_lab_gpu_allocated)", func(value float64) bool { return value == 1 }, "at least one GPU is allocated"),
			v.queryCheck(ctx, "idle utilization", "avg(gpu_lab_gpu_utilization_percent)", func(value float64) bool { return value <= 5 }, "average utilization is 5% or lower"),
		)
	case "gpu-capacity-mismatch":
		report.Checks = append(report.Checks,
			v.podPhaseCheck(ctx, "gpu-lab-capacity-mismatch-workload", "Running"),
			v.queryCheck(ctx, "GPU capacity", "max(gpu_lab_node_gpu_capacity)", func(value float64) bool { return value == 8 }, "synthetic GPU capacity is 8"),
			v.queryCheck(ctx, "GPU allocatable", "max(gpu_lab_node_gpu_allocatable)", func(value float64) bool { return value == 4 }, "synthetic GPU allocatable is 4"),
			v.queryCheck(ctx, "capacity mismatch", "max(gpu_lab_node_gpu_capacity - gpu_lab_node_gpu_allocatable)", func(value float64) bool { return value == 4 }, "capacity and allocatable differ by 4 GPUs"),
		)
	case "scheduling-failure":
		report.Checks = append(report.Checks,
			v.podPhaseCheck(ctx, "gpu-lab-scheduling-failure", "Pending"),
			v.podEventCheck(ctx, "gpu-lab-scheduling-failure", "Insufficient nvidia.com/gpu"),
		)
	case "node-selector-mismatch":
		report.Checks = append(report.Checks,
			v.podPhaseCheck(ctx, "gpu-lab-node-selector-mismatch", "Pending"),
			v.podEventCheck(ctx, "gpu-lab-node-selector-mismatch", "selector"),
		)
	case "gpu-fragmentation":
		for _, name := range []string{
			"gpu-lab-fragmentation-worker-01",
			"gpu-lab-fragmentation-worker-02",
			"gpu-lab-fragmentation-worker-03",
		} {
			report.Checks = append(report.Checks, v.podPhaseCheck(ctx, name, "Running"))
		}
		report.Checks = append(report.Checks,
			v.podPhaseCheck(ctx, "gpu-lab-fragmentation-target", "Pending"),
			v.podEventCheck(ctx, "gpu-lab-fragmentation-target", "Insufficient nvidia.com/gpu"),
		)
	case "exporter-down":
		// Target availability is the defining signal for this scenario.
	default:
		report.Checks = append(report.Checks, Check{
			Name:   "scenario contract",
			Detail: fmt.Sprintf("no verifier contract exists for %q", selected.Name()),
		})
	}
	return report
}

func (v Verifier) activeScenario(ctx context.Context) (string, error) {
	data, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "configmap", scenario.ConfigName, "-n", SystemNS, "-o", "jsonpath={.data.scenario\\.yaml}")
	if err != nil {
		return "", fmt.Errorf("read active scenario: %w", err)
	}
	active, err := scenario.Parse([]byte(data))
	if err != nil {
		return "", fmt.Errorf("parse active scenario: %w", err)
	}
	return active.Name(), nil
}

func (v Verifier) queryCheck(ctx context.Context, name, expression string, predicate func(float64) bool, expected string) Check {
	value, err := v.waitForQuery(ctx, expression, predicate)
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	return Check{Name: name, Detail: fmt.Sprintf("%s (value=%s)", expected, formatFloat(value)), Passed: true}
}

func (v Verifier) podPhaseCheck(ctx context.Context, name, expected string) Check {
	phase, err := v.waitForPodPhase(ctx, name, expected)
	if err != nil {
		return Check{Name: "pod " + name, Detail: err.Error()}
	}
	return Check{Name: "pod " + name, Detail: fmt.Sprintf("phase is %s", phase), Passed: true}
}

func (v Verifier) podEventCheck(ctx context.Context, name, expected string) Check {
	var output string
	err := v.poll(ctx, func() (bool, error) {
		current, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "events", "-n", DemoNamespace, "--field-selector", "involvedObject.name="+name, "--sort-by=.lastTimestamp", "-o", "go-template={{range .items}}{{.message}}\\n{{end}}")
		if err != nil {
			return false, err
		}
		output = current
		return strings.Contains(strings.ToLower(output), strings.ToLower(expected)), nil
	})
	if err != nil {
		return Check{Name: "pod " + name + " event", Detail: fmt.Sprintf("expected event containing %q, got %q: %v", expected, strings.TrimSpace(output), err)}
	}
	return Check{Name: "pod " + name + " event", Detail: fmt.Sprintf("scheduler event contains %q", expected), Passed: true}
}

func (v Verifier) noScenarioPodsCheck(ctx context.Context) Check {
	output, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "pods", "-n", DemoNamespace, "-l", "gpu-lab/scenario", "-o", "name", "--ignore-not-found=true")
	if err != nil {
		return Check{Name: "scenario workloads", Detail: fmt.Errorf("list scenario workloads: %w", err).Error()}
	}
	if strings.TrimSpace(output) != "" {
		return Check{Name: "scenario workloads", Detail: fmt.Sprintf("unexpected scenario workloads remain: %s", strings.TrimSpace(output))}
	}
	return Check{Name: "scenario workloads", Detail: "no scenario-owned workloads remain", Passed: true}
}

func (v Verifier) waitForPodPhase(ctx context.Context, name, expected string) (string, error) {
	returnValue := ""
	err := v.poll(ctx, func() (bool, error) {
		phase, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "pod", name, "-n", DemoNamespace, "-o", "jsonpath={.status.phase}")
		if err != nil {
			return false, err
		}
		returnValue = strings.TrimSpace(phase)
		return returnValue == expected, nil
	})
	if err != nil {
		if returnValue == "" {
			return "", fmt.Errorf("expected phase %s: %w", expected, err)
		}
		return returnValue, fmt.Errorf("expected phase %s, got %s: %w", expected, returnValue, err)
	}
	return returnValue, nil
}

func (v Verifier) waitForQuery(ctx context.Context, expression string, predicate func(float64) bool) (float64, error) {
	value := 0.0
	err := v.poll(ctx, func() (bool, error) {
		current, err := v.query(ctx, expression)
		if err != nil {
			return false, err
		}
		value = current
		return predicate(current), nil
	})
	if err != nil {
		return value, fmt.Errorf("PromQL %q did not reach expected state: %w", expression, err)
	}
	return value, nil
}

func (v Verifier) poll(ctx context.Context, fn func() (bool, error)) error {
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	interval := v.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		ok, err := fn()
		if err != nil {
			lastErr = err
		} else if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Value []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (v Verifier) query(ctx context.Context, expression string) (float64, error) {
	proxyPath := "/api/v1/namespaces/" + MonitoringNS + "/services/" + PrometheusSvc + "/proxy/api/v1/query?query=" + url.QueryEscape(expression)
	data, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "--raw", proxyPath)
	if err != nil {
		return 0, fmt.Errorf("query Prometheus: %w", err)
	}
	return decodePrometheusValue([]byte(data))
}

func decodePrometheusValue(data []byte) (float64, error) {
	var response prometheusResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return 0, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if response.Status != "success" {
		return 0, fmt.Errorf("Prometheus response status is %q", response.Status)
	}
	if len(response.Data.Result) == 0 || len(response.Data.Result[0].Value) < 2 {
		return 0, errors.New("Prometheus query returned no vector result")
	}
	var rawValue string
	if err := json.Unmarshal(response.Data.Result[0].Value[1], &rawValue); err != nil {
		return 0, fmt.Errorf("decode Prometheus value: %w", err)
	}
	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return 0, fmt.Errorf("parse Prometheus value %q: %w", rawValue, err)
	}
	return value, nil
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
