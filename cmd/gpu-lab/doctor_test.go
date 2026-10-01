package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type probeCall struct {
	name string
	args []string
}

func (c probeCall) joined() string { return c.name + " " + strings.Join(c.args, " ") }

// fakePreflight drives doctor with an injected fake runner so no real
// process, socket, or cluster is touched. Every probe call is recorded for
// read-only/bounded assertions.
type fakePreflight struct {
	calls        []probeCall
	maxDeadline  time.Duration
	sawDeadline  bool
	lookTargets  map[string]bool
	responders   map[string]func(args []string) (string, error)
	hostMemory   int64
	hostMemoryOK bool
	diskFree     map[string]int64
}

func newFakePreflight() *fakePreflight {
	return &fakePreflight{
		lookTargets: map[string]bool{"docker": true, "kind": true, "kubectl": true, "helm": true},
		responders:  map[string]func(args []string) (string, error){},
		diskFree:    map[string]int64{},
	}
}

func (f *fakePreflight) env() preflightEnv {
	return preflightEnv{
		LookPath: func(name string) (string, error) {
			if f.lookTargets[name] {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("executable file not found")
		},
		Output: func(ctx context.Context, name string, args ...string) (string, error) {
			f.calls = append(f.calls, probeCall{name: name, args: args})
			if deadline, ok := ctx.Deadline(); ok {
				f.sawDeadline = true
				if remaining := time.Until(deadline); remaining > f.maxDeadline {
					f.maxDeadline = remaining
				}
			}
			if responder, ok := f.responders[name]; ok {
				return responder(args)
			}
			return "", fmt.Errorf("unexpected command %s", name)
		},
		HostMemoryTotal: func(context.Context) (int64, error) {
			if !f.hostMemoryOK {
				return 0, errors.New("host memory probe unavailable")
			}
			return f.hostMemory, nil
		},
		DiskFree: func(_ context.Context, path string) (int64, error) {
			free, ok := f.diskFree[path]
			if !ok {
				return 0, errors.New("statfs measurement unavailable for " + path)
			}
			return free, nil
		},
	}
}

func nodeListJSON(entries ...string) string {
	// entries are pre-rendered item JSON fragments.
	return `{"kind":"NodeList","apiVersion":"v1","items":[` + strings.Join(entries, ",") + `]}`
}

func nodeItem(name string, labels, capacity string) string {
	if labels == "" {
		labels = "{}"
	}
	if capacity == "" {
		capacity = `{"cpu":"4","memory":"8Gi","pods":"110"}`
	}
	return fmt.Sprintf(`{"metadata":{"name":%q,"creationTimestamp":null,"labels":%s},"spec":{"podCIDR":"10.244.1.0/24"},"status":{"addresses":[{"address":"172.18.0.2","type":"InternalIP"}],"capacity":%s}}`, name, labels, capacity)
}

const gpuWorkerLabels = `{"beta.kubernetes.io/instance-type":"kind","gpu.lab/node-id":"REPLACED","gpu.lab/type":"fake","kubernetes.io/arch":"amd64","kubernetes.io/hostname":"REPLACED","kubernetes.io/os":"linux"}`

const controlPlaneLabels = `{"beta.kubernetes.io/instance-type":"kind","kubernetes.io/arch":"amd64","kubernetes.io/exclude-from-queues":"","node-role.kubernetes.io/control-plane":"","node-role.kubernetes.io/control-plane-beta":""}`

func healthyGPUClusterJSON() string {
	workers := make([]string, 0, 3)
	for _, id := range []string{"gpu-node-01", "gpu-node-02", "gpu-node-03"} {
		labels := strings.ReplaceAll(gpuWorkerLabels, "REPLACED", id)
		capacity := fmt.Sprintf(`{"cpu":"4","memory":"8Gi","pods":"110","%s":"8"}`, gpuResourceName)
		workers = append(workers, nodeItem("gpu-lab-worker-"+id, labels, capacity))
	}
	cp := nodeItem("gpu-lab-control-plane-abc123", controlPlaneLabels, "")
	return nodeListJSON(append([]string{cp}, workers...)...)
}

func dockerInfoResponder(info string) func(args []string) (string, error) {
	return func(args []string) (string, error) {
		if args[0] == "info" {
			if info == "" {
				return "", errors.New("cannot connect to the daemon at unix:///var/run/docker.sock")
			}
			return info, nil
		}
		return "28.3.3", nil
	}
}

const healthyDockerInfo = "28.3.3|9663676416|6|/var/lib/docker"

func healthyResponders(clusterJSON string) map[string]func(args []string) (string, error) {
	return map[string]func(args []string) (string, error){
		"docker": dockerInfoResponder(healthyDockerInfo),
		"kind": func(args []string) (string, error) {
			if args[0] == "get" && len(args) > 1 && args[1] == "clusters" {
				if clusterJSON == clusterAbsentSentinel {
					return "", nil
				}
				return "gpu-lab\n", nil
			}
			return "kind v0.30.0", nil
		},
		"kubectl": func(args []string) (string, error) {
			if isNodeProbe(args) {
				if clusterJSON == clusterAbsentSentinel {
					return "", errors.New("error: the server could not find the requested resource")
				}
				return clusterJSON, nil
			}
			return "clientVersion: v1.34.0", nil
		},
		"helm": func(args []string) (string, error) {
			return "v3.18.4+g4b52d10", nil
		},
	}
}

const clusterAbsentSentinel = "__absent__"

func runDoctorWith(t *testing.T, f *fakePreflight) (string, error) {
	t.Helper()
	var stdout strings.Builder
	err := runDoctor(context.Background(), &stdout, f.env())
	return stdout.String(), err
}

func mustContain(t *testing.T, output, want string) {
	t.Helper()
	if !strings.Contains(output, want) {
		t.Fatalf("doctor output missing %q\n---\n%s", want, output)
	}
}

func mustNotContain(t *testing.T, output, unwanted string) {
	t.Helper()
	if strings.Contains(strings.ToLower(output), strings.ToLower(unwanted)) {
		t.Fatalf("doctor output unexpectedly contains %q\n---\n%s", unwanted, output)
	}
}

func requireCalls(t *testing.T, f *fakePreflight, predicate func(probeCall) bool, description string) {
	t.Helper()
	for _, call := range f.calls {
		if predicate(call) {
			return
		}
	}
	t.Fatalf("expected a probe for %s; calls were %v", description, f.calls)
}

func requireNoCalls(t *testing.T, f *fakePreflight, predicate func(probeCall) bool, description string) {
	t.Helper()
	for _, call := range f.calls {
		if predicate(call) {
			t.Fatalf("forbidden probe (%s): %s", description, call.joined())
		}
	}
}

func TestDoctorReportsMissingPrerequisiteAndFails(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	delete(f.lookTargets, "helm")
	delete(f.lookTargets, "kind")
	output, err := runDoctorWith(t, f)
	if err == nil {
		t.Fatalf("doctor succeeded although prerequisites are missing\n%s", output)
	}
	if !strings.Contains(err.Error(), "helm") || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("error should name missing tools, got %v", err)
	}
	mustContain(t, output, "[missing] helm")
	mustContain(t, output, "[ready] docker")
	mustContain(t, output, "cluster probe skipped because kind is missing")
}

