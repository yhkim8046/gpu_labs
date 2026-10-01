package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/exporter"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
)

// Recovery verification answers a different question than fault verification.
//
//   - Verifier.Verify (gpu verify <scenario>) proves the cluster reached the
//     FAULT state a scenario declares.
//   - RecoveryVerifier.VerifyRecovery (gpu verify --recovery) proves the lab
//     is back in the NORMAL state after 'gpu scenario reset' and
//     'gpu training recover'.
//
// The two must never be conflated: a fault-state verify run before a reset is
// not recovery evidence, and a passing recovery run does not prove a fault
// was ever injected.
//
// Contract rules encoded here:
//
//  1. Recovery is judged on gauges (port up, negotiated state and rate,
//     injected fabric delay, scenario adoption, live training progress) and
//     on sample freshness. Cumulative *_total counters are deliberately NOT
//     required to return to zero; they are monotonic totals, and zeroing them
//     would be fake evidence. They are only checked for continued
//     readability.
//  2. A missing series fails. An empty Prometheus vector is never treated as
//     zero and never passes.
//  3. Stale data fails, within what Prometheus can prove. The value pair of
//     an instant query carries the EVALUATION time, not the original scrape
//     time, so age cannot be judged from it. An instant query can also keep
//     serving the last sample of a dead target for the whole 5-minute
//     lookback window, so a value-only check cannot prove freshness either.
//     Three independent gates close that gap as far as the lab can observe:
//     every dcgm-exporter scrape target must report up=1; for the adoption
//     and training series a dedicated query `time() - timestamp(<selector>)`
//     must answer with an age between 0 and maxSampleAge for every series
//     (the timestamp() function reads the ORIGINAL scrape timestamp, which is
//     what makes staleness observable at all); and gpu_lab_training_step must
//     be observed strictly increasing. This assumes the Prometheus server and
//     its scrape targets share a clock (they do inside one Docker Desktop
//     VM); clock skew is not measured separately.
//  4. NaN / non-finite samples fail.
//  5. The training control ConfigMap is parsed strictly with typed JSON
//     decoding: invalid, truncated, trailing-content, wrong-typed, or
//     unknown-field documents fail closed. Null or an absent fault field
//     counts as cleared; a well-formed empty object {} counts as normal.
//
// Everything is fail-closed: a query error, a missing series, a stale
// sample, or a stalled collective loop is a failed check, never a skipped
// one.
//
// Time budget: the value and freshness gates poll inside ONE shared budget
// (RecoveryVerifier.Timeout). They are evaluated sequentially, but the budget
// is global, so a lab that never recovers exits after roughly Timeout plus
// the progress window instead of Timeout times the number of checks. A
// healthy lab passes every gate on the first poll and exits in seconds.

// RecoveryExporterCount is the number of synthetic GPU exporters the lab
// ships, one per worker node. Every exporter must publish its series so a
// partially recovered set cannot pass.
const RecoveryExporterCount = 3

// RecoveryTrainingRanks is the default world size of the synthetic
// distributed-training StatefulSet.
const RecoveryTrainingRanks = 3

// LabWorkerNodes are the synthetic worker node names the fabric exporter uses.
var LabWorkerNodes = []string{"gpu-lab-worker", "gpu-lab-worker2", "gpu-lab-worker3"}

// Default timings. The exporter reloads scenario ConfigMaps on a 2s poll;
// fabric targets scrape every 15s and training targets every 5s, so
// maxSampleAge at 6x the fabric interval tolerates one missed scrape before
// a check turns stale.
const (
	defaultRecoveryBudget = 120 * time.Second
	defaultRecoveryPoll   = 2 * time.Second
	defaultProgressWindow = 30 * time.Second
	maxSampleAge          = 90 * time.Second
)

const (
	trainingControlConfigMap = "gpu-lab-training-control"
	trainingControlKey       = "control.json"
	trainingStepExpr         = `max(gpu_lab_training_step)`
	exporterUpExpr           = `up{service="dcgm-exporter"}`
	adoptionSeriesExpr       = `gpu_lab_scenario_info{scenario="normal"}`
)

