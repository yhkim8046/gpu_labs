// Package rdmacompat provides rdma-core command-compatible views of GPU Lab's
// synthetic InfiniBand state. It intentionally copies the user-facing command
// names and text layout while never claiming that a real verbs device exists.
package rdmacompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
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

type Tool string

const (
	IBStat     Tool = "ibstat"
	IBStatus   Tool = "ibstatus"
	IBVDevInfo Tool = "ibv_devinfo"

	metricSelector  = `{__name__=~"gpu_lab_ib_(port_up|port_state|link_rate_gbps|tx_bytes_total|rx_bytes_total|symbol_errors_total|link_error_recovery_total|link_downed_total|xmit_discards_total|xmit_wait_total)|gpu_lab_rdma_(retries_total|timeouts_total)|gpu_lab_fabric_delay_seconds"}`
	firmwareVersion = "28.43.2026"
	caType          = "MT4129"
	boardID         = "MT_0000000224"
	capabilityMask  = "0xa759e848"
)

type Device struct {
	Node          string
	HCA           string
	Port          int
	LinkLayer     string
	State         string
	PhysicalState string
	Up            int
	RateGbps      float64
	NodeGUID      uint64
	SystemGUID    uint64
	PortGUID      uint64
	BaseLID       int
	SMLID         int
}

type commonOptions struct {
	node string
	help bool
}

type ibstatOptions struct {
	commonOptions
	hca, port          string
	listCAs, listPorts bool
	short              bool
}

type ibstatusOptions struct {
	commonOptions
	selectors []string
}

type ibvDevInfoOptions struct {
	commonOptions
	hca     string
	port    int
	list    bool
	verbose bool
}

// Run executes one of the three rdma-core compatible commands. GPU_LAB_STATE_URL
// selects the local exporter state endpoint inside a runtime container. On the
// host, Prometheus is queried through the gpu-lab Kubernetes context.
func Run(ctx context.Context, r runner.Runner, tool Tool, args []string, stdout io.Writer) error {
	switch tool {
	case IBStat:
		opts, err := parseIBStat(args)
		if err != nil {
			return err
		}
		if opts.help {
			_, err = io.WriteString(stdout, ibstatHelp)
			return err
		}
		devices, err := collectAndSelect(ctx, r, opts.node)
		if err != nil {
			return commandError(tool, err)
		}
		return runIBStat(stdout, devices, opts)
	case IBStatus:
		opts, err := parseIBStatus(args)
		if err != nil {
			return err
		}
		if opts.help {
			_, err = io.WriteString(stdout, ibstatusHelp)
			return err
		}
		devices, err := collectAndSelect(ctx, r, opts.node)
		if err != nil {
			return commandError(tool, err)
		}
		return runIBStatus(stdout, devices, opts)
	case IBVDevInfo:
		opts, err := parseIBVDevInfo(args)
		if err != nil {
			return err
		}
		if opts.help {
			_, err = io.WriteString(stdout, ibvDevInfoHelp)
			return err
		}
		devices, err := collectAndSelect(ctx, r, opts.node)
		if err != nil {
			return commandError(tool, err)
		}
		return runIBVDevInfo(stdout, devices, opts)
	default:
		return fmt.Errorf("unsupported RDMA compatibility command %q", tool)
	}
}

func commandError(tool Tool, err error) error {
	return fmt.Errorf("%s: %w", tool, err)
}

func parseNode(args []string) (commonOptions, []string, error) {
	common := commonOptions{node: strings.TrimSpace(os.Getenv("GPU_LAB_NODE"))}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--node":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return commonOptions{}, nil, errors.New("--node requires a node name")
			}
			i++
			common.node = strings.TrimSpace(args[i])
		case strings.HasPrefix(args[i], "--node="):
			common.node = strings.TrimSpace(strings.TrimPrefix(args[i], "--node="))
			if common.node == "" {
				return commonOptions{}, nil, errors.New("--node requires a node name")
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return common, rest, nil
}