func TestDoctorFailsWhenDockerDaemonSilent(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	f.responders["docker"] = dockerInfoResponder("")
	output, err := runDoctorWith(t, f)
	if err == nil {
		t.Fatal("doctor succeeded although the docker daemon did not respond")
	}
	mustContain(t, output, "[fail] docker daemon")
	mustContain(t, output, "re-run gpu-lab doctor")
}

func TestDoctorAllReadyWithNoClusterSuggestsCreate(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	f.hostMemoryOK = true
	f.hostMemory = 34359738304 // 32767 MiB host
	f.diskFree["/var/lib/docker"] = 40 * 1024 * 1024 * 1024
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, output)
	}
	mustContain(t, output, "[ready] docker daemon")
	mustContain(t, output, "no \"gpu-lab\" kind cluster yet")
	mustContain(t, output, "next step: gpu-lab create")
	mustContain(t, output, "memory/CPU visible to the Docker daemon: 9 GiB memory, 6 CPUs")
	mustContain(t, output, "Docker Desktop")
	mustContain(t, output, "native Linux engine")
	mustContain(t, output, "host total 32767 MiB")
	mustContain(t, output, "measured free space at /var/lib/docker is 40 GiB")
}

func TestDoctorWarnsBelowRecommendedButNotFatal(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	f.responders["docker"] = dockerInfoResponder("28.3.3|3221225472|2|/var/lib/docker") // ~3 GiB, 2 CPUs
	f.diskFree["/var/lib/docker"] = 20 * 1024 * 1024
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("small-but-usable resources must not fail preflight: %v\n%s", err, output)
	}
	if strings.Count(output, "[warn]") < 3 {
		t.Fatalf("expected memory/CPU/disk warnings\n%s", output)
	}
	mustContain(t, output, "not a validated minimum")
	mustNotContain(t, output, "minimum requirement")
	mustContain(t, output, "measured free space at /var/lib/docker is 20 MiB")
}

