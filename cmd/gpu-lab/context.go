package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
)

func contextCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		current, err := m.CurrentContext(ctx)
		if err != nil {
			return err
		}
		contexts, err := m.Contexts(ctx)
		if err != nil {
			return err
		}
		path, err := m.DedicatedKubeconfigPath()
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "current: %s\n", current)
		fmt.Fprintf(stdout, "dedicated kubeconfig: %s\n", path)
		for _, name := range contexts {
			fmt.Fprintln(stdout, name)
		}
		return nil
	}
	switch args[0] {
	case "setup":
		path, err := m.SetupContext(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "gpu-lab context configured\ndedicated kubeconfig: %s\n", path)
		return nil
	case "use":
		if len(args) != 2 {
			return errors.New("usage: gpu context use <context-name>")
		}
		if err := m.UseContext(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "switched kubectl context to %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown context command %q; use list, setup, or use", args[0])
	}
}
