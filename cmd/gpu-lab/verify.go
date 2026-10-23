package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/scenario"
	"github.com/gpu-lab/gpu-lab/internal/verification"
)

const verifyUsage = "usage: gpu verify <scenario> | gpu verify --recovery [fabric-fault]"

// verifyScenario implements two deliberately different judgements:
//
//	gpu verify <scenario>        -> the cluster reached the FAULT state the
//	                                scenario declares. This is NOT recovery
//	                                evidence.
//	gpu verify --recovery [fault] -> the lab is back in the NORMAL state after
//	                                'gpu scenario reset' / 'gpu training
//	                                recover'. Cumulative counters are not
//	                                required to return to zero.
func verifyScenario(ctx context.Context, r runner.Runner, args []string, stdout io.Writer) error {
	if hasHelpFlag(args) {
		fmt.Fprintln(stdout, "gpu verify — judge fault state or recovery state")
		fmt.Fprintln(stdout, verifyUsage)
		fmt.Fprintln(stdout, "  fault state:  gpu verify <scenario>            (run right after 'gpu scenario run')")
		fmt.Fprintln(stdout, "  recovery:     gpu verify --recovery [fault]    (run after 'gpu scenario reset')")
		fmt.Fprintln(stdout, "                a fabric fault name additionally confirms its cumulative")
		fmt.Fprintln(stdout, "                counters are still readable (they are never required to be zero)")
		fmt.Fprintln(stdout, "known fabric faults: ib-link-down, ib-rate-degraded, ib-symbol-errors, rdma-retry-storm, ib-congestion")
		return nil
	}
	if len(args) >= 1 && args[0] == "--recovery" {
		fault := ""
		switch len(args) {
		case 1:
		case 2:
			fault = args[1]
			if !verification.IsKnownFabricFault(fault) {
				return fmt.Errorf("unknown fabric fault %q; known faults: %v", fault, verification.KnownFabricFaults)
			}
		default:
			return errors.New(verifyUsage)
		}
		return runRecoveryVerification(ctx, r, fault, stdout)
	}
	if len(args) != 1 || args[0] == "" {
		return errors.New(verifyUsage)
	}
	selected, err := scenario.LoadBuiltin(args[0])
	if err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	report := verification.New(r).Verify(verifyCtx, selected)
	fmt.Fprintf(stdout, "gpu verify %s (fault state)\n", report.Scenario)
	return printVerificationReport(stdout, report)
}

func runRecoveryVerification(ctx context.Context, r runner.Runner, fault string, stdout io.Writer) error {
	// Recovery needs a wider window than fault verification: the exporter
	// reloads scenario ConfigMaps on a 2s poll and the training step gate
	// needs two scrapes, so allow the underlying verifier its full window
	// inside a bounded outer timeout.
	verifyCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	verifier := verification.NewRecoveryVerifier(r)
	report := verifier.VerifyRecovery(verifyCtx, fault)
	label := "recovery"
	if fault != "" {
		label = "recovery from " + fault
	}
	fmt.Fprintf(stdout, "gpu verify --recovery (%s)\n", label)
	if err := printVerificationReport(stdout, report); err != nil {
		fmt.Fprintln(stdout, "hint: recovery is judged on gauges and live progress; cumulative *_total counters keep their history by design.")
		fmt.Fprintln(stdout, "hint: 'gpu verify <scenario>' before a reset proves the fault state, never the recovery.")
		return err
	}
	return nil
}

func printVerificationReport(stdout io.Writer, report verification.Report) error {
	failures := 0
	for _, check := range report.Checks {
		status := "PASS"
		if !check.Passed {
			status = "FAIL"
			failures++
		}
		fmt.Fprintf(stdout, "[%s] %-34s %s\n", status, check.Name, check.Detail)
	}
	if failures > 0 {
		return fmt.Errorf("verification failed: %d check(s) failed", failures)
	}
	fmt.Fprintln(stdout, "verification passed")
	return nil
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}
