package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNvidiaSMIResolverSkipsShimAndFindsLaterGenuineTool(t *testing.T) {
	root := t.TempDir()
	shellShim := filepath.Join(root, "shim-wrapper", "nvidia-smi")
	versionShim := filepath.Join(root, "shim-version", "nvidia-smi")
	genuine := filepath.Join(root, "vendor", "nvidia-smi")
	writeExecutable(t, shellShim, "#!/bin/sh\nexec gpu nvidia-smi \"$@\"\n")
	writeExecutable(t, versionShim, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, genuine, "#!/bin/sh\nexit 0\n")
	var calls []string
	d := testDependencies(t, map[string]string{"PATH": filepath.Dir(shellShim) + string(os.PathListSeparator) + filepath.Dir(versionShim) + string(os.PathListSeparator) + filepath.Dir(genuine)}, func(_ context.Context, path string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, path+" "+strings.Join(args, " "))
		if path == versionShim {
			return []byte("NVIDIA-SMI synthetic compatibility layer (GPU Lab)"), nil, nil
		}
		if path == genuine {
			return []byte("NVIDIA-SMI 570.00 Driver Version: 570.00"), nil, nil
		}
		return nil, nil, errors.New("unexpected candidate was executed")
	})

	tool, err := FindNvidiaSMI(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Path != genuine {
		t.Fatalf("selected %q, want genuine tool %q", tool.Path, genuine)
	}
	if !reflect.DeepEqual(calls, []string{versionShim + " --version", genuine + " --version"}) {
		t.Fatalf("probe calls = %#v", calls)
	}
}

func TestNvidiaSMIResolverRejectsOverrideShimAndSelfSymlink(t *testing.T) {
	root := t.TempDir()
	self := filepath.Join(root, "gpu")
	link := filepath.Join(root, "nvidia-smi")
	writeExecutable(t, self, "self")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	called := false
	d := testDependencies(t, map[string]string{"GPU_LAB_NVIDIA_SMI": link, "PATH": root}, func(context.Context, string, ...string) ([]byte, []byte, error) {
		called = true
		return nil, nil, nil
	})
	d.Executable = func() (string, error) { return self, nil }
	if _, err := FindNvidiaSMI(context.Background(), d); err == nil {
		t.Fatal("self symlink was accepted as nvidia-smi")
	}
	if called {
		t.Fatal("self symlink was executed")
	}

	shim := filepath.Join(root, "wrapper")
	writeExecutable(t, shim, "#!/bin/sh\nexec gpu nvidia-smi \"$@\"\n")
	d.LookupEnv = func(key string) string {
		if key == "GPU_LAB_NVIDIA_SMI" {
			return shim
		}
		return ""
	}
	d.Executable = func() (string, error) { return "", nil }
	if _, err := FindNvidiaSMI(context.Background(), d); err == nil || !strings.Contains(err.Error(), "shim") {
		t.Fatalf("override shim error = %v", err)
	}
}

func TestGenericResolverSkipsRecursiveWrappersForIBAndDCGMI(t *testing.T) {
	root := t.TempDir()
	shimDir := filepath.Join(root, "shim")
	realDir := filepath.Join(root, "real")
	for _, command := range []string{"ibstat", "dcgmi"} {
		invoker := "gpu"
		if command == "ibstat" {
			invoker = "/opt/gpu-lab"
		}
		writeExecutable(t, filepath.Join(shimDir, command), "#!/bin/sh\nexec "+invoker+" "+command+" \"$@\"\n")
		writeExecutable(t, filepath.Join(realDir, command), "vendor tool")
	}
	d := testDependencies(t, map[string]string{"PATH": shimDir + string(os.PathListSeparator) + realDir}, nil)
	for _, command := range []string{"ibstat", "dcgmi"} {
		path, err := FindExecutable(command, d)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(realDir, command) {
			t.Errorf("%s selected %q, want later genuine candidate", command, path)
		}
	}
}

