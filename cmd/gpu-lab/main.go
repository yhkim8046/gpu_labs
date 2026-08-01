package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gpu-lab:", err)
		os.Exit(exitCode(err))
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	r := runner.New(stdout, stderr)
	m := cluster.NewManager(r)
	switch args[0] {
	case "helm":
		if len(args) == 1 {
			return errors.New("usage: gpu-lab helm <official-helm-args...>")
		}
		return m.Helm(ctx, args[1:]...)
	case "create":
		createCtx, cancel := context.WithTimeout(ctx, cluster.DefaultTimeout())
		defer cancel()
		if err := m.Create(createCtx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "gpu-lab cluster is ready")
		return nil
	case "destroy":
		return m.Destroy(ctx)
	case "doctor":
		return doctor(ctx, r, stdout)
	case "status":
		return m.Status(ctx)
	case "reset":
		return reset(ctx, m, stdout)
	case "scenario":
		return scenarioCommand(ctx, m, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q; run gpu-lab help", args[0])
	}
}

func scenarioCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: gpu-lab scenario list|run <name>|reset")
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
			return errors.New("usage: gpu-lab scenario run <name> [--file path]")
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
	default:
		return fmt.Errorf("unknown scenario command %q", args[0])
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
	if action, ok := scenario.HasAction(selected, "create_pending_workload"); ok {
		workload, err := scenario.PendingWorkloadJSON(selected, action.GPUCount)
		if err != nil {
			return err
		}
		if err := m.ApplyJSON(ctx, workload); err != nil {
			return err
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
		return fmt.Errorf("install missing tools before running gpu-lab create: %s", strings.Join(missing, ", "))
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
	_, _ = io.WriteString(w, `gpu-lab — Kubernetes GPU infrastructure lab

Usage:
  gpu-lab create
  gpu-lab destroy
  gpu-lab reset
  gpu-lab doctor
  gpu-lab status
  gpu-lab scenario list
  gpu-lab scenario run <name>
  gpu-lab scenario reset
  gpu-lab helm <official-helm-args...>

Examples:
  gpu-lab create
  gpu-lab scenario run xid-79
  gpu-lab helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
`)
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	return 1
}