func parseIBStat(args []string) (ibstatOptions, error) {
	common, args, err := parseNode(args)
	if err != nil {
		return ibstatOptions{}, fmt.Errorf("ibstat: %w", err)
	}
	opts := ibstatOptions{commonOptions: common}
	positionals := make([]string, 0, 2)
	for _, arg := range args {
		switch arg {
		case "-h", "--help":
			opts.help = true
		case "-l", "--list_of_cas":
			opts.listCAs = true
		case "-p", "--port_list":
			opts.listPorts = true
		case "-s", "--short":
			opts.short = true
		case "-v", "--verbose":
			// ibstat's regular view already contains the operational fields.
		default:
			if strings.HasPrefix(arg, "-") {
				return ibstatOptions{}, fmt.Errorf("ibstat: invalid option -- %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 2 {
		return ibstatOptions{}, errors.New("ibstat: too many arguments")
	}
	if len(positionals) > 0 {
		opts.hca = positionals[0]
	}
	if len(positionals) == 2 {
		if _, err := positivePort(positionals[1]); err != nil {
			return ibstatOptions{}, fmt.Errorf("ibstat: %w", err)
		}
		opts.port = positionals[1]
	}
	return opts, nil
}

func parseIBStatus(args []string) (ibstatusOptions, error) {
	common, args, err := parseNode(args)
	if err != nil {
		return ibstatusOptions{}, fmt.Errorf("ibstatus: %w", err)
	}
	opts := ibstatusOptions{commonOptions: common}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			opts.help = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return ibstatusOptions{}, fmt.Errorf("ibstatus: unsupported option %q", arg)
		}
		parts := strings.Split(arg, ":")
		if len(parts) > 2 || strings.TrimSpace(parts[0]) == "" {
			return ibstatusOptions{}, fmt.Errorf("ibstatus: invalid device selector %q", arg)
		}
		if len(parts) == 2 {
			if _, err := positivePort(parts[1]); err != nil {
				return ibstatusOptions{}, fmt.Errorf("ibstatus: %w", err)
			}
		}
		opts.selectors = append(opts.selectors, arg)
	}
	return opts, nil
}

func parseIBVDevInfo(args []string) (ibvDevInfoOptions, error) {
	common, args, err := parseNode(args)
	if err != nil {
		return ibvDevInfoOptions{}, fmt.Errorf("ibv_devinfo: %w", err)
	}
	opts := ibvDevInfoOptions{commonOptions: common}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			opts.help = true
		case arg == "-l" || arg == "--list":
			opts.list = true
		case arg == "-v" || arg == "--verbose":
			opts.verbose = true
		case arg == "-d" || arg == "--ib-dev":
			if i+1 >= len(args) {
				return ibvDevInfoOptions{}, errors.New("ibv_devinfo: option requires an argument -- 'd'")
			}
			i++
			opts.hca = args[i]
		case strings.HasPrefix(arg, "--ib-dev="):
			opts.hca = strings.TrimPrefix(arg, "--ib-dev=")
		case arg == "-i" || arg == "--ib-port":
			if i+1 >= len(args) {
				return ibvDevInfoOptions{}, errors.New("ibv_devinfo: option requires an argument -- 'i'")
			}
			i++
			opts.port, err = positivePort(args[i])
			if err != nil {
				return ibvDevInfoOptions{}, fmt.Errorf("ibv_devinfo: %w", err)
			}
		case strings.HasPrefix(arg, "--ib-port="):
			opts.port, err = positivePort(strings.TrimPrefix(arg, "--ib-port="))
			if err != nil {
				return ibvDevInfoOptions{}, fmt.Errorf("ibv_devinfo: %w", err)
			}
		default:
			return ibvDevInfoOptions{}, fmt.Errorf("ibv_devinfo: invalid option -- %q", arg)
		}
	}
	return opts, nil
}

func positivePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 {
		return 0, fmt.Errorf("invalid port %q", value)
	}
	return port, nil
}

func collectAndSelect(ctx context.Context, r runner.Runner, node string) ([]Device, error) {
	devices, local, err := collect(ctx, r)
	if err != nil {
		return nil, err
	}
	if node != "" {
		selected := devices[:0]
		for _, device := range devices {
			if device.Node == node {
				selected = append(selected, device)
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("no RDMA device found for node %q", node)
		}
		return selected, nil
	}
	if !local && len(devices) > 1 {
		// rdma-core commands describe one Linux host. A host-side gpu command
		// therefore defaults to the first lab worker; --node selects another.
		firstNode := devices[0].Node
		selected := devices[:0]
		for _, device := range devices {
			if device.Node == firstNode {
				selected = append(selected, device)
			}
		}
		devices = selected
	}
	if len(devices) == 0 {
		return nil, errors.New("No IB devices found")
	}
	return devices, nil
}

func collect(ctx context.Context, r runner.Runner) ([]Device, bool, error) {
	if endpoint := strings.TrimSpace(os.Getenv("GPU_LAB_STATE_URL")); endpoint != "" {
		devices, err := collectState(ctx, endpoint)
		return devices, true, err
	}
	result, err := monitoring.New(r).Query(ctx, metricSelector)
	if err != nil {
		return nil, false, fmt.Errorf("unable to read synthetic RDMA telemetry: %w", err)
	}
	byKey := make(map[string]*Device)
	for _, sample := range result.Samples {
		node := sample.Metric["node"]
		hca := sample.Metric["hca"]
		portText := sample.Metric["port"]
		port, err := strconv.Atoi(portText)
		if node == "" || hca == "" || err != nil || port < 1 {
			continue
		}
		key := node + "\x00" + hca + "\x00" + portText
		device := byKey[key]
		if device == nil {
			device = &Device{Node: node, HCA: hca, Port: port, LinkLayer: "InfiniBand", State: "ACTIVE", PhysicalState: "LINK_UP", Up: 1, RateGbps: 200}
			byKey[key] = device
		}
		if layer := sample.Metric["link_layer"]; layer != "" {
			device.LinkLayer = layer
		}
		switch sample.Metric["__name__"] {
		case "gpu_lab_ib_port_up":
			device.Up = int(sample.Value)
		case "gpu_lab_ib_port_state":
			if state := sample.Metric["state"]; state != "" {
				device.State = state
			}
			if physical := sample.Metric["physical_state"]; physical != "" {
				device.PhysicalState = physical
			}
		case "gpu_lab_ib_link_rate_gbps":
			device.RateGbps = sample.Value
		}
	}
	devices := make([]Device, 0, len(byKey))
	for _, device := range byKey {
		devices = append(devices, withIdentity(*device))
	}
	sortDevices(devices)
	return devices, false, nil
}

type stateSnapshot struct {
	Node   string `json:"node"`
	Fabric struct {
		HCA           string  `json:"hca"`
		Port          int     `json:"port"`
		LinkLayer     string  `json:"link_layer"`
		PortUp        int     `json:"port_up"`
		State         string  `json:"state"`
		PhysicalState string  `json:"physical_state"`
		LinkRateGbps  float64 `json:"link_rate_gbps"`
	} `json:"fabric"`
}

func collectState(ctx context.Context, endpoint string) ([]Device, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid GPU_LAB_STATE_URL %q", endpoint)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("query synthetic RDMA state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("state endpoint returned %s", response.Status)
	}
	var snapshot stateSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode synthetic RDMA state: %w", err)
	}
	if snapshot.Node == "" || snapshot.Fabric.HCA == "" || snapshot.Fabric.Port < 1 {
		return nil, errors.New("state endpoint did not report an RDMA device")
	}
	device := Device{Node: snapshot.Node, HCA: snapshot.Fabric.HCA, Port: snapshot.Fabric.Port, LinkLayer: snapshot.Fabric.LinkLayer, State: snapshot.Fabric.State, PhysicalState: snapshot.Fabric.PhysicalState, Up: snapshot.Fabric.PortUp, RateGbps: snapshot.Fabric.LinkRateGbps}
	return []Device{withIdentity(device)}, nil
}

