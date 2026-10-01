//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

// probeHostMemoryTotal reports physical host memory, separate from the
// values the Docker daemon reports.
func probeHostMemoryTotal(ctx context.Context) (int64, error) {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(key) != "MemTotal" {
				continue
			}
			fields := strings.Fields(value)
			if len(fields) == 0 {
				return 0, fmt.Errorf("unparsable MemTotal in /proc/meminfo")
			}
			kib, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return 0, err
			}
			unit := "KiB"
			if len(fields) > 1 {
				unit = fields[1]
			}
			switch unit {
			case "KiB":
				return kib * 1024, nil
			case "MiB":
				return kib * 1024 * 1024, nil
			default:
				return 0, fmt.Errorf("unknown /proc/meminfo unit %q", unit)
			}
		}
		return 0, fmt.Errorf("MemTotal missing from /proc/meminfo")
	case "darwin":
		probeCtx, cancel := context.WithTimeout(ctx, toolProbeTimeout)
		defer cancel()
		out, err := runner.New(nil, nil).Output(probeCtx, "sysctl", "-n", "hw.memsize")
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	default:
		return 0, fmt.Errorf("host memory probe is not supported on %s", runtime.GOOS)
	}
}
