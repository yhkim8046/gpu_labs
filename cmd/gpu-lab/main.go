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
	"github.com/gpu-lab/gpu-lab/internal/rdmacompat"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
	"github.com/gpu-lab/gpu-lab/internal/trainingjob"
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
	case "ibstat":
		return rdmacompat.Run(ctx, r, rdmacompat.IBStat, args[1:], stdout)
	case "ibstatus":
		return rdmacompat.Run(ctx, r, rdmacompat.IBStatus, args[1:], stdout)
	case "ibv_devinfo":
		return rdmacompat.Run(ctx, r, rdmacompat.IBVDevInfo, args[1:], stdout)
	case "ib":
		return errors.New("gpu ib status has been replaced; use gpu ibstat, gpu ibstatus, or gpu ibv_devinfo")
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
	case "training":
		return trainingCommand(ctx, r, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q; run gpu help", args[0])
	}
}

type trainingRunOptions struct{ trainingjob.Options }

func trainingCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printTrainingHelp(stdout)
		return nil
	}
	client := trainingjob.NewClient(r)
	switch args[0] {
	case "run":
		o, err := parseTrainingRunArgs(args[1:])
		if err != nil {
			return err
		}
		workCtx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		if err := client.Apply(workCtx, o.Options, trainingjob.ControlState{Generation: time.Now().UnixNano()}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "training job %s applied with %d workers\n", trainingjob.Name, o.Workers)
		if o.Wait {
			if err := client.Wait(workCtx, o.Options); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "training workers are ready")
		}
		return nil
	case "status":
		o, err := parseTrainingNamespaceArgs(args[1:])
		if err != nil {
			return err
		}
		return trainingStatus(ctx, client, o, stdout)
	case "logs":
		o, rank, follow, err := parseTrainingLogsArgs(args[1:])
		if err != nil {
			return err
		}
		return client.Logs(ctx, o, rank, follow, stdout)
	case "inject":
		return trainingInject(ctx, client, args[1:], stdout)
	case "recover":
		o, err := parseTrainingNamespaceArgs(args[1:])
		if err != nil {
			return err
		}
		if err := client.Mutate(ctx, o, func(state *trainingjob.ControlState) error {
			state.StragglerRank = nil
			state.StragglerDelay = ""
			state.CrashRank = nil
			state.CrashToken = ""
			trainingjob.ApplyFabricControl(state, trainingjob.FabricControlForScenario("normal"))
			return nil
		}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "training fault controls recovered")
		return nil
	case "reset":
		o, err := parseTrainingNamespaceArgs(args[1:])
		if err != nil {
			return err
		}
		if err := client.Reset(ctx, o); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "training job %s reset\n", trainingjob.Name)
		return nil
	default:
		return fmt.Errorf("unknown training command %q; run gpu training --help", args[0])
	}
}

func parseTrainingRunArgs(args []string) (trainingRunOptions, error) {
	o := trainingRunOptions{Options: trainingjob.Options{Workers: 3, Image: "gpu-lab:dev", Namespace: trainingjob.Namespace, Timeout: 5 * time.Minute}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--workers":
			if i+1 >= len(args) {
				return o, errors.New("usage: gpu training run [--workers N] [--image IMAGE] [--namespace NS] [--wait] [--timeout DURATION]")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil || n < 1 {
				return o, fmt.Errorf("invalid workers %q", args[i])
			}
			o.Workers = n
		case "--image":
			if i+1 >= len(args) || args[i+1] == "" {
				return o, errors.New("image is required")
			}
			i++
			o.Image = args[i]
		case "--namespace":
			if i+1 >= len(args) || args[i+1] == "" {
				return o, errors.New("namespace is required")
			}
			i++
			o.Namespace = args[i]
		case "--wait":
			o.Wait = true
		case "--timeout":
			if i+1 >= len(args) {
				return o, errors.New("timeout is required")
			}
			i++
			d, e := time.ParseDuration(args[i])
			if e != nil || d <= 0 {
				return o, fmt.Errorf("invalid timeout %q", args[i])
			}
			o.Timeout = d
		case "--help", "-h":
			return o, errors.New("usage: gpu training run [--workers N] [--image IMAGE] [--namespace NS] [--wait] [--timeout DURATION]")
		default:
			return o, fmt.Errorf("unknown training run option %q", args[i])
		}
	}
	return o, nil
}

