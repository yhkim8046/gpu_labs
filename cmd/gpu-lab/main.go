package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/monitoring"
	"github.com/gpu-lab/gpu-lab/internal/nvidiasmi"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
	"github.com/gpu-lab/gpu-lab/internal/verification"
	"github.com/gpu-lab/gpu-lab/internal/version"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gpu:", err)
		os.Exit(exitCode(err))
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		if len(args) != 1 {
			return errors.New("usage: gpu version")
		}
		fmt.Fprintln(stdout, version.String())
		return nil
	}
	r := runner.New(stdout, stderr)
	m := cluster.NewManager(r)
	switch args[0] {
	case "helm":
		return helmCommand(ctx, m, args[1:], stdout)
	case "create":
		return createCommand(ctx, m, args[1:], stdout)
	case "destroy":
		return m.Destroy(ctx)
	case "doctor":
		return doctor(ctx, r, stdout)
	case "status":
		return m.Status(ctx)
	case "dashboard":
		return dashboardCommand(ctx, r, args[1:], stdout)
	case "metrics":
		return metricsCommand(ctx, r, args[1:], stdout)
	case "nvidia-smi":
		return nvidiasmi.Run(ctx, r, args[1:], stdout)
	case "verify":
		return verifyScenario(ctx, r, args[1:], stdout)
	case "context":
		return contextCommand(ctx, m, args[1:], stdout)
	case "reset":
		return reset(ctx, m, stdout)
	case "scenario":
		return scenarioCommand(ctx, m, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q; run gpu help", args[0])
	}
}

type createOptions struct {
	image       string
	imageSource string
	installAll  bool
}

func createCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printCreateHelp(stdout)
		return nil
	}
	options, err := parseCreateArgs(args)
	if err != nil {
		return err
	}
	if options.imageSource != "" {
		m.ImageSource = options.imageSource
		switch options.imageSource {
		case cluster.ImageSourceLocal:
			if options.image == "" {
				m.Image = cluster.LocalImageName
			}
		case cluster.ImageSourceRegistry:
			if options.image == "" && m.Image == cluster.LocalImageName {
				m.Image = cluster.RuntimeImageForVersion()
			}
		}
	}
	if options.image != "" {
		m.Image = options.image
	}
	createCtx, cancel := context.WithTimeout(ctx, cluster.DefaultTimeout())
	defer cancel()
	if err := m.Create(createCtx); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "gpu-lab base cluster is ready")
	if options.installAll {
		if err := m.InstallAll(createCtx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "all GPU Lab Helm components are installed")
		return nil
	}
	fmt.Fprintln(stdout, "next: gpu helm install nvidia-device-plugin")
	return nil
}

func parseCreateArgs(args []string) (createOptions, error) {
	options := createOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--local":
			if options.imageSource != "" && options.imageSource != cluster.ImageSourceLocal {
				return createOptions{}, errors.New("cannot combine --local with another image source")
			}
			options.imageSource = cluster.ImageSourceLocal
		case "--registry":
			if options.imageSource != "" && options.imageSource != cluster.ImageSourceRegistry {
				return createOptions{}, errors.New("cannot combine --registry with another image source")
			}
			options.imageSource = cluster.ImageSourceRegistry
		case "--image-source":
			if i+1 >= len(args) {
				return createOptions{}, errors.New("usage: gpu create [--local|--registry] [--image <image>] [--all]")
			}
			i++
			source := args[i]
			if source != cluster.ImageSourceAuto && source != cluster.ImageSourceLocal && source != cluster.ImageSourceRegistry {
				return createOptions{}, fmt.Errorf("invalid image source %q; expected auto, local, or registry", source)
			}
			if options.imageSource != "" && options.imageSource != source {
				return createOptions{}, errors.New("cannot combine multiple image sources")
			}
			options.imageSource = source
		case "--image":
			if i+1 >= len(args) || args[i+1] == "" {
				return createOptions{}, errors.New("usage: gpu create [--local|--registry] [--image <image>] [--all]")
			}
			i++
			options.image = args[i]
		case "--all":
			options.installAll = true
		default:
			return createOptions{}, fmt.Errorf("unknown create option %q; run gpu create --help", args[i])
		}
	}
	return options, nil
}

func helmCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		printHelmHelp(stdout)
		return nil
	}
	if len(args) == 1 && args[0] == "catalog" {
		fmt.Fprintln(stdout, "COMPONENT\tNAMESPACE\tDESCRIPTION")
		for _, component := range cluster.Components() {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", component.Name, component.Namespace, component.Description)
		}
		return nil
	}
	if len(args) >= 2 && args[0] == "install" {
		if args[1] == "all" && len(args) == 2 {
			return m.InstallAll(ctx)
		}
		if _, known := cluster.FindComponent(args[1]); known && (len(args) == 2 || strings.HasPrefix(args[2], "-")) {
			if err := m.InstallComponent(ctx, args[1], args[2:]...); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s installed with official Helm\n", args[1])
			return nil
		}
	}
	if len(args) >= 2 && (args[0] == "uninstall" || args[0] == "delete") {
		if _, known := cluster.FindComponent(args[1]); known && (len(args) == 2 || strings.HasPrefix(args[2], "-")) {
			return m.UninstallComponent(ctx, args[1], args[2:]...)
		}
	}
	return m.Helm(ctx, args...)
}

func verifyScenario(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: gpu verify <scenario>")
	}
	selected, err := scenario.LoadBuiltin(args[0])
	if err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	report := verification.New(r).Verify(verifyCtx, selected)
	failures := 0
	fmt.Fprintf(stdout, "gpu verify %s\n", report.Scenario)
	for _, check := range report.Checks {
		status := "PASS"
		if !check.Passed {
			status = "FAIL"
			failures++
		}
		fmt.Fprintf(stdout, "[%s] %-24s %s\n", status, check.Name, check.Detail)
	}
	if failures > 0 {
		return fmt.Errorf("scenario verification failed: %d check(s) failed", failures)
	}
	fmt.Fprintln(stdout, "scenario verification passed")
	return nil
}

func scenarioCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: gpu scenario list|run|inspect <name>|reset")
	}
	switch args[0] {
	case "list":
		names, err := scenario.ListBuiltin()
		if err != nil {
			return err
		}
		for _, name := range names {
			fmt.Fprintln(stdout, name)
		}
		return nil
	case "reset":
		return reset(ctx, m, stdout)
	case "run":
		if len(args) < 2 {
			return errors.New("usage: gpu scenario run <name> [--file path]")
		}
		name := args[1]
		var selected scenario.Scenario
		var err error
		if len(args) >= 4 && args[2] == "--file" {
			selected, err = scenario.LoadFile(args[3])
		} else {
			selected, err = scenario.LoadBuiltin(name)
		}
		if err != nil {
			return err
		}
		return applyScenario(ctx, m, selected, stdout)
	case "inspect":
		return inspectScenario(args[1:], stdout)
	default:
		return fmt.Errorf("unknown scenario command %q", args[0])
	}
}

type dashboardOptions struct {
	port int
}

func dashboardCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	options := dashboardOptions{port: 3000}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 >= len(args) {
				return errors.New("usage: gpu dashboard [--port <port>]")
			}
			i++
			port, err := strconv.Atoi(args[i])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid dashboard port %q", args[i])
			}
			options.port = port
		case "--help", "-h":
			if len(args) != 1 {
				return errors.New("usage: gpu dashboard [--port <port>]")
			}
			fmt.Fprintln(stdout, "gpu dashboard — forward Grafana to localhost")
			fmt.Fprintln(stdout, "Usage: gpu dashboard [--port <port>]")
			return nil
		default:
			return fmt.Errorf("unknown dashboard option %q; run gpu dashboard --help", args[i])
		}
	}
	fmt.Fprintf(stdout, "Grafana: http://127.0.0.1:%d\n", options.port)
	fmt.Fprintln(stdout, "Press Ctrl-C to stop port-forwarding.")
	return r.Run(ctx, "kubectl", "--context", cluster.KubeContext, "-n", cluster.MonitoringNS, "port-forward", "svc/"+cluster.GrafanaService, fmt.Sprintf("%d:80", options.port))
}