// KnownFabricFaults lists the fabric incidents whose signature recovery is
// checked. The names match the built-in scenario files.
var KnownFabricFaults = []string{
	"ib-link-down",
	"ib-rate-degraded",
	"ib-symbol-errors",
	"rdma-retry-storm",
	"ib-congestion",
}

// IsKnownFabricFault reports whether name is a fabric fault supported by
// recovery verification.
func IsKnownFabricFault(name string) bool {
	for _, known := range KnownFabricFaults {
		if known == name {
			return true
		}
	}
	return false
}

// CommandRunner is the narrow command surface recovery verification needs. It
// keeps the recovery contract unit-testable without a cluster.
type CommandRunner interface {
	Output(ctx context.Context, name string, args ...string) (string, error)
}

// RecoveryVerifier judges whether the lab returned to the normal state.
type RecoveryVerifier struct {
	Runner CommandRunner
	// Timeout is the SHARED budget for all value and freshness gates.
	Timeout time.Duration
	// PollInterval is the delay between repolls of a gate that has not yet
	// passed.
	PollInterval time.Duration
	// ProgressWindow is the dedicated window for observing a strict
	// gpu_lab_training_step increase. It is not taken from Timeout so a
	// budget consumed by waiting gates still leaves room for the progress
	// gate.
	ProgressWindow time.Duration
}

// NewRecoveryVerifier builds a verifier with the lab's default timing. The
// runner is any command executor, including runner.Runner.
func NewRecoveryVerifier(r CommandRunner) RecoveryVerifier {
	return RecoveryVerifier{Runner: r, Timeout: defaultRecoveryBudget, PollInterval: defaultRecoveryPoll, ProgressWindow: defaultProgressWindow}
}

func (v RecoveryVerifier) budget() time.Duration {
	if v.Timeout <= 0 {
		return defaultRecoveryBudget
	}
	return v.Timeout
}

func (v RecoveryVerifier) pollInterval() time.Duration {
	if v.PollInterval <= 0 {
		return defaultRecoveryPoll
	}
	return v.PollInterval
}

func (v RecoveryVerifier) progressWindow() time.Duration {
	if v.ProgressWindow <= 0 {
		return defaultProgressWindow
	}
	return v.ProgressWindow
}

// recoveryQuery is one declarative recovery check: a PromQL expression plus
// the predicate the observed value must satisfy. Keeping the tables pure makes
// the contract testable without a cluster.
type recoveryQuery struct {
	Name     string
	Expr     string
	Pass     func(float64) bool
	Expected string
	// CounterSeries marks an informational check on a cumulative counter.
	// Such a check passes when the series exists; it never asserts the value
	// is zero.
	CounterSeries bool
}

// recoveryGauge guards a predicate against non-finite samples so NaN or Inf
// can never satisfy a numeric equality by accident.
func recoveryGauge(pred func(float64) bool) func(float64) bool {
	return func(value float64) bool {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		return pred(value)
	}
}

// finitePositive passes when the sample exists and is a positive finite
// number, which is how the count(...) expressions prove series presence.
func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

// BaselineFabricQueries returns the gauge queries every worker must satisfy
// for the fabric to count as recovered.
func BaselineFabricQueries(node string) []recoveryQuery {
	return []recoveryQuery{
		{
			Name:     node + " port up",
			Expr:     fabricMetric("gpu_lab_ib_port_up", node),
			Pass:     recoveryGauge(func(v float64) bool { return v == 1 }),
			Expected: "port gauge is back to up=1",
		},
		{
			Name:     node + " negotiated state",
			Expr:     fabricStateMetric(node, "ACTIVE", "LINK_UP"),
			Pass:     recoveryGauge(func(v float64) bool { return v == 1 }),
			Expected: `state ACTIVE / physical_state LINK_UP series is published`,
		},
		{
			Name:     node + " link rate",
			Expr:     fabricMetric("gpu_lab_ib_link_rate_gbps", node),
			Pass:     recoveryGauge(func(v float64) bool { return v == exporter.DefaultIBLinkRateGbps }),
			Expected: fmt.Sprintf("negotiated rate gauge is back to %g Gbps", exporter.DefaultIBLinkRateGbps),
		},
		{
			Name:     node + " injected fabric delay",
			Expr:     fabricMetric("gpu_lab_fabric_delay_seconds", node),
			Pass:     recoveryGauge(func(v float64) bool { return v == 0 }),
			Expected: "injected fabric delay gauge is back to 0",
		},
	}
}