func parseTrainingNamespaceArgs(args []string) (string, error) {
	ns := trainingjob.Namespace
	for i := 0; i < len(args); i++ {
		if args[i] == "--namespace" && i+1 < len(args) {
			i++
			ns = args[i]
		} else {
			return "", fmt.Errorf("unknown training option %q", args[i])
		}
	}
	return ns, nil
}
func parseTrainingLogsArgs(args []string) (string, int, bool, error) {
	ns := trainingjob.Namespace
	rank := -1
	follow := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--namespace":
			if i+1 >= len(args) {
				return "", 0, false, errors.New("namespace is required")
			}
			i++
			ns = args[i]
		case "--rank":
			if i+1 >= len(args) {
				return "", 0, false, errors.New("rank is required")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil || n < 0 {
				return "", 0, false, fmt.Errorf("invalid rank %q", args[i])
			}
			rank = n
		case "--follow":
			follow = true
		default:
			return "", 0, false, fmt.Errorf("unknown training logs option %q", args[i])
		}
	}
	if rank < 0 {
		return "", 0, false, errors.New("training logs requires --rank N")
	}
	return ns, rank, follow, nil
}

func trainingInject(ctx context.Context, client trainingjob.Client, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: gpu training inject straggler|worker-crash ...")
	}
	kind := args[0]
	ns := trainingjob.Namespace
	rank := -1
	delay := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--namespace":
			if i+1 >= len(args) {
				return errors.New("namespace is required")
			}
			i++
			ns = args[i]
		case "--rank":
			if i+1 >= len(args) {
				return errors.New("rank is required")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil || n < 0 {
				return fmt.Errorf("invalid rank %q", args[i])
			}
			rank = n
		case "--delay":
			if i+1 >= len(args) {
				return errors.New("delay is required")
			}
			i++
			d, e := time.ParseDuration(args[i])
			if e != nil || d <= 0 {
				return fmt.Errorf("invalid delay %q", args[i])
			}
			delay = args[i]
		default:
			return fmt.Errorf("unknown training inject option %q", args[i])
		}
	}
	if rank < 0 {
		return errors.New("training inject requires --rank N")
	}
	err := client.Mutate(ctx, ns, func(state *trainingjob.ControlState) error {
		switch kind {
		case "straggler":
			if delay == "" {
				return errors.New("straggler requires --delay DURATION")
			}
			state.StragglerRank = &rank
			state.StragglerDelay = delay
			state.CrashRank = nil
			state.CrashToken = ""
		case "worker-crash":
			state.CrashRank = &rank
			state.CrashToken = strconv.FormatInt(time.Now().UnixNano(), 10)
		default:
			return fmt.Errorf("unknown injection %q", kind)
		}
		return nil
	})
	if err == nil {
		fmt.Fprintf(stdout, "training %s injected for rank %d\n", kind, rank)
	}
	return err
}

func trainingStatus(ctx context.Context, client trainingjob.Client, namespace string, stdout io.Writer) error {
	out, err := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "-n", namespace, "get", "statefulset/"+trainingjob.Name, "-o", "custom-columns=NAME:.metadata.name,DESIRED:.spec.replicas,READY:.status.readyReplicas,CURRENT:.status.currentReplicas,UPDATED:.status.updatedReplicas", "--no-headers")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, strings.TrimSpace(out))
	pods, err := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name="+trainingjob.Name+",app.kubernetes.io/managed-by=gpu-lab", "-o", "custom-columns=NAME:.metadata.name,READY:.status.containerStatuses[*].ready,RESTARTS:.status.containerStatuses[*].restartCount,PHASE:.status.phase", "--no-headers")
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, pods)
	podNames, err := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name="+trainingjob.Name+",app.kubernetes.io/managed-by=gpu-lab", "-o", "name")
	if err != nil {
		return err
	}
	if strings.TrimSpace(podNames) != "" {
		fmt.Fprintln(stdout, "PROGRESS")
	}
	for _, item := range strings.Fields(podNames) {
		pod := strings.TrimPrefix(item, "pod/")
		metrics, metricsErr := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "get", "--raw", fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:9401/proxy/metrics", namespace, pod))
		if metricsErr != nil {
			fmt.Fprintf(stdout, "%s metrics-unavailable: %v\n", pod, metricsErr)
			continue
		}
		snapshot := trainingMetricSnapshot(metrics)
		fmt.Fprintf(stdout, "%s step=%s checkpoint=%s loss=%s allreduce=%ss restarts=%s errors=%s fabric_active=%s fabric_delay=%ss fabric_errors=%s fabric_retries=%s\n",
			pod, snapshot["gpu_lab_training_step"], snapshot["gpu_lab_training_checkpoint_step"], snapshot["gpu_lab_training_loss"], snapshot["gpu_lab_training_allreduce_seconds"], snapshot["gpu_lab_training_restarts_total"], snapshot["gpu_lab_training_allreduce_errors_total"], snapshot["gpu_lab_training_fabric_fault_active"], snapshot["gpu_lab_training_fabric_delay_seconds"], snapshot["gpu_lab_training_fabric_errors_total"], snapshot["gpu_lab_training_fabric_retries_total"])
	}
	control, err := client.ReadControl(ctx, namespace)
	if err != nil {
		return err
	}
	controlJSON, err := control.JSON()
	if err != nil {
		return err
	}
	mode := control.FabricMode
	if strings.TrimSpace(mode) == "" {
		mode = "normal"
	}
	fabricDelay := control.FabricDelay
	if fabricDelay == "" {
		fabricDelay = "0s"
	}
	fabricTarget := control.FabricTargetNode
	if fabricTarget == "" {
		fabricTarget = "-"
	}
	fmt.Fprintf(stdout, "FABRIC mode=%s target=%s delay=%s\n", mode, fabricTarget, fabricDelay)
	fmt.Fprintf(stdout, "CONTROL %s\n", strings.TrimSpace(string(controlJSON)))
	return nil
}

