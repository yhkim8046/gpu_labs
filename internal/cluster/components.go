package cluster

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	deployassets "github.com/gpu-lab/gpu-lab/deploy"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/version"
)

const (
	ComponentDevicePlugin = "nvidia-device-plugin"
	ComponentDCGMExporter = "dcgm-exporter"
	ComponentMonitoring   = "monitoring"
	SystemNamespace       = "gpu-lab-system"
)

type Component struct {
	Name        string
	Release     string
	Namespace   string
	Description string
}

var labComponents = []Component{
	{Name: ComponentDevicePlugin, Release: "nvidia-device-plugin", Namespace: SystemNamespace, Description: "synthetic nvidia.com/gpu extended resources"},
	{Name: ComponentDCGMExporter, Release: "dcgm-exporter", Namespace: SystemNamespace, Description: "synthetic DCGM metrics and scenario state"},
	{Name: ComponentMonitoring, Release: MonitoringRelease, Namespace: MonitoringNS, Description: "Prometheus, Grafana, alerts, and GPU dashboard"},
}

func Components() []Component {
	return append([]Component(nil), labComponents...)
}

func FindComponent(name string) (Component, bool) {
	if name == "kube-prometheus-stack" {
		name = ComponentMonitoring
	}
	for _, component := range labComponents {
		if component.Name == name {
			return component, true
		}
	}
	return Component{}, false
}

func (m Manager) InstallComponent(ctx context.Context, name string, extraArgs ...string) error {
	component, ok := FindComponent(name)
	if !ok {
		return fmt.Errorf("unknown GPU Lab Helm component %q", name)
	}
	if err := m.require("kind", "kubectl", "helm"); err != nil {
		return err
	}
	exists, err := m.Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("gpu-lab cluster does not exist; run gpu create first")
	}
	if _, err := m.Kubeconfig.Setup(ctx, ClusterName); err != nil {
		return fmt.Errorf("setup gpu-lab kubeconfig: %w", err)
	}
	image, err := m.clusterRuntimeImage(ctx)
	if err != nil {
		return err
	}
	m.Image = image

	switch component.Name {
	case ComponentDevicePlugin, ComponentDCGMExporter:
		chartPath, chartVersion, cleanup, err := m.componentChart(component.Name)
		if err != nil {
			return err
		}
		defer cleanup()
		args := []string{"--kube-context", KubeContext, "install", component.Release, chartPath, "--namespace", component.Namespace, "--create-namespace", "--wait", "--timeout", "5m", "--set-string", "image=" + m.Image}
		if chartVersion != "" {
			args = append(args, "--version", chartVersion)
		}
		args = append(args, extraArgs...)
		return m.Runner.Run(ctx, "helm", args...)
	case ComponentMonitoring:
		valuesPath, cleanup, err := m.tempAsset("monitoring/values.yaml")
		if err != nil {
			return err
		}
		defer cleanup()
		args := []string{"--kube-context", KubeContext, "install", component.Release, m.Chart, "--namespace", component.Namespace, "--create-namespace", "--values", valuesPath, "--wait", "--timeout", "10m"}
		if m.ChartVersion != "" {
			args = append(args, "--version", m.ChartVersion)
		}
		args = append(args, extraArgs...)
		if err := m.Runner.Run(ctx, "helm", args...); err != nil {
			return err
		}
		for _, asset := range []string{"monitoring/servicemonitor.yaml", "monitoring/alerts.yaml", "monitoring/dashboard-configmap.yaml"} {
			if err := m.ApplyAsset(ctx, asset); err != nil {
				return fmt.Errorf("apply %s: %w", asset, err)
			}
		}
		return m.waitForMonitoring(ctx)
	default:
		return fmt.Errorf("GPU Lab Helm component %q has no installer", component.Name)
	}
}

func (m Manager) componentChart(name string) (string, string, func(), error) {
	mode := m.LabChartMode
	if mode == "" || mode == ImageSourceAuto {
		if version.Version == "dev" {
			mode = "embedded"
		} else {
			mode = ImageSourceRegistry
		}
	}
	switch mode {
	case "embedded", ImageSourceLocal:
		path, cleanup, err := m.tempAssetDir("charts/" + name)
		return path, "", cleanup, err
	case ImageSourceRegistry:
		chartVersion := strings.TrimPrefix(version.Version, "v")
		if chartVersion == "" || chartVersion == "dev" {
			return "", "", nil, fmt.Errorf("registry component charts require a release version")
		}
		return m.LabChartRepo + "/" + name, chartVersion, func() {}, nil
	default:
		return "", "", nil, fmt.Errorf("invalid GPU_LAB_COMPONENT_CHART_SOURCE %q; expected auto, embedded, or registry", m.LabChartMode)
	}
}

func (m Manager) InstallAll(ctx context.Context) error {
	for _, component := range labComponents {
		if err := m.InstallComponent(ctx, component.Name); err != nil {
			return fmt.Errorf("install %s: %w", component.Name, err)
		}
	}
	return nil
}

func (m Manager) UninstallComponent(ctx context.Context, name string, extraArgs ...string) error {
	component, ok := FindComponent(name)
	if !ok {
		return fmt.Errorf("unknown GPU Lab Helm component %q", name)
	}
	if !runner.Exists("helm") {
		return fmt.Errorf("helm is not installed; install the official Helm CLI, then retry")
	}
	if component.Name == ComponentMonitoring {
		for _, asset := range []string{"monitoring/servicemonitor.yaml", "monitoring/alerts.yaml", "monitoring/dashboard-configmap.yaml"} {
			if err := m.DeleteAsset(ctx, asset); err != nil {
				return fmt.Errorf("delete %s: %w", asset, err)
			}
		}
	}
	args := []string{"--kube-context", KubeContext, "uninstall", component.Release, "--namespace", component.Namespace}
	args = append(args, extraArgs...)
	return m.Runner.Run(ctx, "helm", args...)
}

func (m Manager) tempAssetDir(assetDir string) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "gpu-lab-chart-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	err = fs.WalkDir(deployassets.FS, assetDir, func(assetPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(assetDir, assetPath)
		if err != nil {
			return err
		}
		if relativePath == "." {
			return nil
		}
		destination := filepath.Join(tempDir, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := deployassets.FS.ReadFile(assetPath)
		if err != nil {
			return err
		}
		data = renderAsset(data, m.Image)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o600)
	})
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("materialize embedded chart %s: %w", assetDir, err)
	}
	return filepath.Clean(tempDir), cleanup, nil
}
