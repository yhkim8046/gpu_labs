package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
	"github.com/gpu-lab/gpu-lab/internal/trainingjob"
)

func scenarioCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: gpu scenario list|run|inspect <name>|reset")
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
			return errors.New("usage: gpu scenario run <name> [--file path]")
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
	case "inspect":
		return inspectScenario(args[1:], stdout)
	default:
		return fmt.Errorf("unknown scenario command %q", args[0])
	}
}

const inspectScenarioUsage = "usage: gpu scenario inspect <name> [--file path] [--solution]"

type inspectScenarioOptions struct {
	name     string
	file     string
	solution bool
}

func inspectScenario(args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, inspectScenarioUsage)
		fmt.Fprintln(stdout, "Metric values are hidden by default; use --solution to reveal them.")
		return nil
	}
	options, err := parseInspectScenarioArgs(args)
	if err != nil {
		return err
	}
	var selected scenario.Scenario
	if options.file == "" {
		selected, err = scenario.LoadBuiltin(options.name)
	} else {
		selected, err = scenario.LoadFile(options.file)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "scenario: %s\n", selected.Name())
	fmt.Fprintf(stdout, "description: %s\n", selected.Spec.Description)
	if selected.Spec.Duration == "" {
		fmt.Fprintln(stdout, "duration: persistent until reset")
	} else {
		fmt.Fprintf(stdout, "duration: %s\n", selected.Spec.Duration)
	}
	if len(selected.Spec.Targets.Selector) == 0 && len(selected.Spec.Targets.GPUIndices) == 0 {
		fmt.Fprintln(stdout, "targets: all synthetic GPUs")
	} else {
		fmt.Fprintln(stdout, "targets:")
		keys := sortedKeys(selected.Spec.Targets.Selector)
		for _, key := range keys {
			fmt.Fprintf(stdout, "  %s=%s\n", key, selected.Spec.Targets.Selector[key])
		}
		if len(selected.Spec.Targets.GPUIndices) > 0 {
			indices := append([]int(nil), selected.Spec.Targets.GPUIndices...)
			sort.Ints(indices)
			parts := make([]string, 0, len(indices))
			for _, index := range indices {
				parts = append(parts, strconv.Itoa(index))
			}
			fmt.Fprintf(stdout, "  gpu_indices=%s\n", strings.Join(parts, ","))
		}
	}
	fmt.Fprintln(stdout, "metrics:")
	printMetricOverrides(stdout, selected.Spec.Metrics, options.solution)
	if len(selected.Spec.Actions) == 0 {
		fmt.Fprintln(stdout, "actions: none")
	} else {
		fmt.Fprintln(stdout, "actions:")
		for _, action := range selected.Spec.Actions {
			fmt.Fprintf(stdout, "  - type=%s", action.Type)
			if action.Name != "" {
				fmt.Fprintf(stdout, " name=%s", action.Name)
			}
			if action.GPUCount > 0 {
				fmt.Fprintf(stdout, " gpu_count=%d", action.GPUCount)
			}
			if action.Mode != "" {
				fmt.Fprintf(stdout, " mode=%s", action.Mode)
			}
			if action.WaitForReady {
				fmt.Fprint(stdout, " wait_for_ready=true")
			}
			fmt.Fprintln(stdout)
		}
	}
	return nil
}

