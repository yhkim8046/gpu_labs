#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

scenario_names=(
  gpu-util-high
  vram-pressure
  xid-79
  exporter-down
  scheduling-failure
  thermal-throttling
  xid-48
  gpu-idle
  node-selector-mismatch
  gpu-fragmentation
  ecc-double-bit
  power-throttle
  pcie-replay
  gpu-allocated-idle
  gpu-capacity-mismatch
)

keep_cluster="${E2E_KEEP_CLUSTER:-0}"

collect_failure_state() {
  if ! command -v kubectl >/dev/null 2>&1; then
    return
  fi
  kubectl --context gpu-lab get nodes -o wide || true
  kubectl --context gpu-lab get pods -A -o wide || true
  kubectl --context gpu-lab get events -A --sort-by=.lastTimestamp || true
  kubectl --context gpu-lab get prometheusrule,servicemonitor -A || true
  if command -v kind >/dev/null 2>&1; then
    kind export logs "${RUNNER_TEMP:-/tmp}/gpu-lab-kind-logs" --name gpu-lab || true
  fi
}

cleanup() {
  status=$?
  if [[ "$status" -ne 0 ]]; then
    collect_failure_state
  fi
  if [[ "$keep_cluster" != "1" ]] && command -v kind >/dev/null 2>&1; then
    if kind get clusters | grep -qx 'gpu-lab'; then
      go run ./cmd/gpu-lab destroy || kind delete cluster --name gpu-lab || true
    fi
  fi
  exit "$status"
}

trap cleanup EXIT

go run ./cmd/gpu-lab doctor
go run ./cmd/gpu-lab create
go run ./cmd/gpu-lab helm catalog
go run ./cmd/gpu-lab helm install nvidia-device-plugin
go run ./cmd/gpu-lab helm install dcgm-exporter
go run ./cmd/gpu-lab helm install monitoring
go run ./cmd/gpu-lab helm list --all-namespaces
go run ./cmd/gpu-lab verify normal
go run ./cmd/gpu-lab nvidia-smi --list-gpus
exporter_pod="$(kubectl --context gpu-lab -n gpu-lab-system get pods -l app.kubernetes.io/name=dcgm-exporter -o jsonpath='{.items[0].metadata.name}')"
kubectl --context gpu-lab -n gpu-lab-system exec "$exporter_pod" -- nvidia-smi --list-gpus

for scenario_name in "${scenario_names[@]}"; do
  go run ./cmd/gpu-lab scenario run "$scenario_name"
  go run ./cmd/gpu-lab verify "$scenario_name"
  if [[ "$scenario_name" == "xid-79" ]]; then
    xid_errors="$(go run ./cmd/gpu-lab nvidia-smi --query-gpu=xid.errors --format=csv,noheader,nounits | sort -u)"
    grep -qx '79' <<<"$xid_errors"
  fi
  go run ./cmd/gpu-lab scenario reset
  go run ./cmd/gpu-lab verify normal
done

echo "gpu-lab e2e passed"