type metricQuery struct {
	Name  string
	Query string
}

var defaultMetricQueries = []metricQuery{
	{Name: "GPU utilization (%)", Query: "avg(gpu_lab_gpu_utilization_percent)"},
	{Name: "GPU memory used (%)", Query: "max(gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes) * 100"},
	{Name: "GPU temperature (°C)", Query: "max(gpu_lab_gpu_temperature_celsius)"},
	{Name: "GPU power (W)", Query: "max(gpu_lab_gpu_power_watts)"},
	{Name: "GPU XID", Query: "max(gpu_lab_gpu_xid_code)"},
	{Name: "GPU health", Query: "min(gpu_lab_gpu_health)"},
	{Name: "GPU health status", Query: "max(gpu_lab_gpu_health_status)"},
	{Name: "Double-bit ECC errors", Query: "max(gpu_lab_gpu_ecc_dbe_total)"},
	{Name: "Power violations", Query: "max(gpu_lab_gpu_power_violation_total)"},
	{Name: "PCIe replay errors", Query: "max(gpu_lab_gpu_pcie_replay_total)"},
	{Name: "Allocated GPUs", Query: "sum(gpu_lab_gpu_allocated)"},
	{Name: "GPU capacity", Query: "max(gpu_lab_node_gpu_capacity)"},
	{Name: "GPU allocatable", Query: "max(gpu_lab_node_gpu_allocatable)"},
	{Name: "Exporter targets", Query: `sum(up{service="dcgm-exporter"})`},
}

type metricsOptions struct {
	query string
	json  bool
}

func metricsCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "gpu metrics — show lab metrics")
		fmt.Fprintln(stdout, "Usage: gpu metrics [--query <PromQL>] [--json]")
		return nil
	}
	options, err := parseMetricsArgs(args)
	if err != nil {
		return err
	}
	client := monitoring.New(r)
	if options.query != "" {
		result, err := client.Query(ctx, options.query)
		if err != nil {
			return err
		}
		return printMetricResult(stdout, options.query, result, options.json)
	}
	if options.json {
		values := make(map[string]float64, len(defaultMetricQueries))
		for _, query := range defaultMetricQueries {
			result, err := client.Query(ctx, query.Query)
			if err != nil {
				return fmt.Errorf("%s: %w", query.Name, err)
			}
			values[query.Name] = result.Samples[0].Value
		}
		return writeJSON(stdout, values)
	}
	fmt.Fprintln(stdout, "gpu metrics")
	for _, query := range defaultMetricQueries {
		result, err := client.Query(ctx, query.Query)
		if err != nil {
			return fmt.Errorf("%s: %w", query.Name, err)
		}
		fmt.Fprintf(stdout, "%-24s %s\n", query.Name, formatMetricValue(result.Samples[0].Value))
	}
	return nil
}

func parseMetricsArgs(args []string) (metricsOptions, error) {
	options := metricsOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--query":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return metricsOptions{}, errors.New("usage: gpu metrics [--query <PromQL>] [--json]")
			}
			i++
			options.query = args[i]
		case "--json":
			options.json = true
		case "--help", "-h":
			return metricsOptions{}, errors.New("usage: gpu metrics [--query <PromQL>] [--json]")
		default:
			return metricsOptions{}, fmt.Errorf("unknown metrics option %q; run gpu metrics --help", args[i])
		}
	}
	return options, nil
}

