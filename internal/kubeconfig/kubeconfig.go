package kubeconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

const (
	KindContext = "kind-gpu-lab"
	LabContext  = "gpu-lab"
)

type Manager struct {
	Runner runner.Runner
}

func New(r runner.Runner) Manager { return Manager{Runner: r} }

func (m Manager) DedicatedPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube", "gpu-lab.config"), nil
}

// Setup exports the kind credentials to a dedicated kubeconfig and creates a
// gpu-lab alias in the user's primary kubeconfig without changing the user's
// current context.
func (m Manager) Setup(ctx context.Context, clusterName string) (string, error) {
	if !runner.Exists("kind") || !runner.Exists("kubectl") {
		return "", fmt.Errorf("kind and kubectl are required to setup the gpu-lab kubeconfig")
	}
	current, err := m.Runner.Output(ctx, "kubectl", "config", "current-context")
	if err != nil {
		return "", err
	}
	current = strings.TrimSpace(current)
	if err := m.Runner.Run(ctx, "kind", "export", "kubeconfig", "--name", clusterName); err != nil {
		return "", err
	}
	if err := m.Runner.Run(ctx, "kubectl", "config", "set-context", LabContext, "--cluster", KindContext, "--user", KindContext); err != nil {
		return "", err
	}
	if current != "" {
		if err := m.Runner.Run(ctx, "kubectl", "config", "use-context", current); err != nil {
			return "", err
		}
	}

	path, err := m.DedicatedPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := m.Runner.Run(ctx, "kind", "export", "kubeconfig", "--name", clusterName, "--kubeconfig", path); err != nil {
		return "", err
	}
	contexts, err := m.contexts(ctx, "--kubeconfig", path)
	if err != nil {
		return "", err
	}
	if contains(contexts, KindContext) && !contains(contexts, LabContext) {
		if err := m.Runner.Run(ctx, "kubectl", "--kubeconfig", path, "config", "rename-context", KindContext, LabContext); err != nil {
			return "", err
		}
	} else if contains(contexts, KindContext) && contains(contexts, LabContext) {
		if err := m.Runner.Run(ctx, "kubectl", "--kubeconfig", path, "config", "delete-context", KindContext); err != nil {
			return "", err
		}
	}
	if err := m.Runner.Run(ctx, "kubectl", "--kubeconfig", path, "config", "use-context", LabContext); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

func (m Manager) Use(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("context name cannot be empty")
	}
	contexts, err := m.contexts(ctx)
	if err != nil {
		return err
	}
	if !contains(contexts, name) {
		if name == LabContext {
			if _, err := m.Setup(ctx, "gpu-lab"); err != nil {
				return err
			}
			contexts, err = m.contexts(ctx)
			if err != nil {
				return err
			}
		}
	}
	if !contains(contexts, name) {
		return fmt.Errorf("context %q does not exist; run gpu-lab context list", name)
	}
	return m.Runner.Run(ctx, "kubectl", "config", "use-context", name)
}

func (m Manager) Current(ctx context.Context) (string, error) {
	out, err := m.Runner.Output(ctx, "kubectl", "config", "current-context")
	return strings.TrimSpace(out), err
}

func (m Manager) List(ctx context.Context) ([]string, error) {
	return m.contexts(ctx)
}

func (m Manager) contexts(ctx context.Context, args ...string) ([]string, error) {
	args = append(args, "config", "get-contexts", "-o", "name")
	out, err := m.Runner.Output(ctx, "kubectl", args...)
	if err != nil {
		return nil, err
	}
	var contexts []string
	for _, line := range strings.Split(out, "\n") {
		if value := strings.TrimSpace(line); value != "" {
			contexts = append(contexts, value)
		}
	}
	return contexts, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