func withIdentity(device Device) Device {
	ordinal := nodeOrdinal(device.Node)
	device.NodeGUID = 0xa088c2030088b300 | uint64(ordinal*4)
	device.SystemGUID = device.NodeGUID
	device.PortGUID = device.NodeGUID + uint64(device.Port)
	if device.Up == 0 || strings.EqualFold(device.State, "DOWN") {
		device.BaseLID = 0
		device.SMLID = 0
	} else {
		device.BaseLID = ordinal + 1
		device.SMLID = 1
	}
	return device
}

func nodeOrdinal(node string) int {
	switch node {
	case "gpu-lab-worker":
		return 1
	case "gpu-lab-worker2":
		return 2
	case "gpu-lab-worker3":
		return 3
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(node))
	return int(h.Sum32()%4095) + 1
}

func sortDevices(devices []Device) {
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Node != devices[j].Node {
			return devices[i].Node < devices[j].Node
		}
		if devices[i].HCA != devices[j].HCA {
			return devices[i].HCA < devices[j].HCA
		}
		return devices[i].Port < devices[j].Port
	})
}

func filterDevice(devices []Device, hca string, port int) []Device {
	selected := make([]Device, 0, len(devices))
	for _, device := range devices {
		if hca != "" && device.HCA != hca {
			continue
		}
		if port > 0 && device.Port != port {
			continue
		}
		selected = append(selected, device)
	}
	return selected
}

func runIBStat(w io.Writer, devices []Device, opts ibstatOptions) error {
	port := 0
	if opts.port != "" {
		port, _ = strconv.Atoi(opts.port)
	}
	devices = filterDevice(devices, opts.hca, port)
	if len(devices) == 0 {
		return errors.New("ibstat: CA or port not found")
	}
	if opts.listCAs {
		for _, name := range uniqueHCAs(devices) {
			fmt.Fprintln(w, name)
		}
		return nil
	}
	if opts.listPorts {
		for _, device := range devices {
			fmt.Fprintln(w, guidHex(device.PortGUID))
		}
		return nil
	}
	for i, device := range devices {
		if i > 0 {
			fmt.Fprintln(w)
		}
		printIBStatDevice(w, device, opts.short)
	}
	return nil
}

func printIBStatDevice(w io.Writer, device Device, short bool) {
	fmt.Fprintf(w, "CA '%s'\n", device.HCA)
	fmt.Fprintf(w, "\tCA type: %s\n", caType)
	fmt.Fprintln(w, "\tNumber of ports: 1")
	fmt.Fprintf(w, "\tFirmware version: %s\n", firmwareVersion)
	fmt.Fprintln(w, "\tHardware version: 0")
	fmt.Fprintf(w, "\tNode GUID: %s\n", guidHex(device.NodeGUID))
	fmt.Fprintf(w, "\tSystem image GUID: %s\n", guidHex(device.SystemGUID))
	if short {
		return
	}
	fmt.Fprintf(w, "\tPort %d:\n", device.Port)
	fmt.Fprintf(w, "\t\tState: %s\n", ibstatState(device.State))
	fmt.Fprintf(w, "\t\tPhysical state: %s\n", ibstatPhysical(device.PhysicalState))
	fmt.Fprintf(w, "\t\tRate: %s\n", rateNumber(device.RateGbps))
	fmt.Fprintf(w, "\t\tBase lid: %d\n", device.BaseLID)
	fmt.Fprintln(w, "\t\tLMC: 0")
	fmt.Fprintf(w, "\t\tSM lid: %d\n", device.SMLID)
	fmt.Fprintf(w, "\t\tCapability mask: %s\n", capabilityMask)
	fmt.Fprintf(w, "\t\tPort GUID: %s\n", guidHex(device.PortGUID))
	fmt.Fprintf(w, "\t\tLink layer: %s\n", device.LinkLayer)
}