// FaultSignatureQueries returns the per-fault checks that go beyond the common
// baseline. Cumulative counters are checked only for existence so the operator
// can still read the historical totals; their values are never required to be
// zero.
func FaultSignatureQueries(fault string) []recoveryQuery {
	node, ok := faultTargetNode(fault)
	if !ok {
		return nil
	}
	counters := func(metrics ...string) []recoveryQuery {
		queries := make([]recoveryQuery, 0, len(metrics))
		for _, metric := range metrics {
			queries = append(queries, recoveryQuery{
				Name:          node + " " + metric + " readable",
				Expr:          fmt.Sprintf(`count(%s)`, fabricMetric(metric, node)),
				Pass:          finitePositive,
				Expected:      "cumulative counter series is still readable after recovery",
				CounterSeries: true,
			})
		}
		return queries
	}
	switch fault {
	case "ib-link-down":
		return counters("gpu_lab_ib_link_downed_total")
	case "ib-symbol-errors":
		return counters("gpu_lab_ib_symbol_errors_total", "gpu_lab_ib_link_error_recovery_total")
	case "rdma-retry-storm":
		return counters("gpu_lab_rdma_retries_total", "gpu_lab_rdma_timeouts_total")
	case "ib-congestion":
		return counters("gpu_lab_ib_xmit_wait_total", "gpu_lab_ib_xmit_discards_total")
	default:
		// ib-rate-degraded is a gauge-only fault (negotiated rate), already
		// covered by BaselineFabricQueries, so there is no cumulative counter
		// to confirm.
		return nil
	}
}

// faultTargetNode maps a fabric fault to the node it targeted, matching the
// fixtures in scenarios/ib-*.yaml and Verify in verification.go.
func faultTargetNode(fault string) (string, bool) {
	switch fault {
	case "ib-link-down":
		return "gpu-lab-worker2", true
	case "ib-rate-degraded":
		return "gpu-lab-worker3", true
	case "ib-symbol-errors":
		return "gpu-lab-worker", true
	case "rdma-retry-storm":
		return "gpu-lab-worker2", true
	case "ib-congestion":
		return "gpu-lab-worker3", true
	default:
		return "", false
	}
}

// ScenarioAdoptionQueries prove every exporter rebuilt its model from the
// normal scenario: the value gate here plus the freshness gate on the same
// raw series together mean "all three exporters published scenario=normal
// recently", not merely "such a sample exists in the lookback window".
func ScenarioAdoptionQueries() []recoveryQuery {
	return []recoveryQuery{
		{
			Name:     "exporters adopted normal",
			Expr:     `count(gpu_lab_scenario_info{scenario="normal"})`,
			Pass:     recoveryGauge(func(v float64) bool { return v == RecoveryExporterCount }),
			Expected: fmt.Sprintf("all %d exporters publish scenario=normal", RecoveryExporterCount),
		},
	}
}

