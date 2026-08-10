package trainingjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

const (
	Name      = "gpu-lab-training"
	Control   = "gpu-lab-training-control"
	Namespace = "gpu-lab-demo"
	ManagedBy = "gpu-lab"
)

type Options struct {
	Workers   int
	Image     string
	Namespace string
	Wait      bool
	Timeout   time.Duration
}

func (o Options) normalize() (Options, error) {
	if o.Workers == 0 {
		o.Workers = 3
	}
	if o.Workers < 1 {
		return Options{}, errors.New("workers must be at least 1")
	}
	if strings.TrimSpace(o.Image) == "" {
		o.Image = "gpu-lab:dev"
	}
	if strings.TrimSpace(o.Namespace) == "" {
		o.Namespace = Namespace
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Timeout <= 0 {
		return Options{}, errors.New("timeout must be positive")
	}
	return o, nil
}

type ControlState struct {
	Generation       int64  `json:"generation"`
	StragglerRank    *int   `json:"straggler_rank,omitempty"`
	StragglerDelay   string `json:"straggler_delay,omitempty"`
	CrashRank        *int   `json:"crash_rank,omitempty"`
	CrashToken       string `json:"crash_token,omitempty"`
	FabricMode       string `json:"fabric_mode,omitempty"`
	FabricTargetNode string `json:"fabric_target_node,omitempty"`
	FabricDelay      string `json:"fabric_delay,omitempty"`
}

// FabricControl is the small, flat control-plane contract shared by scenario
// orchestration and the synthetic training worker. The mode is deliberately a
// string so the ConfigMap remains easy to inspect and edit during a lab.
type FabricControl struct {
	Mode       string
	TargetNode string
	Delay      string
}

// FabricControlForScenario maps the built-in fabric incidents to the
// synthetic node IDs used by the exporter. Non-fabric scenarios intentionally
// return a neutral control so applying a normal GPU scenario also clears a
// previous fabric fault from the training workload.
func FabricControlForScenario(name string) FabricControl {
	switch strings.TrimSpace(name) {
	case "ib-link-down":
		return FabricControl{Mode: "down", TargetNode: "gpu-node-02"}
	case "ib-rate-degraded":
		return FabricControl{Mode: "degraded", TargetNode: "gpu-node-03", Delay: "1.5s"}
	case "ib-symbol-errors":
		return FabricControl{Mode: "errors", TargetNode: "gpu-node-01"}
	case "rdma-retry-storm":
		return FabricControl{Mode: "retries", TargetNode: "gpu-node-02", Delay: "1s"}
	case "ib-congestion":
		return FabricControl{Mode: "congestion", TargetNode: "gpu-node-03", Delay: "800ms"}
	default:
		return FabricControl{Mode: "normal"}
	}
}

func (c FabricControl) normalized() FabricControl {
	c.Mode = strings.TrimSpace(strings.ToLower(c.Mode))
	if c.Mode == "" {
		c.Mode = "normal"
	}
	if c.Mode == "normal" {
		c.TargetNode = ""
		c.Delay = ""
	}
	return c
}

// ApplyFabricControl updates only fabric fields. Existing straggler and crash
// controls are intentionally preserved when scenarios are synchronized.
func ApplyFabricControl(state *ControlState, control FabricControl) {
	if state == nil {
		return
	}
	control = control.normalized()
	state.FabricMode = control.Mode
	state.FabricTargetNode = strings.TrimSpace(control.TargetNode)
	state.FabricDelay = strings.TrimSpace(control.Delay)
}

func (c ControlState) JSON() ([]byte, error) {
	// Older training ConfigMaps may omit the field. Normalize the serialized
	// representation so status and worker logs always expose an explicit mode.
	c.FabricMode = strings.ToLower(strings.TrimSpace(c.FabricMode))
	if c.FabricMode == "" {
		c.FabricMode = "normal"
	}
	if strings.EqualFold(c.FabricMode, "normal") {
		c.FabricTargetNode = ""
		c.FabricDelay = ""
	}
	return json.MarshalIndent(c, "", "  ")
}

func Manifest(o Options, control ControlState) ([]byte, error) {
	o, err := o.normalize()
	if err != nil {
		return nil, err
	}
	controlData, err := control.JSON()
	if err != nil {
		return nil, err
	}
	labels := map[string]any{"app.kubernetes.io/name": Name, "app.kubernetes.io/part-of": "gpu-lab-training", "app.kubernetes.io/managed-by": ManagedBy, "gpu-lab/component": "distributed-training"}
	selector := map[string]any{"app.kubernetes.io/name": Name, "app.kubernetes.io/managed-by": ManagedBy}
	resources := []any{map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": o.Namespace}},
		map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": Control, "namespace": o.Namespace, "labels": labels}, "data": map[string]string{"control.json": string(controlData)}},
		map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": Name, "namespace": o.Namespace, "labels": labels}, "spec": map[string]any{"clusterIP": "None", "publishNotReadyAddresses": true, "selector": selector, "ports": []any{map[string]any{"name": "coordinator", "port": 8080, "targetPort": 8080}, map[string]any{"name": "metrics", "port": 9401, "targetPort": 9401}}}},
		statefulSet(o, labels, selector),
	}
	return json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": resources})
}

func statefulSet(o Options, labels, selector map[string]any) map[string]any {
	return map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": Name, "namespace": o.Namespace, "labels": labels}, "spec": map[string]any{
		"serviceName": Name, "replicas": o.Workers, "podManagementPolicy": "Parallel", "selector": map[string]any{"matchLabels": selector}, "template": map[string]any{"metadata": map[string]any{"labels": selector}, "spec": map[string]any{
			"terminationGracePeriodSeconds": 10, "topologySpreadConstraints": []any{map[string]any{"maxSkew": 1, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "DoNotSchedule", "labelSelector": map[string]any{"matchLabels": selector}}}, "containers": []any{map[string]any{"name": "training-worker", "image": o.Image, "imagePullPolicy": "IfNotPresent", "command": []string{"/usr/local/bin/training-worker"}, "ports": []any{map[string]any{"name": "coordinator", "containerPort": 8080}, map[string]any{"name": "metrics", "containerPort": 9401}}, "env": []any{
				map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}, map[string]any{"name": "NODE_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.nodeName"}}}, map[string]any{"name": "WORLD_SIZE", "value": strconv.Itoa(o.Workers)}, map[string]any{"name": "JOB_NAME", "value": Name}, map[string]any{"name": "COORDINATOR_ADDR", "value": Name + "-0." + Name + "." + o.Namespace + ".svc:8080"}, map[string]any{"name": "CHECKPOINT_INTERVAL", "value": "2s"}}, "resources": map[string]any{"limits": map[string]any{"nvidia.com/gpu": "1"}, "requests": map[string]any{"nvidia.com/gpu": "1"}}, "volumeMounts": []any{map[string]any{"name": "control", "mountPath": "/etc/gpu-lab-training"}, map[string]any{"name": "checkpoint", "mountPath": "/var/lib/gpu-lab-training"}}, "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/readyz", "port": 9401}, "periodSeconds": 5, "failureThreshold": 6}, "livenessProbe": map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": 9401}, "periodSeconds": 10, "failureThreshold": 6}}}, "volumes": []any{map[string]any{"name": "control", "configMap": map[string]any{"name": Control}}, map[string]any{"name": "checkpoint", "emptyDir": map[string]any{}}},
		}},
	}}
}