func printMetricResult(stdout io.Writer, expression string, result monitoring.QueryResult, asJSON bool) error {
	if asJSON {
		return writeJSON(stdout, struct {
			Query   string              `json:"query"`
			Type    string              `json:"result_type"`
			Samples []monitoring.Sample `json:"samples"`
		}{expression, result.ResultType, result.Samples})
	}
	fmt.Fprintf(stdout, "query: %s\n", expression)
	for _, sample := range result.Samples {
		labels := make([]string, 0, len(sample.Metric))
		for name, value := range sample.Metric {
			labels = append(labels, fmt.Sprintf("%s=%q", name, value))
		}
		sort.Strings(labels)
		if len(labels) > 0 {
			fmt.Fprintf(stdout, "%-36s %s\n", "{"+strings.Join(labels, ", ")+"}", formatMetricValue(sample.Value))
		} else {
			fmt.Fprintln(stdout, formatMetricValue(sample.Value))
		}
	}
	return nil
}

func formatMetricValue(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func writeJSON(stdout io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

func inspectScenario(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: gpu scenario inspect <name> [--file path]")
	}
	name := args[0]
	var selected scenario.Scenario
	var err error
	if len(args) == 1 {
		selected, err = scenario.LoadBuiltin(name)
	} else if len(args) == 3 && args[1] == "--file" {
		selected, err = scenario.LoadFile(args[2])
	} else {
		return errors.New("usage: gpu scenario inspect <name> [--file path]")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "scenario: %s\n", selected.Name())
	fmt.Fprintf(stdout, "description: %s\n", selected.Spec.Description)
	if selected.Spec.Duration == "" {
		fmt.Fprintln(stdout, "duration: persistent until reset")
	} else {
		fmt.Fprintf(stdout, "duration: %s\n", selected.Spec.Duration)
	}
	if len(selected.Spec.Targets.Selector) == 0 {
		fmt.Fprintln(stdout, "targets: all synthetic GPUs")
	} else {
		fmt.Fprintln(stdout, "targets:")
		keys := sortedKeys(selected.Spec.Targets.Selector)
		for _, key := range keys {
			fmt.Fprintf(stdout, "  %s=%s\n", key, selected.Spec.Targets.Selector[key])
		}
	}
	fmt.Fprintln(stdout, "metrics:")
	printMetricOverrides(stdout, selected.Spec.Metrics)
	if len(selected.Spec.Actions) == 0 {
		fmt.Fprintln(stdout, "actions: none")
	} else {
		fmt.Fprintln(stdout, "actions:")
		for _, action := range selected.Spec.Actions {
			fmt.Fprintf(stdout, "  - type=%s", action.Type)
			if action.Name != "" {
				fmt.Fprintf(stdout, " name=%s", action.Name)
			}
			if action.GPUCount > 0 {
				fmt.Fprintf(stdout, " gpu_count=%d", action.GPUCount)
			}
			if action.Mode != "" {
				fmt.Fprintf(stdout, " mode=%s", action.Mode)
			}
			if action.WaitForReady {
				fmt.Fprint(stdout, " wait_for_ready=true")
			}
			fmt.Fprintln(stdout)
		}
	}
	return nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func printMetricOverrides(stdout io.Writer, metrics scenario.MetricOverrides) {
	count := 0
	if metrics.GPUUtilizationPercent != nil {
		fmt.Fprintf(stdout, "  gpu_utilization_percent=%s\n", formatMetricValue(*metrics.GPUUtilizationPercent))
		count++
	}
	if metrics.GPUMemoryUsedPercent != nil {
		fmt.Fprintf(stdout, "  gpu_memory_used_percent=%s\n", formatMetricValue(*metrics.GPUMemoryUsedPercent))
		count++
	}
	if metrics.GPUMemoryTotalBytes != nil {
		fmt.Fprintf(stdout, "  gpu_memory_total_bytes=%d\n", *metrics.GPUMemoryTotalBytes)
		count++
	}
	if metrics.TemperatureCelsius != nil {
		fmt.Fprintf(stdout, "  temperature_celsius=%s\n", formatMetricValue(*metrics.TemperatureCelsius))
		count++
	}
	if metrics.PowerWatts != nil {
		fmt.Fprintf(stdout, "  power_watts=%s\n", formatMetricValue(*metrics.PowerWatts))
		count++
	}
	if metrics.XIDCode != nil {
		fmt.Fprintf(stdout, "  xid_code=%d\n", *metrics.XIDCode)
		count++
	}
	if metrics.Health != nil {
		fmt.Fprintf(stdout, "  health=%d\n", *metrics.Health)
		count++
	}
	if metrics.ECCDbeTotal != nil {
		fmt.Fprintf(stdout, "  ecc_dbe_total=%d\n", *metrics.ECCDbeTotal)
		count++
	}
	if metrics.PowerViolationTotal != nil {
		fmt.Fprintf(stdout, "  power_violation_total=%d\n", *metrics.PowerViolationTotal)
		count++
	}
	if metrics.PCIeReplayTotal != nil {
		fmt.Fprintf(stdout, "  pcie_replay_total=%d\n", *metrics.PCIeReplayTotal)
		count++
	}
	if metrics.ThrottleActive != nil {
		fmt.Fprintf(stdout, "  throttle_active=%d\n", *metrics.ThrottleActive)
		count++
	}
	if metrics.ThrottleReason != "" {
		fmt.Fprintf(stdout, "  throttle_reason=%s\n", metrics.ThrottleReason)
		count++
	}
	if metrics.GPUAllocatedCount != nil {
		fmt.Fprintf(stdout, "  gpu_allocated_count=%d\n", *metrics.GPUAllocatedCount)
		count++
	}
	if metrics.GPUCapacity != nil {
		fmt.Fprintf(stdout, "  gpu_capacity=%d\n", *metrics.GPUCapacity)
		count++
	}
	if metrics.GPUAllocatable != nil {
		fmt.Fprintf(stdout, "  gpu_allocatable=%d\n", *metrics.GPUAllocatable)
		count++
	}
	if count == 0 {
		fmt.Fprintln(stdout, "  none")
	}
}

func contextCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		current, err := m.CurrentContext(ctx)
		if err != nil {
			return err
		}
		contexts, err := m.Contexts(ctx)
		if err != nil {
			return err
		}
		path, err := m.DedicatedKubeconfigPath()
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "current: %s\n", current)
		fmt.Fprintf(stdout, "dedicated kubeconfig: %s\n", path)
		for _, name := range contexts {
			fmt.Fprintln(stdout, name)
		}
		return nil
	}
	switch args[0] {
	case "setup":
		path, err := m.SetupContext(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "gpu-lab context configured\ndedicated kubeconfig: %s\n", path)
		return nil
	case "use":
		if len(args) != 2 {
			return errors.New("usage: gpu context use <context-name>")
		}
		if err := m.UseContext(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "switched kubectl context to %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown context command %q; use list, setup, or use", args[0])
	}
}

