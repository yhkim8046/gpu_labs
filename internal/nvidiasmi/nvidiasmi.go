// Package nvidiasmi provides a small nvidia-smi-compatible view of the
// synthetic GPU state produced by GPU Lab.
package nvidiasmi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/monitoring"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

const metricSelector = `{__name__=~"gpu_lab_gpu_(utilization_percent|memory_used_bytes|memory_total_bytes|temperature_celsius|power_watts|xid_code|ecc_dbe_total|health|throttle_active|allocated)"}`

type GPU struct {
	Node             string
	ID               string
	Index            int
	Name             string
	UUID             string
	Utilization      float64
	MemoryUsedBytes  int64
	MemoryTotalBytes int64
	Temperature      float64
	PowerWatts       float64
	PowerLimitWatts  float64
	XID              int
	Health           int
	ECCDbe           int64
	ThrottleActive   int
	Allocated        int
}

type options struct {
	list     bool
	query    []string
	noHeader bool
	noUnits  bool
	help     bool
	version  bool
}

// Run executes the compatibility command. On a host it reads Prometheus via
// kubectl. Inside a GPU Lab container it can read the local exporter state via
// GPU_LAB_STATE_URL, avoiding a kubectl dependency in the runtime image.
func Run(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	parsed, err := parseArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		_, err := io.WriteString(stdout, helpText+"\n")
		return err
	}
	if parsed.version {
		_, err := io.WriteString(stdout, "NVIDIA-SMI synthetic compatibility layer (GPU Lab)\n")
		return err
	}
	if parsed.list {
		gpus, err := collect(ctx, r)
		if err != nil {
			return err
		}
		return printList(stdout, gpus)
	}
	gpus, err := collect(ctx, r)
	if err != nil {
		return err
	}
	if len(parsed.query) > 0 {
		return printQuery(stdout, gpus, parsed)
	}
	return printSummary(stdout, gpus)
}

func parseArgs(args []string) (options, error) {
	parsed := options{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			parsed.help = true
		case arg == "-L" || arg == "--list-gpus":
			parsed.list = true
		case strings.HasPrefix(arg, "--query-gpu="):
			if err := addQueryFields(&parsed, strings.TrimPrefix(arg, "--query-gpu=")); err != nil {
				return options{}, err
			}
		case arg == "--query-gpu":
			if i+1 >= len(args) {
				return options{}, errors.New("nvidia-smi: --query-gpu requires a comma-separated field list")
			}
			i++
			if err := addQueryFields(&parsed, args[i]); err != nil {
				return options{}, err
			}
		case strings.HasPrefix(arg, "--format="):
			if err := parseFormat(&parsed, strings.TrimPrefix(arg, "--format=")); err != nil {
				return options{}, err
			}
		case arg == "--format":
			if i+1 >= len(args) {
				return options{}, errors.New("nvidia-smi: --format requires a value")
			}
			i++
			if err := parseFormat(&parsed, args[i]); err != nil {
				return options{}, err
			}
		case arg == "--version":
			parsed.version = true
		default:
			return options{}, fmt.Errorf("nvidia-smi: unsupported option %q; run 'gpu nvidia-smi --help'", arg)
		}
	}
	if (parsed.noHeader || parsed.noUnits) && len(parsed.query) == 0 {
		return options{}, errors.New("nvidia-smi: --format is only supported with --query-gpu")
	}
	return parsed, nil
}

func addQueryFields(parsed *options, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("nvidia-smi: --query-gpu requires at least one field")
	}
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if !supportedFields[field] {
			return fmt.Errorf("nvidia-smi: unsupported query field %q", field)
		}
		parsed.query = append(parsed.query, field)
	}
	return nil
}

func parseFormat(parsed *options, value string) error {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || parts[0] != "csv" {
		return fmt.Errorf("nvidia-smi: unsupported format %q; use csv[,noheader][,nounits]", value)
	}
	for _, part := range parts[1:] {
		switch part {
		case "noheader":
			parsed.noHeader = true
		case "nounits":
			parsed.noUnits = true
		case "":
		default:
			return fmt.Errorf("nvidia-smi: unsupported format modifier %q", part)
		}
	}
	return nil
}

