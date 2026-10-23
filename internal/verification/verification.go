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
	case "thermal-escalation":
		generation, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "configmap", scenario.ConfigName, "-n", SystemNS, "-o", "jsonpath={.data.generation}")
		if err != nil || strings.TrimSpace(generation) == "" {
			report.Checks = append(report.Checks, Check{Name: "timeline generation", Detail: fmt.Sprintf("cannot read applied generation: %v", err)})
			break
		}
		report.Checks = append(report.Checks, v.queryCheck(ctx, "thermal timeline", thermalTimelineQuery(strings.TrimSpace(generation)), func(value float64) bool { return value == 3 }, "all 3 exporters agree on the applied generation, a fresh timeline phase, and its 24 GPU readings"))
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
	case "ib-link-down":
		node := "gpu-lab-worker2"
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "IB port up", fabricMetric("gpu_lab_ib_port_up", node), func(value float64) bool { return value == 0 }, "gpu-node-02 mlx5_0 port 1 is down"),
			v.queryCheck(ctx, "IB port state", fabricStateMetric(node, "DOWN", "DISABLED"), func(value float64) bool { return value == 1 }, "port state is DOWN/DISABLED"),
			v.queryCheck(ctx, "IB link downed", fabricMetric("gpu_lab_ib_link_downed_total", node), func(value float64) bool { return value > 0 }, "link-downed counter is non-zero"),
			v.queryCheck(ctx, "fabric delay", fabricMetric("gpu_lab_fabric_delay_seconds", node), func(value float64) bool { return value == 0 }, "link-down contract carries zero training delay"),
		)
	case "ib-rate-degraded":
		node := "gpu-lab-worker3"
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "IB link rate", fabricMetric("gpu_lab_ib_link_rate_gbps", node), func(value float64) bool { return value == 25 }, "gpu-node-03 link rate is 25 Gbps"),
			v.queryCheck(ctx, "fabric delay", fabricMetric("gpu_lab_fabric_delay_seconds", node), func(value float64) bool { return value >= 1.5 }, "fabric delay is at least 1.5 seconds"),
		)
	case "ib-symbol-errors":
		node := "gpu-lab-worker"
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "IB symbol errors", fabricMetric("gpu_lab_ib_symbol_errors_total", node), func(value float64) bool { return value > 0 }, "symbol-error counter is non-zero"),
			v.queryCheck(ctx, "IB link recovery", fabricMetric("gpu_lab_ib_link_error_recovery_total", node), func(value float64) bool { return value > 0 }, "link-error-recovery counter is non-zero"),
		)
	case "rdma-retry-storm":
		node := "gpu-lab-worker2"
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "RDMA retries", fabricMetric("gpu_lab_rdma_retries_total", node), func(value float64) bool { return value > 0 }, "RDMA retry counter is non-zero"),
			v.queryCheck(ctx, "RDMA timeouts", fabricMetric("gpu_lab_rdma_timeouts_total", node), func(value float64) bool { return value > 0 }, "RDMA timeout counter is non-zero"),
			v.queryCheck(ctx, "fabric delay", fabricMetric("gpu_lab_fabric_delay_seconds", node), func(value float64) bool { return value >= 1 }, "fabric delay is at least 1 second"),
		)
	case "ib-congestion":
		node := "gpu-lab-worker3"
		report.Checks = append(report.Checks,
			v.queryCheck(ctx, "IB transmit wait", fabricMetric("gpu_lab_ib_xmit_wait_total", node), func(value float64) bool { return value > 0 }, "transmit-wait counter is non-zero"),
			v.queryCheck(ctx, "IB transmit discards", fabricMetric("gpu_lab_ib_xmit_discards_total", node), func(value float64) bool { return value > 0 }, "transmit-discard counter is non-zero"),
			v.queryCheck(ctx, "fabric delay", fabricMetric("gpu_lab_fabric_delay_seconds", node), func(value float64) bool { return value >= 0.8 }, "fabric delay is at least 0.8 seconds"),
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
	err := v.poll(ctx, func() (bool, error) {
		return checkPodEventOnce(ctx, name, expected, v.Runner.Output)
	})
	if err != nil {
		return Check{Name: "pod " + name + " event", Detail: fmt.Sprintf("expected a current-pod event containing %q: %v", expected, err)}
	}
	return Check{Name: "pod " + name + " event", Detail: fmt.Sprintf("scheduler event contains %q", expected), Passed: true}
}

type podIdentity struct {
	UID       string
	CreatedAt time.Time
}

