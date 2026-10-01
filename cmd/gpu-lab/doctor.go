package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

// Teaching-cluster sizing guidance. These are comfortable teaching targets,
// not validated minimum requirements: the lab can start on smaller machines
// with slower feedback. Never phrase them as hard prerequisites.
const (
	recommendedDockerVMMemoryMiB int64 = 8 * 1024
	recommendedDockerVMCPUs      int   = 4
	recommendedDockerFreeMiB     int64 = 10 * 1024
	dockerProbeTimeout                 = 8 * time.Second
	kubectlProbeTimeout                = 8 * time.Second
	kindProbeTimeout                   = 8 * time.Second
	toolProbeTimeout                   = 8 * time.Second
)

// Course topology defaults verified against deploy/kind/cluster.yaml: three
// GPU workers carrying the teaching labels gpu.lab/type=fake and
// gpu.lab/node-id=gpu-node-01..03 x eight GPUs, plus one GPU-less
// control-plane node.
const (
	gpuResourceName      = "nvidia.com/gpu"
	workerTypeLabelKey   = "gpu.lab/type"
	workerTypeLabelValue = "fake"
	workerNodeIDLabelKey = "gpu.lab/node-id"

	// controlPlaneLabelSuffix is the standard Kubernetes well-known role
	// label value applied to control-plane nodes (beta variants included).
	expectedGPUWorkerNodes    = 3
	expectedGPUsPerWorker     = 8
	expectedTotalGPUs         = expectedGPUWorkerNodes * expectedGPUsPerWorker
	expectedControlPlaneNodes = 1
)

// preflightEnv abstracts the outside world so doctor can be exercised with a
// fake runner in tests.
type preflightEnv struct {
	// LookPath resolves an executable; defaults to exec.LookPath.
	LookPath func(name string) (string, error)
	// Output runs a bounded read-only command; tests inject a fake runner.
	Output func(ctx context.Context, name string, args ...string) (string, error)
	// HostMemoryTotal reports the physical host memory in bytes, separate
	// from what the Docker daemon reports.
	HostMemoryTotal func(ctx context.Context) (int64, error)
	// DiskFree measures free bytes for a path; nil means the path cannot be
	// measured from this process and the probe falls back to manual guidance.
	DiskFree func(ctx context.Context, path string) (int64, error)
}

func defaultPreflightEnv(r runner.Runner) preflightEnv {
	// DiskFree stays nil by default: DockerRootDir comes from the daemon, and
	// the daemon may run in a VM or on a remote host, so a local statfs of
	// that path could measure an unrelated local directory. Without a
	// confirmed local correspondence, doctor guides with manual 'df -h'
	// instructions instead of pretending to measure. Tests may still inject
	// a DiskFree implementation to exercise the measured branches.
	return preflightEnv{
		LookPath:        exec.LookPath,
		Output:          r.Output,
		HostMemoryTotal: probeHostMemoryTotal,
	}
}

type probeStatus string

const (
	statusReady   probeStatus = "ready"
	statusInfo    probeStatus = "info"
	statusWarn    probeStatus = "warn"
	statusMissing probeStatus = "missing"
	statusFail    probeStatus = "fail"
)

type probeResult struct {
	Status  probeStatus
	Name    string
	Message string
	// Fatal marks a prerequisite gap that must stop create.
	Fatal bool
}

// controlPlaneRoleLabels are the well-known Kubernetes role labels that
// mark control-plane nodes (stable and beta keys).
var controlPlaneRoleLabels = []string{
	"node-role.kubernetes.io/control-plane",
	"node-role.kubernetes.io/master",
	"node-role.kubernetes.io/control-plane-beta",
	"node-role.kubernetes.io/master-beta",
}

func doctor(ctx context.Context, r runner.Runner, stdout io.Writer) error {
	return runDoctor(ctx, stdout, defaultPreflightEnv(r))
}