func runIBStatus(w io.Writer, devices []Device, opts ibstatusOptions) error {
	if len(opts.selectors) > 0 {
		selected := make([]Device, 0, len(devices))
		for _, selector := range opts.selectors {
			parts := strings.Split(selector, ":")
			port := 0
			if len(parts) == 2 {
				port, _ = strconv.Atoi(parts[1])
			}
			selected = append(selected, filterDevice(devices, parts[0], port)...)
		}
		devices = selected
	}
	if len(devices) == 0 {
		return errors.New("ibstatus: no matching InfiniBand device")
	}
	for i, device := range devices {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Infiniband device '%s' port %d status:\n", device.HCA, device.Port)
		fmt.Fprintf(w, "\tdefault gid:\t%s\n", gid(device.PortGUID))
		fmt.Fprintf(w, "\tbase lid:\t0x%x\n", device.BaseLID)
		fmt.Fprintf(w, "\tsm lid:\t\t0x%x\n", device.SMLID)
		fmt.Fprintf(w, "\tstate:\t\t%d: %s\n", stateNumber(device.State), strings.ToUpper(device.State))
		fmt.Fprintf(w, "\tphys state:\t%d: %s\n", physicalNumber(device.PhysicalState), ibstatusPhysical(device.PhysicalState))
		fmt.Fprintf(w, "\trate:\t\t%s Gb/sec (%s)\n", rateNumber(device.RateGbps), rateMode(device.RateGbps))
		fmt.Fprintf(w, "\tlink_layer:\t%s\n", device.LinkLayer)
	}
	return nil
}

func runIBVDevInfo(w io.Writer, devices []Device, opts ibvDevInfoOptions) error {
	devices = filterDevice(devices, opts.hca, opts.port)
	if len(devices) == 0 {
		if opts.hca != "" {
			return fmt.Errorf("IB device '%s' wasn't found", opts.hca)
		}
		return errors.New("No IB devices found")
	}
	if opts.list {
		names := uniqueHCAs(devices)
		plural := ""
		if len(names) != 1 {
			plural = "s"
		}
		fmt.Fprintf(w, "%d HCA%s found:\n", len(names), plural)
		for _, name := range names {
			fmt.Fprintf(w, "\t%s\n", name)
		}
		fmt.Fprintln(w)
		return nil
	}
	for _, device := range devices {
		printIBVDevice(w, device, opts.verbose)
	}
	return nil
}