func TestDoctorManualDiskGuidanceWhenUnmeasurable(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	// /var/lib/docker is not measurable through the fake statfs map.
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, output)
	}
	mustContain(t, output, "[info] docker storage")
	mustContain(t, output, "manual check")
	mustContain(t, output, "could not be measured")
	mustContain(t, output, "df -h /var/lib/docker")
	// Manual guidance must never claim a measurement was completed.
	mustNotContain(t, output, "[ready] docker storage")
}

func TestDoctorManualDiskGuidanceWhenRootUnknown(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	f.responders["docker"] = dockerInfoResponder("28.3.3|9663676416|6|<no value>")
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, output)
	}
	mustContain(t, output, "[info] docker storage")
	mustContain(t, output, "manual check")
	mustContain(t, output, "df -h")
}

func TestDoctorVerifiesExpectedGPUTopology(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(healthyGPUClusterJSON())
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, output)
	}
	mustContain(t, output, "[ready] topology")
	mustContain(t, output, "3 GPU worker nodes x 8 GPUs each (cluster total 24 across all nodes)")
	mustContain(t, output, "1 GPU-less control-plane node(s)")
	mustContain(t, output, "a single node shows 8")
	requireCalls(t, f, func(c probeCall) bool {
		return c.name == "kind" && len(c.args) >= 2 && c.args[0] == "get" && c.args[1] == "clusters"
	}, "'kind get clusters'")
	requireCalls(t, f, func(c probeCall) bool {
		return c.name == "kubectl" && strings.Contains(c.joined(), "--context gpu-lab get nodes -o json")
	}, "explicit --context gpu-lab node probe")
	requireNoCalls(t, f, func(c probeCall) bool {
		return strings.Contains(c.name, "kubeconfig") || strings.Contains(c.joined(), "config view") || strings.Contains(c.joined(), "config dump")
	}, "kubeconfig view/dump")
	requireNoCalls(t, f, func(c probeCall) bool {
		return c.name == "kubectl" && !strings.Contains(c.joined(), "--context gpu-lab") && strings.Contains(c.joined(), "get nodes")
	}, "node probe without the lab context")
}

func TestDoctorVerifiesGPURegistrationViaCapacity(t *testing.T) {
	// If GPU count is absent from status.capacity the probe must warn even
	// when the teaching label exists (device plugin not registered).
	f := newFakePreflight()
	json := func() string {
		labels := strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-01")
		entries := []string{
			nodeItem("gpu-lab-worker-x1", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-01"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-x2", labels, ""),
			nodeItem("gpu-lab-worker-x3", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-03"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
		}
		return nodeListJSON(append([]string{nodeItem("gpu-lab-control-plane-abc123", controlPlaneLabels, "")}, entries...)...)
	}()
	f.responders = healthyResponders(json)
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("drift must stay non-fatal: %v", err)
	}
	mustContain(t, output, "[warn] topology")
	mustContain(t, output, "[gpu-lab-worker-x2] do not report nvidia.com/gpu yet")
	mustContain(t, output, "gpu-lab helm install nvidia-device-plugin")
	mustNotContain(t, output, "helm install device-plugin")
}