func TestCompiledGPULabShimIsSkippedForLaterNativeExecutable(t *testing.T) {
	root := findModuleRoot(t)
	work := t.TempDir()
	shimDir := filepath.Join(work, "shim")
	realDir := filepath.Join(work, "real")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	compiledShim := filepath.Join(shimDir, "ibstat")
	build := exec.Command("go", "build", "-o", compiledShim, "./cmd/ibstat")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build GPU Lab ibstat shim: %v\n%s", err, output)
	}
	genuine := filepath.Join(realDir, "ibstat")
	writeExecutable(t, genuine, "vendor tool")
	d := testDependencies(t, map[string]string{"PATH": shimDir + string(os.PathListSeparator) + realDir}, nil)
	path, err := FindExecutable("ibstat", d)
	if err != nil {
		t.Fatal(err)
	}
	if path != genuine {
		t.Fatalf("selected %q, want later genuine executable %q", path, genuine)
	}
}

func TestAutoDoesNotFallBackAfterAuthenticToolProbeOrCollectionFailure(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "nvidia-smi")
	writeExecutable(t, tool, "vendor tool")
	env := map[string]string{"PATH": root, "GPU_LAB_STATE_URL": "http://gpu-lab.test/api/v1/state"}
	for _, test := range []struct {
		name string
		run  func(context.Context, string, ...string) ([]byte, []byte, error)
		want string
	}{
		{
			name: "version probe failure",
			run: func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
				return nil, nil, errors.New("probe timed out")
			},
			want: "failed --version",
		},
		{
			name: "real collection failure",
			run: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
				if len(args) > 0 && args[0] == "--version" {
					return []byte("NVIDIA-SMI 570.00"), nil, nil
				}
				return nil, nil, errors.New("driver unavailable")
			},
			want: "collect real GPU data",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := testDependencies(t, env, test.run)
			report, err := Inspect(context.Background(), Options{Mode: ModeAuto}, d)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Inspect error = %v, want %q", err, test.want)
			}
			if report.Mode != ModeReal {
				t.Fatalf("mode = %q, want real", report.Mode)
			}
			if len(report.Checks) == 0 || strings.Contains(report.Source, "synthetic") {
				t.Fatalf("report unexpectedly fell back to synthetic: %#v", report)
			}
		})
	}
}

func TestAutoWithoutHardwareOrLabReportsUnavailable(t *testing.T) {
	d := testDependencies(t, map[string]string{"PATH": ""}, nil)
	report, err := Inspect(context.Background(), Options{Mode: ModeAuto}, d)
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if report.Mode != ModeAuto || len(report.Checks) != 1 || report.Checks[0].Status != StatusUnavailable {
		t.Fatalf("report = %#v", report)
	}
}