var supportedFields = map[string]bool{
	"index": true, "name": true, "uuid": true, "temperature.gpu": true,
	"power.draw": true, "power.limit": true, "memory.used": true,
	"memory.total": true, "utilization.gpu": true,
	"ecc.errors.uncorrected.aggregate": true, "xid.errors": true,
	"pstate": true, "compute_mode": true,
}

func collect(ctx context.Context, r runner.Runner) ([]GPU, error) {
	if stateURL := strings.TrimSpace(os.Getenv("GPU_LAB_STATE_URL")); stateURL != "" {
		return collectState(ctx, stateURL)
	}
	result, err := monitoring.New(r).Query(ctx, metricSelector)
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: unable to read synthetic GPU telemetry: %w", err)
	}
	byKey := make(map[string]*GPU)
	for _, sample := range result.Samples {
		node := sample.Metric["node"]
		id := sample.Metric["gpu"]
		if node == "" || id == "" {
			continue
		}
		key := node + "\x00" + id
		gpu := byKey[key]
		if gpu == nil {
			gpu = newGPU(node, id)
			byKey[key] = gpu
		}
		setMetric(gpu, sample.Metric["__name__"], sample.Value)
	}
	return sortedGPUs(byKey), nil
}

type stateSnapshot struct {
	Node     string `json:"node"`
	Readings []struct {
		GPU                string  `json:"gpu"`
		UtilizationPercent float64 `json:"utilization_percent"`
		MemoryUsedBytes    int64   `json:"memory_used_bytes"`
		MemoryTotalBytes   int64   `json:"memory_total_bytes"`
		TemperatureCelsius float64 `json:"temperature_celsius"`
		PowerWatts         float64 `json:"power_watts"`
		XIDCode            int     `json:"xid_code"`
		Health             int     `json:"health"`
		ECCDbeTotal        int64   `json:"ecc_dbe_total"`
		ThrottleActive     int     `json:"throttle_active"`
		Allocated          int     `json:"allocated"`
	} `json:"readings"`
}

func collectState(ctx context.Context, endpoint string) ([]GPU, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("nvidia-smi: invalid GPU_LAB_STATE_URL %q", endpoint)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: create state request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: query synthetic GPU state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("nvidia-smi: state endpoint returned %s", response.Status)
	}
	var state stateSnapshot
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("nvidia-smi: decode synthetic GPU state: %w", err)
	}
	byKey := make(map[string]*GPU, len(state.Readings))
	for _, reading := range state.Readings {
		gpu := newGPU(state.Node, reading.GPU)
		gpu.Utilization = reading.UtilizationPercent
		gpu.MemoryUsedBytes = reading.MemoryUsedBytes
		gpu.MemoryTotalBytes = reading.MemoryTotalBytes
		gpu.Temperature = reading.TemperatureCelsius
		gpu.PowerWatts = reading.PowerWatts
		gpu.XID = reading.XIDCode
		gpu.Health = reading.Health
		gpu.ECCDbe = reading.ECCDbeTotal
		gpu.ThrottleActive = reading.ThrottleActive
		gpu.Allocated = reading.Allocated
		byKey[gpu.Node+"\x00"+gpu.ID] = gpu
	}
	return sortedGPUs(byKey), nil
}

func newGPU(node, id string) *GPU {
	index := len(id)
	if dash := strings.LastIndex(id, "-"); dash >= 0 {
		if parsed, err := strconv.Atoi(id[dash+1:]); err == nil {
			index = parsed
		}
	}
	return &GPU{
		Node:            node,
		ID:              id,
		Index:           index,
		Name:            "NVIDIA H200",
		UUID:            "GPU-" + stableID(node+"/"+id),
		PowerLimitWatts: 700,
		Health:          1,
	}
}

func stableID(value string) string {
	var hash uint64 = 14695981039346656037
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= 1099511628211
	}
	return fmt.Sprintf("%016x", hash)
}