func TestDoctorWarnsOnWrongGPUCount(t *testing.T) {
	f := newFakePreflight()
	fourGPU := fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"4"}`, gpuResourceName)
	build := func() string {
		entries := []string{
			nodeItem("gpu-lab-worker-a", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-01"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-b", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-02"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-c", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-03"), fourGPU),
		}
		return nodeListJSON(append([]string{nodeItem("gpu-lab-control-plane-abc123", controlPlaneLabels, "")}, entries...)...)
	}()
	f.responders = healthyResponders(build)
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("count deviation is a warning: %v", err)
	}
	mustContain(t, output, "[warn] topology")
	mustContain(t, output, "gpu-lab-worker-c=4")
	mustContain(t, output, "bulk destroy/create is only for confirmed drift")
}

func TestDoctorTreatsGPUOnControlPlaneAsDrift(t *testing.T) {
	f := newFakePreflight()
	build := func() string {
		capacity := fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)
		return nodeListJSON(
			nodeItem("gpu-lab-control-plane-abc123", controlPlaneLabels, capacity),
			nodeItem("gpu-lab-worker-a", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-01"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-b", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-02"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-c", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-03"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
		)
	}()
	f.responders = healthyResponders(build)
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("drift must stay non-fatal: %v", err)
	}
	mustContain(t, output, "[warn] topology")
	mustContain(t, output, "reports 8 nvidia.com/gpu")
	mustContain(t, output, "gpu-lab status")
}

func TestDoctorGuidesWhenContextMissingButClusterExists(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(healthyGPUClusterJSON())
	f.responders["kubectl"] = func(args []string) (string, error) {
		if isNodeProbe(args) {
			return "", errors.New(`error: the context "gpu-lab" does not exist in the kubeconfig`)
		}
		return "clientVersion: v1.34.0", nil
	}
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("a missing context is guidance, not a fatal error: %v", err)
	}
	mustContain(t, output, "gpu-lab context setup")
	mustContain(t, output, "exports the kind credentials and creates the \"gpu-lab\" alias")
	requireNoCalls(t, f, func(c probeCall) bool {
		return c.name == "kind" && strings.Contains(c.joined(), "export kubeconfig")
	}, "doctor executing kubeconfig export itself")
	requireNoCalls(t, f, func(c probeCall) bool {
		return strings.Contains(c.joined(), "config view") || strings.Contains(c.joined(), "config dump")
	}, "kubeconfig inspection")
}

func TestDoctorBoundsProbesAndTruncatesErrors(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(clusterAbsentSentinel)
	long := "boom " + strings.Repeat("x", 400)
	f.responders["helm"] = func(args []string) (string, error) {
		return "", errors.New(long)
	}
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("a failing version probe is a warning: %v\n%s", err, output)
	}
	if !f.sawDeadline {
		t.Fatal("probes must run with a bounded context deadline")
	}
	if f.maxDeadline > 10*time.Second {
		t.Fatalf("probe deadline too long: %v", f.maxDeadline)
	}
	mustContain(t, output, "version probe failed")
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "boom") && len(line) > 400 {
			t.Fatalf("probe error text not truncated: %q", line)
		}
	}
}

func TestDoctorRedactsSensitiveProbeErrors(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(healthyGPUClusterJSON())
	f.responders["kubectl"] = func(args []string) (string, error) {
		if isNodeProbe(args) {
			return "", errors.New("connection refused while reading /users/me/.kube/config with client-key-data=SECRETTOKEN")
		}
		return "clientVersion: v1.34.0", nil
	}
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("cluster probe failure is a warning: %v", err)
	}
	mustNotContain(t, output, "client-key-data")
	mustNotContain(t, output, "SECRETTOKEN")
}

func TestDoctorClassifiesUnlabeledNodes(t *testing.T) {
	f := newFakePreflight()
	build := func() string {
		return nodeListJSON(
			nodeItem("gpu-lab-control-plane-abc123", controlPlaneLabels, ""),
			nodeItem("gpu-lab-worker-a", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-01"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-b", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-02"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-c", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-03"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("mystery-node", "", ""),
		)
	}()
	f.responders = healthyResponders(build)
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("unexpected node classification is a warning: %v", err)
	}
	mustContain(t, output, "mystery-node")
	mustContain(t, output, "neither a control-plane role label nor the GPU worker teaching labels")
	mustNotContain(t, output, "[ready] topology")
}

func TestDoctorGPUWithoutTeachingLabelsIsUnclassified(t *testing.T) {
	// A node that reports 8 GPUs but carries neither teaching labels nor a
	// control-plane role label must not be accepted as a healthy lab worker.
	f := newFakePreflight()
	capacity := fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)
	build := func() string {
		return nodeListJSON(
			nodeItem("gpu-lab-control-plane-abc123", controlPlaneLabels, ""),
			nodeItem("gpu-lab-worker-a", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-01"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("gpu-lab-worker-b", strings.ReplaceAll(gpuWorkerLabels, "REPLACED", "gpu-node-02"), fmt.Sprintf(`{"cpu":"4","memory":"8Gi","%s":"8"}`, gpuResourceName)),
			nodeItem("rogue-gpu-node", `{"kubernetes.io/arch":"amd64"}`, capacity),
		)
	}()
	f.responders = healthyResponders(build)
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("classification drift is a warning: %v", err)
	}
	mustContain(t, output, "[warn] topology")
	mustContain(t, output, "rogue-gpu-node (reports 8 nvidia.com/gpu)")
	mustContain(t, output, "neither a control-plane role label nor the GPU worker teaching labels")
	mustNotContain(t, output, "[ready] topology")
}

func TestDoctorDefaultsToManualDiskGuidanceWithoutInjectedStatfs(t *testing.T) {
	// The default preflight must not statfs a daemon-reported root locally:
	// with a remote/VM daemon that path could point at an unrelated local
	// directory, so guidance falls back to a manual 'df -h' suggestion.
	var stdout strings.Builder
	env := preflightEnv{
		LookPath: func(name string) (string, error) { return "/usr/local/bin/" + name, nil },
		Output: func(_ context.Context, name string, args ...string) (string, error) {
			switch name {
			case "docker":
				if args[0] == "info" {
					return healthyDockerInfo, nil
				}
				return "28.3.3", nil
			case "kind":
				if args[0] == "get" {
					return "\n", nil
				}
				return "kind v0.30.0", nil
			case "helm":
				return "v3.18.4", nil
			case "kubectl":
				return "clientVersion: v1.34.0", nil
			}
			return "", fmt.Errorf("unexpected %s", name)
		},
		HostMemoryTotal: nil,
		DiskFree:        nil,
	}
	if err := runDoctor(context.Background(), &stdout, env); err != nil {
		t.Fatalf("doctor failed: %v", err)
	}
	output := stdout.String()
	mustContain(t, output, "[info] docker storage")
	mustContain(t, output, "manual check")
	mustContain(t, output, "df -h /var/lib/docker")
	mustNotContain(t, output, "measured free space")
}

func TestDoctorRejectsMalformedNodeProbe(t *testing.T) {
	for _, payload := range []string{"not json at all", `{"kind":"PodList","items":[]}`, `{"kind":"NodeList","items":[]}`} {
		f := newFakePreflight()
		f.responders = healthyResponders(payload)
		output, err := runDoctorWith(t, f)
		if err != nil {
			t.Fatalf("malformed node payload is a warning: %v", err)
		}
		mustContain(t, output, "[warn] topology")
	}
}

func TestDoctorOnlyUsesReadOnlyProbeVerbs(t *testing.T) {
	f := newFakePreflight()
	f.responders = healthyResponders(healthyGPUClusterJSON())
	output, err := runDoctorWith(t, f)
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, output)
	}
	for _, call := range f.calls {
		joined := call.joined()
		switch call.name {
		case "kubectl":
			if !strings.HasPrefix(joined, "kubectl --context gpu-lab get nodes -o json") &&
				!strings.Contains(joined, "version --client=true") {
				t.Fatalf("unexpected kubectl probe: %s", joined)
			}
		case "kind":
			if call.args[0] != "get" && call.args[0] != "version" {
				t.Fatalf("non-read-only kind verb in probe: %s", joined)
			}
			if strings.Contains(joined, "export kubeconfig") {
				t.Fatalf("doctor must not export kubeconfig: %s", joined)
			}
		case "docker":
			if call.args[0] != "info" && call.args[0] != "version" {
				t.Fatalf("non-read-only docker verb in probe: %s", joined)
			}
		case "helm", "sysctl":
			// allowed probes only
			if call.name == "helm" && call.args[0] != "version" {
				t.Fatalf("unexpected helm probe: %s", joined)
			}
			if call.name == "sysctl" && !(call.args[0] == "-n" && call.args[1] == "hw.memsize") {
				t.Fatalf("unexpected sysctl probe: %s", joined)
			}
		default:
			t.Fatalf("unexpected probe binary %q in %s", call.name, joined)
		}
	}
}

func isNodeProbe(args []string) bool {
	joined := strings.Join(args, " ")
	return strings.Contains(joined, " get nodes ")
}