func parseInspectScenarioArgs(args []string) (inspectScenarioOptions, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return inspectScenarioOptions{}, errors.New(inspectScenarioUsage)
	}
	options := inspectScenarioOptions{name: args[0]}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if options.file != "" {
				return inspectScenarioOptions{}, fmt.Errorf("--file may only be specified once; %s", inspectScenarioUsage)
			}
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return inspectScenarioOptions{}, fmt.Errorf("--file requires a path; %s", inspectScenarioUsage)
			}
			i++
			options.file = args[i]
		case "--solution":
			if options.solution {
				return inspectScenarioOptions{}, fmt.Errorf("--solution may only be specified once; %s", inspectScenarioUsage)
			}
			options.solution = true
		default:
			return inspectScenarioOptions{}, fmt.Errorf("unknown scenario inspect option %q; %s", args[i], inspectScenarioUsage)
		}
	}
	return options, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func printMetricOverrides(stdout io.Writer, metrics scenario.MetricOverrides, solution bool) {
	count := 0
	if metrics.GPUUtilizationPercent != nil {
		printMetricOverride(stdout, &count, "gpu_utilization_percent", formatMetricValue(*metrics.GPUUtilizationPercent), solution)
	}
	if metrics.GPUMemoryUsedPercent != nil {
		printMetricOverride(stdout, &count, "gpu_memory_used_percent", formatMetricValue(*metrics.GPUMemoryUsedPercent), solution)
	}
	if metrics.GPUMemoryTotalBytes != nil {
		printMetricOverride(stdout, &count, "gpu_memory_total_bytes", strconv.FormatInt(*metrics.GPUMemoryTotalBytes, 10), solution)
	}
	if metrics.TemperatureCelsius != nil {
		printMetricOverride(stdout, &count, "temperature_celsius", formatMetricValue(*metrics.TemperatureCelsius), solution)
	}
	if metrics.PowerWatts != nil {
		printMetricOverride(stdout, &count, "power_watts", formatMetricValue(*metrics.PowerWatts), solution)
	}
	if metrics.XIDCode != nil {
		printMetricOverride(stdout, &count, "xid_code", strconv.Itoa(*metrics.XIDCode), solution)
	}
	if metrics.Health != nil {
		printMetricOverride(stdout, &count, "health", strconv.Itoa(*metrics.Health), solution)
	}
	if metrics.ECCDbeTotal != nil {
		printMetricOverride(stdout, &count, "ecc_dbe_total", strconv.FormatInt(*metrics.ECCDbeTotal, 10), solution)
	}
	if metrics.PowerViolationTotal != nil {
		printMetricOverride(stdout, &count, "power_violation_total", strconv.FormatInt(*metrics.PowerViolationTotal, 10), solution)
	}
	if metrics.PCIeReplayTotal != nil {
		printMetricOverride(stdout, &count, "pcie_replay_total", strconv.FormatInt(*metrics.PCIeReplayTotal, 10), solution)
	}
	if metrics.ThrottleActive != nil {
		printMetricOverride(stdout, &count, "throttle_active", strconv.Itoa(*metrics.ThrottleActive), solution)
	}
	if metrics.ThrottleReason != "" {
		printMetricOverride(stdout, &count, "throttle_reason", metrics.ThrottleReason, solution)
	}
	if metrics.GPUAllocatedCount != nil {
		printMetricOverride(stdout, &count, "gpu_allocated_count", strconv.Itoa(*metrics.GPUAllocatedCount), solution)
	}
	if metrics.GPUCapacity != nil {
		printMetricOverride(stdout, &count, "gpu_capacity", strconv.Itoa(*metrics.GPUCapacity), solution)
	}
	if metrics.GPUAllocatable != nil {
		printMetricOverride(stdout, &count, "gpu_allocatable", strconv.Itoa(*metrics.GPUAllocatable), solution)
	}
	if metrics.IBPortUp != nil {
		printMetricOverride(stdout, &count, "ib_port_up", strconv.Itoa(*metrics.IBPortUp), solution)
	}
	if metrics.IBState != "" {
		printMetricOverride(stdout, &count, "ib_state", metrics.IBState, solution)
	}
	if metrics.IBPhysicalState != "" {
		printMetricOverride(stdout, &count, "ib_physical_state", metrics.IBPhysicalState, solution)
	}
	if metrics.IBLinkRateGbps != nil {
		printMetricOverride(stdout, &count, "ib_link_rate_gbps", formatMetricValue(*metrics.IBLinkRateGbps), solution)
	}
	if metrics.IBTxBytesTotal != nil {
		printMetricOverride(stdout, &count, "ib_tx_bytes_total", strconv.FormatInt(*metrics.IBTxBytesTotal, 10), solution)
	}
	if metrics.IBRxBytesTotal != nil {
		printMetricOverride(stdout, &count, "ib_rx_bytes_total", strconv.FormatInt(*metrics.IBRxBytesTotal, 10), solution)
	}
	if metrics.IBSymbolErrorsTotal != nil {
		printMetricOverride(stdout, &count, "ib_symbol_errors_total", strconv.FormatInt(*metrics.IBSymbolErrorsTotal, 10), solution)
	}
	if metrics.IBLinkErrorRecoveryTotal != nil {
		printMetricOverride(stdout, &count, "ib_link_error_recovery_total", strconv.FormatInt(*metrics.IBLinkErrorRecoveryTotal, 10), solution)
	}
	if metrics.IBLinkDownedTotal != nil {
		printMetricOverride(stdout, &count, "ib_link_downed_total", strconv.FormatInt(*metrics.IBLinkDownedTotal, 10), solution)
	}
	if metrics.IBXmitDiscardsTotal != nil {
		printMetricOverride(stdout, &count, "ib_xmit_discards_total", strconv.FormatInt(*metrics.IBXmitDiscardsTotal, 10), solution)
	}
	if metrics.IBXmitWaitTotal != nil {
		printMetricOverride(stdout, &count, "ib_xmit_wait_total", strconv.FormatInt(*metrics.IBXmitWaitTotal, 10), solution)
	}
	if metrics.RDMARetriesTotal != nil {
		printMetricOverride(stdout, &count, "rdma_retries_total", strconv.FormatInt(*metrics.RDMARetriesTotal, 10), solution)
	}
	if metrics.RDMATimeoutsTotal != nil {
		printMetricOverride(stdout, &count, "rdma_timeouts_total", strconv.FormatInt(*metrics.RDMATimeoutsTotal, 10), solution)
	}
	if metrics.FabricDelaySeconds != nil {
		printMetricOverride(stdout, &count, "fabric_delay_seconds", formatMetricValue(*metrics.FabricDelaySeconds), solution)
	}
	if count == 0 {
		fmt.Fprintln(stdout, "  none")
	}
}

func printMetricOverride(stdout io.Writer, count *int, name, value string, solution bool) {
	if !solution {
		value = "[hidden; use --solution]"
	}
	fmt.Fprintf(stdout, "  %s=%s\n", name, value)
	(*count)++
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
	for _, action := range selected.Spec.Actions {
		var workload []byte
		switch action.Type {
		case "create_pending_workload":
			workload, err = scenario.PendingWorkloadJSON(selected, action.GPUCount)
		case "create_gpu_workload":
			workload, err = scenario.GPUWorkloadJSON(selected, action)
		default:
			continue
		}
		if err != nil {
			return err
		}
		if err := m.ApplyJSON(ctx, workload); err != nil {
			return err
		}
		if action.WaitForReady {
			if err := m.WaitForScenarioPod(ctx, action.Name); err != nil {
				return err
			}
		}
	}
	// The training ConfigMap is an optional consumer of GPU scenarios. A lab
	// can run GPU-only exercises, so a missing training workload must never
	// make scenario application fail.
	trainingClient := trainingjob.NewClient(m.Runner)
	_ = trainingClient.SyncScenarioControl(ctx, trainingjob.Namespace, selected.Name())
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