// TrainingRecoveryQueries prove the training workload observes a healthy
// collective world. Step progress is checked separately because it needs two
// observations over time.
func TrainingRecoveryQueries() []recoveryQuery {
	return []recoveryQuery{
		{
			Name:     "training ranks published",
			Expr:     `count(gpu_lab_training_rank_up)`,
			Pass:     recoveryGauge(func(v float64) bool { return v == RecoveryTrainingRanks }),
			Expected: fmt.Sprintf("all %d ranks publish gpu_lab_training_rank_up", RecoveryTrainingRanks),
		},
		{
			Name:     "training ranks up",
			Expr:     `sum(gpu_lab_training_rank_up)`,
			Pass:     recoveryGauge(func(v float64) bool { return v == RecoveryTrainingRanks }),
			Expected: fmt.Sprintf("all %d ranks report up=1", RecoveryTrainingRanks),
		},
		{
			Name:     "training observed fabric delay",
			Expr:     `sum(gpu_lab_training_fabric_delay_seconds)`,
			Pass:     recoveryGauge(func(v float64) bool { return v == 0 }),
			Expected: "worker-observed fabric delay is back to 0",
		},
		{
			Name:          "allreduce error counter readable",
			Expr:          `count(gpu_lab_training_allreduce_errors_total)`,
			Pass:          finitePositive,
			Expected:      "cumulative AllReduce error counter series is still readable",
			CounterSeries: true,
		},
	}
}

// freshnessCheck demands two things about the raw (non-aggregated) selector
// Expr. Shape: the vector must hold exactly Count series whose values pass
// Value when set. Recency: the ORIGINAL scrape of every series must be
// recent, judged by a second query using the PromQL timestamp() function,
// because the value pair of an instant query carries the evaluation time and
// proves nothing about staleness.
type freshnessCheck struct {
	Name     string
	Expr     string
	Count    int
	Value    func(float64) bool
	Expected string
}

// FreshnessChecks returns the staleness gates. The up gate requires that the
// last scrape attempt of each exporter succeeded; the age gates require that
// time() - timestamp(selector) answers with 0 <= age <= maxSampleAge for
// every series of the adoption and training selectors. Together they bound
// the 5-minute Prometheus lookback gap as tightly as the lab can observe.
func FreshnessChecks() []freshnessCheck {
	return []freshnessCheck{
		{
			Name:     "exporter scrape targets up",
			Expr:     exporterUpExpr,
			Count:    RecoveryExporterCount,
			Value:    recoveryGauge(func(v float64) bool { return v == 1 }),
			Expected: "every dcgm-exporter scrape target is up=1 with a recent sample",
		},
		{
			Name:     "scenario adoption samples fresh",
			Expr:     adoptionSeriesExpr,
			Count:    RecoveryExporterCount,
			Value:    recoveryGauge(func(v float64) bool { return v == 1 }),
			Expected: fmt.Sprintf("all %d scenario=normal samples ingested within %s", RecoveryExporterCount, maxSampleAge),
		},
		{
			Name:     "training step samples fresh",
			Expr:     `gpu_lab_training_step`,
			Count:    RecoveryTrainingRanks,
			Expected: fmt.Sprintf("all %d step samples ingested within %s", RecoveryTrainingRanks, maxSampleAge),
		},
	}
}

// RecoveryChecks returns the declarative value-gate table for a recovery run.
// It is consumed through VerifyRecovery but kept separate so tests can assert
// the meaning of each stage without a cluster.
func RecoveryChecks(fault string) []recoveryQuery {
	queries := ScenarioAdoptionQueries()
	for _, node := range LabWorkerNodes {
		queries = append(queries, BaselineFabricQueries(node)...)
	}
	if fault != "" {
		queries = append(queries, FaultSignatureQueries(fault)...)
	}
	return append(queries, TrainingRecoveryQueries()...)
}

