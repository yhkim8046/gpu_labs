package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	deployassets "github.com/gpu-lab/gpu-lab/deploy"
	"github.com/gpu-lab/gpu-lab/internal/kubeconfig"
	"github.com/gpu-lab/gpu-lab/internal/monitoring"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/version"
)

const (
	ClusterName               = "gpu-lab"
	KubeContext               = kubeconfig.LabContext
	LocalImageName            = "gpu-lab:dev"
	ImageName                 = LocalImageName
	DefaultImageRepo          = "ghcr.io/yhkim8046/gpu-lab-runtime"
	DefaultComponentChartRepo = "oci://ghcr.io/yhkim8046/gpu-lab-charts"
	ImageSourceAuto           = "auto"
	ImageSourceLocal          = "local"
	ImageSourceRegistry       = "registry"
	MonitoringRelease         = "gpu-lab-monitoring"
	MonitoringNS              = "gpu-lab-monitoring"
	GrafanaService            = "gpu-lab-monitoring-grafana"
	DefaultChartVersion       = "87.21.0"
)

type Manager struct {
	Runner       runner.Runner
	Image        string
	ImageSource  string
	Chart        string
	ChartVersion string
	LabChartRepo string
	LabChartMode string
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
		LabChartRepo: strings.TrimRight(envOr("GPU_LAB_COMPONENT_CHART_REPOSITORY", DefaultComponentChartRepo), "/"),
		LabChartMode: envOr("GPU_LAB_COMPONENT_CHART_SOURCE", ImageSourceAuto),
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
	if err := m.require("docker", "kind", "kubectl"); err != nil {
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
	if err := m.loadImage(ctx, source); err != nil {
		return err
	}
	if _, err := m.Kubeconfig.Setup(ctx, ClusterName); err != nil {
		return fmt.Errorf("setup gpu-lab kubeconfig: %w", err)
	}
	if err := m.saveClusterConfig(ctx); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m"); err != nil {
		return err
	}
	return nil
}

// loadImage uses a platform-specific archive for registry images. Docker
// Desktop can keep a multi-platform OCI index locally, while kind's
// docker-image loader may try to import attestations for platforms that are
// not present in the local content store. Saving one platform avoids that
// containerd import failure on Apple Silicon and other non-amd64 hosts.
func (m Manager) loadImage(ctx context.Context, source string) error {
	if source != ImageSourceRegistry {
		return m.Runner.Run(ctx, "kind", "load", "docker-image", m.Image, "--name", ClusterName)
	}

	platform, err := hostImagePlatform()
	if err != nil {
		return err
	}
	archive, err := os.CreateTemp("", "gpu-lab-runtime-*.tar")
	if err != nil {
		return fmt.Errorf("create runtime image archive: %w", err)
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("close runtime image archive: %w", err)
	}
	defer os.Remove(archivePath)

	if err := m.Runner.Run(ctx, "docker", "image", "save", "--platform", platform, "--output", archivePath, m.Image); err != nil {
		return fmt.Errorf("save runtime image for %s: %w", platform, err)
	}
	if err := m.Runner.Run(ctx, "kind", "load", "image-archive", archivePath, "--name", ClusterName); err != nil {
		return fmt.Errorf("load runtime image archive for %s: %w", platform, err)
	}
	return nil
}

func hostImagePlatform() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return "linux/" + runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("unsupported host architecture %q; expected amd64 or arm64", runtime.GOARCH)
	}
}

// waitForMonitoring keeps the monitoring component install deterministic.
// Helm waits for the operator release, but the Prometheus CR, its StatefulSet,
// and the first ServiceMonitor scrape are reconciled asynchronously afterwards.
func (m Manager) waitForMonitoring(ctx context.Context) error {
	if err := m.Runner.Run(ctx, "kubectl", "--context", KubeContext, "wait", "--for=condition=Ready", "pod", "-l", "app.kubernetes.io/name=prometheus", "-n", MonitoringNS, "--timeout=5m"); err != nil {
		return fmt.Errorf("wait for Prometheus pod: %w", err)
	}
	exporterService, err := m.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "service/dcgm-exporter", "-n", "gpu-lab-system", "-o", "name", "--ignore-not-found=true")
	if err != nil {
		return fmt.Errorf("check DCGM Exporter service: %w", err)
	}
	if strings.TrimSpace(exporterService) == "" {
		return nil
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
		} else if len(result.Samples) == 0 {
			lastErr = fmt.Errorf("DCGM Exporter query returned no samples")
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
	}
	for _, args := range commands {
		full := append([]string{"--context", KubeContext}, args...)
		if err := m.Runner.Run(ctx, "kubectl", full...); err != nil {
			return err
		}
	}
	if runner.Exists("helm") {
		if err := m.Runner.Run(ctx, "helm", "--kube-context", KubeContext, "list", "--all-namespaces"); err != nil {
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

func (m Manager) DeleteAsset(ctx context.Context, asset string) error {
	data, err := deployassets.FS.ReadFile(asset)
	if err != nil {
		return err
	}
	data = renderAsset(data, m.Image)
	return m.Runner.RunInput(ctx, data, "kubectl", "--context", KubeContext, "delete", "-f", "-", "--ignore-not-found=true")
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

func (m Manager) saveClusterConfig(ctx context.Context) error {
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "gpu-lab-config",
			"namespace": "kube-system",
			"labels": map[string]string{
				"app.kubernetes.io/part-of":    "gpu-lab",
				"app.kubernetes.io/managed-by": "gpu-lab",
			},
		},
		"data": map[string]string{
			"runtime-image": m.Image,
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal gpu-lab cluster config: %w", err)
	}
	if err := m.ApplyJSON(ctx, data); err != nil {
		return fmt.Errorf("save gpu-lab cluster config: %w", err)
	}
	return nil
}

func (m Manager) clusterRuntimeImage(ctx context.Context) (string, error) {
	image, err := m.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "configmap/gpu-lab-config", "-n", "kube-system", "-o", "jsonpath={.data.runtime-image}")
	if err != nil {
		return "", fmt.Errorf("read gpu-lab cluster config: %w", err)
	}
	image = strings.TrimSpace(image)
	if image == "" {
		return "", fmt.Errorf("gpu-lab cluster config has no runtime image; run gpu create again")
	}
	return image, nil
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
	if helmUsesCluster(args) && !hasHelmContext(args) {
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
		args = append([]string{"--kube-context", KubeContext}, args...)
	}
	return m.Runner.Run(ctx, "helm", args...)
}

func helmUsesCluster(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "install", "upgrade", "uninstall", "delete", "list", "ls", "status", "get", "history", "rollback", "test":
			return true
		default:
			return false
		}
	}
	return false
}

func hasHelmContext(args []string) bool {
	for _, arg := range args {
		if arg == "--kube-context" || strings.HasPrefix(arg, "--kube-context=") || arg == "--kubeconfig" || strings.HasPrefix(arg, "--kubeconfig=") {
			return true
		}
	}
	return false
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