// runDoctor is the testable body: every external process goes through env
// with a bounded timeout and read-only arguments only. It never creates,
// repairs, or destroys resources and never prints kubeconfig material.
func runDoctor(ctx context.Context, stdout io.Writer, env preflightEnv) error {
	fmt.Fprintln(stdout, "Synthetic lab: GPU telemetry and device plugin behavior are simulated; no NVIDIA driver or CUDA execution is used.")
	fmt.Fprintln(stdout, "Preflight is read-only: it never creates, repairs, or destroys resources.")

	results := checkRequiredTools(ctx, env)
	results = append(results, checkDockerDaemon(ctx, env)...)
	results = append(results, checkClusterContext(ctx, env)...)

	for _, result := range results {
		fmt.Fprintf(stdout, "[%s] %s: %s\n", result.Status, result.Name, result.Message)
	}

	var failures []string
	for _, result := range results {
		if result.Fatal {
			failures = append(failures, fmt.Sprintf("%s (%s)", result.Name, result.Message))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("preflight failed; resolve before gpu-lab create: %s", strings.Join(failures, "; "))
	}
	return nil
}

func checkRequiredTools(ctx context.Context, env preflightEnv) []probeResult {
	results := make([]probeResult, 0, 4)
	for _, name := range []string{"docker", "kind", "kubectl", "helm"} {
		if _, err := env.LookPath(name); err != nil {
			results = append(results, probeResult{Status: statusMissing, Name: name, Message: "not found in PATH; install it before running gpu-lab create", Fatal: true})
			continue
		}
		version, err := probeOutput(ctx, env, toolProbeTimeout, name, versionArgs(name)...)
		if err != nil {
			results = append(results, probeResult{Status: statusWarn, Name: name, Message: fmt.Sprintf("found, but version probe failed: %v", bounded(err))})
			continue
		}
		results = append(results, probeResult{Status: statusReady, Name: name, Message: firstLine(version)})
	}
	return results
}

func checkDockerDaemon(ctx context.Context, env preflightEnv) []probeResult {
	results := make([]probeResult, 0, 5)
	const format = "{{.ServerVersion}}|{{.MemTotal}}|{{.NCPU}}|{{.DockerRootDir}}"
	out, err := probeOutput(ctx, env, dockerProbeTimeout, "docker", "info", "--format", format)
	if err != nil {
		results = append(results, probeResult{Status: statusFail, Name: "docker daemon", Message: fmt.Sprintf("did not respond to a bounded 'docker info' probe: %v", bounded(err)), Fatal: true})
		results = append(results, probeResult{Status: statusInfo, Name: "docker daemon", Message: "start Docker Desktop / the docker service, then re-run gpu-lab doctor"})
		return results
	}
	fields := strings.Split(strings.TrimSpace(out), "|")
	if len(fields) != 4 {
		results = append(results, probeResult{Status: statusFail, Name: "docker daemon", Message: "unexpected 'docker info' reply format; upgrade Docker and re-run", Fatal: true})
		return results
	}
	serverVersion := cleanProbeValue(fields[0])
	memoryBytes, memoryErr := strconv.ParseInt(cleanProbeValue(fields[1]), 10, 64)
	cpuCount, cpuErr := strconv.Atoi(cleanProbeValue(fields[2]))

	results = append(results, probeResult{Status: statusReady, Name: "docker daemon", Message: fmt.Sprintf("responds (server %s)", serverVersion)})

	if memoryErr == nil && cpuErr == nil && memoryBytes > 0 {
		memoryMiB := memoryBytes / (1024 * 1024)
		results = append(results, probeResult{Status: statusReady, Name: "docker resources", Message: fmt.Sprintf("memory/CPU visible to the Docker daemon: %s memory, %d CPUs; with Docker Desktop (macOS/Windows) these are the Docker VM limits rather than host totals, with a native Linux engine they usually equal the host", formatMiB(memoryMiB), cpuCount)})
		if memoryMiB < recommendedDockerVMMemoryMiB {
			results = append(results, probeResult{Status: statusWarn, Name: "docker resources", Message: fmt.Sprintf("recommended teaching target is >= %s of memory for the Docker daemon (recommended for smooth teaching, not a validated minimum)", formatMiB(recommendedDockerVMMemoryMiB))})
		}
		if cpuCount < recommendedDockerVMCPUs {
			results = append(results, probeResult{Status: statusWarn, Name: "docker resources", Message: fmt.Sprintf("recommended teaching target is >= %d CPUs visible to the Docker daemon (recommended for smooth teaching, not a validated minimum)", recommendedDockerVMCPUs)})
		}
	} else {
		results = append(results, probeResult{Status: statusInfo, Name: "docker resources", Message: "the daemon did not report memory/CPU limits; inspect 'docker info' manually"})
	}

	if hostMemory, hostErr := probeHostMemory(ctx, env); hostErr == nil && hostMemory > 0 {
		results = append(results, probeResult{Status: statusInfo, Name: "host memory", Message: fmt.Sprintf("physical host total %s (separate from the value the Docker daemon reports above; see docs/getting-started.md for sizing guidance)", formatMiB(hostMemory/1024/1024))})
	}

	results = append(results, checkDockerDisk(ctx, env, cleanProbeValue(fields[3])))
	return results
}

func checkDockerDisk(ctx context.Context, env preflightEnv, rootDir string) probeResult {
	if rootDir == "" || rootDir == "unknown" {
		return probeResult{Status: statusInfo, Name: "docker storage", Message: "manual check: the daemon did not report its storage path; inspect free space yourself with 'docker info' and 'df -h' (for example inside the Docker Desktop VM)"}
	}
	if env.DiskFree == nil {
		return probeResult{Status: statusInfo, Name: "docker storage", Message: fmt.Sprintf("manual check: daemon storage path is %s but free space cannot be measured from this process; verify it yourself with 'df -h %s' on the daemon host", rootDir, rootDir)}
	}
	freeBytes, err := env.DiskFree(ctx, rootDir)
	if err != nil {
		return probeResult{Status: statusInfo, Name: "docker storage", Message: fmt.Sprintf("manual check: free space at %s could not be measured from this process (%v); verify it yourself with 'df -h %s' on the daemon host, for example inside the Docker Desktop VM", rootDir, bounded(err), rootDir)}
	}
	freeMiB := freeBytes / (1024 * 1024)
	if freeMiB < recommendedDockerFreeMiB {
		return probeResult{Status: statusWarn, Name: "docker storage", Message: fmt.Sprintf("measured free space at %s is %s, below the recommended teaching target of %s; images and kind nodes need room (recommended, not a validated minimum)", rootDir, formatMiB(freeMiB), formatMiB(recommendedDockerFreeMiB))}
	}
	return probeResult{Status: statusReady, Name: "docker storage", Message: fmt.Sprintf("measured free space at %s is %s", rootDir, formatMiB(freeMiB))}
}

const devicePluginInstallHint = "if GPU devices are not registered for this session yet, install the plugin first with 'gpu-lab helm install nvidia-device-plugin'"

// checkClusterContext verifies the lab cluster and expected GPU topology
// read-only. A missing cluster is a normal state before the first session.
func checkClusterContext(ctx context.Context, env preflightEnv) []probeResult {
	if _, err := env.LookPath("kind"); err != nil {
		return []probeResult{{Status: statusInfo, Name: "lab cluster", Message: "cluster probe skipped because kind is missing from PATH (reported above)"}}
	}
	out, err := probeOutput(ctx, env, kindProbeTimeout, "kind", "get", "clusters")
	if err != nil {
		return []probeResult{{Status: statusWarn, Name: "lab cluster", Message: fmt.Sprintf("could not list kind clusters: %v", bounded(err))}}
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		// 'kind get clusters' lists kind cluster names. The lab kubecontext
		// name is derived from it (kubeconfig.LabContext), so compare the
		// bare kind cluster name here rather than a context string.
		if strings.TrimSpace(line) == cluster.ClusterName {
			found = true
			break
		}
	}
	if !found {
		return []probeResult{{Status: statusInfo, Name: "lab cluster", Message: fmt.Sprintf("no %q kind cluster yet (normal before the first course session); next step: gpu-lab create", cluster.ClusterName)}}
	}

	// Bounded read-only node probe, targeting the lab context explicitly.
	// Never use kubectl config view / config dump and never print raw
	// kubeconfig material. If the context is missing but the kind cluster
	// exists, guide students through the supported context flow.
	probeArgs := func() []string {
		return []string{"--context", cluster.KubeContext, "get", "nodes", "-o", "json"}
	}
	raw, err := probeOutput(ctx, env, kubectlProbeTimeout, "kubectl", probeArgs()...)
	if err != nil && contextLooksMissing(bounded(err).Error()) {
		// Suggest only. The supported recovery is 'gpu-lab context setup'
		// (kubeconfig.Manager.Setup): it exports the kind credentials and
		// creates the gpu-lab alias for kind-gpu-lab in the primary
		// kubeconfig plus a dedicated gpu-lab.config, without switching the
		// user current context. Bare 'kind export kubeconfig' alone would
		// only create the kind-gpu-lab context, so point at the project
		// command. Doctor itself never writes kubeconfig state.
		return []probeResult{{Status: statusWarn, Name: "lab cluster", Message: fmt.Sprintf("the %q kind cluster exists but the lab kube context %q is missing or unreachable; repair it with 'gpu-lab context setup' (exports the kind credentials and creates the %q alias without switching your current context), then verify with 'kubectl --context %s get nodes' (original error: %v)", cluster.ClusterName, cluster.KubeContext, cluster.KubeContext, cluster.KubeContext, bounded(err))}}
	}
	if err != nil {
		return []probeResult{{Status: statusWarn, Name: "lab cluster", Message: fmt.Sprintf("cluster exists but the bounded node probe failed: %v", bounded(err))}}
	}
	summaries, decodeErr := parseNodeSummaries(raw)
	if decodeErr != nil {
		return []probeResult{{Status: statusWarn, Name: "topology", Message: fmt.Sprintf("node probe returned an unexpected reply: %v", bounded(decodeErr))}}
	}
	return evaluateTopology(summaries)
}

// contextLooksMissing detects the kubectl error shapes for an absent or
// unreachable kube context.
func contextLooksMissing(message string) bool {
	lowered := strings.ToLower(message)
	if !strings.Contains(lowered, "context") {
		return false
	}
	for _, marker := range []string{"does not exist", "not found", "no such"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

type nodeSummary struct {
	Name            string
	IsControlPlane  bool
	IsLabeledWorker bool
	GPUCount        int // -1 means the node does not report the GPU resource
}

// parseNodeSummaries decodes only the bounded fields doctor needs (name,
// role labels, GPU capacity). Everything else in the node JSON is dropped at
// decode time, so secret-bearing fields cannot be echoed.
func parseNodeSummaries(raw string) ([]nodeSummary, error) {
	var envelope struct {
		Kind  string `json:"kind"`
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Capacity map[string]string `json:"capacity"`
			} `json:"status"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("cannot decode node list: %w", err)
	}
	if envelope.Kind != "NodeList" {
		return nil, fmt.Errorf("node probe returned kind %q", envelope.Kind)
	}
	if len(envelope.Items) == 0 {
		return nil, fmt.Errorf("node probe returned an empty node list")
	}
	summaries := make([]nodeSummary, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		name := item.Metadata.Name
		if name == "" {
			return nil, fmt.Errorf("node probe returned an item without a name")
		}
		labels := item.Metadata.Labels
		isControlPlane := false
		for _, key := range controlPlaneRoleLabels {
			if _, ok := labels[key]; ok {
				isControlPlane = true
			}
		}
		labeledWorker := labels[workerTypeLabelKey] == workerTypeLabelValue
		if _, hasNodeID := labels[workerNodeIDLabelKey]; hasNodeID {
			labeledWorker = true
		}

		gpuCount := -1
		if capacity, ok := item.Status.Capacity[gpuResourceName]; ok {
			parsed, err := strconv.Atoi(strings.TrimSpace(capacity))
			if err != nil {
				return nil, fmt.Errorf("node %s has unparsable %s capacity %q", name, gpuResourceName, capacity)
			}
			if parsed < 0 {
				return nil, fmt.Errorf("node %s reports negative %s capacity", name, gpuResourceName)
			}
			gpuCount = parsed
		}
		summaries = append(summaries, nodeSummary{Name: name, IsControlPlane: isControlPlane, IsLabeledWorker: labeledWorker, GPUCount: gpuCount})
	}
	return summaries, nil
}

// evaluateTopology turns bounded node summaries into ordered probe results.
func evaluateTopology(summaries []nodeSummary) []probeResult {
	var (
		workers        []nodeSummary
		controlPlanes  []nodeSummary
		unclassified   []string
		workerNoGPU    []string
		workerBadCount []string
		controlWithGPU []string
		totalGPUs      int
	)
	for _, node := range summaries {
		switch {
		case node.IsControlPlane:
			controlPlanes = append(controlPlanes, node)
			if node.GPUCount > 0 {
				controlWithGPU = append(controlWithGPU, fmt.Sprintf("%s reports %d %s", node.Name, node.GPUCount, gpuResourceName))
			}
		case node.IsLabeledWorker && node.GPUCount < 0:
			workers = append(workers, node)
			workerNoGPU = append(workerNoGPU, node.Name)
		case node.IsLabeledWorker:
			workers = append(workers, node)
			totalGPUs += node.GPUCount
			if node.GPUCount != expectedGPUsPerWorker {
				workerBadCount = append(workerBadCount, fmt.Sprintf("%s=%d", node.Name, node.GPUCount))
			}
		default:
			// A GPU-reading node without teaching labels must not be treated
			// as a healthy lab worker: role comes from labels, GPU capacity
			// only from there. Route it to the warning/unclassified lane.
			if node.GPUCount >= 0 {
				unclassified = append(unclassified, fmt.Sprintf("%s (reports %d %s)", node.Name, node.GPUCount, gpuResourceName))
				continue
			}
			unclassified = append(unclassified, node.Name)
		}
	}

	results := make([]probeResult, 0, 5)
	results = append(results, probeResult{Status: statusReady, Name: "lab cluster", Message: fmt.Sprintf("%q kind cluster exists", cluster.ClusterName)})

	if len(unclassified) > 0 {
		results = append(results, probeResult{Status: statusWarn, Name: "topology", Message: fmt.Sprintf("node(s) [%s] carry neither a control-plane role label nor the GPU worker teaching labels (%s / %s); inspect them with 'kubectl --context %s get nodes --show-labels' before changing anything", strings.Join(unclassified, ", "), workerTypeLabelKey, workerNodeIDLabelKey, cluster.KubeContext)})
	}

	if len(workerNoGPU) > 0 {
		results = append(results, probeResult{Status: statusWarn, Name: "topology", Message: fmt.Sprintf("GPU worker node(s) [%s] do not report %s yet; %s; also check plugin pods with 'kubectl --context %s get pods -n %s'", strings.Join(workerNoGPU, ", "), gpuResourceName, devicePluginInstallHint, cluster.KubeContext, cluster.SystemNamespace)})
	}

	if len(controlWithGPU) > 0 {
		results = append(results, probeResult{Status: statusWarn, Name: "topology", Message: fmt.Sprintf("unexpected GPU registration on control-plane node(s): %s; the course default keeps the control plane GPU-less — inspect component values with 'gpu-lab status' and the node labels above before changing anything", strings.Join(controlWithGPU, ", "))})
	}

	gpuWorkersWithDevices := 0
	for _, node := range workers {
		if node.GPUCount >= 0 {
			gpuWorkersWithDevices++
		}
	}

	switch {
	case len(workerNoGPU) > 0 || len(workerBadCount) > 0 || len(controlWithGPU) > 0 || len(unclassified) > 0:
		if len(workerBadCount) > 0 {
			results = append(results, probeResult{Status: statusWarn, Name: "topology", Message: fmt.Sprintf("every GPU worker must register exactly %d GPUs; deviation: %s (expected %d workers x %d GPUs = %d total, observed total %d). %s; compare component values with 'gpu-lab status' before changing anything — bulk destroy/create is only for confirmed drift, not a blanket fix", expectedGPUsPerWorker, strings.Join(workerBadCount, ", "), expectedGPUWorkerNodes, expectedGPUsPerWorker, expectedTotalGPUs, totalGPUs, devicePluginInstallHint)})
		}
	case gpuWorkersWithDevices != expectedGPUWorkerNodes || totalGPUs != expectedTotalGPUs || len(controlPlanes) != expectedControlPlaneNodes:
		results = append(results, probeResult{Status: statusWarn, Name: "topology", Message: fmt.Sprintf("observed %d GPU worker node(s) (total %d GPUs) and %d control-plane node(s); the course default expects %d GPU workers x %d GPUs (cluster total %d across all nodes) with %d GPU-less control plane. %s; compare component values with 'gpu-lab status' before changing anything — bulk destroy/create is only for confirmed drift, not a blanket fix", gpuWorkersWithDevices, totalGPUs, len(controlPlanes), expectedGPUWorkerNodes, expectedGPUsPerWorker, expectedTotalGPUs, expectedControlPlaneNodes, devicePluginInstallHint)})
	default:
		results = append(results, probeResult{Status: statusReady, Name: "topology", Message: fmt.Sprintf("matches the course default: %d GPU worker nodes x %d GPUs each (cluster total %d across all nodes), %d GPU-less control-plane node(s); a dashboard showing %d GPUs with the All filter is expected, a single node shows %d", expectedGPUWorkerNodes, expectedGPUsPerWorker, expectedTotalGPUs, expectedControlPlaneNodes, expectedTotalGPUs, expectedGPUsPerWorker)})
	}
	return results
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

func probeOutput(ctx context.Context, env preflightEnv, timeout time.Duration, name string, args ...string) (string, error) {
	if env.Output == nil {
		return "", fmt.Errorf("no command runner configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return env.Output(probeCtx, name, args...)
}

// cleanProbeValue normalizes Go-template empty placeholders.
func cleanProbeValue(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "<no value>", "<none>", "<error>", "":
		return "unknown"
	}
	return value
}

// bounded keeps probe error text short so failures never leak long command
// output, and redacts text that could carry credentials or kubeconfig data.
func bounded(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if containsSensitiveMarker(message) {
		return fmt.Errorf("details suppressed to avoid printing credentials or kubeconfig material")
	}
	if len(message) > 200 {
		message = message[:200] + "..."
	}
	return fmt.Errorf("%s", message)
}

var sensitiveMarkers = []string{
	"client-key-data",
	"client-key",
	"client-certificate-data",
	"certificate-authority-data",
	"basic-auth",
	"token=",
	"bearer ",
	"password=",
	"secret=",
}

func containsSensitiveMarker(message string) bool {
	lowered := strings.ToLower(message)
	for _, marker := range sensitiveMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	return strings.TrimSpace(line)
}

func formatMiB(mib int64) string {
	switch {
	case mib >= 1024 && mib%1024 == 0:
		return fmt.Sprintf("%d GiB", mib/1024)
	default:
		return fmt.Sprintf("%d MiB", mib)
	}
}

func probeHostMemory(ctx context.Context, env preflightEnv) (int64, error) {
	if env.HostMemoryTotal == nil {
		return 0, fmt.Errorf("no host memory probe configured")
	}
	return env.HostMemoryTotal(ctx)
}
