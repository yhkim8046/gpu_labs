#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
cd "$repo_root"

namespace="${GPU_LAB_TRAINING_NAMESPACE:-gpu-lab-demo}"
keep_state="${IB_E2E_KEEP_STATE:-0}"
query_log="$(mktemp "/tmp/gpu-lab-ib-e2e.XXXXXX")"

gpu() {
  go run ./cmd/gpu-lab "$@"
}

cleanup() {
  local status="$?"
  if [[ "$status" -ne 0 || "$keep_state" != "1" ]]; then
    gpu scenario reset || true
    gpu training recover --namespace "$namespace" || true
    gpu verify normal || true
  fi
  rm -f "$query_log"
  exit "$status"
}

wait_query() {
  local expression="$1"
  local description="$2"
  local attempts=36
  local attempt

  for attempt in $(seq 1 "$attempts"); do
    if gpu metrics --query "$expression" >"$query_log" 2>&1; then
      echo "ok: $description"
      return 0
    fi
    sleep 5
  done

  echo "timed out after 180s: $description" >&2
  sed -n '1,120p' "$query_log" >&2
  return 1
}

require_text() {
  local output="$1"
  local expected="$2"
  local description="$3"
  if ! grep -Fq -- "$expected" <<<"$output"; then
    echo "missing expected rdma-core output for $description: $expected" >&2
    printf '%s\n' "$output" >&2
    return 1
  fi
  echo "ok: $description"
}

check_normal_command_outputs() {
  local node="$1"
  local output
  output="$(gpu ibstat --node "$node")"
  require_text "$output" $'\t\tState: Active' "ibstat active state"
  require_text "$output" $'\t\tPhysical state: LinkUp' "ibstat physical LinkUp"
  require_text "$output" $'\t\tRate: 200' "ibstat 200 Gbps rate"
  output="$(gpu ibstatus --node "$node" mlx5_0:1)"
  require_text "$output" $'\tstate:\t\t4: ACTIVE' "ibstatus ACTIVE state"
  require_text "$output" $'\trate:\t\t200 Gb/sec (4X HDR)' "ibstatus HDR rate"
  output="$(gpu ibv_devinfo --node "$node" -d mlx5_0 -i 1 -v)"
  require_text "$output" $'\t\t\tstate:\t\t\tPORT_ACTIVE (4)' "ibv_devinfo active port"
  require_text "$output" $'\t\t\teffective_speed:\t200.0 Gbps' "ibv_devinfo effective speed"
}

check_link_down_command_outputs() {
  local output
  output="$(gpu ibstat --node gpu-lab-worker2)"
  require_text "$output" $'\t\tState: Down' "ibstat down state"
  require_text "$output" $'\t\tPhysical state: Disabled' "ibstat disabled physical state"
  output="$(gpu ibstatus --node gpu-lab-worker2 mlx5_0:1)"
  require_text "$output" $'\tstate:\t\t1: DOWN' "ibstatus DOWN state"
  require_text "$output" $'\tphys state:\t3: Disabled' "ibstatus disabled physical state"
  output="$(gpu ibv_devinfo --node gpu-lab-worker2 -d mlx5_0 -i 1 -v)"
  require_text "$output" $'\t\t\tstate:\t\t\tPORT_DOWN (1)' "ibv_devinfo down port"
  require_text "$output" $'\t\t\tphys_state:\t\tDISABLED (3)' "ibv_devinfo disabled physical state"
}

check_degraded_command_outputs() {
  local output
  output="$(gpu ibstat --node gpu-lab-worker3)"
  require_text "$output" $'\t\tRate: 25' "ibstat degraded rate"
  output="$(gpu ibstatus --node gpu-lab-worker3 mlx5_0:1)"
  require_text "$output" $'\trate:\t\t25 Gb/sec (1X EDR)' "ibstatus degraded rate"
  output="$(gpu ibv_devinfo --node gpu-lab-worker3 -d mlx5_0 -i 1 -v)"
  require_text "$output" $'\t\t\teffective_speed:\t25.0 Gbps' "ibv_devinfo degraded speed"
}

reset_and_check() {
  local scenario="$1"
  gpu scenario reset
  gpu training recover --namespace "$namespace"
  gpu verify normal
  wait_query 'min(gpu_lab_ib_port_up) == 1' "$scenario: ports recovered"
  wait_query 'min(gpu_lab_ib_link_rate_gbps) >= 100' "$scenario: link rate recovered"
  wait_query 'max(gpu_lab_training_fabric_fault_active) == 0' "$scenario: training fabric fault cleared"
  wait_query 'sum(changes(gpu_lab_training_step[1m])) > 0' "$scenario: training progress recovered"
}

run_scenario() {
  local name="$1"
  local metric_query="$2"
  local impact_query="$3"

  echo "== $name =="
  gpu scenario run "$name"
  gpu verify "$name"
  wait_query "$metric_query" "$name: fabric metric"
  case "$name" in
    ib-link-down) check_link_down_command_outputs ;;
    ib-rate-degraded) check_degraded_command_outputs ;;
  esac
  if [[ -n "$impact_query" ]]; then
    wait_query "$impact_query" "$name: training impact"
  fi
  reset_and_check "$name"
}

trap cleanup EXIT

echo "checking existing cluster, monitoring, fabric exporter, and training"
check_normal_command_outputs gpu-lab-worker
exporter_pod="$(kubectl --context gpu-lab -n gpu-lab-system get pods -l app.kubernetes.io/name=dcgm-exporter -o jsonpath='{.items[0].metadata.name}')"
kubectl --context gpu-lab -n gpu-lab-system exec "$exporter_pod" -- ibstat
kubectl --context gpu-lab -n gpu-lab-system exec "$exporter_pod" -- ibstatus
kubectl --context gpu-lab -n gpu-lab-system exec "$exporter_pod" -- ibv_devinfo -v
gpu training status --namespace "$namespace"
wait_query 'min(gpu_lab_ib_port_up) == 1' "baseline: all synthetic IB ports up"
wait_query 'min(gpu_lab_ib_link_rate_gbps) >= 100' "baseline: link rate at least 100 Gbps"
wait_query 'sum(gpu_lab_training_rank_up) >= 3' "baseline: three training ranks ready"
wait_query 'sum(changes(gpu_lab_training_step[1m])) > 0' "baseline: training is progressing"

run_scenario \
  "ib-link-down" \
  "min(gpu_lab_ib_port_up) == 0" \
  "max(gpu_lab_training_fabric_fault_active) > 0"

run_scenario \
  "ib-rate-degraded" \
  "min(gpu_lab_ib_link_rate_gbps) < 100" \
  "max(gpu_lab_training_fabric_delay_seconds) > 0"

run_scenario \
  "ib-symbol-errors" \
  "sum(gpu_lab_ib_symbol_errors_total) > 0" \
  "sum(gpu_lab_training_fabric_errors_total) > 0"

run_scenario \
  "rdma-retry-storm" \
  "sum(gpu_lab_rdma_retries_total) > 0" \
  "sum(gpu_lab_training_fabric_retries_total) > 0"

run_scenario \
  "ib-congestion" \
  "sum(gpu_lab_ib_xmit_wait_total) + sum(gpu_lab_ib_xmit_discards_total) > 0" \
  "max(gpu_lab_training_fabric_delay_seconds) > 0"

echo "gpu-lab synthetic IB/RDMA e2e passed"
