package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/trainingjob"
)

type trainingRunOptions struct{ trainingjob.Options }

func trainingCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printTrainingHelp(stdout)
		return nil
	}
	client := trainingjob.NewClient(r)
	switch args[0] {
	case "run":
		o, err := parseTrainingRunArgs(args[1:])
		if err != nil {
			return err
		}
		workCtx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		if err := client.Apply(workCtx, o.Options, trainingjob.ControlState{Generation: time.Now().UnixNano()}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "training job %s applied with %d workers\n", trainingjob.Name, o.Workers)
		if o.Wait {
			if err := client.Wait(workCtx, o.Options); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "training workers are ready")
		}
		return nil
	case "status":
		o, err := parseTrainingNamespaceArgs(args[1:])
		if err != nil {
			return err
		}
		return trainingStatus(ctx, client, o, stdout)
	case "logs":
		o, rank, follow, err := parseTrainingLogsArgs(args[1:])
		if err != nil {
			return err
		}
		return client.Logs(ctx, o, rank, follow, stdout)
	case "inject":
		return trainingInject(ctx, client, args[1:], stdout)
	case "recover":
		o, err := parseTrainingNamespaceArgs(args[1:])
		if err != nil {
			return err
		}
		if err := client.Mutate(ctx, o, func(state *trainingjob.ControlState) error {
			state.StragglerRank = nil
			state.StragglerDelay = ""
			state.CrashRank = nil
			state.CrashToken = ""
			trainingjob.ApplyFabricControl(state, trainingjob.FabricControlForScenario("normal"))
			return nil
		}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "training fault controls recovered")
		return nil
	case "reset":
		o, err := parseTrainingNamespaceArgs(args[1:])
		if err != nil {
			return err
		}
		if err := client.Reset(ctx, o); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "training job %s reset\n", trainingjob.Name)
		return nil
	default:
		return fmt.Errorf("unknown training command %q; run gpu training --help", args[0])
	}
}

func parseTrainingRunArgs(args []string) (trainingRunOptions, error) {
	o := trainingRunOptions{Options: trainingjob.Options{Workers: 3, Image: "gpu-lab:dev", Namespace: trainingjob.Namespace, Timeout: 5 * time.Minute}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--workers":
			if i+1 >= len(args) {
				return o, errors.New("usage: gpu training run [--workers N] [--image IMAGE] [--namespace NS] [--wait] [--timeout DURATION]")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil || n < 1 {
				return o, fmt.Errorf("invalid workers %q", args[i])
			}
			o.Workers = n
		case "--image":
			if i+1 >= len(args) || args[i+1] == "" {
				return o, errors.New("image is required")
			}
			i++
			o.Image = args[i]
		case "--namespace":
			if i+1 >= len(args) || args[i+1] == "" {
				return o, errors.New("namespace is required")
			}
			i++
			o.Namespace = args[i]
		case "--wait":
			o.Wait = true
		case "--timeout":
			if i+1 >= len(args) {
				return o, errors.New("timeout is required")
			}
			i++
			d, e := time.ParseDuration(args[i])
			if e != nil || d <= 0 {
				return o, fmt.Errorf("invalid timeout %q", args[i])
			}
			o.Timeout = d
		case "--help", "-h":
			return o, errors.New("usage: gpu training run [--workers N] [--image IMAGE] [--namespace NS] [--wait] [--timeout DURATION]")
		default:
			return o, fmt.Errorf("unknown training run option %q", args[i])
		}
	}
	return o, nil
}

func parseTrainingNamespaceArgs(args []string) (string, error) {
	ns := trainingjob.Namespace
	for i := 0; i < len(args); i++ {
		if args[i] == "--namespace" && i+1 < len(args) {
			i++
			ns = args[i]
		} else {
			return "", fmt.Errorf("unknown training option %q", args[i])
		}
	}
	return ns, nil
}
func parseTrainingLogsArgs(args []string) (string, int, bool, error) {
	ns := trainingjob.Namespace
	rank := -1
	follow := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--namespace":
			if i+1 >= len(args) {
				return "", 0, false, errors.New("namespace is required")
			}
			i++
			ns = args[i]
		case "--rank":
			if i+1 >= len(args) {
				return "", 0, false, errors.New("rank is required")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil || n < 0 {
				return "", 0, false, fmt.Errorf("invalid rank %q", args[i])
			}
			rank = n
		case "--follow":
			follow = true
		default:
			return "", 0, false, fmt.Errorf("unknown training logs option %q", args[i])
		}
	}
	if rank < 0 {
		return "", 0, false, errors.New("training logs requires --rank N")
	}
	return ns, rank, follow, nil
}

