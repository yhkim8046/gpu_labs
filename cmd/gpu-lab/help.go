package main

import (
	"io"
)

func printHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu — Kubernetes GPU infrastructure lab

Usage:
  gpu version
  gpu create [--local|--registry] [--image <image>] [--all]
  gpu destroy
  gpu reset
  gpu doctor
  gpu status
  gpu dashboard [--port <port>]
  gpu metrics [--query <PromQL>] [--json]
  gpu ibstat [options] [<ca_name> [port_num]]
  gpu ibstatus [<device[:port]> ...]
  gpu ibv_devinfo [-d DEVICE] [-i PORT] [-l] [-v]
  gpu training run [--workers 3] [--image gpu-lab:dev] [--namespace gpu-lab-demo] [--wait]
  gpu training status [--namespace gpu-lab-demo]
  gpu training logs --rank N [--follow] [--namespace gpu-lab-demo]
  gpu training inject straggler --rank N --delay 2s
  gpu training inject worker-crash --rank N
  gpu training recover
  gpu training reset
  gpu nvidia-smi [--list-gpus]
  gpu nvidia-smi --query-gpu=<fields> --format=csv[,noheader][,nounits]
  gpu verify <scenario>
  gpu verify --recovery [fabric-fault]
  gpu context list
  gpu context setup
  gpu context use <context-name>
  gpu scenario list
  gpu scenario run <name>
  gpu scenario inspect <name> [--file path] [--solution]
  gpu scenario reset
  gpu helm catalog
  gpu helm install <gpu-lab-component>
  gpu helm <official-helm-args...>

Examples:
  gpu create
  gpu helm install nvidia-device-plugin
  gpu helm install dcgm-exporter
  gpu helm install monitoring
  gpu scenario run xid-79
  gpu helm list --all-namespaces

Image environment:
  GPU_LAB_IMAGE=<image>                         override runtime image
  GPU_LAB_IMAGE_SOURCE=auto|local|registry      choose build or pull mode
  GPU_LAB_RUNTIME_IMAGE_REPOSITORY=<repository> release image repository
  GPU_LAB_COMPONENT_CHART_SOURCE=auto|embedded|registry
  GPU_LAB_COMPONENT_CHART_REPOSITORY=<oci-repository>
`)
}