// VerifyRecovery judges whether the lab returned to the normal state. When
// fault is non-empty, that fault's cumulative counters are additionally
// confirmed readable (never zeroed) so the incident trail stays auditable.
func (v RecoveryVerifier) VerifyRecovery(ctx context.Context, fault string) Report {
	report := Report{Scenario: "recovery"}
	if fault != "" {
		report.Scenario = "recovery:" + fault
	}

	active, err := v.activeScenario(ctx)
	if err != nil {
		report.Checks = append(report.Checks, Check{Name: "active scenario", Detail: err.Error()})
		return report
	}
	if active != "normal" {
		report.Checks = append(report.Checks, Check{
			Name:   "active scenario",
			Detail: fmt.Sprintf("cluster still runs fault scenario %q; run 'gpu scenario reset' (and 'gpu training recover' after a training injection) first", active),
		})
		return report
	}
	report.Checks = append(report.Checks, Check{
		Name:   "active scenario",
		Detail: `ConfigMap is set to "normal"`,
		Passed: true,
	})

	report.Checks = append(report.Checks, v.trainingControlRecoveryCheck(ctx))

	// One shared budget for all value and freshness gates. Sequential checks
	// therefore cost the lab's convergence time, not N times a per-check
	// timeout.
	budgetCtx, cancel := context.WithTimeout(ctx, v.budget())
	defer cancel()

	for _, query := range RecoveryChecks(fault) {
		report.Checks = append(report.Checks, v.recoveryQueryCheck(budgetCtx, query))
	}
	for _, check := range FreshnessChecks() {
		report.Checks = append(report.Checks, v.freshnessGate(budgetCtx, check))
	}

	// The progress gate has its own window so it is not starved when the
	// budget was consumed waiting for other gates.
	report.Checks = append(report.Checks, v.trainingProgressCheck(ctx))
	return report
}

// recoveryQueryCheck executes one declarative check and classifies the failure
// so operators can tell "value is wrong" apart from "series is missing". A
// query error is retried inside the poll window because the value may still be
// propagating, but it is never converted into a pass.
func (v RecoveryVerifier) recoveryQueryCheck(ctx context.Context, query recoveryQuery) Check {
	lastDetail := "no sample observed"
	err := v.poll(ctx, func() (bool, error) {
		value, err := v.query(ctx, query.Expr)
		if err != nil {
			lastDetail = "no sample: " + err.Error()
			return false, nil
		}
		if !query.Pass(value) {
			if query.CounterSeries {
				lastDetail = fmt.Sprintf("%s (count=%s)", query.Expected, formatFloat(value))
			} else {
				lastDetail = fmt.Sprintf("%s (value=%s)", query.Expected, formatFloat(value))
			}
			return false, nil
		}
		if query.CounterSeries {
			lastDetail = fmt.Sprintf("%s (count=%s)", query.Expected, formatFloat(value))
		} else {
			lastDetail = fmt.Sprintf("%s (value=%s)", query.Expected, formatFloat(value))
		}
		return true, nil
	})
	if err != nil {
		return Check{Name: query.Name, Detail: lastDetail + "; " + err.Error()}
	}
	return Check{Name: query.Name, Detail: lastDetail, Passed: true}
}