func trainingMetricSnapshot(metrics string) map[string]string {
	names := []string{
		"gpu_lab_training_step",
		"gpu_lab_training_checkpoint_step",
		"gpu_lab_training_loss",
		"gpu_lab_training_allreduce_seconds",
		"gpu_lab_training_restarts_total",
		"gpu_lab_training_allreduce_errors_total",
		"gpu_lab_training_fabric_fault_active",
		"gpu_lab_training_fabric_delay_seconds",
		"gpu_lab_training_fabric_errors_total",
		"gpu_lab_training_fabric_retries_total",
	}
	snapshot := make(map[string]string, len(names))
	for _, name := range names {
		snapshot[name] = "n/a"
	}
	for _, line := range strings.Split(metrics, "\n") {
		for _, name := range names {
			if line != name && !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				snapshot[name] = fields[len(fields)-1]
			}
		}
	}
	return snapshot
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

type ibStatusOptions struct {
	node string
	json bool
}

type ibPortKey struct {
	Node string
	HCA  string
	Port string
}

type ibPortStatus struct {
	Node          string  `json:"node"`
	HCA           string  `json:"hca"`
	Port          string  `json:"port"`
	LinkLayer     string  `json:"link_layer"`
	State         string  `json:"state"`
	PhysicalState string  `json:"physical_state"`
	Up            float64 `json:"up"`
	RateGbps      float64 `json:"rate_gbps"`
	TXBytes       float64 `json:"tx_bytes"`
	RXBytes       float64 `json:"rx_bytes"`
	SymbolErrors  float64 `json:"symbol_errors"`
	LinkDowned    float64 `json:"link_downed"`
	XmitDiscards  float64 `json:"xmit_discards"`
	XmitWait      float64 `json:"xmit_wait"`
	RDMARetries   float64 `json:"rdma_retries"`
	RDMATimeouts  float64 `json:"rdma_timeouts"`
}

var ibMetricNames = []string{
	"gpu_lab_ib_port_up",
	"gpu_lab_ib_port_state",
	"gpu_lab_ib_link_rate_gbps",
	"gpu_lab_ib_tx_bytes_total",
	"gpu_lab_ib_rx_bytes_total",
	"gpu_lab_ib_symbol_errors_total",
	"gpu_lab_ib_link_error_recovery_total",
	"gpu_lab_ib_link_downed_total",
	"gpu_lab_ib_xmit_discards_total",
	"gpu_lab_ib_xmit_wait_total",
	"gpu_lab_rdma_retries_total",
	"gpu_lab_rdma_timeouts_total",
	"gpu_lab_fabric_delay_seconds",
}

type prometheusQuerier interface {
	Query(context.Context, string) (monitoring.QueryResult, error)
}

func ibCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printIBHelp(stdout)
		return nil
	}
	if args[0] != "status" {
		return fmt.Errorf("unknown ib command %q; run gpu ib --help", args[0])
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		printIBHelp(stdout)
		return nil
	}
	options, err := parseIBStatusArgs(args[1:])
	if err != nil {
		return err
	}
	rows, err := queryIBStatus(ctx, monitoring.New(r), options.node)
	if err != nil {
		return err
	}
	if options.json {
		return writeJSON(stdout, rows)
	}
	printIBTable(stdout, rows)
	return nil
}