func applyScenario(ctx context.Context, m cluster.Manager, selected scenario.Scenario, stdout io.Writer) error {
	generation := strconv.FormatInt(time.Now().UnixNano(), 10)
	data, err := scenario.ConfigMapJSON(selected, generation)
	if err != nil {
		return err
	}
	if err := m.DeleteScenarioPods(ctx); err != nil {
		return err
	}
	if err := m.ApplyJSON(ctx, data); err != nil {
		return err
	}
	for _, action := range selected.Spec.Actions {
		var workload []byte
		switch action.Type {
		case "create_pending_workload":
			workload, err = scenario.PendingWorkloadJSON(selected, action.GPUCount)
		case "create_gpu_workload":
			workload, err = scenario.GPUWorkloadJSON(selected, action)
		default:
			continue
		}
		if err != nil {
			return err
		}
		if err := m.ApplyJSON(ctx, workload); err != nil {
			return err
		}
		if action.WaitForReady {
			if err := m.WaitForScenarioPod(ctx, action.Name); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(stdout, "scenario %s applied (generation %s)\n", selected.Name(), generation)
	return nil
}

func reset(ctx context.Context, m cluster.Manager, stdout io.Writer) error {
	normal, err := scenario.LoadBuiltin("normal")
	if err != nil {
		return err
	}
	if err := applyScenario(ctx, m, normal, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "scenario state reset to normal")
	return nil
}

func doctor(ctx context.Context, r runner.Runner, stdout io.Writer) error {
	fmt.Fprintln(stdout, "Synthetic lab: GPU telemetry and device plugin behavior are simulated; no NVIDIA driver or CUDA execution is used.")
	missing := make([]string, 0)
	for _, name := range []string{"docker", "kind", "kubectl", "helm"} {
		if !runner.Exists(name) {
			fmt.Fprintf(stdout, "[missing] %s\n", name)
			missing = append(missing, name)
			continue
		}
		version, err := r.Output(ctx, name, versionArgs(name)...)
		if err != nil {
			fmt.Fprintf(stdout, "[found]   %s (version unavailable: %v)\n", name, err)
			continue
		}
		fmt.Fprintf(stdout, "[ready]   %s %s", name, strings.TrimSpace(version))
		fmt.Fprintln(stdout)
	}
	if len(missing) > 0 {
		return fmt.Errorf("install missing tools before running gpu create: %s", strings.Join(missing, ", "))
	}
	return nil
}

func versionArgs(name string) []string {
	switch name {
	case "docker":
		return []string{"version", "--format", "{{.Client.Version}}"}
	case "kind":
		return []string{"version"}
	case "kubectl":
		return []string{"version", "--client=true", "--output=yaml"}
	case "helm":
		return []string{"version", "--short"}
	default:
		return []string{"version"}
	}
}

func printHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu — Kubernetes GPU infrastructure lab

Usage:
  gpu version
  gpu create [--local|--registry] [--image <image>] [--all]
  gpu destroy
  gpu reset
  gpu doctor
  gpu status
  gpu dashboard [--port <port>]
  gpu metrics [--query <PromQL>] [--json]
  gpu nvidia-smi [--list-gpus]
  gpu nvidia-smi --query-gpu=<fields> --format=csv[,noheader][,nounits]
  gpu verify <scenario>
  gpu context list
  gpu context setup
  gpu context use <context-name>
  gpu scenario list
  gpu scenario run <name>
  gpu scenario inspect <name> [--file path]
  gpu scenario reset
  gpu helm catalog
  gpu helm install <gpu-lab-component>
  gpu helm <official-helm-args...>

Examples:
  gpu create
  gpu helm install nvidia-device-plugin
  gpu helm install dcgm-exporter
  gpu helm install monitoring
  gpu scenario run xid-79
  gpu helm list --all-namespaces

Image environment:
  GPU_LAB_IMAGE=<image>                         override runtime image
  GPU_LAB_IMAGE_SOURCE=auto|local|registry      choose build or pull mode
  GPU_LAB_RUNTIME_IMAGE_REPOSITORY=<repository> release image repository
  GPU_LAB_COMPONENT_CHART_SOURCE=auto|embedded|registry
  GPU_LAB_COMPONENT_CHART_REPOSITORY=<oci-repository>
`)
}

func printCreateHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu create — create or reuse the base GPU lab cluster

Usage:
  gpu create
  gpu create --local
  gpu create --registry
  gpu create --image ghcr.io/<owner>/gpu-lab-runtime:1.0.0
  gpu create --all

Options:
  --local                  build and load the local gpu-lab:dev image
  --registry               pull and load the versioned runtime image
  --image <image>          override the runtime image reference
  --image-source <source> choose auto, local, or registry
  --all                    install all components after cluster bootstrap
`)
}

func printHelmHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu helm — use official Helm against the GPU Lab cluster

Course components:
  gpu helm catalog
  gpu helm install nvidia-device-plugin
  gpu helm install dcgm-exporter
  gpu helm install monitoring
  gpu helm uninstall <component>

Official Helm passthrough:
  gpu helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
  gpu helm search repo prometheus-community
  gpu helm install <release> <chart> [official Helm flags...]
  gpu helm list --all-namespaces

The component shorthand expands to a real Helm install. Cluster-aware Helm
commands default to the gpu-lab kube context. The legacy gpu-lab binary name
remains available for compatibility.
`)
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	return 1
}
