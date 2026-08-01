package scenarios

import "embed"

// FS contains the built-in, versioned lab scenarios.
//
//go:embed *.yaml
var FS embed.FS