func parseIBStatusArgs(args []string) (ibStatusOptions, error) {
	options := ibStatusOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--node":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return ibStatusOptions{}, errors.New("--node requires a non-empty node name")
			}
			i++
			options.node = strings.TrimSpace(args[i])
			if err := validateIBNode(options.node); err != nil {
				return ibStatusOptions{}, err
			}
		case "--json":
			options.json = true
		case "--help", "-h":
			return ibStatusOptions{}, errors.New("usage: gpu ib status [--node NODE] [--json]")
		default:
			return ibStatusOptions{}, fmt.Errorf("unknown ib status option %q; run gpu ib status --help", args[i])
		}
	}
	return options, nil
}

func validateIBNode(node string) error {
	if strings.TrimSpace(node) == "" {
		return errors.New("node name cannot be empty")
	}
	for _, r := range node {
		if r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		return fmt.Errorf("invalid node name %q", node)
	}
	return nil
}

func queryIBStatus(ctx context.Context, client prometheusQuerier, node string) ([]ibPortStatus, error) {
	if err := validateIBNodeOptional(node); err != nil {
		return nil, err
	}
	results := make(map[string]monitoring.QueryResult, len(ibMetricNames))
	anySamples := false
	for _, metric := range ibMetricNames {
		result, err := client.Query(ctx, metric)
		if err != nil {
			if isEmptyPrometheusResult(err) {
				results[metric] = monitoring.QueryResult{}
				continue
			}
			return nil, fmt.Errorf("query %s: %w", metric, err)
		}
		if len(result.Samples) > 0 {
			anySamples = true
		}
		results[metric] = result
	}
	rows := joinIBSamples(results, node)
	if len(rows) == 0 {
		if node != "" {
			return nil, fmt.Errorf("no InfiniBand/RDMA fabric samples found for node %q", node)
		}
		if !anySamples {
			return nil, errors.New("no InfiniBand/RDMA fabric samples found")
		}
		return nil, errors.New("fabric samples did not contain node/hca/port labels")
	}
	return rows, nil
}

func validateIBNodeOptional(node string) error {
	if strings.TrimSpace(node) == "" {
		return nil
	}
	return validateIBNode(strings.TrimSpace(node))
}

func isEmptyPrometheusResult(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no result") || strings.Contains(message, "no samples") || strings.Contains(message, "empty result")
}

func joinIBSamples(results map[string]monitoring.QueryResult, node string) []ibPortStatus {
	rows := make(map[ibPortKey]*ibPortStatus)
	// Walk the declared metric order instead of the map order so label
	// precedence remains deterministic when an exporter includes more than one
	// sample for a port.
	for _, metric := range ibMetricNames {
		result := results[metric]
		for _, sample := range result.Samples {
			key, ok := ibSampleKey(sample.Metric)
			if !ok || (node != "" && key.Node != node) {
				continue
			}
			row := rows[key]
			if row == nil {
				row = &ibPortStatus{Node: key.Node, HCA: key.HCA, Port: key.Port, LinkLayer: "unknown", State: "unknown", PhysicalState: "unknown"}
				rows[key] = row
			}
			if layer := label(sample.Metric, "link_layer", "linkLayer", "link_type"); layer != "" {
				row.LinkLayer = layer
			}
			switch metric {
			case "gpu_lab_ib_port_up":
				row.Up = sample.Value
			case "gpu_lab_ib_port_state":
				if value := label(sample.Metric, "state", "port_state", "link_state"); value != "" {
					row.State = value
				}
				if value := label(sample.Metric, "physical_state", "physical_port_state"); value != "" {
					row.PhysicalState = value
				}
			case "gpu_lab_ib_link_rate_gbps":
				row.RateGbps = sample.Value
			case "gpu_lab_ib_tx_bytes_total":
				row.TXBytes = sample.Value
			case "gpu_lab_ib_rx_bytes_total":
				row.RXBytes = sample.Value
			case "gpu_lab_ib_symbol_errors_total":
				row.SymbolErrors = sample.Value
			case "gpu_lab_ib_link_downed_total":
				row.LinkDowned = sample.Value
			case "gpu_lab_ib_xmit_discards_total":
				row.XmitDiscards = sample.Value
			case "gpu_lab_ib_xmit_wait_total":
				row.XmitWait = sample.Value
			case "gpu_lab_rdma_retries_total":
				row.RDMARetries = sample.Value
			case "gpu_lab_rdma_timeouts_total":
				row.RDMATimeouts = sample.Value
			}
		}
	}
	ordered := make([]ibPortStatus, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, *row)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Node != ordered[j].Node {
			return ordered[i].Node < ordered[j].Node
		}
		if ordered[i].HCA != ordered[j].HCA {
			return ordered[i].HCA < ordered[j].HCA
		}
		return ordered[i].Port < ordered[j].Port
	})
	return ordered
}

