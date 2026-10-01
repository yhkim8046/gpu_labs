package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/gpu-lab/gpu-lab/internal/monitoring"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

type metricQuery struct {
	Name  string
	Query string
}

var defaultMetricQueries = []metricQuery{
	{Name: "GPU utilization (%)", Query: "avg(gpu_lab_gpu_utilization_percent)"},
	{Name: "GPU memory used (%)", Query: "max(gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes) * 100"},
	{Name: "GPU temperature (°C)", Query: "max(gpu_lab_gpu_temperature_celsius)"},
	{Name: "GPU power (W)", Query: "max(gpu_lab_gpu_power_watts)"},
	{Name: "GPU XID", Query: "max(gpu_lab_gpu_xid_code)"},
	{Name: "GPU health", Query: "min(gpu_lab_gpu_health)"},
	{Name: "GPU health status", Query: "max(gpu_lab_gpu_health_status)"},
	{Name: "Double-bit ECC errors", Query: "max(gpu_lab_gpu_ecc_dbe_total)"},
	{Name: "Power violations", Query: "max(gpu_lab_gpu_power_violation_total)"},
	{Name: "PCIe replay errors", Query: "max(gpu_lab_gpu_pcie_replay_total)"},
	{Name: "Allocated GPUs", Query: "sum(gpu_lab_gpu_allocated)"},
	{Name: "GPU capacity", Query: "max(gpu_lab_node_gpu_capacity)"},
	{Name: "GPU allocatable", Query: "max(gpu_lab_node_gpu_allocatable)"},
	{Name: "Exporter targets", Query: `sum(up{service="dcgm-exporter"})`},
}

type metricsOptions struct {
	query string
	json  bool
}

func metricsCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "gpu metrics — show lab metrics")
		fmt.Fprintln(stdout, "Usage: gpu metrics [--query <PromQL>] [--json]")
		return nil
	}
	options, err := parseMetricsArgs(args)
	if err != nil {
		return err
	}
	client := monitoring.New(r)
	if options.query != "" {
		result, err := client.Query(ctx, options.query)
		if err != nil {
			return err
		}
		return printMetricResult(stdout, options.query, result, options.json)
	}
	if options.json {
		values := make(map[string]float64, len(defaultMetricQueries))
		for _, query := range defaultMetricQueries {
			result, err := client.Query(ctx, query.Query)
			if err != nil {
				return fmt.Errorf("%s: %w", query.Name, err)
			}
			values[query.Name] = result.Samples[0].Value
		}
		return writeJSON(stdout, values)
	}
	fmt.Fprintln(stdout, "gpu metrics")
	for _, query := range defaultMetricQueries {
		result, err := client.Query(ctx, query.Query)
		if err != nil {
			return fmt.Errorf("%s: %w", query.Name, err)
		}
		fmt.Fprintf(stdout, "%-24s %s\n", query.Name, formatMetricValue(result.Samples[0].Value))
	}
	return nil
}

func parseMetricsArgs(args []string) (metricsOptions, error) {
	options := metricsOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--query":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return metricsOptions{}, errors.New("usage: gpu metrics [--query <PromQL>] [--json]")
			}
			i++
			options.query = args[i]
		case "--json":
			options.json = true
		case "--help", "-h":
			return metricsOptions{}, errors.New("usage: gpu metrics [--query <PromQL>] [--json]")
		default:
			return metricsOptions{}, fmt.Errorf("unknown metrics option %q; run gpu metrics --help", args[i])
		}
	}
	return options, nil
}

func printMetricResult(stdout io.Writer, expression string, result monitoring.QueryResult, asJSON bool) error {
	if asJSON {
		return writeJSON(stdout, struct {
			Query   string              `json:"query"`
			Type    string              `json:"result_type"`
			Samples []monitoring.Sample `json:"samples"`
		}{expression, result.ResultType, result.Samples})
	}
	fmt.Fprintf(stdout, "query: %s\n", expression)
	for _, sample := range result.Samples {
		labels := make([]string, 0, len(sample.Metric))
		for name, value := range sample.Metric {
			labels = append(labels, fmt.Sprintf("%s=%q", name, value))
		}
		sort.Strings(labels)
		if len(labels) > 0 {
			fmt.Fprintf(stdout, "%-36s %s\n", "{"+strings.Join(labels, ", ")+"}", formatMetricValue(sample.Value))
		} else {
			fmt.Fprintln(stdout, formatMetricValue(sample.Value))
		}
	}
	return nil
}

func formatMetricValue(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func writeJSON(stdout io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}