func TestInvalidInspectionOptionsDoNotProbe(t *testing.T) {
	calls := 0
	d := testDependencies(t, map[string]string{"PATH": ""}, func(context.Context, string, ...string) ([]byte, []byte, error) {
		calls++
		return nil, nil, nil
	})
	for _, options := range []Options{{Mode: "other"}, {Mode: ModeAuto, Timeout: -time.Second}} {
		if _, err := Inspect(context.Background(), options, d); err == nil {
			t.Errorf("Inspect(%#v) succeeded", options)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid options caused %d probes", calls)
	}
}

func TestStateSnapshotMissingHealthRemainsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"node":"gpu-node-01","readings":[{"gpu":"gpu-node-01-00","xid_code":0,"ecc_dbe_total":4,"temperature_celsius":65,"utilization_percent":10,"memory_used_bytes":12,"memory_total_bytes":100}]}`)
	}))
	defer server.Close()
	d := testDependencies(t, map[string]string{"GPU_LAB_STATE_URL": server.URL, "PATH": ""}, nil)
	d.HTTPClient = server.Client()
	d.Now = func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }
	report, err := Inspect(context.Background(), Options{Mode: ModeSynthetic}, d)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkByName(report, "gpu-node-01/gpu-node-01-00 health").Status; got != StatusUnavailable {
		t.Fatalf("missing health status = %q", got)
	}
	if got := checkByName(report, "gpu-node-01/gpu-node-01-00 Xid").Status; got != StatusPass {
		t.Fatalf("present Xid status = %q", got)
	}
	if got := checkByName(report, "gpu-node-01/gpu-node-01-00 aggregate uncorrected ECC (lifetime history)").Status; got != StatusWarn {
		t.Fatalf("positive cumulative ECC status = %q", got)
	}
	if !strings.Contains(checkByName(report, "snapshot timing").Detail, "retrieval-time snapshot") {
		t.Fatalf("state snapshot timing was not labeled: %#v", checkByName(report, "snapshot timing"))
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, readErr := os.ReadFile(filepath.Join(directory, "go.mod"))
		if readErr == nil && strings.Contains(string(data), "module github.com/gpu-lab/gpu-lab\n") {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find repository module root")
		}
		directory = parent
	}
}

func TestDedicatedKubeconfigEvidenceUsesExplicitReadOnlyContext(t *testing.T) {
	home := t.TempDir()
	kubeconfig := filepath.Join(home, ".kube", "gpu-lab.config")
	if err := os.MkdirAll(filepath.Dir(kubeconfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	var args []string
	d := testDependencies(t, map[string]string{"PATH": ""}, func(_ context.Context, name string, commandArgs ...string) ([]byte, []byte, error) {
		if name != "kubectl" {
			t.Fatalf("command = %q", name)
		}
		args = append(args, commandArgs...)
		return []byte(`{"items":[{"metadata":{"labels":{"gpu.lab/type":"fake"},"annotations":{}}}]}`), nil, nil
	})
	d.HomeDir = func() (string, error) { return home, nil }
	evidence, err := detectSyntheticEvidence(context.Background(), d)
	if err != nil || evidence.Source == "" {
		t.Fatalf("evidence = %#v, err=%v", evidence, err)
	}
	want := []string{"--kubeconfig", kubeconfig, "--context", "gpu-lab", "get", "nodes", "-o", "json"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("kubectl args = %#v, want %#v", args, want)
	}
}

func TestPrometheusFreshnessUsesScrapeTimestampAndRejectsStaleNaNOrMissing(t *testing.T) {
	query := prometheusInspectionQuery()
	if strings.Count(query, "timestamp(") != len(inspectionMetrics) || strings.Contains(query, `__name__=~`) {
		t.Fatalf("query does not use separate scrape-age probes: %s", query)
	}
	rows := []map[string]any{}
	add := func(field, value string) {
		rows = append(rows, map[string]any{
			"metric": map[string]string{"node": "node-01", "gpu": "gpu-00", "inspection_field": field},
			"value":  []any{float64(9999999999), value},
		})
	}
	add("health", "1")
	add("health_age", "120.1")
	add("xid", "NaN")
	add("xid_age", "0.1")
	add("temperature", "65")
	add("utilization", "10")
	add("utilization_age", "0.1")
	add("memory_used", "12")
	add("memory_used_age", "0.1")
	add("memory_total", "100")
	add("memory_total_age", "0.1")
	data, err := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": rows}})
	if err != nil {
		t.Fatal(err)
	}
	readings, err := parsePrometheusInspection(data)
	if err != nil {
		t.Fatal(err)
	}
	report := NewReport(ModeSynthetic, ModeSynthetic, "Prometheus")
	addSyntheticGPUChecks(&report, readings[0], true)
	if got := checkByName(report, "node-01/gpu-00 health").Status; got != StatusUnavailable {
		t.Fatalf("stale health status = %q", got)
	}
	if got := checkByName(report, "node-01/gpu-00 Xid").Status; got != StatusUnavailable {
		t.Fatalf("NaN Xid status = %q", got)
	}
	if got := checkByName(report, "node-01/gpu-00 aggregate uncorrected ECC (lifetime history)").Status; got != StatusUnavailable {
		t.Fatalf("missing ECC status = %q", got)
	}
	if got := checkByName(report, "node-01/gpu-00 temperature").Status; got != StatusUnavailable {
		t.Fatalf("missing temperature age status = %q", got)
	}
	if got := checkByName(report, "node-01/gpu-00 utilization").Status; got != StatusPass {
		t.Fatalf("fresh utilization status = %q", got)
	}
}

func TestRealXMLSupportsLegacyAndModernECCAndMissingIdentity(t *testing.T) {
	fixture := `<nvidia_smi_log><driver_version>570.00</driver_version>