// freshnessGate enforces the two-stage contract of a freshnessCheck: first
// the raw vector shape (count and values), then the original-scrape age via
// the PromQL timestamp() function. A target that is dead but still inside
// the lookback window passes the shape stage and must fail the age stage.
func (v RecoveryVerifier) freshnessGate(ctx context.Context, check freshnessCheck) Check {
	name := check.Name
	shapeDetail := "no samples observed"
	err := v.poll(ctx, func() (bool, error) {
		samples, err := v.querySamples(ctx, check.Expr)
		if err != nil {
			shapeDetail = "no samples: " + err.Error()
			return false, nil
		}
		if len(samples) != check.Count {
			shapeDetail = fmt.Sprintf("%s (series=%d, want %d)", check.Expected, len(samples), check.Count)
			return false, nil
		}
		for _, sample := range samples {
			if math.IsNaN(sample.value) || math.IsInf(sample.value, 0) {
				shapeDetail = fmt.Sprintf("%s (non-finite sample value)", check.Expected)
				return false, nil
			}
			if check.Value != nil && !check.Value(sample.value) {
				shapeDetail = fmt.Sprintf("%s (value=%s)", check.Expected, formatFloat(sample.value))
				return false, nil
			}
		}
		shapeDetail = fmt.Sprintf("%s (series=%d)", check.Expected, len(samples))
		return true, nil
	})
	if err != nil {
		return Check{Name: name, Detail: shapeDetail + "; " + err.Error()}
	}

	ageExpr := fmt.Sprintf("time() - timestamp(%s)", check.Expr)
	ageDetail := "no age samples observed"
	err = v.poll(ctx, func() (bool, error) {
		samples, err := v.querySamples(ctx, ageExpr)
		if err != nil {
			ageDetail = "no age samples: " + err.Error()
			return false, nil
		}
		if len(samples) != check.Count {
			ageDetail = fmt.Sprintf("%s (age series=%d, want %d)", check.Expected, len(samples), check.Count)
			return false, nil
		}
		oldest := 0.0
		for _, sample := range samples {
			age := sample.value
			if math.IsNaN(age) || math.IsInf(age, 0) || age < 0 {
				ageDetail = fmt.Sprintf("%s (sample age %s is not a valid age in seconds; future or broken timestamps fail closed)", check.Expected, formatFloat(age))
				return false, nil
			}
			if age > maxSampleAge.Seconds() {
				ageDetail = fmt.Sprintf("%s (oldest sample %.0fs old exceeds %s; the scrape is stale or the target is gone)", check.Expected, age, maxSampleAge)
				return false, nil
			}
			if age > oldest {
				oldest = age
			}
		}
		ageDetail = fmt.Sprintf("%s (series=%d, oldest age %.0fs)", check.Expected, len(samples), oldest)
		return true, nil
	})
	if err != nil {
		return Check{Name: name, Detail: shapeDetail + "; " + ageDetail + "; " + err.Error()}
	}
	return Check{Name: name, Detail: shapeDetail + "; " + ageDetail, Passed: true}
}

// trainingControlRecoveryCheck reads the training control ConfigMap directly so
// a stale worker cannot mask a control plane that never recovered, and so a
// leftover straggler or crash injection cannot be mistaken for fabric recovery.
func (v RecoveryVerifier) trainingControlRecoveryCheck(ctx context.Context) Check {
	out, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "-n", DemoNamespace,
		"get", "configmap", trainingControlConfigMap, "-o", "jsonpath={.data."+escapeJSONPathKey(trainingControlKey)+"}")
	if err != nil {
		return Check{Name: "training control", Detail: fmt.Sprintf("read training control: %v; run 'gpu training recover' first", err)}
	}
	return EvaluateTrainingControl([]byte(out))
}

// recoveryControlState mirrors the JSON contract of
// internal/trainingjob.ControlState. It is kept local and strict: recovery
// verification must fail closed on any document the writer would never
// produce, so unknown fields are rejected even though the writer's own type
// would ignore them on the round trip.
type recoveryControlState struct {
	Generation       int64  `json:"generation"`
	StragglerRank    *int   `json:"straggler_rank"`
	StragglerDelay   string `json:"straggler_delay"`
	CrashRank        *int   `json:"crash_rank"`
	CrashToken       string `json:"crash_token"`
	FabricMode       string `json:"fabric_mode"`
	FabricTargetNode string `json:"fabric_target_node"`
	FabricDelay      string `json:"fabric_delay"`
}