func printIBVDevice(w io.Writer, device Device, verbose bool) {
	fmt.Fprintf(w, "hca_id:\t%s\n", device.HCA)
	fmt.Fprintln(w, "\ttransport:\t\t\tInfiniBand (0)")
	fmt.Fprintf(w, "\tfw_ver:\t\t\t\t%s\n", firmwareVersion)
	fmt.Fprintf(w, "\tnode_guid:\t\t\t%s\n", guidColon(device.NodeGUID))
	fmt.Fprintf(w, "\tsys_image_guid:\t\t\t%s\n", guidColon(device.SystemGUID))
	fmt.Fprintln(w, "\tvendor_id:\t\t\t0x02c9")
	fmt.Fprintln(w, "\tvendor_part_id:\t\t\t4129")
	fmt.Fprintln(w, "\thw_ver:\t\t\t\t0x0")
	fmt.Fprintf(w, "\tboard_id:\t\t\t%s\n", boardID)
	fmt.Fprintln(w, "\tphys_port_cnt:\t\t\t1")
	if verbose {
		fmt.Fprintln(w, "\tmax_mr_size:\t\t\t0xffffffffffffffff")
		fmt.Fprintln(w, "\tpage_size_cap:\t\t\t0xfffff000")
		fmt.Fprintln(w, "\tmax_qp:\t\t\t\t262144")
		fmt.Fprintln(w, "\tmax_qp_wr:\t\t\t32768")
		fmt.Fprintln(w, "\tdevice_cap_flags:\t\t0x00000000")
		fmt.Fprintln(w, "\tmax_sge:\t\t\t30")
		fmt.Fprintln(w, "\tmax_cq:\t\t\t\t16777216")
		fmt.Fprintln(w, "\tmax_cqe:\t\t\t4194303")
		fmt.Fprintln(w, "\tmax_mr:\t\t\t\t16777216")
		fmt.Fprintln(w, "\tmax_pd:\t\t\t\t16777216")
		fmt.Fprintln(w, "\tatomic_cap:\t\t\tATOMIC_HCA (1)")
	}
	fmt.Fprintf(w, "\t\tport:\t%d\n", device.Port)
	fmt.Fprintf(w, "\t\t\tstate:\t\t\t%s (%d)\n", ibvState(device.State), stateNumber(device.State))
	fmt.Fprintln(w, "\t\t\tmax_mtu:\t\t4096 (5)")
	fmt.Fprintln(w, "\t\t\tactive_mtu:\t\t4096 (5)")
	fmt.Fprintf(w, "\t\t\tsm_lid:\t\t\t%d\n", device.SMLID)
	fmt.Fprintf(w, "\t\t\tport_lid:\t\t%d\n", device.BaseLID)
	fmt.Fprintln(w, "\t\t\tport_lmc:\t\t0x00")
	fmt.Fprintf(w, "\t\t\tlink_layer:\t\t%s\n", device.LinkLayer)
	if verbose {
		width, widthCode, speedCode := rateVerbs(device.RateGbps)
		fmt.Fprintln(w, "\t\t\tmax_msg_sz:\t\t0x40000000")
		fmt.Fprintln(w, "\t\t\tport_cap_flags:\t\t0xa651e848")
		fmt.Fprintln(w, "\t\t\tport_cap_flags2:\t0x0000")
		fmt.Fprintln(w, "\t\t\tmax_vl_num:\t\t8 (4)")
		fmt.Fprintln(w, "\t\t\tbad_pkey_cntr:\t\t0x0")
		fmt.Fprintln(w, "\t\t\tqkey_viol_cntr:\t\t0x0")
		fmt.Fprintln(w, "\t\t\tsm_sl:\t\t\t0")
		fmt.Fprintln(w, "\t\t\tpkey_tbl_len:\t\t128")
		fmt.Fprintln(w, "\t\t\tgid_tbl_len:\t\t128")
		fmt.Fprintf(w, "\t\t\tactive_width:\t\t%s (%d)\n", width, widthCode)
		fmt.Fprintf(w, "\t\t\tactive_speed:\t\t%s Gbps (%d)\n", rateDecimal(device.RateGbps), speedCode)
		fmt.Fprintf(w, "\t\t\teffective_speed:\t%.1f Gbps\n", device.RateGbps)
		fmt.Fprintf(w, "\t\t\tphys_state:\t\t%s (%d)\n", ibvPhysical(device.PhysicalState), physicalNumber(device.PhysicalState))
		fmt.Fprintf(w, "\t\t\tGID[  0]:\t\t%s\n", gid(device.PortGUID))
	}
	fmt.Fprintln(w)
}

func uniqueHCAs(devices []Device) []string {
	seen := make(map[string]bool)
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		if !seen[device.HCA] {
			seen[device.HCA] = true
			names = append(names, device.HCA)
		}
	}
	sort.Strings(names)
	return names
}

