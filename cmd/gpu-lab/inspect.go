package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/inspection"
)

func inspectCommand(ctx context.Context, args []string, defaultMode inspection.Mode, stdout io.Writer) error {
	options := inspection.Options{Mode: defaultMode, Timeout: inspection.DefaultTimeout}
	jsonOutput := false
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--mode":
			if seen["mode"] {
				return errors.New("gpu inspect: --mode may be specified once")
			}
			seen["mode"] = true
			if !hasValue {
				if i+1 >= len(args) {
					return errors.New("gpu inspect: --mode requires auto, real, or synthetic")
				}
				i++
				value = args[i]
			}
			mode, err := inspection.ParseMode(value)
			if err != nil {
				return fmt.Errorf("gpu inspect: %w", err)
			}
			options.Mode = mode
		case "--timeout":
			if seen["timeout"] {
				return errors.New("gpu inspect: --timeout may be specified once")
			}
			seen["timeout"] = true
			if !hasValue {
				if i+1 >= len(args) {
					return errors.New("gpu inspect: --timeout requires a duration")
				}
				i++
				value = args[i]
			}
			duration, err := time.ParseDuration(value)
			if err != nil || duration <= 0 {
				return fmt.Errorf("gpu inspect: invalid --timeout %q; use a positive duration such as 10s", value)
			}
			options.Timeout = duration
		case "--json":
			if seen["json"] {
				return errors.New("gpu inspect: --json may be specified once")
			}
			seen["json"] = true
			if hasValue {
				return errors.New("gpu inspect: --json does not take a value")
			}
			jsonOutput = true
		case "--help", "-h":
			_, err := io.WriteString(stdout, "Usage: gpu inspect [--mode auto|real|synthetic] [--json] [--timeout DURATION]\n")
			return err
		default:
			return fmt.Errorf("gpu inspect: unexpected argument %q", arg)
		}
	}
	if options.Mode == "" {
		options.Mode = inspection.ModeAuto
	}
	if options.Mode != inspection.ModeAuto && options.Mode != inspection.ModeReal && options.Mode != inspection.ModeSynthetic {
		return fmt.Errorf("gpu inspect: invalid default mode %q", options.Mode)
	}
	if stdout == nil {
		return errors.New("gpu inspect: stdout is nil")
	}
	report, inspectErr := inspection.Inspect(ctx, options, inspection.DefaultDependencies())
	if reportErr := inspection.WriteReport(stdout, report, jsonOutput); reportErr != nil {
		return reportErr
	}
	return inspectErr
}

func parseGlobalMode(args []string, envMode string) (inspection.Mode, []string, error) {
	mode := inspection.ModeAuto
	seen := false
	index := 0
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "--mode" {
			break
		}
		if seen {
			return "", nil, errors.New("gpu: global --mode may be specified once")
		}
		seen = true
		if !hasValue {
			if index+1 >= len(args) {
				return "", nil, errors.New("gpu: --mode requires auto, real, or synthetic before the command")
			}
			index++
			value = args[index]
		}
		parsed, err := inspection.ParseMode(value)
		if err != nil {
			return "", nil, fmt.Errorf("gpu: %w", err)
		}
		mode = parsed
		index++
	}
	remaining := args[index:]
	if !seen && !(len(remaining) > 0 && remaining[0] == "inspect" && hasInspectModeSelector(remaining[1:])) {
		if strings.TrimSpace(envMode) != "" {
			parsed, err := inspection.ParseMode(envMode)
			if err != nil {
				return "", nil, fmt.Errorf("GPU_LAB_MODE: %w", err)
			}
			mode = parsed
		}
	}
	return mode, remaining, nil
}

func hasInspectModeSelector(args []string) bool {
	for _, arg := range args {
		if arg == "--mode" || strings.HasPrefix(arg, "--mode=") {
			return true
		}
	}
	return false
}
