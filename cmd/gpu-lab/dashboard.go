package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

type dashboardOptions struct {
	port int
}

func dashboardCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	options := dashboardOptions{port: 3000}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 >= len(args) {
				return errors.New("usage: gpu dashboard [--port <port>]")
			}
			i++
			port, err := strconv.Atoi(args[i])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid dashboard port %q", args[i])
			}
			options.port = port
		case "--help", "-h":
			if len(args) != 1 {
				return errors.New("usage: gpu dashboard [--port <port>]")
			}
			fmt.Fprintln(stdout, "gpu dashboard — forward Grafana to localhost")
			fmt.Fprintln(stdout, "Usage: gpu dashboard [--port <port>]")
			return nil
		default:
			return fmt.Errorf("unknown dashboard option %q; run gpu dashboard --help", args[i])
		}
	}
	fmt.Fprintf(stdout, "Grafana: http://127.0.0.1:%d\n", options.port)
	fmt.Fprintln(stdout, "Press Ctrl-C to stop port-forwarding.")
	return r.Run(ctx, "kubectl", "--context", cluster.KubeContext, "-n", cluster.MonitoringNS, "port-forward", "svc/"+cluster.GrafanaService, fmt.Sprintf("%d:80", options.port))
}