func guidHex(value uint64) string { return fmt.Sprintf("0x%016x", value) }

func guidColon(value uint64) string {
	return fmt.Sprintf("%04x:%04x:%04x:%04x", uint16(value>>48), uint16(value>>32), uint16(value>>16), uint16(value))
}

func gid(portGUID uint64) string { return "fe80:0000:0000:0000:" + guidColon(portGUID) }

func ibstatState(state string) string {
	switch strings.ToUpper(state) {
	case "ACTIVE":
		return "Active"
	case "INIT":
		return "Initializing"
	case "ARMED":
		return "Armed"
	default:
		return "Down"
	}
}

func ibvState(state string) string {
	switch strings.ToUpper(state) {
	case "ACTIVE":
		return "PORT_ACTIVE"
	case "INIT":
		return "PORT_INIT"
	case "ARMED":
		return "PORT_ARMED"
	default:
		return "PORT_DOWN"
	}
}

func stateNumber(state string) int {
	switch strings.ToUpper(state) {
	case "ACTIVE":
		return 4
	case "ARMED":
		return 3
	case "INIT":
		return 2
	default:
		return 1
	}
}

func ibstatPhysical(state string) string {
	switch strings.ToUpper(state) {
	case "LINK_UP":
		return "LinkUp"
	case "POLLING":
		return "Polling"
	default:
		return "Disabled"
	}
}

func ibstatusPhysical(state string) string {
	switch strings.ToUpper(state) {
	case "LINK_UP":
		return "LinkUp"
	case "POLLING":
		return "Polling"
	default:
		return "Disabled"
	}
}

func ibvPhysical(state string) string {
	switch strings.ToUpper(state) {
	case "LINK_UP":
		return "LINK_UP"
	case "POLLING":
		return "POLLING"
	default:
		return "DISABLED"
	}
}

func physicalNumber(state string) int {
	switch strings.ToUpper(state) {
	case "LINK_UP":
		return 5
	case "POLLING":
		return 2
	default:
		return 3
	}
}

func rateNumber(rate float64) string  { return strconv.FormatFloat(rate, 'f', -1, 64) }
func rateDecimal(rate float64) string { return strconv.FormatFloat(rate, 'f', 1, 64) }

func rateMode(rate float64) string {
	switch {
	case rate >= 200:
		return "4X HDR"
	case rate >= 100:
		return "4X EDR"
	case rate >= 50:
		return "2X HDR"
	case rate >= 25:
		return "1X EDR"
	case rate >= 10:
		return "1X QDR"
	default:
		return "1X SDR"
	}
}

func rateVerbs(rate float64) (string, int, int) {
	switch {
	case rate >= 200:
		return "4X", 2, 256
	case rate >= 100:
		return "4X", 2, 128
	case rate >= 50:
		return "2X", 16, 64
	case rate >= 25:
		return "1X", 1, 32
	case rate >= 10:
		return "1X", 1, 8
	default:
		return "1X", 1, 1
	}
}

const ibstatHelp = `Usage: ibstat [options] [<ca_name> [port_num]]

Options:
  -l, --list_of_cas      list all IB devices
  -p, --port_list        show port GUIDs
  -s, --short            short output
  -v, --verbose          verbose output
      --node <node>      GPU Lab extension: select a synthetic worker node
`

const ibstatusHelp = `Usage: ibstatus [<device[:port]> ...]

Display basic status of InfiniBand device(s).
      --node <node>      GPU Lab extension: select a synthetic worker node
`

const ibvDevInfoHelp = `Usage: ibv_devinfo             print the ca attributes

Options:
  -d, --ib-dev=<dev>     use IB device <dev> (default all devices)
  -i, --ib-port=<port>   use port <port> of IB device (default all ports)
  -l, --list             print only the IB devices names
  -v, --verbose          print all the attributes of the IB device(s)
      --node <node>      GPU Lab extension: select a synthetic worker node
`
