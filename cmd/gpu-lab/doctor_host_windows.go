//go:build windows

package main

import (
	"context"
	"fmt"
)

// probeHostMemoryTotal is not implemented on Windows; callers degrade to an
// info line only when it errors.
func probeHostMemoryTotal(ctx context.Context) (int64, error) {
	return 0, fmt.Errorf("host memory probe is not supported on Windows")
}
