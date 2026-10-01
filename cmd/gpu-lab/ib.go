package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gpu-lab/gpu-lab/internal/monitoring"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

type ibStatusOptions struct {
	node string
	json bool
}

type ibPortKey struct {
	Node string
	HCA  string
	Port string
}

type ibPortStatus struct {
	Node          string  `json:"node"`
	HCA           string  `json:"hca"`
	Port          string  `json:"port"`
	LinkLayer     string  `json:"link_layer"`
	State         string  `json:"state"`
	PhysicalState string  `json:"physical_state"`
	Up            float64 `json:"up"`
	RateGbps      float64 `json:"rate_gbps"`
	TXBytes       float64 `json:"tx_bytes"`
	RXBytes       float64 `json:"rx_bytes"`
	SymbolErrors  float64 `json:"symbol_errors"`
	LinkDowned    float64 `json:"link_downed"`
	XmitDiscards  float64 `json:"xmit_discards"`
	XmitWait      float64 `json:"xmit_wait"`
	RDMARetries   float64 `json:"rdma_retries"`
	RDMATimeouts  float64 `json:"rdma_timeouts"`
}

var ibMetricNames = []string{
	"gpu_lab_ib_port_up",
	"gpu_lab_ib_port_state",
	"gpu_lab_ib_link_rate_gbps",
	"gpu_lab_ib_tx_bytes_total",
	"gpu_lab_ib_rx_bytes_total",
	"gpu_lab_ib_symbol_errors_total",
	"gpu_lab_ib_link_error_recovery_total",
	"gpu_lab_ib_link_downed_total",
	"gpu_lab_ib_xmit_discards_total",
	"gpu_lab_ib_xmit_wait_total",
	"gpu_lab_rdma_retries_total",
	"gpu_lab_rdma_timeouts_total",
	"gpu_lab_fabric_delay_seconds",
}

type prometheusQuerier interface {
	Query(context.Context, string) (monitoring.QueryResult, error)
}

func ibCommand(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printIBHelp(stdout)
		return nil
	}
	if args[0] != "status" {
		return fmt.Errorf("unknown ib command %q; run gpu ib --help", args[0])
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		printIBHelp(stdout)
		return nil
	}
	options, err := parseIBStatusArgs(args[1:])
	if err != nil {
		return err
	}
	rows, err := queryIBStatus(ctx, monitoring.New(r), options.node)
	if err != nil {
		return err
	}
	if options.json {
		return writeJSON(stdout, rows)
	}
	printIBTable(stdout, rows)
	return nil
}

func parseIBStatusArgs(args []string) (ibStatusOptions, error) {
	options := ibStatusOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--node":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return ibStatusOptions{}, errors.New("--node requires a non-empty node name")
			}
			i++
			options.node = strings.TrimSpace(args[i])
			if err := validateIBNode(options.node); err != nil {
				return ibStatusOptions{}, err
			}
		case "--json":
			options.json = true
		case "--help", "-h":
			return ibStatusOptions{}, errors.New("usage: gpu ib status [--node NODE] [--json]")
		default:
			return ibStatusOptions{}, fmt.Errorf("unknown ib status option %q; run gpu ib status --help", args[i])
		}
	}
	return options, nil
}

func validateIBNode(node string) error {
	if strings.TrimSpace(node) == "" {
		return errors.New("node name cannot be empty")
	}
	for _, r := range node {
		if r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		return fmt.Errorf("invalid node name %q", node)
	}
	return nil
}

func queryIBStatus(ctx context.Context, client prometheusQuerier, node string) ([]ibPortStatus, error) {
	if err := validateIBNodeOptional(node); err != nil {
		return nil, err
	}
	results := make(map[string]monitoring.QueryResult, len(ibMetricNames))
	anySamples := false
	for _, metric := range ibMetricNames {
		result, err := client.Query(ctx, metric)
		if err != nil {
			if isEmptyPrometheusResult(err) {
				results[metric] = monitoring.QueryResult{}
				continue
			}
			return nil, fmt.Errorf("query %s: %w", metric, err)
		}
		if len(result.Samples) > 0 {
			anySamples = true
		}
		results[metric] = result
	}
	rows := joinIBSamples(results, node)
	if len(rows) == 0 {
		if node != "" {
			return nil, fmt.Errorf("no InfiniBand/RDMA fabric samples found for node %q", node)
		}
		if !anySamples {
			return nil, errors.New("no InfiniBand/RDMA fabric samples found")
		}
		return nil, errors.New("fabric samples did not contain node/hca/port labels")
	}
	return rows, nil
}

