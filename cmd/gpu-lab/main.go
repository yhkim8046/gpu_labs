package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/nvidiasmi"
	"github.com/gpu-lab/gpu-lab/internal/rdmacompat"
	"github.com/gpu-lab/gpu-lab/internal/runner"
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

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	return 1
}