// EvaluateTrainingControl is pure so the recovery contract is testable without
// a cluster. Decoding is typed and strict:
//
//   - empty, whitespace-only, null, array, or scalar documents fail;
//   - invalid or truncated JSON fails;
//   - trailing content after the object fails;
//   - wrong-typed or unknown fields fail;
//   - a well-formed object with all fault fields absent or null (including
//     {}) counts as NORMAL: the lab writer omits cleared fault fields, so an
//     empty document carries no fault bits by construction.
func EvaluateTrainingControl(data []byte) Check {
	const name = "training control"
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Check{Name: name, Detail: "training control ConfigMap is empty; run 'gpu training run', then 'gpu training recover'"}
	}
	if !strings.HasPrefix(trimmed, "{") {
		return Check{Name: name, Detail: fmt.Sprintf("training control must be a JSON object, got %.40q; run 'gpu training recover'", trimmed)}
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var state recoveryControlState
	if err := decoder.Decode(&state); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("training control is not a valid control document: %v; run 'gpu training recover'", err)}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		detail := "training control has trailing content after the JSON object"
		if err != nil {
			detail = fmt.Sprintf("training control has invalid trailing content: %v", err)
		}
		return Check{Name: name, Detail: detail + "; run 'gpu training recover'"}
	}
	mode := strings.ToLower(strings.TrimSpace(state.FabricMode))
	if mode != "" && mode != "normal" {
		return Check{Name: name, Detail: fmt.Sprintf("fabric_mode is %q; run 'gpu training recover'", mode)}
	}
	if strings.TrimSpace(state.FabricTargetNode) != "" {
		return Check{Name: name, Detail: fmt.Sprintf("fabric_target_node still set to %q; run 'gpu training recover'", state.FabricTargetNode)}
	}
	if delay := strings.TrimSpace(state.FabricDelay); delay != "" {
		parsed, err := time.ParseDuration(delay)
		if err != nil {
			return Check{Name: name, Detail: fmt.Sprintf("fabric_delay %q is not a valid duration; run 'gpu training recover'", delay)}
		}
		if parsed != 0 {
			return Check{Name: name, Detail: fmt.Sprintf("fabric_delay still set to %q; run 'gpu training recover'", delay)}
		}
	}
	if state.StragglerRank != nil || strings.TrimSpace(state.StragglerDelay) != "" {
		return Check{Name: name, Detail: "straggler injection is still active; run 'gpu training recover'"}
	}
	if state.CrashRank != nil || strings.TrimSpace(state.CrashToken) != "" {
		return Check{Name: name, Detail: "worker-crash injection is still active; run 'gpu training recover'"}
	}
	return Check{Name: name, Detail: "fabric_mode is normal with no target, delay, straggler, or crash injection", Passed: true}
}

// promSample is one raw instant-vector sample. timestamp carries the
// EVALUATION time of the query (the first element of Prometheus's value
// pair). It is validated as a finite number so a broken response fails
// closed, but it must never be read as an original scrape time; original
// scrape age comes from the timestamp() function in the age query.
type promSample struct {
	value     float64
	timestamp float64
}

// decodePrometheusSamples decodes every result entry of an instant query,
// keeping the ingestion timestamp that aggregated queries erase.
func decodePrometheusSamples(data []byte) ([]promSample, error) {
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Prometheus samples: %w", err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus samples status is %q", response.Status)
	}
	samples := make([]promSample, 0, len(response.Data.Result))
	for index, result := range response.Data.Result {
		if len(result.Value) < 2 {
			return nil, fmt.Errorf("Prometheus sample %d has no value pair", index)
		}
		timestamp, err := decodePrometheusToken(result.Value[0])
		if err != nil {
			return nil, fmt.Errorf("Prometheus sample %d timestamp: %w", index, err)
		}
		value, err := decodePrometheusToken(result.Value[1])
		if err != nil {
			return nil, fmt.Errorf("Prometheus sample %d value: %w", index, err)
		}
		samples = append(samples, promSample{value: value, timestamp: timestamp})
	}
	return samples, nil
}

// decodePrometheusToken accepts the documented string form of the [timestamp,
// "value"] pair and the bare-number form some gateways emit. null is never a
// zero: missing, null, and non-finite tokens all fail closed.
func decodePrometheusToken(raw json.RawMessage) (float64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, errors.New("token is missing or null")
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		value, err := strconv.ParseFloat(asString, 64)
		if err != nil {
			return 0, fmt.Errorf("token %q is not numeric: %w", asString, err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("token %q is not finite", asString)
		}
		return value, nil
	}
	var asNumber float64
	if err := json.Unmarshal(raw, &asNumber); err != nil {
		return 0, fmt.Errorf("token %s is not a number: %w", text, err)
	}
	if math.IsNaN(asNumber) || math.IsInf(asNumber, 0) {
		return 0, fmt.Errorf("token %s is not finite", text)
	}
	return asNumber, nil
}