func validateIBNodeOptional(node string) error {
	if strings.TrimSpace(node) == "" {
		return nil
	}
	return validateIBNode(strings.TrimSpace(node))
}

func isEmptyPrometheusResult(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no result") || strings.Contains(message, "no samples") || strings.Contains(message, "empty result")
}

func joinIBSamples(results map[string]monitoring.QueryResult, node string) []ibPortStatus {
	rows := make(map[ibPortKey]*ibPortStatus)
	// Walk the declared metric order instead of the map order so label
	// precedence remains deterministic when an exporter includes more than one
	// sample for a port.
	for _, metric := range ibMetricNames {
		result := results[metric]
		for _, sample := range result.Samples {
			key, ok := ibSampleKey(sample.Metric)
			if !ok || (node != "" && key.Node != node) {
				continue
			}
			row := rows[key]
			if row == nil {
				row = &ibPortStatus{Node: key.Node, HCA: key.HCA, Port: key.Port, LinkLayer: "unknown", State: "unknown", PhysicalState: "unknown"}
				rows[key] = row
			}
			if layer := label(sample.Metric, "link_layer", "linkLayer", "link_type"); layer != "" {
				row.LinkLayer = layer
			}
			switch metric {
			case "gpu_lab_ib_port_up":
				row.Up = sample.Value
			case "gpu_lab_ib_port_state":
				if value := label(sample.Metric, "state", "port_state", "link_state"); value != "" {
					row.State = value
				}
				if value := label(sample.Metric, "physical_state", "physical_port_state"); value != "" {
					row.PhysicalState = value
				}
			case "gpu_lab_ib_link_rate_gbps":
				row.RateGbps = sample.Value
			case "gpu_lab_ib_tx_bytes_total":
				row.TXBytes = sample.Value
			case "gpu_lab_ib_rx_bytes_total":
				row.RXBytes = sample.Value
			case "gpu_lab_ib_symbol_errors_total":
				row.SymbolErrors = sample.Value
			case "gpu_lab_ib_link_downed_total":
				row.LinkDowned = sample.Value
			case "gpu_lab_ib_xmit_discards_total":
				row.XmitDiscards = sample.Value
			case "gpu_lab_ib_xmit_wait_total":
				row.XmitWait = sample.Value
			case "gpu_lab_rdma_retries_total":
				row.RDMARetries = sample.Value
			case "gpu_lab_rdma_timeouts_total":
				row.RDMATimeouts = sample.Value
			}
		}
	}
	ordered := make([]ibPortStatus, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, *row)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Node != ordered[j].Node {
			return ordered[i].Node < ordered[j].Node
		}
		if ordered[i].HCA != ordered[j].HCA {
			return ordered[i].HCA < ordered[j].HCA
		}
		return ordered[i].Port < ordered[j].Port
	})
	return ordered
}

func ibSampleKey(metric map[string]string) (ibPortKey, bool) {
	key := ibPortKey{
		Node: label(metric, "node", "kubernetes_node", "node_name"),
		HCA:  label(metric, "hca", "hca_name", "device"),
		Port: label(metric, "port", "port_number", "port_id"),
	}
	return key, key.Node != "" && key.HCA != "" && key.Port != ""
}

func label(labels map[string]string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(labels[name]); value != "" {
			return value
		}
	}
	return ""
}

func printIBTable(stdout io.Writer, rows []ibPortStatus) {
	fmt.Fprintln(stdout, "NODE\tHCA\tPORT\tLINK_LAYER\tSTATE\tPHYSICAL_STATE\tUP\tRATE_GBPS\tTX_BYTES\tRX_BYTES\tSYMBOL_ERRORS\tLINK_DOWNED\tXMIT_DISCARDS\tXMIT_WAIT\tRDMA_RETRIES\tRDMA_TIMEOUTS")
	for _, row := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Node, row.HCA, row.Port, row.LinkLayer, row.State, row.PhysicalState,
			formatMetricValue(row.Up), formatMetricValue(row.RateGbps), formatMetricValue(row.TXBytes), formatMetricValue(row.RXBytes),
			formatMetricValue(row.SymbolErrors), formatMetricValue(row.LinkDowned), formatMetricValue(row.XmitDiscards), formatMetricValue(row.XmitWait),
			formatMetricValue(row.RDMARetries), formatMetricValue(row.RDMATimeouts))
	}
}

// Kept only for the legacy parser's internal tests. The public `gpu ib`
// command now returns a migration message and the rdma-core command names are
// exposed directly.
func printIBHelp(w io.Writer) {
	_, _ = io.WriteString(w, "gpu ib status was replaced; use gpu ibstat, gpu ibstatus, or gpu ibv_devinfo\n")
}
