package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	deployassets "github.com/gpu-lab/gpu-lab/deploy"
	"github.com/gpu-lab/gpu-lab/internal/kubeconfig"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

const (
	ClusterName         = "gpu-lab"
	KubeContext         = kubeconfig.LabContext
	ImageName           = "gpu-lab:dev"
	MonitoringRelease   = "gpu-lab-monitoring"
	MonitoringNS        = "gpu-lab-monitoring"
	DefaultChartVersion = "87.21.0"
)

type Manager struct {
	Runner       runner.Runner
	Image        string
	Chart        string
	ChartVersion string
	Workdir      string
	Kubeconfig   kubeconfig.Manager
}

func NewManager(r runner.Runner) Manager {
	chart := os.Getenv("GPU_LAB_HELM_CHART")
	if chart == "" {
		chart = "oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack"
	}
	return Manager{
		Runner:       r,
		Image:        ImageName,
		Chart:        chart,
		ChartVersion: envOr("GPU_LAB_HELM_CHART_VERSION", DefaultChartVersion),
		Workdir:      ".",
		Kubeconfig:   kubeconfig.New(r),
	}
}

func (m Manager) Create(ctx context.Context) error {
	if err := m.require("docker", "kind", "kubectl", "helm"); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "docker", "build", "-t", m.Image, m.Workdir); err != nil {
		return err
	}
	exists, err := m.Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		configPath, cleanup, err := m.tempAsset("kind/cluster.yaml")
		if err != nil {
			return err
		}
		defer cleanup()
		if err := m.Runner.Run(ctx, "kind", "create", "cluster", "--name", ClusterName, "--config", configPath); err != nil {
			return err
		}
	}
	if err := m.Runner.Run(ctx, "kind", "load", "docker-image", m.Image, "--name", ClusterName); err != nil {
		return err
	}
	if _, err := m.Kubeconfig.Setup(ctx, ClusterName); err != nil {
		return fmt.Errorf("setup gpu-lab kubeconfig: %w", err)
	}
	for _, asset := range []string{"device-plugin/device-plugin.yaml", "exporter/exporter.yaml", "demo/namespace.yaml"} {
		if err := m.ApplyAsset(ctx, asset); err != nil {
			return fmt.Errorf("apply %s: %w", asset, err)
		}
	}
	normal, err := scenario.LoadBuiltin("normal")
	if err != nil {
		return err
	}
	normalData, err := scenario.ConfigMapJSON(normal, "0")
	if err != nil {
		return err
	}
	if err := m.ApplyJSON(ctx, normalData); err != nil {
		return fmt.Errorf("seed normal scenario: %w", err)
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m"); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "rollout", "status", "daemonset/fake-gpu-device-plugin", "-n", "gpu-lab-system", "--timeout=5m"); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "rollout", "status", "daemonset/mock-gpu-exporter", "-n", "gpu-lab-system", "--timeout=5m"); err != nil {
		return err
	}
	valuesPath, cleanup, err := m.tempAsset("monitoring/values.yaml")
	if err != nil {
		return err
	}
	defer cleanup()
	helmArgs := []string{"--kube-context", KubeContext, "upgrade", "--install", MonitoringRelease, m.Chart, "--namespace", MonitoringNS, "--create-namespace", "--values", valuesPath, "--wait", "--timeout", "10m"}
	if m.ChartVersion != "" {
		helmArgs = append(helmArgs, "--version", m.ChartVersion)
	}
	if err := m.Runner.Run(ctx, "helm", helmArgs...); err != nil {
		return err
	}
	for _, asset := range []string{"monitoring/servicemonitor.yaml", "monitoring/alerts.yaml", "monitoring/dashboard-configmap.yaml"} {
		if err := m.ApplyAsset(ctx, asset); err != nil {
			return fmt.Errorf("apply %s: %w", asset, err)
		}
	}
	return nil
}

func (m Manager) Destroy(ctx context.Context) error {
	if !runner.Exists("kind") {
		return fmt.Errorf("kind is not installed")
	}
	return m.Runner.Run(ctx, "kind", "delete", "cluster", "--name", ClusterName)
}

func (m Manager) Exists(ctx context.Context) (bool, error) {
	if !runner.Exists("kind") {
		return false, fmt.Errorf("kind is not installed")
	}
	out, err := m.Runner.Output(ctx, "kind", "get", "clusters")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == ClusterName {
			return true, nil
		}
	}
	return false, nil
}

func (m Manager) Status(ctx context.Context) error {
	if _, err := m.Kubeconfig.Setup(ctx, ClusterName); err != nil {
		return fmt.Errorf("setup gpu-lab kubeconfig: %w", err)
	}
	commands := [][]string{
		{"get", "nodes", "-o", "custom-columns=NAME:.metadata.name,GPU-CAPACITY:.status.capacity.nvidia\\.com/gpu,GPU-ALLOCATABLE:.status.allocatable.nvidia\\.com/gpu,STATUS:.status.conditions[-1].type"},
		{"get", "pods", "-A", "-o", "wide"},
		{"get", "configmap", "gpu-lab-scenario", "-n", "gpu-lab-system", "-o", "jsonpath={.data.scenario\\.yaml}"},
	}
	for _, args := range commands {
		full := append([]string{"--context", KubeContext}, args...)
		if err := m.Runner.Run(ctx, "kubectl", full...); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) ApplyAsset(ctx context.Context, asset string) error {
	data, err := deployassets.FS.ReadFile(asset)
	if err != nil {
		return err
	}
	return m.Runner.RunInput(ctx, data, "kubectl", "--context", KubeContext, "apply", "-f", "-")
}

func (m Manager) ApplyJSON(ctx context.Context, data []byte) error {
	return m.Runner.RunInput(ctx, data, "kubectl", "--context", KubeContext, "apply", "-f", "-")
}

func (m Manager) DeleteScenarioPods(ctx context.Context) error {
	return m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "delete", "pod", "-n", "gpu-lab-demo", "-l", "app.kubernetes.io/managed-by=gpu-lab,gpu-lab/scenario", "--ignore-not-found=true")
}

func (m Manager) SetupContext(ctx context.Context) (string, error) {
	return m.Kubeconfig.Setup(ctx, ClusterName)
}

func (m Manager) UseContext(ctx context.Context, name string) error {
	return m.Kubeconfig.Use(ctx, name)
}

func (m Manager) CurrentContext(ctx context.Context) (string, error) {
	return m.Kubeconfig.Current(ctx)
}

func (m Manager) Contexts(ctx context.Context) ([]string, error) {
	return m.Kubeconfig.List(ctx)
}

func (m Manager) DedicatedKubeconfigPath() (string, error) {
	return m.Kubeconfig.DedicatedPath()
}

func (m Manager) Helm(ctx context.Context, args ...string) error {
	if !runner.Exists("helm") {
		return fmt.Errorf("helm is not installed; install the official Helm CLI, then retry")
	}
	return m.Runner.Run(ctx, "helm", args...)
}

func (m Manager) require(names ...string) error {
	var missing []string
	for _, name := range names {
		if !runner.Exists(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required tools: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (m Manager) tempAsset(asset string) (string, func(), error) {
	data, err := deployassets.FS.ReadFile(asset)
	if err != nil {
		return "", nil, err
	}
	file, err := os.CreateTemp("", "gpu-lab-*")
	if err != nil {
		return "", nil, err
	}
	name := file.Name()
	cleanup := func() {
		_ = os.Remove(name)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Clean(name), cleanup, nil
}

func DefaultTimeout() time.Duration { return 15 * time.Minute }

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