// trainingProgressCheck observes gpu_lab_training_step twice and requires a
// strict increase, which rejects a stale or stuck workload that still serves
// the metrics endpoint.
func (v RecoveryVerifier) trainingProgressCheck(ctx context.Context) Check {
	const name = "training step progress"
	first, err := v.query(ctx, trainingStepExpr)
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("no sample for %s: %v", trainingStepExpr, err)}
	}
	if math.IsNaN(first) || math.IsInf(first, 0) {
		return Check{Name: name, Detail: fmt.Sprintf("%s is not finite (%s)", trainingStepExpr, formatFloat(first))}
	}
	deadline := time.NewTimer(v.progressWindow())
	defer deadline.Stop()
	ticker := time.NewTicker(v.pollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Check{Name: name, Detail: fmt.Sprintf("training progress stopped before an increase from step %s: %v", formatFloat(first), ctx.Err())}
		case <-deadline.C:
			return Check{Name: name, Detail: fmt.Sprintf("%s stayed at %s for %s; the worker is stale or stalled", trainingStepExpr, formatFloat(first), v.progressWindow())}
		case <-ticker.C:
			current, err := v.query(ctx, trainingStepExpr)
			if err != nil || math.IsNaN(current) || math.IsInf(current, 0) {
				continue // a transient query failure keeps the same observation window
			}
			if current > first {
				return Check{Name: name, Detail: fmt.Sprintf("%s advanced %s -> %s; the collective loop is live", trainingStepExpr, formatFloat(first), formatFloat(current)), Passed: true}
			}
		}
	}
}

func (v RecoveryVerifier) activeScenario(ctx context.Context) (string, error) {
	data, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "configmap", scenario.ConfigName, "-n", SystemNS, "-o", "jsonpath={.data.scenario\\.yaml}")
	if err != nil {
		return "", fmt.Errorf("read active scenario: %w", err)
	}
	parsed, err := scenario.Parse([]byte(data))
	if err != nil {
		return "", fmt.Errorf("parse active scenario: %w", err)
	}
	return parsed.Name(), nil
}

func (v RecoveryVerifier) query(ctx context.Context, expression string) (float64, error) {
	data, err := v.queryRaw(ctx, expression)
	if err != nil {
		return 0, err
	}
	return decodePrometheusValue([]byte(data))
}

func (v RecoveryVerifier) querySamples(ctx context.Context, expression string) ([]promSample, error) {
	data, err := v.queryRaw(ctx, expression)
	if err != nil {
		return nil, err
	}
	return decodePrometheusSamples([]byte(data))
}

func (v RecoveryVerifier) queryRaw(ctx context.Context, expression string) (string, error) {
	proxyPath := "/api/v1/namespaces/" + MonitoringNS + "/services/" + PrometheusSvc + "/proxy/api/v1/query?query=" + url.QueryEscape(expression)
	data, err := v.Runner.Output(ctx, "kubectl", "--context", KubeContext, "get", "--raw", proxyPath)
	if err != nil {
		return "", fmt.Errorf("query Prometheus: %w", err)
	}
	return data, nil
}

func (v RecoveryVerifier) poll(ctx context.Context, fn func() (bool, error)) error {
	deadline := time.NewTimer(v.budget())
	defer deadline.Stop()
	ticker := time.NewTicker(v.pollInterval())
	defer ticker.Stop()
	var lastErr error
	for {
		// Never invoke fn on a context that has already expired or been
		// cancelled: once the shared budget is spent, the remaining checks
		// must fail fast instead of firing one doomed query each.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ok, err := fn()
		lastErr = err
		if ok && err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("timed out after %s waiting for the normal state", v.budget())
		case <-ticker.C:
		}
	}
}

func escapeJSONPathKey(key string) string {
	return strings.ReplaceAll(key, ".", `\.`)
}
