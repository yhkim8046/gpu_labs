package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gpu-lab/gpu-lab/internal/inspection"
	"github.com/gpu-lab/gpu-lab/internal/nvidiasmi"
	"github.com/gpu-lab/gpu-lab/internal/rdmacompat"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func nativeCommand(ctx context.Context, r runner.Runner, command string, args []string, mode inspection.Mode, stdout, stderr io.Writer) error {
	selection, err := inspection.SelectNative(ctx, command, mode, inspection.DefaultDependencies())
	if err != nil {
		writeNativeNotice(stderr, selection.Mode)
		return err
	}
	if selection.Mode == inspection.ModeReal {
		writeNativeNotice(stderr, selection.Mode)
		return inspection.ForwardNative(ctx, selection.Path, args, stdout, stderr)
	}

	if command != "nvidia-smi" {
		writeNativeNotice(stderr, selection.Mode)
	}
	switch command {
	case "nvidia-smi":
		return nvidiasmi.Run(ctx, r, args, stdout)
	case "ibstat":
		return rdmacompat.Run(ctx, r, rdmacompat.IBStat, args, stdout)
	case "ibstatus":
		return rdmacompat.Run(ctx, r, rdmacompat.IBStatus, args, stdout)
	case "ibv_devinfo":
		return rdmacompat.Run(ctx, r, rdmacompat.IBVDevInfo, args, stdout)
	case "dcgmi":
		return errors.New("dcgmi is unsupported in synthetic mode")
	default:
		return fmt.Errorf("unsupported native command %q", command)
	}
}

func writeNativeNotice(w io.Writer, mode inspection.Mode) {
	if w != nil {
		fmt.Fprintf(w, "%s (mode=%s)\n", inspection.Disclaimer, mode)
	}
}