type Client struct {
	Runner  runner.Runner
	Context string
}

func NewClient(r runner.Runner) Client { return Client{Runner: r, Context: cluster.KubeContext} }
func (c Client) kubectl(ctx context.Context, args ...string) []string {
	return append([]string{"--context", c.Context}, args...)
}
func (c Client) Apply(ctx context.Context, o Options, state ControlState) error {
	data, err := Manifest(o, state)
	if err != nil {
		return err
	}
	return c.Runner.RunInput(ctx, data, "kubectl", c.kubectl(ctx, "apply", "-f", "-")...)
}
func (c Client) Wait(ctx context.Context, o Options) error {
	o, err := o.normalize()
	if err != nil {
		return err
	}
	return c.Runner.Run(ctx, "kubectl", c.kubectl(ctx, "-n", o.Namespace, "rollout", "status", "statefulset/"+Name, "--timeout="+o.Timeout.String())...)
}

func (c Client) ReadControl(ctx context.Context, namespace string) (ControlState, error) {
	out, err := c.Runner.Output(ctx, "kubectl", c.kubectl(ctx, "-n", namespace, "get", "configmap/"+Control, "-o", "jsonpath={.data.control\\.json}")...)
	if err != nil {
		return ControlState{}, err
	}
	var state ControlState
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return ControlState{}, fmt.Errorf("decode training control: %w", err)
	}
	return state, nil
}
func (c Client) WriteControl(ctx context.Context, namespace string, state ControlState) error {
	data, err := state.JSON()
	if err != nil {
		return err
	}
	patch := map[string]any{"data": map[string]string{"control.json": string(data)}}
	raw, _ := json.Marshal(patch)
	return c.Runner.RunInput(ctx, raw, "kubectl", c.kubectl(ctx, "-n", namespace, "patch", "configmap/"+Control, "--type=merge", "-p", string(raw))...)
}
func (c Client) Mutate(ctx context.Context, namespace string, mutate func(*ControlState) error) error {
	state, err := c.ReadControl(ctx, namespace)
	if err != nil {
		return err
	}
	if err := mutate(&state); err != nil {
		return err
	}
	state.Generation++
	return c.WriteControl(ctx, namespace, state)
}

// SyncScenarioControl best-effort callers can use this to couple a GPU
// scenario to the training workload without changing any existing fault
// controls. Mutate increments the ConfigMap generation exactly once.
func (c Client) SyncScenarioControl(ctx context.Context, namespace, scenarioName string) error {
	control := FabricControlForScenario(scenarioName)
	return c.Mutate(ctx, namespace, func(state *ControlState) error {
		ApplyFabricControl(state, control)
		return nil
	})
}
func (c Client) Logs(ctx context.Context, namespace string, rank int, follow bool, out io.Writer) error {
	if rank < 0 {
		return errors.New("rank must be non-negative")
	}
	pod := Name + "-" + strconv.Itoa(rank)
	args := c.kubectl(ctx, "-n", namespace, "logs", pod, "-c", "training-worker")
	if follow {
		args = append(args, "--follow")
	}
	return c.Runner.Run(ctx, "kubectl", args...)
}
func (c Client) Reset(ctx context.Context, namespace string) error {
	if strings.TrimSpace(namespace) == "" {
		return errors.New("namespace is required")
	}
	selector := "app.kubernetes.io/name=" + Name + ",app.kubernetes.io/managed-by=" + ManagedBy
	return c.Runner.Run(ctx, "kubectl", c.kubectl(ctx, "-n", namespace, "delete", "statefulset,service,configmap", "--selector", selector, "--ignore-not-found=true")...)
}
