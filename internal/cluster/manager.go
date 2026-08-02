package cluster

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	deployassets "github.com/gpu-lab/gpu-lab/deploy"
	"github.com/gpu-lab/gpu-lab/internal/kubeconfig"
	"github.com/gpu-lab/gpu-lab/internal/monitoring"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
	"github.com/gpu-lab/gpu-lab/internal/version"
)

const (
	ClusterName         = "gpu-lab"
	KubeContext         = kubeconfig.LabContext
	LocalImageName      = "gpu-lab:dev"
	ImageName           = LocalImageName
	DefaultImageRepo    = "ghcr.io/yhkim8046/gpu-lab-runtime"
	ImageSourceAuto     = "auto"
	ImageSourceLocal    = "local"
	ImageSourceRegistry = "registry"
	MonitoringRelease   = "gpu-lab-monitoring"
	MonitoringNS        = "gpu-lab-monitoring"
	GrafanaService      = "gpu-lab-monitoring-grafana"
	DefaultChartVersion = "87.21.0"
)

type Manager struct {
	Runner       runner.Runner
	Image        string
	ImageSource  string
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
	imageSource := envOr("GPU_LAB_IMAGE_SOURCE", ImageSourceAuto)
	image := os.Getenv("GPU_LAB_IMAGE")
	if image == "" {
		if imageSource == ImageSourceLocal || (imageSource == ImageSourceAuto && version.Version == "dev") {
			image = LocalImageName
		} else {
			image = RuntimeImageForVersion()
		}
	}
	return Manager{
		Runner:       r,
		Image:        image,
		ImageSource:  imageSource,
		Chart:        chart,
		ChartVersion: envOr("GPU_LAB_HELM_CHART_VERSION", DefaultChartVersion),
		Workdir:      ".",
		Kubeconfig:   kubeconfig.New(r),
	}
}

func RuntimeImageForVersion() string {
	repository := envOr("GPU_LAB_RUNTIME_IMAGE_REPOSITORY", DefaultImageRepo)
	tag := strings.TrimPrefix(version.Version, "v")
	if tag == "" {
		tag = "dev"
	}
	return fmt.Sprintf("%s:%s", strings.TrimRight(repository, "/"), tag)
}

func (m Manager) Create(ctx context.Context) error {
	if err := m.require("docker", "kind", "kubectl", "helm"); err != nil {
		return err
	}
	source, err := m.resolveImageSource()
	if err != nil {
		return err
	}
	if source == ImageSourceLocal {
		if err := m.Runner.Run(ctx, "docker", "build", "-t", m.Image, m.Workdir); err != nil {
			return err
		}
	} else {
		if err := m.Runner.Run(ctx, "docker", "pull", m.Image); err != nil {
			return fmt.Errorf("pull runtime image %s: %w", m.Image, err)
		}
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
	if err := m.RemoveLegacyComponents(ctx); err != nil {
		return err
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
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "rollout", "restart", "daemonset/nvidia-device-plugin", "daemonset/dcgm-exporter", "-n", "gpu-lab-system"); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "rollout", "status", "daemonset/nvidia-device-plugin", "-n", "gpu-lab-system", "--timeout=5m"); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "rollout", "status", "daemonset/dcgm-exporter", "-n", "gpu-lab-system", "--timeout=5m"); err != nil {
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
	if err := m.waitForMonitoring(ctx); err != nil {
		return err
	}
	return nil
}

// waitForMonitoring keeps create deterministic on fresh clusters. Helm waits
// for the operator release, but the Prometheus CR, its StatefulSet, and the
// first ServiceMonitor scrape are reconciled asynchronously afterwards.
func (m Manager) waitForMonitoring(ctx context.Context) error {
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "wait", "--for=condition=Ready", "pod", "-l", "app.kubernetes.io/name=prometheus", "-n", MonitoringNS, "--timeout=5m"); err != nil {
		return fmt.Errorf("wait for Prometheus pod: %w", err)
	}

	client := monitoring.New(m.Runner)
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		result, err := client.Query(ctx, `sum(up{service="dcgm-exporter"})`)
		if err == nil && len(result.Samples) > 0 && result.Samples[0].Value == 3 {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("expected 3 dcgm-exporter targets, got %s", formatMonitoringValue(result.Samples[0].Value))
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for initial DCGM Exporter scrape: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("wait for initial DCGM Exporter scrape: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func formatMonitoringValue(value float64) string {
	return fmt.Sprintf("%g", value)
}

func (m Manager) RemoveLegacyComponents(ctx context.Context) error {
	legacySystemResources := []string{
		"daemonset/fake-gpu-device-plugin",
		"daemonset/mock-gpu-exporter",
		"service/mock-gpu-exporter",
		"serviceaccount/mock-gpu-exporter",
		"role/mock-gpu-exporter",
		"rolebinding/mock-gpu-exporter",
	}
	args := append([]string{"--context", KubeContext, "delete"}, legacySystemResources...)
	args = append(args, "-n", "gpu-lab-system", "--ignore-not-found=true")
	if err := m.Runner.Run(ctx, "kubectl", args...); err != nil {
		return fmt.Errorf("remove legacy gpu component resources: %w", err)
	}
	// A new kind cluster does not have the Prometheus Operator CRD yet.
	// `--ignore-not-found` does not suppress kubectl's "server doesn't have a
	// resource type" error, so discover the CRD before deleting the legacy
	// object. Existing clusters still get the cleanup.
	crd, err := m.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "crd/servicemonitors.monitoring.coreos.com", "-o", "name", "--ignore-not-found=true")
	if err != nil {
		return fmt.Errorf("check legacy ServiceMonitor CRD: %w", err)
	}
	if strings.TrimSpace(crd) == "" {
		return nil
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "delete", "servicemonitor/mock-gpu-exporter", "-n", MonitoringNS, "--ignore-not-found=true"); err != nil {
		return fmt.Errorf("remove legacy mock exporter ServiceMonitor: %w", err)
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
	data = renderAsset(data, m.Image)
	return m.Runner.RunInput(ctx, data, "kubectl", "--context", KubeContext, "apply", "-f", "-")
}

func renderAsset(data []byte, image string) []byte {
	return bytes.ReplaceAll(data, []byte(LocalImageName), []byte(image))
}

func (m Manager) resolveImageSource() (string, error) {
	switch m.ImageSource {
	case "", ImageSourceAuto:
		if m.Image == LocalImageName && version.Version == "dev" {
			return ImageSourceLocal, nil
		}
		return ImageSourceRegistry, nil
	case ImageSourceLocal, ImageSourceRegistry:
		return m.ImageSource, nil
	default:
		return "", fmt.Errorf("invalid GPU_LAB_IMAGE_SOURCE %q; expected auto, local, or registry", m.ImageSource)
	}
}

func (m Manager) ApplyJSON(ctx context.Context, data []byte) error {
	return m.Runner.RunInput(ctx, data, "kubectl", "--context", KubeContext, "apply", "-f", "-")
}

func (m Manager) DeleteScenarioPods(ctx context.Context) error {
	return m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "delete", "pod", "-n", "gpu-lab-demo", "-l", "app.kubernetes.io/managed-by=gpu-lab,gpu-lab/scenario", "--ignore-not-found=true")
}

func (m Manager) WaitForScenarioPod(ctx context.Context, name string) error {
	return m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "wait", "--for=condition=Ready", "pod/"+name, "-n", "gpu-lab-demo", "--timeout=2m")
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
