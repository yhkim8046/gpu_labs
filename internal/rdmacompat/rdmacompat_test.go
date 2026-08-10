package rdmacompat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func stateServer(t *testing.T, state, physical string, up int, rate float64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"node":"gpu-lab-worker2","fabric":{"hca":"mlx5_0","port":1,"link_layer":"InfiniBand","port_up":%d,"state":%q,"physical_state":%q,"link_rate_gbps":%g}}`, up, state, physical, rate)
	}))
	t.Cleanup(server.Close)
	return server
}

func runWithState(t *testing.T, tool Tool, args []string, server *httptest.Server) string {
	t.Helper()
	t.Setenv("GPU_LAB_STATE_URL", server.URL)
	var output bytes.Buffer
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), tool, args, &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestIBStatMatchesOperationalLayoutAndDownState(t *testing.T) {
	server := stateServer(t, "DOWN", "DISABLED", 0, 200)
	output := runWithState(t, IBStat, nil, server)
	for _, expected := range []string{
		"CA 'mlx5_0'\n",
		"\tCA type: MT4129\n",
		"\tNumber of ports: 1\n",
		"\tFirmware version: 28.43.2026\n",
		"\tPort 1:\n",
		"\t\tState: Down\n",
		"\t\tPhysical state: Disabled\n",
		"\t\tRate: 200\n",
		"\t\tBase lid: 0\n",
		"\t\tSM lid: 0\n",
		"\t\tLink layer: InfiniBand\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("ibstat output missing %q:\n%s", expected, output)
		}
	}
}

func TestIBStatusMatchesRDMACoreLayout(t *testing.T) {
	server := stateServer(t, "ACTIVE", "LINK_UP", 1, 200)
	output := runWithState(t, IBStatus, []string{"mlx5_0:1"}, server)
	for _, expected := range []string{
		"Infiniband device 'mlx5_0' port 1 status:\n",
		"\tdefault gid:\tfe80:0000:0000:0000:",
		"\tbase lid:\t0x3\n",
		"\tsm lid:\t\t0x1\n",
		"\tstate:\t\t4: ACTIVE\n",
		"\tphys state:\t5: LinkUp\n",
		"\trate:\t\t200 Gb/sec (4X HDR)\n",
		"\tlink_layer:\tInfiniBand\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("ibstatus output missing %q:\n%s", expected, output)
		}
	}
}

func TestIBVDevInfoReflectsDegradedRateAndVerbosePhysicalState(t *testing.T) {
	server := stateServer(t, "ACTIVE", "LINK_UP", 1, 25)
	output := runWithState(t, IBVDevInfo, []string{"-d", "mlx5_0", "-i", "1", "-v"}, server)
	for _, expected := range []string{
		"hca_id:\tmlx5_0\n",
		"\ttransport:\t\t\tInfiniBand (0)\n",
		"\tvendor_id:\t\t\t0x02c9\n",
		"\tvendor_part_id:\t\t\t4129\n",
		"\t\tport:\t1\n",
		"\t\t\tstate:\t\t\tPORT_ACTIVE (4)\n",
		"\t\t\tactive_width:\t\t1X (1)\n",
		"\t\t\tactive_speed:\t\t25.0 Gbps (32)\n",
		"\t\t\teffective_speed:\t25.0 Gbps\n",
		"\t\t\tphys_state:\t\tLINK_UP (5)\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("ibv_devinfo output missing %q:\n%s", expected, output)
		}
	}
}

func TestListAndSelectionOptions(t *testing.T) {
	server := stateServer(t, "ACTIVE", "LINK_UP", 1, 200)
	if got := runWithState(t, IBStat, []string{"-l"}, server); got != "mlx5_0\n" {
		t.Fatalf("ibstat -l = %q", got)
	}
	if got := runWithState(t, IBVDevInfo, []string{"-l"}, server); got != "1 HCA found:\n\tmlx5_0\n\n" {
		t.Fatalf("ibv_devinfo -l = %q", got)
	}
	if got := runWithState(t, IBStat, []string{"mlx5_0", "1", "-s"}, server); strings.Contains(got, "Port 1:") {
		t.Fatalf("ibstat -s unexpectedly printed port details: %q", got)
	}
	if err := Run(context.Background(), runner.New(io.Discard, io.Discard), IBVDevInfo, []string{"-i", "0"}, io.Discard); err == nil {
		t.Fatal("ibv_devinfo accepted port zero")
	}
}

func TestIdentityIsStableAcrossCommands(t *testing.T) {
	device := withIdentity(Device{Node: "gpu-lab-worker3", HCA: "mlx5_0", Port: 1, Up: 1, State: "ACTIVE"})
	if guidHex(device.NodeGUID) != "0xa088c2030088b30c" || guidColon(device.NodeGUID) != "a088:c203:0088:b30c" {
		t.Fatalf("unexpected deterministic GUID: %s %s", guidHex(device.NodeGUID), guidColon(device.NodeGUID))
	}
	if device.BaseLID != 4 || device.SMLID != 1 {
		t.Fatalf("unexpected LIDs: base=%d sm=%d", device.BaseLID, device.SMLID)
	}
}
