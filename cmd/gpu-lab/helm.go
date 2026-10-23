package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
)

func helmCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		printHelmHelp(stdout)
		return nil
	}
	if len(args) == 1 && args[0] == "catalog" {
		fmt.Fprintln(stdout, "COMPONENT\tNAMESPACE\tDESCRIPTION")
		for _, component := range cluster.Components() {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", component.Name, component.Namespace, component.Description)
		}
		return nil
	}
	if len(args) >= 2 && args[0] == "install" {
		if args[1] == "all" && len(args) == 2 {
			return m.InstallAll(ctx)
		}
		if _, known := cluster.FindComponent(args[1]); known && (len(args) == 2 || strings.HasPrefix(args[2], "-")) {
			if err := m.InstallComponent(ctx, args[1], args[2:]...); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s installed with official Helm\n", args[1])
			return nil
		}
	}
	if len(args) >= 2 && (args[0] == "uninstall" || args[0] == "delete") {
		if _, known := cluster.FindComponent(args[1]); known && (len(args) == 2 || strings.HasPrefix(args[2], "-")) {
			return m.UninstallComponent(ctx, args[1], args[2:]...)
		}
	}
	return m.Helm(ctx, args...)
}

func printHelmHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu helm — use official Helm against the GPU Lab cluster

Course components:
  gpu helm catalog
  gpu helm install nvidia-device-plugin
  gpu helm install dcgm-exporter
  gpu helm install monitoring
  gpu helm uninstall <component>

Official Helm passthrough:
  gpu helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
  gpu helm search repo prometheus-community
  gpu helm install <release> <chart> [official Helm flags...]
  gpu helm list --all-namespaces

The component shorthand expands to a real Helm install. Cluster-aware Helm
commands default to the gpu-lab kube context. The legacy gpu-lab binary name
remains available for compatibility.
`)
}