type podEvent struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"involvedObject"`
	Message        string `json:"message"`
	EventTime      string `json:"eventTime"`
	FirstTimestamp string `json:"firstTimestamp"`
	LastTimestamp  string `json:"lastTimestamp"`
	Series         *struct {
		LastObservedTime string `json:"lastObservedTime"`
	} `json:"series"`
}

func checkPodEventOnce(ctx context.Context, name, expected string, output func(context.Context, string, ...string) (string, error)) (bool, error) {
	podArgs := []string{"--context", KubeContext, "get", "pod", name, "-n", DemoNamespace, "-o", "json"}
	podJSON, err := output(ctx, "kubectl", podArgs...)
	if err != nil {
		return false, fmt.Errorf("read pod identity: %w", err)
	}
	identity, err := decodePodIdentity(podJSON, name)
	if err != nil {
		return false, err
	}

	eventsJSON, err := output(ctx, "kubectl", "--context", KubeContext, "get", "events", "-n", DemoNamespace, "--field-selector", "involvedObject.uid="+identity.UID, "-o", "json")
	if err != nil {
		return false, fmt.Errorf("read scheduler events for pod UID %q: %w", identity.UID, err)
	}
	events, err := decodePodEvents(eventsJSON)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.InvolvedObject.Kind != "Pod" || event.InvolvedObject.Name != name || event.InvolvedObject.Namespace != DemoNamespace || event.InvolvedObject.UID != identity.UID {
			continue
		}
		if !strings.Contains(strings.ToLower(event.Message), strings.ToLower(expected)) {
			continue
		}
		eventAt, err := relevantPodEventTime(event)
		if err != nil {
			return false, err
		}
		if eventAt.Before(identity.CreatedAt) {
			continue
		}

		currentJSON, err := output(ctx, "kubectl", podArgs...)
		if err != nil {
			return false, fmt.Errorf("recheck pod identity before accepting event: %w", err)
		}
		current, err := decodePodIdentity(currentJSON, name)
		if err != nil {
			return false, err
		}
		if current.UID != identity.UID {
			return false, fmt.Errorf("pod %q was recreated while verifying its scheduler event", name)
		}
		return true, nil
	}
	return false, nil
}

func decodePodIdentity(raw, expectedName string) (podIdentity, error) {
	var pod struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string `json:"name"`
			Namespace         string `json:"namespace"`
			UID               string `json:"uid"`
			CreationTimestamp string `json:"creationTimestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &pod); err != nil {
		return podIdentity{}, fmt.Errorf("decode current pod identity: %w", err)
	}
	if pod.Kind != "Pod" || pod.Metadata.Name != expectedName || pod.Metadata.Namespace != DemoNamespace {
		return podIdentity{}, fmt.Errorf("pod identity response does not describe %s/%s", DemoNamespace, expectedName)
	}
	if pod.Metadata.UID == "" {
		return podIdentity{}, fmt.Errorf("pod %s/%s has no UID; scheduler event is unverified", DemoNamespace, expectedName)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, pod.Metadata.CreationTimestamp)
	if err != nil || createdAt.IsZero() {
		return podIdentity{}, fmt.Errorf("pod %s/%s has an invalid creationTimestamp %q", DemoNamespace, expectedName, pod.Metadata.CreationTimestamp)
	}
	return podIdentity{UID: pod.Metadata.UID, CreatedAt: createdAt}, nil
}

func decodePodEvents(raw string) ([]podEvent, error) {
	var list struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("decode scheduler event list: %w", err)
	}
	if list.Kind != "EventList" && list.Kind != "List" {
		return nil, fmt.Errorf("scheduler event response has kind %q", list.Kind)
	}
	if list.Items == nil {
		return nil, errors.New("scheduler event response has no items; event is unverified")
	}
	events := make([]podEvent, 0, len(list.Items))
	for index, rawItem := range list.Items {
		var event podEvent
		if err := json.Unmarshal(rawItem, &event); err != nil {
			return nil, fmt.Errorf("decode scheduler event %d: %w", index, err)
		}
		if event.Kind != "" && event.Kind != "Event" {
			return nil, fmt.Errorf("scheduler event %d has kind %q", index, event.Kind)
		}
		if event.InvolvedObject.Kind == "" || event.InvolvedObject.Name == "" || event.InvolvedObject.Namespace == "" || event.InvolvedObject.UID == "" {
			return nil, fmt.Errorf("scheduler event %d has incomplete involvedObject identity; event is unverified", index)
		}
		if _, err := relevantPodEventTime(event); err != nil {
			return nil, fmt.Errorf("scheduler event %d: %w", index, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func relevantPodEventTime(event podEvent) (time.Time, error) {
	var created time.Time
	if event.Metadata.CreationTimestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, event.Metadata.CreationTimestamp)
		if err != nil {
			return time.Time{}, fmt.Errorf("event has invalid metadata.creationTimestamp %q", event.Metadata.CreationTimestamp)
		}
		if !parsed.IsZero() {
			created = parsed
		}
	}
	occurrences := []struct {
		name  string
		value string
	}{
		{name: "eventTime", value: event.EventTime},
		{name: "firstTimestamp", value: event.FirstTimestamp},
		{name: "lastTimestamp", value: event.LastTimestamp},
	}
	if event.Series != nil {
		occurrences = append(occurrences, struct {
			name  string
			value string
		}{name: "series.lastObservedTime", value: event.Series.LastObservedTime})
	}
	var latest time.Time
	for _, occurrence := range occurrences {
		if occurrence.value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurrence.value)
		if err != nil {
			return time.Time{}, fmt.Errorf("event has invalid %s %q", occurrence.name, occurrence.value)
		}
		if !parsed.IsZero() && parsed.After(latest) {
			latest = parsed
		}
	}
	if !latest.IsZero() {
		return latest, nil
	}
	if created.IsZero() {
		return time.Time{}, fmt.Errorf("event has no usable occurrence timestamp or metadata.creationTimestamp %q", event.Metadata.CreationTimestamp)
	}
	return created, nil
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

func fabricMetric(metric, node string) string {
	return fmt.Sprintf(`max(%s{node="%s",hca="mlx5_0",port="1",link_layer="InfiniBand"})`, metric, node)
}

func fabricStateMetric(node, state, physicalState string) string {
	return fmt.Sprintf(`max(gpu_lab_ib_port_state{node="%s",hca="mlx5_0",port="1",link_layer="InfiniBand",state="%s",physical_state="%s"})`, node, state, physicalState)
}