func trainingInject(ctx context.Context, client trainingjob.Client, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: gpu training inject straggler|worker-crash ...")
	}
	kind := args[0]
	ns := trainingjob.Namespace
	rank := -1
	delay := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--namespace":
			if i+1 >= len(args) {
				return errors.New("namespace is required")
			}
			i++
			ns = args[i]
		case "--rank":
			if i+1 >= len(args) {
				return errors.New("rank is required")
			}
			i++
			n, e := strconv.Atoi(args[i])
			if e != nil || n < 0 {
				return fmt.Errorf("invalid rank %q", args[i])
			}
			rank = n
		case "--delay":
			if i+1 >= len(args) {
				return errors.New("delay is required")
			}
			i++
			d, e := time.ParseDuration(args[i])
			if e != nil || d <= 0 {
				return fmt.Errorf("invalid delay %q", args[i])
			}
			delay = args[i]
		default:
			return fmt.Errorf("unknown training inject option %q", args[i])
		}
	}
	if rank < 0 {
		return errors.New("training inject requires --rank N")
	}
	err := client.Mutate(ctx, ns, func(state *trainingjob.ControlState) error {
		switch kind {
		case "straggler":
			if delay == "" {
				return errors.New("straggler requires --delay DURATION")
			}
			state.StragglerRank = &rank
			state.StragglerDelay = delay
			state.CrashRank = nil
			state.CrashToken = ""
		case "worker-crash":
			state.CrashRank = &rank
			state.CrashToken = strconv.FormatInt(time.Now().UnixNano(), 10)
		default:
			return fmt.Errorf("unknown injection %q", kind)
		}
		return nil
	})
	if err == nil {
		fmt.Fprintf(stdout, "training %s injected for rank %d\n", kind, rank)
	}
	return err
}

func trainingStatus(ctx context.Context, client trainingjob.Client, namespace string, stdout io.Writer) error {
	out, err := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "-n", namespace, "get", "statefulset/"+trainingjob.Name, "-o", "custom-columns=NAME:.metadata.name,DESIRED:.spec.replicas,READY:.status.readyReplicas,CURRENT:.status.currentReplicas,UPDATED:.status.updatedReplicas", "--no-headers")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, strings.TrimSpace(out))
	pods, err := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name="+trainingjob.Name+",app.kubernetes.io/managed-by=gpu-lab", "-o", "custom-columns=NAME:.metadata.name,READY:.status.containerStatuses[*].ready,RESTARTS:.status.containerStatuses[*].restartCount,PHASE:.status.phase", "--no-headers")
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, pods)
	podNames, err := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name="+trainingjob.Name+",app.kubernetes.io/managed-by=gpu-lab", "-o", "name")
	if err != nil {
		return err
	}
	if strings.TrimSpace(podNames) != "" {
		fmt.Fprintln(stdout, "PROGRESS")
	}
	for _, item := range strings.Fields(podNames) {
		pod := strings.TrimPrefix(item, "pod/")
		metrics, metricsErr := client.Runner.Output(ctx, "kubectl", "--context", client.Context, "get", "--raw", fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:9401/proxy/metrics", namespace, pod))
		if metricsErr != nil {
			fmt.Fprintf(stdout, "%s metrics-unavailable: %v\n", pod, metricsErr)
			continue
		}
		snapshot := trainingMetricSnapshot(metrics)
		fmt.Fprintf(stdout, "%s step=%s checkpoint=%s loss=%s allreduce=%ss restarts=%s errors=%s fabric_active=%s fabric_delay=%ss fabric_errors=%s fabric_retries=%s\n",
			pod, snapshot["gpu_lab_training_step"], snapshot["gpu_lab_training_checkpoint_step"], snapshot["gpu_lab_training_loss"], snapshot["gpu_lab_training_allreduce_seconds"], snapshot["gpu_lab_training_restarts_total"], snapshot["gpu_lab_training_allreduce_errors_total"], snapshot["gpu_lab_training_fabric_fault_active"], snapshot["gpu_lab_training_fabric_delay_seconds"], snapshot["gpu_lab_training_fabric_errors_total"], snapshot["gpu_lab_training_fabric_retries_total"])
	}
	control, err := client.ReadControl(ctx, namespace)
	if err != nil {
		return err
	}
	controlJSON, err := control.JSON()
	if err != nil {
		return err
	}
	mode := control.FabricMode
	if strings.TrimSpace(mode) == "" {
		mode = "normal"
	}
	fabricDelay := control.FabricDelay
	if fabricDelay == "" {
		fabricDelay = "0s"
	}
	fabricTarget := control.FabricTargetNode
	if fabricTarget == "" {
		fabricTarget = "-"
	}
	fmt.Fprintf(stdout, "FABRIC mode=%s target=%s delay=%s\n", mode, fabricTarget, fabricDelay)
	fmt.Fprintf(stdout, "CONTROL %s\n", strings.TrimSpace(string(controlJSON)))
	return nil
}

func trainingMetricSnapshot(metrics string) map[string]string {
	names := []string{
		"gpu_lab_training_step",
		"gpu_lab_training_checkpoint_step",
		"gpu_lab_training_loss",
		"gpu_lab_training_allreduce_seconds",
		"gpu_lab_training_restarts_total",
		"gpu_lab_training_allreduce_errors_total",
		"gpu_lab_training_fabric_fault_active",
		"gpu_lab_training_fabric_delay_seconds",
		"gpu_lab_training_fabric_errors_total",
		"gpu_lab_training_fabric_retries_total",
	}
	snapshot := make(map[string]string, len(names))
	for _, name := range names {
		snapshot[name] = "n/a"
	}
	for _, line := range strings.Split(metrics, "\n") {
		for _, name := range names {
			if line != name && !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				snapshot[name] = fields[len(fields)-1]
			}
		}
	}
	return snapshot
}

func printTrainingHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu training — operate the synthetic distributed-training StatefulSet

Usage:
  gpu training run [--workers N] [--image IMAGE] [--namespace NS] [--wait] [--timeout DURATION]
  gpu training status [--namespace NS]
  gpu training logs --rank N [--follow] [--namespace NS]
  gpu training inject straggler --rank N --delay 2s [--namespace NS]
  gpu training inject worker-crash --rank N [--namespace NS]
  gpu training recover [--namespace NS]
  gpu training reset [--namespace NS]
`)
}
