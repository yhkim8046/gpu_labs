package verification

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the actual Go and shell queries in the Prometheus query engine.
// String assertions cannot detect evaluation errors such as metric-label
// collisions, missing vectors, or stale input. CI provides its pinned promtool.
func TestPromQLRegression(t *testing.T) {
	tool := os.Getenv("PROMTOOL_PATH")
	if tool == "" {
		var err error
		tool, err = exec.LookPath("promtool")
		if err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatal("CI must provide promtool via PROMTOOL_PATH or PATH")
			}
			t.Skip("promtool is not installed; set PROMTOOL_PATH (required in CI)")
		}
	}
	type sample struct {
		Labels string  `json:"labels"`
		Value  float64 `json:"value"`
	}
	type expression struct {
		Expr    string   `json:"expr"`
		Time    string   `json:"eval_time"`
		Samples []sample `json:"exp_samples"`
	}
	type series struct {
		Name   string `json:"series"`
		Values string `json:"values"`
	}
	type testCase struct {
		Name        string       `json:"name"`
		Interval    string       `json:"interval"`
		Input       []series     `json:"input_series"`
		Expressions []expression `json:"promql_expr_test"`
	}
	var cases []testCase
	for _, phase := range []struct {
		name                                            string
		temperature, utilization, power, used, throttle int
	}{
		{"baseline", 45, 15, 80, 20, 0},
		{"warning", 82, 88, 210, 60, 0},
		{"critical", 96, 62, 245, 74, 1},
		{"recovery", 45, 15, 80, 20, 0},
	} {
		var input []series
		for node := range 3 {
			for gpu := range 8 {
				for metric, value := range map[string]int{
					"temperature_celsius": phase.temperature, "utilization_percent": phase.utilization,
					"power_watts": phase.power, "memory_used_bytes": phase.used,
					"memory_total_bytes": 100, "throttle_active": phase.throttle,
				} {
					input = append(input, series{fmt.Sprintf(`gpu_lab_gpu_%s{node="node-%d",gpu="gpu-%d"}`, metric, node, gpu), fmt.Sprintf("%d+0x60", value)})
				}
			}
			input = append(input, series{fmt.Sprintf(`gpu_lab_scenario_phase{node="node-%d",scenario="thermal-escalation",phase="%s",generation="108"}`, node, phase.name), "1+0x60"})
		}
		query := thermalTimelineQuery("108")
		cases = append(cases, testCase{Name: phase.name, Interval: "1s", Input: input, Expressions: []expression{{query, "1s", []sample{{"{}", 3}}}}})
		if phase.name != "baseline" {
			continue
		}
		missing := append([]series(nil), input[1:]...)
		cases = append(cases, testCase{Name: "missing GPU reading fails", Interval: "1s", Input: missing, Expressions: []expression{{query, "1s", []sample{}}}})
		nan := append([]series(nil), input...)
		nan[0].Values = "NaN NaN"
		cases = append(cases, testCase{Name: "non-finite GPU reading fails", Interval: "1s", Input: nan, Expressions: []expression{{query, "1s", []sample{}}}})
		stale := append([]series(nil), input...)
		original, _, _ := strings.Cut(stale[0].Values, "+")
		stale[0].Values = original + " " + original
		cases = append(cases, testCase{Name: "one stale GPU reading fails", Interval: "1s", Input: stale, Expressions: []expression{{query, "45s", []sample{}}}})
		cases = append(cases, testCase{Name: "previous scenario generation fails", Interval: "1s", Input: input, Expressions: []expression{{thermalTimelineQuery("109"), "1s", []sample{}}}})
	}
	helper, err := filepath.Abs(filepath.Join("..", "..", "test", "e2e", "common.sh"))
	if err != nil {
		t.Fatal(err)
	}
	selector := "gpu_lab_gpu_memory_used_bytes\ngpu_lab_gpu_memory_total_bytes"
	memory := `max(abs(100 * gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes - 20)) < 0.000001`
	command := exec.Command("bash", "-c", `source "$1"; gpu_e2e_guarded_query "$2" "$3" 30`, "promql-test", helper, selector, memory)
	query, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generate E2E query: %v: %s", err, query)
	}
	memoryInput := []series{{`gpu_lab_gpu_memory_used_bytes{node="n",gpu="0"}`, "20+0x60"}, {`gpu_lab_gpu_memory_total_bytes{node="n",gpu="0"}`, "100+0x60"}}
	cases = append(cases, testCase{Name: "multi-metric E2E freshness has no label collision", Interval: "1s", Input: memoryInput, Expressions: []expression{{string(query), "1s", []sample{{"{}", 0}}}}})
	memoryInput = append([]series(nil), memoryInput...)
	memoryInput[0].Values = "20 20"
	cases = append(cases, testCase{Name: "multi-metric E2E freshness rejects stale samples", Interval: "1s", Input: memoryInput, Expressions: []expression{{string(query), "45s", []sample{}}}})
	// Recent samples from a deleted workload are fresh by age alone. They
	// must not establish the initial progress baseline for its replacement.
	command = exec.Command("bash", "-c", `source "$1"; GPU_LAB_E2E_SAMPLES_SINCE=10 gpu_e2e_guarded_query "$2" "$3" 30`, "promql-test", helper, selector, memory)
	query, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("generate workload-scoped query: %v: %s", err, query)
	}
	cases = append(cases, testCase{Name: "recent previous workload is rejected", Interval: "1s", Input: memoryInput, Expressions: []expression{{string(query), "12s", []sample{}}}})
	currentWorkload := []series{{`gpu_lab_gpu_memory_used_bytes{node="n",gpu="0"}`, "20+0x60"}, {`gpu_lab_gpu_memory_total_bytes{node="n",gpu="0"}`, "100+0x60"}}
	cases = append(cases, testCase{Name: "replacement workload samples accepted", Interval: "1s", Input: currentWorkload, Expressions: []expression{{string(query), "12s", []sample{{"{}", 0}}}}})
	data, err := json.MarshalIndent(map[string]any{"evaluation_interval": "1s", "tests": cases}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "promql.json")
	if err := os.WriteFile(fixture, data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(tool, "test", "rules", fixture).CombinedOutput()
	if err != nil {
		t.Fatalf("Prometheus query regression: %v\n%s", err, output)
	}
	t.Logf("%d query-engine regression cases passed", len(cases))
}