func setMetric(gpu *GPU, name string, value float64) {
	switch name {
	case "gpu_lab_gpu_utilization_percent":
		gpu.Utilization = value
	case "gpu_lab_gpu_memory_used_bytes":
		gpu.MemoryUsedBytes = int64(value)
	case "gpu_lab_gpu_memory_total_bytes":
		gpu.MemoryTotalBytes = int64(value)
	case "gpu_lab_gpu_temperature_celsius":
		gpu.Temperature = value
	case "gpu_lab_gpu_power_watts":
		gpu.PowerWatts = value
	case "gpu_lab_gpu_xid_code":
		gpu.XID = int(value)
	case "gpu_lab_gpu_health":
		gpu.Health = int(value)
	case "gpu_lab_gpu_ecc_dbe_total":
		gpu.ECCDbe = int64(value)
	case "gpu_lab_gpu_throttle_active":
		gpu.ThrottleActive = int(value)
	case "gpu_lab_gpu_allocated":
		gpu.Allocated = int(value)
	}
}

func sortedGPUs(byKey map[string]*GPU) []GPU {
	gpus := make([]GPU, 0, len(byKey))
	for _, gpu := range byKey {
		gpus = append(gpus, *gpu)
	}
	sort.Slice(gpus, func(i, j int) bool {
		if gpus[i].Node != gpus[j].Node {
			return gpus[i].Node < gpus[j].Node
		}
		return gpus[i].Index < gpus[j].Index
	})
	for i := range gpus {
		gpus[i].Index = i
	}
	return gpus
}

func printList(stdout io.Writer, gpus []GPU) error {
	for _, gpu := range gpus {
		fmt.Fprintf(stdout, "GPU %d: %s (UUID: %s, node: %s)\n", gpu.Index, gpu.Name, gpu.UUID, gpu.Node)
	}
	return nil
}

func printSummary(stdout io.Writer, gpus []GPU) error {
	if len(gpus) == 0 {
		return errors.New("nvidia-smi: no synthetic GPUs were reported")
	}
	fmt.Fprintln(stdout, "+"+strings.Repeat("-", summaryInnerWidth)+"+")
	fmt.Fprintln(stdout, summaryLine("NVIDIA-SMI 550.163.01              Driver Version: 550.163.01      CUDA Version: 12.4"))
	fmt.Fprintln(stdout, "|"+strings.Repeat("-", 41)+"+"+strings.Repeat("-", 24)+"+"+strings.Repeat("-", 22)+"|")
	fmt.Fprintln(stdout, summaryColumns(" GPU  Name                 Persistence-M", " Bus-Id          Disp.A", " Volatile Uncorr. ECC"))
	fmt.Fprintln(stdout, summaryColumns(" Fan  Temp   Perf          Pwr:Usage/Cap", "         Memory-Usage", " GPU-Util  Compute M."))
	fmt.Fprintln(stdout, summaryColumns("", "", "               MIG M."))
	fmt.Fprintln(stdout, "|"+strings.Repeat("=", 41)+"+"+strings.Repeat("=", 24)+"+"+strings.Repeat("=", 22)+"|")
	for _, gpu := range gpus {
		perf := queryValue(gpu, "pstate", true)
		computeMode := queryValue(gpu, "compute_mode", true)
		fmt.Fprintln(stdout, summaryColumns(
			fmt.Sprintf("%3d  %-33sOn ", gpu.Index, gpu.Name),
			fmt.Sprintf("   %s Off", busID(gpu.Index)),
			fmt.Sprintf("%21d ", gpu.ECCDbe),
		))
		fmt.Fprintln(stdout, summaryColumns(
			fmt.Sprintf(" N/A  %3.0fC    %-2s        %3.0fW / %3.0fW", gpu.Temperature, perf, gpu.PowerWatts, gpu.PowerLimitWatts),
			fmt.Sprintf("%22s ", fmt.Sprintf("%dMiB / %dMiB", toMiB(gpu.MemoryUsedBytes), toMiB(gpu.MemoryTotalBytes))),
			fmt.Sprintf("%8.0f%%      %-7s", gpu.Utilization, computeMode),
		))
		fmt.Fprintln(stdout, summaryColumns("", "", "              Disabled"))
		fmt.Fprintln(stdout, "|"+strings.Repeat("-", 41)+"+"+strings.Repeat("-", 24)+"+"+strings.Repeat("-", 22)+"|")
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "+"+strings.Repeat("-", summaryInnerWidth)+"+")
	fmt.Fprintln(stdout, summaryLine(" Processes:"))
	fmt.Fprintln(stdout, summaryLine("  GPU   GI   CI        PID   Type   Process name                             GPU Memory Usage"))
	fmt.Fprintln(stdout, summaryLine("        ID   ID"))
	fmt.Fprintln(stdout, "|"+strings.Repeat("=", summaryInnerWidth)+"|")
	fmt.Fprintln(stdout, summaryLine(" No running processes found"))
	fmt.Fprintln(stdout, "+"+strings.Repeat("-", summaryInnerWidth)+"+")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Note: values are synthetic; this command does not use an NVIDIA driver or CUDA.")
	for _, gpu := range gpus {
		if gpu.XID != 0 {
			fmt.Fprintf(stdout, "Synthetic alert: GPU %d reports XID %d.\n", gpu.Index, gpu.XID)
		}
	}
	return nil
}