func ibSampleKey(metric map[string]string) (ibPortKey, bool) {
	key := ibPortKey{
		Node: label(metric, "node", "kubernetes_node", "node_name"),
		HCA:  label(metric, "hca", "hca_name", "device"),
		Port: label(metric, "port", "port_number", "port_id"),
	}
	return key, key.Node != "" && key.HCA != "" && key.Port != ""
}

func label(labels map[string]string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(labels[name]); value != "" {
			return value
		}
	}
	return ""
}

func printIBTable(stdout io.Writer, rows []ibPortStatus) {
	fmt.Fprintln(stdout, "NODE\tHCA\tPORT\tLINK_LAYER\tSTATE\tPHYSICAL_STATE\tUP\tRATE_GBPS\tTX_BYTES\tRX_BYTES\tSYMBOL_ERRORS\tLINK_DOWNED\tXMIT_DISCARDS\tXMIT_WAIT\tRDMA_RETRIES\tRDMA_TIMEOUTS")
	for _, row := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Node, row.HCA, row.Port, row.LinkLayer, row.State, row.PhysicalState,
			formatMetricValue(row.Up), formatMetricValue(row.RateGbps), formatMetricValue(row.TXBytes), formatMetricValue(row.RXBytes),
			formatMetricValue(row.SymbolErrors), formatMetricValue(row.LinkDowned), formatMetricValue(row.XmitDiscards), formatMetricValue(row.XmitWait),
			formatMetricValue(row.RDMARetries), formatMetricValue(row.RDMATimeouts))
	}
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
	if len(selected.Spec.Targets.Selector) == 0 && len(selected.Spec.Targets.GPUIndices) == 0 {
		fmt.Fprintln(stdout, "targets: all synthetic GPUs")
	} else {
		fmt.Fprintln(stdout, "targets:")
		keys := sortedKeys(selected.Spec.Targets.Selector)
		for _, key := range keys {
			fmt.Fprintf(stdout, "  %s=%s\n", key, selected.Spec.Targets.Selector[key])
		}
		if len(selected.Spec.Targets.GPUIndices) > 0 {
			indices := append([]int(nil), selected.Spec.Targets.GPUIndices...)
			sort.Ints(indices)
			parts := make([]string, 0, len(indices))
			for _, index := range indices {
				parts = append(parts, strconv.Itoa(index))
			}
			fmt.Fprintf(stdout, "  gpu_indices=%s\n", strings.Join(parts, ","))
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
	// The training ConfigMap is an optional consumer of GPU scenarios. A lab
	// can run GPU-only exercises, so a missing training workload must never
	// make scenario application fail.
	trainingClient := trainingjob.NewClient(m.Runner)
	_ = trainingClient.SyncScenarioControl(ctx, trainingjob.Namespace, selected.Name())
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
  gpu ibstat [options] [<ca_name> [port_num]]
  gpu ibstatus [<device[:port]> ...]
  gpu ibv_devinfo [-d DEVICE] [-i PORT] [-l] [-v]
  gpu training run [--workers 3] [--image gpu-lab:dev] [--namespace gpu-lab-demo] [--wait]
  gpu training status [--namespace gpu-lab-demo]
  gpu training logs --rank N [--follow] [--namespace gpu-lab-demo]
  gpu training inject straggler --rank N --delay 2s
  gpu training inject worker-crash --rank N
  gpu training recover
  gpu training reset
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

func printTrainingHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu training — operate the synthetic distributed-training StatefulSet

Usage:
  gpu training run [--workers N] [--image IMAGE] [--namespace NS] [--wait] [--timeout DURATION]
  gpu training status [--namespace NS]
  gpu training logs --rank N [--follow] [--namespace NS]
  gpu training inject straggler --rank N --delay 2s [--namespace NS]
  gpu training inject worker-crash --rank N [--namespace NS]
  gpu training recover [--namespace NS]
  gpu training reset [--namespace NS]
`)
}

// Kept only for the legacy parser's internal tests. The public `gpu ib`
// command now returns a migration message and the rdma-core command names are
// exposed directly.
func printIBHelp(w io.Writer) {
	_, _ = io.WriteString(w, "gpu ib status was replaced; use gpu ibstat, gpu ibstatus, or gpu ibv_devinfo\n")
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