<gpu id="0000:65:00.0"><product_name>GPU A</product_name><uuid>GPU-a</uuid><pci><pci_bus_id>0000:65:00.0</pci_bus_id></pci>
<temperature><gpu_temp>42 C</gpu_temp></temperature><utilization><gpu_util>17 %</gpu_util></utilization><fb_memory_usage><total>80 MiB</total><used>20 MiB</used></fb_memory_usage>
<ecc_errors><volatile><double_bit><total>2</total></double_bit></volatile><aggregate><double_bit><aggregate>7</aggregate></double_bit></aggregate></ecc_errors></gpu>
<gpu><uuid>GPU-b</uuid><ecc_errors><volatile><sram_uncorrectable>1</sram_uncorrectable><dram_uncorrectable>0</dram_uncorrectable></volatile><aggregate><sram_uncorrectable><total>3</total></sram_uncorrectable><dram_uncorrectable><aggregate>0</aggregate></dram_uncorrectable></aggregate></ecc_errors></gpu>
<gpu><ecc_errors/></gpu></nvidia_smi_log>`
	parsed, err := parseSMI([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	report := NewReport(ModeReal, ModeReal, "fixture")
	addRealChecks(&report, parsed)
	if checkByName(report, "GPU 0 volatile uncorrected ECC (since last driver load)").Status != StatusWarn || checkByName(report, "GPU 0 aggregate uncorrected ECC (lifetime history)").Status != StatusWarn {
		t.Fatalf("legacy ECC checks = %#v", report.Checks)
	}
	modernVolatile := checkByName(report, "GPU 1 volatile uncorrected ECC (since last driver load)")
	modernAggregate := checkByName(report, "GPU 1 aggregate uncorrected ECC (lifetime history)")
	if modernVolatile.Status != StatusWarn || modernVolatile.Value != "SRAM=1, DRAM=0" || modernAggregate.Status != StatusWarn || modernAggregate.Value != "SRAM=3, DRAM=0" {
		t.Fatalf("modern ECC checks = %#v / %#v", modernVolatile, modernAggregate)
	}
	if got := checkByName(report, "GPU 2 identity").Status; got != StatusUnavailable {
		t.Fatalf("missing identity status = %q", got)
	}
	if got := checkByName(report, "GPU 2 volatile uncorrected ECC (since last driver load)").Status; got != StatusUnavailable {
		t.Fatalf("missing ECC status = %q", got)
	}
}

func TestRealXMLNANAndNAAreUnavailableOrUnsupported(t *testing.T) {
	fixture := `<nvidia_smi_log><driver_version>N/A</driver_version><gpu><product_name>N/A</product_name>
