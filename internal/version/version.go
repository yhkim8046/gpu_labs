package version

import "fmt"

// These values are replaced by release builds through -ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func String() string {
	return fmt.Sprintf("gpu-lab %s (commit %s, built %s)", Version, Commit, BuildDate)
}
