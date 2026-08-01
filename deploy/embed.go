package deploy

import "embed"

// FS contains the runtime manifests shipped with gpu-lab.
//
//go:embed kind/* device-plugin/* exporter/* demo/* monitoring/*
var FS embed.FS
