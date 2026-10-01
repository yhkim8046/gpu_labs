package verification

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// normalScenarioYAML is the on-disk normal fixture shape used by the active
// scenario gate.
const normalScenarioYAML = "apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: normal\nspec:\n  description: baseline\n  actions: []\n"

// nowUnix keeps the fake's sample ingestion timestamps in sync with the
// freshness gates' time.Now comparison.
func nowUnix() float64 { return float64(time.Now().Unix()) }

func promVector(value string) string {
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%v,"%s"]}]}}`, nowUnix(), value)
}

// promSeries builds a raw instant-vector response with count samples that all
// carry value and the given ingestion timestamp.
func promSeries(count int, value string, timestamp float64) string {
	entries := make([]string, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, fmt.Sprintf(`{"metric":{"series":"%d"},"value":[%v,"%s"]}`, i, timestamp, value))
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(entries, ","))
}

const promEmpty = `{"status":"success","data":{"resultType":"vector","result":[]}}`

type fakeCommand struct {
	t              *testing.T
	scenarioYAML   string
	controlJSON    string
	controlErr     error
	byQuery        func(query string) string
	stepObservings int
	stepSequence   []string
	queries        int
}

func (f *fakeCommand) Output(ctx context.Context, name string, args ...string) (string, error) {
	if name != "kubectl" {
		f.t.Fatalf("unexpected command %q", name)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "get") && strings.Contains(joined, "configmap") && strings.Contains(joined, "gpu-lab-scenario") {
		return f.scenarioYAML, nil
	}
	if strings.Contains(joined, "gpu-lab-training-control") {
		if f.controlErr != nil {
			return "", f.controlErr
		}
		return f.controlJSON, nil
	}
	if strings.Contains(joined, "--raw") {
		idx := -1
		for i, a := range args {
			if a == "--raw" {
				idx = i + 1
				break
			}
		}
		raw := args[idx]
		f.queries++
		query := rawQuery(raw)
		// The progress gate reads the aggregated max(); the freshness gate
		// reads the raw series. Keep the two paths independent.
		if strings.HasPrefix(query, `max(gpu_lab_training_step)`) && f.stepSequence != nil {
			value := f.stepSequence[f.stepObservings]
			if f.stepObservings < len(f.stepSequence)-1 {
				f.stepObservings++
			}
			return promVector(value), nil
		}
		return f.byQuery(query), nil
	}
	f.t.Fatalf("unexpected kubectl invocation: %s", joined)
	return "", nil
}

func rawQuery(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return parsed.Query().Get("query")
}

// ageQuery builds the PromQL the freshness gate uses to read the ORIGINAL
// scrape time via the timestamp() function.
func ageQuery(selector string) string { return "time() - timestamp(" + selector + ")" }

// baselineQueryResponder serves a fully recovered lab: gauges at baseline,
// fault counters still readable, and every raw freshness series with a fresh
// original-scrape age from the timestamp() function. The overrides hook lets a
// test break exactly one gate.
func baselineQueryResponder(overrides func(query string) (string, bool)) func(string) string {
	return func(query string) string {
		if overrides != nil {
			if body, ok := overrides(query); ok {
				return body
			}
		}
		// Original-scrape-age queries must be matched BEFORE the raw shape
		// queries, since they embed the same selector.
		if strings.HasPrefix(query, "time() - timestamp(") {
			switch {
			case strings.Contains(query, "gpu_lab_scenario_info"):
				return promSeries(3, "8", nowUnix())
			case strings.Contains(query, "up{service="):
				return promSeries(3, "6", nowUnix())
			case strings.Contains(query, "gpu_lab_training_step"):
				return promSeries(3, "3", nowUnix())
			}
		}
		switch {
		case strings.HasPrefix(query, `count(gpu_lab_scenario_info{scenario="normal"})`):
			return promVector("3")
		case query == `gpu_lab_scenario_info{scenario="normal"}`:
			return promSeries(3, "1", nowUnix())
		case query == `up{service="dcgm-exporter"}`:
			return promSeries(3, "1", nowUnix())
		case query == `gpu_lab_training_step`:
			return promSeries(3, "10", nowUnix())
		case strings.Contains(query, "gpu_lab_ib_port_up"):
			return promVector("1")
		case strings.Contains(query, "gpu_lab_ib_port_state"):
			return promVector("1")
		case strings.Contains(query, "gpu_lab_ib_link_rate_gbps") && strings.HasPrefix(query, "max("):
			return promVector("200")
		case strings.Contains(query, "gpu_lab_fabric_delay_seconds") && strings.HasPrefix(query, "max("):
			return promVector("0")
		case strings.HasPrefix(query, `count(gpu_lab_training_rank_up)`):
			return promVector("3")
		case strings.HasPrefix(query, `sum(gpu_lab_training_rank_up)`):
			return promVector("3")
		case strings.HasPrefix(query, `sum(gpu_lab_training_fabric_delay_seconds)`):
			return promVector("0")
		default:
			// every *_total counter readable check uses count(...)
			return promVector("1")
		}
	}
}

func newRecoveryVerifier(f CommandRunner) RecoveryVerifier {
	return RecoveryVerifier{Runner: f, Timeout: 2 * time.Second, PollInterval: 25 * time.Millisecond, ProgressWindow: time.Second}
}

// Healthy recovery: baseline gauges restored, fault counters still present and
// non-zero, control recovered, samples fresh, and step advancing must pass.
func TestVerifyRecoveryPassesWhenGaugesRestoreAndCountersPersist(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery:      baselineQueryResponder(nil),
		stepSequence: []string{"10", "11"},
	}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "rdma-retry-storm")
	if !report.Passed() {
		for _, c := range report.Checks {
			if !c.Passed {
				t.Fatalf("expected PASS, check %q failed: %s", c.Name, c.Detail)
			}
		}
		t.Fatal("expected recovery to pass")
	}
	for _, c := range report.Checks {
		if strings.Contains(c.Name, "readable") && !strings.Contains(c.Detail, "count=") {
			t.Fatalf("counter check %q should report a count, got %q", c.Name, c.Detail)
		}
	}
}

// A missing series (empty vector) must fail, never be treated as zero-pass.
func TestVerifyRecoveryFailsClosedOnMissingSeries(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery: baselineQueryResponder(func(query string) (string, bool) {
			if strings.HasPrefix(query, `max(gpu_lab_ib_link_rate_gbps`) {
				return promEmpty, true
			}
			return "", false
		}),
		stepSequence: []string{"10", "11"},
	}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "")
	for _, c := range report.Checks {
		if strings.HasSuffix(c.Name, "link rate") {
			if c.Passed {
				t.Fatal("missing rate series must not pass")
			}
			if !strings.Contains(strings.ToLower(c.Detail), "no sample") {
				t.Fatalf("missing series detail should explain no-sample: %q", c.Detail)
			}
			if report.Passed() {
				t.Fatal("overall report must fail when a required series is missing")
			}
			return
		}
	}
	t.Fatal("link rate check was not evaluated")
}

// An exporter that never reloaded the normal scenario keeps publishing the
// fault, so the aggregated adoption gate fails.
func TestVerifyRecoveryRejectsStaleExporters(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery: baselineQueryResponder(func(query string) (string, bool) {
			if strings.HasPrefix(query, `count(gpu_lab_scenario_info{scenario="normal"})`) {
				return promVector("2"), true // one exporter still stale
			}
			return "", false
		}),
		stepSequence: []string{"10", "11"},
	}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "")
	for _, c := range report.Checks {
		if c.Name == "exporters adopted normal" && c.Passed {
			t.Fatal("stale exporter adoption must fail")
		}
	}
	if report.Passed() {
		t.Fatal("stale adoption must fail the report")
	}
}

// A dead target inside the 5-minute lookback still answers the raw shape
// query with correct values and an EVALUATION-time timestamp; only the
// timestamp() age query reveals that the original scrape is 300s old. The
// gate must fail on that age even though every value and shape check passes.
func TestVerifyRecoveryRejectsOldScrapesWithinLookback(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery: baselineQueryResponder(func(query string) (string, bool) {
			if query == ageQuery(`gpu_lab_scenario_info{scenario="normal"}`) {
				// evaluation timestamp now, but original scrape 300s ago
				return promSeries(3, "300", nowUnix()), true
			}
			return "", false
		}),
		stepSequence: []string{"10", "11"},
	}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "")
	found := false
	for _, c := range report.Checks {
		if c.Name == "scenario adoption samples fresh" {
			found = true
			if c.Passed {
				t.Fatal("a 300s-old original scrape must not pass the freshness gate")
			}
			if !strings.Contains(c.Detail, "stale") {
				t.Fatalf("freshness failure should explain staleness: %q", c.Detail)
			}
		}
	}
	if !found || report.Passed() {
		t.Fatal("old scrape age must fail the report")
	}
}

// A negative age (sample timestamp ahead of the evaluation clock) is a broken
// or future-dated series and must fail closed, not be read as fresh.
func TestFreshnessGateRejectsFutureSamples(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery: baselineQueryResponder(func(query string) (string, bool) {
			if query == ageQuery(`gpu_lab_training_step`) {
				return promSeries(3, "-5", nowUnix()), true
			}
			return "", false
		}),
		stepSequence: []string{"10", "11"},
	}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "")
	found := false
	for _, c := range report.Checks {
		if c.Name == "training step samples fresh" {
			found = true
			if c.Passed {
				t.Fatal("negative age must fail the freshness gate")
			}
			if !strings.Contains(c.Detail, "future") {
				t.Fatalf("future rejection should be explained: %q", c.Detail)
			}
		}
	}
	if !found || report.Passed() {
		t.Fatal("future samples must fail the report")
	}
}

// up=0 on any exporter scrape target means the series may be frozen history,
// so the freshness gate must fail even if every value gate passes.
func TestVerifyRecoveryRejectsDownedExporterTargets(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery: baselineQueryResponder(func(query string) (string, bool) {
			if query == `up{service="dcgm-exporter"}` {
				return promSeries(2, "1", nowUnix()) + strings.Repeat("", 0), true
			}
			return "", false
		}),
		stepSequence: []string{"10", "11"},
	}
	// Only two up series instead of three: a dead target drops out.
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "")
	found := false
	for _, c := range report.Checks {
		if c.Name == "exporter scrape targets up" {
			found = true
			if c.Passed {
				t.Fatal("missing exporter target must fail the freshness gate")
			}
		}
	}
	if !found || report.Passed() {
		t.Fatal("downed exporter targets must fail the report")
	}
}

// A stalled collective (step never increases) must fail the progress gate even
// if every other gauge and freshness gate looks normal.
func TestVerifyRecoveryRejectsStalledProgress(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery:      baselineQueryResponder(nil),
		stepSequence: []string{"42", "42"},
	}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "")
	found := false
	for _, c := range report.Checks {
		if c.Name == "training step progress" {
			found = true
			if c.Passed {
				t.Fatal("stalled progress must fail")
			}
		}
	}
	if !found || report.Passed() {
		t.Fatal("stalled progress must fail the report")
	}
}

// Fault-state scenario still active => immediate fail with remediation hint.
func TestVerifyRecoveryRequiresNormalActiveScenario(t *testing.T) {
	faultYAML := "apiVersion: gpu-lab.io/v1alpha1\nkind: Scenario\nmetadata:\n  name: rdma-retry-storm\nspec:\n  description: d\n  metrics:\n    rdma_retries_total: 250\n    rdma_timeouts_total: 8\n    fabric_delay_seconds: 1\n  actions: []\n"
	fake := &fakeCommand{t: t, scenarioYAML: faultYAML, controlJSON: `{"generation":1,"fabric_mode":"retries","fabric_target_node":"gpu-node-02","fabric_delay":"1s"}`, byQuery: baselineQueryResponder(nil), stepSequence: []string{"1", "2"}}
	report := newRecoveryVerifier(fake).VerifyRecovery(context.Background(), "rdma-retry-storm")
	if report.Passed() {
		t.Fatal("must fail while fault scenario is active")
	}
	if report.Checks[0].Passed {
		t.Fatal("first check should flag non-normal scenario")
	}
	if !strings.Contains(report.Checks[0].Detail, "scenario reset") {
		t.Fatalf("remediation hint missing: %q", report.Checks[0].Detail)
	}
}

// Control-plane recovery judgement: typed strict decoding, fail closed on
// anything the lab writer would never produce.
func TestEvaluateTrainingControl(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"normal", `{"generation":3,"fabric_mode":"normal"}`, true},
		{"omitted-mode", `{"generation":3}`, true},
		{"empty-object-is-normal", `{}`, true},
		{"null-cleared-fields", `{"generation":3,"straggler_rank":null,"crash_rank":null,"crash_token":null}`, true},
		{"zero-duration", `{"generation":3,"fabric_mode":"normal","fabric_delay":"0s"}`, true},
		{"leftover-straggler", `{"generation":3,"fabric_mode":"normal","straggler_rank":2,"straggler_delay":"2s"}`, false},
		{"straggler-rank-zero", `{"generation":3,"fabric_mode":"normal","straggler_rank":0}`, false},
		{"leftover-crash", `{"generation":3,"fabric_mode":"normal","crash_rank":1,"crash_token":"123"}`, false},
		{"active-fabric", `{"generation":3,"fabric_mode":"retries","fabric_target_node":"gpu-node-02","fabric_delay":"1s"}`, false},
		{"nonzero-delay-only", `{"generation":3,"fabric_delay":"2s"}`, false},
		{"bogus-delay", `{"generation":3,"fabric_delay":"bogus"}`, false},
		{"empty", ``, false},
		{"whitespace", `   `, false},
		{"literal-null", `null`, false},
		{"array", `[1,2]`, false},
		{"scalar", `123`, false},
		{"string", `"normal"`, false},
		{"invalid-json", `{"fabric_mode":`, false},
		{"truncated", `{"generation":3,"fabric_mode":"norm`, false},
		{"trailing-content", `{"fabric_mode":"normal"} {"fabric_mode":"normal"}`, false},
		{"trailing-garbage", `{"fabric_mode":"normal"} oops`, false},
		{"wrong-type-mode-number", `{"fabric_mode":123}`, false},
		{"wrong-type-generation-string", `{"generation":"abc"}`, false},
		{"wrong-type-target-list", `{"fabric_target_node":["gpu-node-02"]}`, false},
		{"unknown-field", `{"fabric_mode":"normal","fabric_mode_extra":"retries"}`, false},
	}
	for _, c := range cases {
		got := EvaluateTrainingControl([]byte(c.json))
		if got.Passed != c.want {
			t.Fatalf("%s: passed=%v want=%v detail=%q", c.name, got.Passed, c.want, got.Detail)
		}
	}
}

// Counter checks must never require the value to be zero.
func TestCounterChecksAcceptLargeValues(t *testing.T) {
	queries := FaultSignatureQueries("rdma-retry-storm")
	if len(queries) != 2 {
		t.Fatalf("want 2 counters, got %d", len(queries))
	}
	for _, q := range queries {
		if !q.CounterSeries {
			t.Fatalf("%s should be a counter check", q.Name)
		}
		if !strings.HasPrefix(q.Expr, "count(") {
			t.Fatalf("counter check must use count(...): %s", q.Expr)
		}
		if !q.Pass(250) || !q.Pass(8) {
			t.Fatal("counter predicate must accept accumulated values")
		}
		if q.Pass(0) {
			t.Fatal("counter predicate must fail only when the series is absent (count=0)")
		}
	}
}

// Gauges must reject non-finite samples.
func TestGaugePredicatesRejectNonFinite(t *testing.T) {
	g := recoveryGauge(func(v float64) bool { return v == 1 })
	if g(nan()) || !g(1) {
		t.Fatal("gauge predicate must reject NaN/Inf and accept 1")
	}
}

// The check table must contain exactly one adoption gate, per-worker gauges and
// training gates, and every raw freshness gate.
func TestRecoveryCheckTableShape(t *testing.T) {
	table := RecoveryChecks("ib-symbol-errors")
	adoption := 0
	for _, q := range table {
		if q.Name == "exporters adopted normal" {
			adoption++
		}
	}
	if adoption != 1 {
		t.Fatalf("want exactly one adoption gate, got %d", adoption)
	}
	if len(table) < 4*len(LabWorkerNodes) {
		t.Fatalf("expected per-worker baseline gates, table too small: %d", len(table))
	}
	fresh := FreshnessChecks()
	if len(fresh) != 3 {
		t.Fatalf("want up + adoption-age + step-age freshness gates, got %d", len(fresh))
	}
	for _, f := range fresh {
		if strings.HasPrefix(f.Expr, "count(") || strings.HasPrefix(f.Expr, "sum(") || strings.HasPrefix(f.Expr, "max(") {
			t.Fatalf("freshness gate must read raw series to keep timestamps: %s", f.Expr)
		}
	}
}

// The raw decoder records the EVALUATION timestamp from value[0] (it is not
// an original scrape time) and rejects missing, null, or non-parseable tokens.
func TestDecodePrometheusSamplesKeepsEvaluationTimestamp(t *testing.T) {
	body := `{"status":"success","data":{"result":[{"metric":{},"value":[1700.5,"3"]}]}}`
	samples, err := decodePrometheusSamples([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].value != 3 || samples[0].timestamp != 1700.5 {
		t.Fatalf("unexpected samples: %+v", samples)
	}
	// Bare-number value form (gateway deviation) is accepted.
	if _, err := decodePrometheusSamples([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[1700,3]}]}}`)); err != nil {
		t.Fatalf("bare-number form should decode: %v", err)
	}
	for name, body := range map[string]string{
		"missing-value-pair": `{"status":"success","data":{"result":[{"metric":{},"value":[1700]}]}}`,
		"null-value":         `{"status":"success","data":{"result":[{"metric":{},"value":[1700,null]}]}}`,
		"null-timestamp":     `{"status":"success","data":{"result":[{"metric":{},"value":[null,"1"]}]}}`,
		"nan-string":         `{"status":"success","data":{"result":[{"metric":{},"value":[1700,"NaN"]}]}}`,
		"string-nonsense":    `{"status":"success","data":{"result":[{"metric":{},"value":[1700,"abc"]}]}}`,
		"error-status":       `{"status":"error","data":{"result":[]}}`,
	} {
		if _, err := decodePrometheusSamples([]byte(body)); err == nil {
			t.Fatalf("%s must fail closed, got no error", name)
		}
	}
}

// Once the shared budget expires, the remaining gates must fail fast without
// issuing one more query each. A lab that never recovers under a tiny budget
// therefore stops issuing Prometheus queries instead of waiting its own
// timeout per check.
func TestPollStopsQueryingAfterBudgetExpiry(t *testing.T) {
	fake := &fakeCommand{
		t:            t,
		scenarioYAML: normalScenarioYAML,
		controlJSON:  `{"generation":7,"fabric_mode":"normal"}`,
		byQuery:      func(string) string { return promEmpty }, // nothing ever recovers
		stepSequence: []string{"1", "1"},
	}
	verifier := RecoveryVerifier{Runner: fake, Timeout: 60 * time.Millisecond, PollInterval: 5 * time.Millisecond, ProgressWindow: 60 * time.Millisecond}
	started := time.Now()
	report := verifier.VerifyRecovery(context.Background(), "")
	elapsed := time.Since(started)
	if report.Passed() {
		t.Fatal("a lab with no samples must never pass")
	}
	// One shared 60ms budget for the value and freshness gates plus a 60ms
	// progress window. If every check waited for its own timeout instead, the
	// run would take seconds and issue hundreds of queries.
	if elapsed > 4*time.Second {
		t.Fatalf("shared budget not honoured: run took %s", elapsed)
	}
	if fake.queries > 200 {
		t.Fatalf("run issued %d Prometheus queries; checks must stop querying once the budget is spent", fake.queries)
	}
}

func nan() float64 {
	var f float64
	var inf = f * 1e308
	return inf / inf
}