<temperature><gpu_temp>N/A °C</gpu_temp></temperature><utilization><gpu_util>NaN %</gpu_util></utilization>
<fb_memory_usage><total>100 MiB</total><used>N/A</used></fb_memory_usage>
<ecc_errors><volatile><double_bit><total>NaN</total></double_bit></volatile></ecc_errors></gpu></nvidia_smi_log>`
	parsed, err := parseSMI([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	report := NewReport(ModeReal, ModeReal, "fixture")
	addRealChecks(&report, parsed)
	if got := checkByName(report, "driver version").Status; got != StatusUnsupported {
		t.Errorf("driver N/A status = %q", got)
	}
	if got := checkByName(report, "GPU 0 identity").Status; got != StatusUnavailable {
		t.Errorf("identity N/A status = %q", got)
	}
	if got := checkByName(report, "GPU 0 temperature").Status; got != StatusUnsupported {
		t.Errorf("temperature N/A status = %q", got)
	}
	if got := checkByName(report, "GPU 0 utilization").Status; got != StatusUnavailable {
		t.Errorf("utilization NaN status = %q", got)
	}
	if got := checkByName(report, "GPU 0 memory used / total").Status; got != StatusUnsupported {
		t.Errorf("memory N/A status = %q", got)
	}
	if got := checkByName(report, "GPU 0 volatile uncorrected ECC (since last driver load)").Status; got != StatusUnavailable {
		t.Errorf("ECC NaN status = %q", got)
	}
}

func TestDCGMIEmptyListingIsUnavailable(t *testing.T) {
	root := t.TempDir()
	dcgmi := filepath.Join(root, "dcgmi")
	writeExecutable(t, dcgmi, "dcgmi")
	d := testDependencies(t, map[string]string{"PATH": root}, func(_ context.Context, path string, args ...string) ([]byte, []byte, error) {
		if path != dcgmi || !reflect.DeepEqual(args, []string{"discovery", "-l"}) {
			t.Fatalf("DCGMI command = %q %#v", path, args)
		}
		return nil, nil, nil
	})
	report := NewReport(ModeReal, ModeReal, "fixture")
	addDCGMICheck(context.Background(), &report, d)
	check := checkByName(report, "DCGM discovery")
	if check.Status != StatusUnavailable {
		t.Fatalf("empty listing check = %#v", check)
	}
}

func TestBoundedCommandWaitDelayClosesInheritedPipes(t *testing.T) {
	t.Setenv("GPU_INSPECTION_PIPE_HELPER", "parent")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := runBoundedCommand(ctx, os.Args[0], "-test.run=^TestInspectionPipeHelper$", "--")
	if err == nil {
		t.Fatal("expected context timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded command waited %s for an inherited pipe", elapsed)
	}
}

func TestInspectionPipeHelper(t *testing.T) {
	switch os.Getenv("GPU_INSPECTION_PIPE_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestInspectionPipeHelper$")
		child.Env = []string{"GPU_INSPECTION_PIPE_HELPER=child"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "child":
		time.Sleep(2 * time.Second)
		os.Exit(0)
	default:
		return
	}
}

func TestForwardNativePreservesArgumentsAndExitCode(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("GPU_INSPECTION_FORWARD_CAPTURE", capture)
	args := []string{"-test.run=^TestForwardNativeHelper$", "--", "--query-gpu=temperature.gpu,memory.used", "--format=csv,noheader,nounits", "one argument with spaces"}
	var stdout, stderr strings.Builder
	err := ForwardNative(context.Background(), os.Args[0], args, &stdout, &stderr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("forwarded exit = %v, want 23", err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read forwarded arguments: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := args[2:]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forwarded args = %#v, want %#v", got, want)
	}
}

func TestForwardNativeHelper(t *testing.T) {
	capture := os.Getenv("GPU_INSPECTION_FORWARD_CAPTURE")
	if capture == "" {
		return
	}
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		os.Exit(3)
	}
	data, err := json.Marshal(os.Args[separator+1:])
	if err != nil || os.WriteFile(capture, data, 0o600) != nil {
		os.Exit(4)
	}
	os.Exit(23)
}

func testDependencies(t *testing.T, environment map[string]string, run func(context.Context, string, ...string) ([]byte, []byte, error)) Dependencies {
	t.Helper()
	home := t.TempDir()
	d := Dependencies{
		LookupEnv: func(key string) string { return environment[key] },
		HomeDir:   func() (string, error) { return home, nil },
		Executable: func() (string, error) {
			return "", nil
		},
		RunCommand: run,
		HTTPClient: &http.Client{Timeout: time.Second},
		Now:        time.Now,
	}
	if run == nil {
		d.RunCommand = func(context.Context, string, ...string) ([]byte, []byte, error) {
			return nil, nil, errors.New("unexpected command")
		}
	}
	return d
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func checkByName(report Report, name string) Check {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return Check{Name: name, Status: StatusUnavailable, Detail: "missing check"}
}
