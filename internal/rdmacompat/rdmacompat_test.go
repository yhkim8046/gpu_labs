package rdmacompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/testutil"
)

func stateClient(state, physical string, up int, rate float64) *http.Client {
	return testutil.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"node":"gpu-lab-worker2","fabric":{"hca":"mlx5_0","port":1,"link_layer":"InfiniBand","port_up":%d,"state":%q,"physical_state":%q,"link_rate_gbps":%g}}`, up, state, physical, rate)
	}))
}

func runWithState(t *testing.T, tool Tool, args []string, client *http.Client) string {
	t.Helper()
	t.Setenv("GPU_LAB_STATE_URL", "http://gpu-lab.test/api/v1/state")
	var output bytes.Buffer
	if err := run(context.Background(), runner.New(io.Discard, io.Discard), tool, args, &output, client); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

// TestStateEndpointErrorMessages pins the exact user-facing error wording that
// predates the shared stateclient refactor, including the "ibstat: " command
// prefix added by commandError and the RDMA-specific state messages.
func TestStateEndpointErrorMessages(t *testing.T) {
	const sentinel = "injected transport failure"
	tests := []struct {
		name       string
		endpoint   string
		transport  http.RoundTripper
		want       string
		wantPrefix bool
	}{
		{
			name:     "invalid GPU_LAB_STATE_URL",
			endpoint: "/relative/state",
			transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("state endpoint reached with an invalid URL")
				return nil, errors.New("unexpected request")
			}),
			want: "ibstat: invalid GPU_LAB_STATE_URL \"/relative/state\"",
		},
		{
			name:     "non-2xx status",
			endpoint: "http://gpu-lab.test/api/v1/state",
			transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Status:     "503 Service Unavailable",
					Body:       io.NopCloser(strings.NewReader("unavailable")),
				}, nil
			}),
			want: "ibstat: state endpoint returned 503 Service Unavailable",
		},
		{
			name:     "malformed JSON",
			endpoint: "http://gpu-lab.test/api/v1/state",
			transport: testutil.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "not json")
			})).Transport,
			want:       "ibstat: decode synthetic RDMA state: ",
			wantPrefix: true,
		},
		{
			name:     "transport error",
			endpoint: "http://gpu-lab.test/api/v1/state",
			transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New(sentinel)
			}),
			// http.Client wraps RoundTripper errors with the request URL, exactly
			// as it did before the shared stateclient refactor.
			want: "ibstat: query synthetic RDMA state: Get \"http://gpu-lab.test/api/v1/state\": " + sentinel,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GPU_LAB_STATE_URL", test.endpoint)
			client := &http.Client{Transport: test.transport}
			var output bytes.Buffer
			err := run(context.Background(), runner.New(io.Discard, io.Discard), IBStat, nil, &output, client)
			if err == nil {
				t.Fatal("expected an error")
			}
			if test.wantPrefix {
				if !strings.HasPrefix(err.Error(), test.want) {
					t.Fatalf("error = %q, want prefix %q", err.Error(), test.want)
				}
				return
			}
			if err.Error() != test.want {
				t.Fatalf("error = %q, want %q", err.Error(), test.want)
			}
		})
	}
}

func TestIBStatMatchesOperationalLayoutAndDownState(t *testing.T) {
	client := stateClient("DOWN", "DISABLED", 0, 200)
	output := runWithState(t, IBStat, nil, client)
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
	client := stateClient("ACTIVE", "LINK_UP", 1, 200)
	output := runWithState(t, IBStatus, []string{"mlx5_0:1"}, client)
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
	client := stateClient("ACTIVE", "LINK_UP", 1, 25)
	output := runWithState(t, IBVDevInfo, []string{"-d", "mlx5_0", "-i", "1", "-v"}, client)
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

// TestIBStatSolePortAndListPrecedence pins the ibstat layout against upstream
// infiniband-diags/ibstat.c (rdma-core v56.0). A positional CA name plus port
// number enters ca_stat()'s alone mode: the header gains a colon and
// port_dump(alone=1) prints the Port header and every field unindented.
// Because the alone branch requires !no_ports, "-s <ca> <port>" falls back to
// the regular ca_dump() header block instead, which also omits the colon. A
// bare CA name keeps the colonless ca_dump() header with a two-tab port
// section. The --port_list branch runs before --list_of_cas exactly as
// upstream's main() checks list_ports before list_only, and both list modes
// ignore the positional port argument like umad_get_ca_portguids() does.
// The fixture node is gpu-lab-worker2 (ordinal 2): Node/System GUID
// 0xa088c2030088b308, Port GUID 0xa088c2030088b309, Base lid 3, SM lid 1.
func TestIBStatSolePortAndListPrecedence(t *testing.T) {
	client := stateClient("ACTIVE", "LINK_UP", 1, 200)

	const nodeGUID = "0xa088c2030088b308"
	const portGUID = "0xa088c2030088b309"

	sole := runWithState(t, IBStat, []string{"mlx5_0", "1"}, client)
	if sole != "CA: 'mlx5_0'\n"+
		"Port 1:\n"+
		"State: Active\n"+
		"Physical state: LinkUp\n"+
		"Rate: 200\n"+
		"Base lid: 3\n"+
		"LMC: 0\n"+
		"SM lid: 1\n"+
		"Capability mask: 0xa759e848\n"+
		"Port GUID: "+portGUID+"\n"+
		"Link layer: InfiniBand\n" {
		t.Fatalf("ibstat mlx5_0 1 =\n%s", sole)
	}

	short := runWithState(t, IBStat, []string{"mlx5_0", "1", "-s"}, client)
	if short != "CA 'mlx5_0'\n"+
		"\tCA type: MT4129\n"+
		"\tNumber of ports: 1\n"+
		"\tFirmware version: 28.43.2026\n"+
		"\tHardware version: 0\n"+
		"\tNode GUID: "+nodeGUID+"\n"+
		"\tSystem image GUID: "+nodeGUID+"\n" {
		t.Fatalf("ibstat mlx5_0 1 -s =\n%s", short)
	}

	regular := runWithState(t, IBStat, []string{"mlx5_0"}, client)
	if regular != "CA 'mlx5_0'\n"+
		"\tCA type: MT4129\n"+
		"\tNumber of ports: 1\n"+
		"\tFirmware version: 28.43.2026\n"+
		"\tHardware version: 0\n"+
		"\tNode GUID: "+nodeGUID+"\n"+
		"\tSystem image GUID: "+nodeGUID+"\n"+
		"\tPort 1:\n"+
		"\t\tState: Active\n"+
		"\t\tPhysical state: LinkUp\n"+
		"\t\tRate: 200\n"+
		"\t\tBase lid: 3\n"+
		"\t\tLMC: 0\n"+
		"\t\tSM lid: 1\n"+
		"\t\tCapability mask: 0xa759e848\n"+
		"\t\tPort GUID: "+portGUID+"\n"+
		"\t\tLink layer: InfiniBand\n" {
		t.Fatalf("ibstat mlx5_0 =\n%s", regular)
	}

	// List modes ignore the positional port argument the way upstream
	// handles list_ports/list_only before any port lookup: port 99 does not
	// exist, yet the CA-scoped GUID list still prints.
	for _, args := range [][]string{{"-l", "-p"}, {"-p"}, {"-p", "mlx5_0", "99"}, {"-p", "mlx5_0"}} {
		if got := runWithState(t, IBStat, args, client); got != portGUID+"\n" {
			t.Fatalf("ibstat %v = %q, want the port GUID only", args, got)
		}
	}
	for _, args := range [][]string{{"-l"}, {"-l", "mlx5_0", "99"}} {
		if got := runWithState(t, IBStat, args, client); got != "mlx5_0\n" {
			t.Fatalf("ibstat %v = %q, want the CA name only", args, got)
		}
	}
	// -s maps to ca_stat()'s no_ports flag, so upstream never consults the
	// port number: even port 99 yields the CA header instead of an error.
	shortUnknownPort := runWithState(t, IBStat, []string{"-s", "mlx5_0", "99"}, client)
	if shortUnknownPort != short {
		t.Fatalf("ibstat -s mlx5_0 99 =\n%s", shortUnknownPort)
	}
}

func TestListAndSelectionOptions(t *testing.T) {
	client := stateClient("ACTIVE", "LINK_UP", 1, 200)
	if got := runWithState(t, IBStat, []string{"-l"}, client); got != "mlx5_0\n" {
		t.Fatalf("ibstat -l = %q", got)
	}
	if got := runWithState(t, IBVDevInfo, []string{"-l"}, client); got != "1 HCA found:\n\tmlx5_0\n\n" {
		t.Fatalf("ibv_devinfo -l = %q", got)
	}
	if got := runWithState(t, IBStat, []string{"mlx5_0", "1", "-s"}, client); strings.Contains(got, "Port 1:") {
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