const summaryInnerWidth = 89

func summaryLine(content string) string {
	return "|" + padSummaryCell(" "+content, summaryInnerWidth) + "|"
}

func summaryColumns(first, second, third string) string {
	return "|" + padSummaryCell(first, 41) + "|" + padSummaryCell(second, 24) + "|" + padSummaryCell(third, 22) + "|"
}

func padSummaryCell(value string, width int) string {
	if len(value) > width {
		return value[:width]
	}
	return value + strings.Repeat(" ", width-len(value))
}

func busID(index int) string {
	slots := []string{"19", "3B", "4C", "5D", "9B", "AB", "CB", "DB"}
	if index >= 0 && index < len(slots) {
		return "00000000:" + slots[index] + ":00.0"
	}
	return fmt.Sprintf("00000000:%02X:00.0", 0x19+index)
}

func printQuery(stdout io.Writer, gpus []GPU, parsed options) error {
	if len(gpus) == 0 {
		return errors.New("nvidia-smi: no synthetic GPUs were reported")
	}
	if !parsed.noHeader {
		fmt.Fprintln(stdout, strings.Join(parsed.query, ", "))
	}
	for _, gpu := range gpus {
		values := make([]string, 0, len(parsed.query))
		for _, field := range parsed.query {
			values = append(values, queryValue(gpu, field, parsed.noUnits))
		}
		fmt.Fprintln(stdout, strings.Join(values, ", "))
	}
	return nil
}

func queryValue(gpu GPU, field string, noUnits bool) string {
	unit := func(value string, suffix string) string {
		if noUnits {
			return value
		}
		return value + " " + suffix
	}
	switch field {
	case "index":
		return strconv.Itoa(gpu.Index)
	case "name":
		return gpu.Name
	case "uuid":
		return gpu.UUID
	case "temperature.gpu":
		return unit(fmt.Sprintf("%.0f", gpu.Temperature), "C")
	case "power.draw":
		return unit(fmt.Sprintf("%.0f", gpu.PowerWatts), "W")
	case "power.limit":
		return unit(fmt.Sprintf("%.0f", gpu.PowerLimitWatts), "W")
	case "memory.used":
		return unit(strconv.FormatInt(toMiB(gpu.MemoryUsedBytes), 10), "MiB")
	case "memory.total":
		return unit(strconv.FormatInt(toMiB(gpu.MemoryTotalBytes), 10), "MiB")
	case "utilization.gpu":
		return unit(fmt.Sprintf("%.0f", gpu.Utilization), "%")
	case "ecc.errors.uncorrected.aggregate":
		return strconv.FormatInt(gpu.ECCDbe, 10)
	case "xid.errors":
		return strconv.Itoa(gpu.XID)
	case "pstate":
		if gpu.ThrottleActive != 0 {
			return "P2"
		}
		return "P0"
	case "compute_mode":
		return "Default"
	default:
		return "N/A"
	}
}

func toMiB(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return (bytes + (1024*1024)/2) / (1024 * 1024)
}

const helpText = `nvidia-smi — synthetic NVIDIA-SMI-compatible view for GPU Lab

Usage:
  nvidia-smi
  nvidia-smi --list-gpus
  nvidia-smi --query-gpu=<fields> --format=csv[,noheader][,nounits]

Supported fields:
  index,name,uuid,temperature.gpu,power.draw,power.limit,
  memory.used,memory.total,utilization.gpu,
  ecc.errors.uncorrected.aggregate,xid.errors,pstate,compute_mode

This command reports synthetic GPU Lab telemetry. It does not access an
NVIDIA driver, CUDA runtime, or physical GPU.`
