#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

# shellcheck source=test/e2e/common.sh
source "$repo_root/test/e2e/common.sh"
gpu_e2e_init ib

namespace="${GPU_LAB_TRAINING_NAMESPACE:-gpu-lab-demo}"
keep_state="${IB_E2E_KEEP_STATE:-0}"
gpu_e2e_require_cluster_opt_in "InfiniBand/RDMA E2E"

training_step_selector="gpu_lab_training_step{namespace=\"$namespace\"}"
training_rank_selector="gpu_lab_training_rank_up{namespace=\"$namespace\"}"
training_checkpoint_selector="gpu_lab_training_checkpoint_step{namespace=\"$namespace\"}"

cleanup() {
  local status=$?
  trap - EXIT
  if [[ "$status" -ne 0 ]]; then
    gpu_e2e_collect_failure_state ib-failure
    kubectl --context gpu-lab --request-timeout=10s -n "$namespace" get statefulset,pods,configmap -o yaml >>"$E2E_LOG_DIR/ib-failure-cluster-state.txt" 2>&1 || true
  fi
  if [[ "$status" -ne 0 || "$keep_state" != "1" ]]; then
    gpu scenario reset || true
    gpu training recover --namespace "$namespace" || true
    if [[ "$status" -ne 0 ]]; then
      gpu verify --recovery || true
    fi
    gpu training reset --namespace "$namespace" || true
  fi
  gpu_e2e_remove_temp
  exit "$status"
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
  local normal_selector="gpu_lab_training_fabric_fault_active{namespace=\"$namespace\",mode=\"normal\"}"
  gpu_wait_metric "$normal_selector" "count($normal_selector == 0) == 3" \
    "$scenario: all ranks received normal control" 180 20
  gpu verify --recovery "$scenario"
  gpu_wait_metric \
    gpu_lab_ib_port_up \
    'min(gpu_lab_ib_port_up) == 1' \
    "$scenario: all ports recovered" 45 30
  gpu_wait_metric \
    gpu_lab_ib_link_rate_gbps \
    'min(gpu_lab_ib_link_rate_gbps) >= 100' \
    "$scenario: link rate recovered" 45 30
  gpu_wait_metric \
    "gpu_lab_training_rank_up{namespace=\"$namespace\"}" \
    "sum(gpu_lab_training_rank_up{namespace=\"$namespace\"}) == 3" \
    "$scenario: all training ranks are up" 45 20
  local progress_before
  progress_before="$(gpu_read_fresh_metric "$training_step_selector" "min($training_step_selector)" 20)"
  gpu_wait_metric_increase \
    "$training_step_selector" "min($training_step_selector)" "$progress_before" \
    "$scenario: training progress continues after reset" 45 20
  gpu_wait_metric \
    "$training_checkpoint_selector" \
    "count($training_checkpoint_selector >= 1) == 3" \
    "$scenario: all rank checkpoints remain published" 45 20
}

run_scenario() {
  local name="$1"
  local fault_selector="$2"
  local fault_condition="$3"
  local impact_selector="$4"
  local impact_condition="$5"
  local mode adoption_selector
  case "$name" in
    ib-link-down) mode=down ;;
    ib-rate-degraded) mode=degraded ;;
    ib-symbol-errors) mode=errors ;;
    rdma-retry-storm) mode=retries ;;
    ib-congestion) mode=congestion ;;
    *) echo "unknown fabric scenario: $name" >&2; return 2 ;;
  esac

  echo "== fabric scenario: $name =="
  gpu scenario run "$name"
  gpu verify "$name"
  gpu_wait_metric "$fault_selector" "$fault_condition" "$name: fabric signature observed" 60 30
  # Exporter adoption and kubelet's projected control volume are independent.
  # Use the documented 180s delivery budget, then retain the 60s impact check.
  adoption_selector="gpu_lab_training_fabric_fault_active{namespace=\"$namespace\",mode=\"$mode\"}"
  gpu_wait_metric "$adoption_selector" "count($adoption_selector) == 3" \
    "$name: all three ranks received fabric mode=$mode" 180 20
  if [[ -n "$impact_selector" ]]; then
    gpu_wait_metric "$impact_selector" "$impact_condition" "$name: training impact observed" 60 20
  fi
  case "$name" in
    ib-link-down) check_link_down_command_outputs ;;
    ib-rate-degraded) check_degraded_command_outputs ;;
  esac
  reset_and_check "$name"
}

trap cleanup EXIT

if namespace_resource="$(kubectl --context gpu-lab --request-timeout=10s get namespace "$namespace" -o name --ignore-not-found=true)" && [[ -n "$namespace_resource" ]]; then
  gpu training reset --namespace "$namespace"
fi
training_args=(run --workers 3 --namespace "$namespace" --wait --timeout 5m)
if [[ -n "${GPU_LAB_TRAINING_IMAGE:-}" ]]; then
  training_args+=(--image "$GPU_LAB_TRAINING_IMAGE")
fi
gpu training "${training_args[@]}"
# The replacement Pods are now ready. Only samples scraped after this point
# may establish progress; the deleted job could have had a much larger step.
GPU_LAB_E2E_SAMPLES_SINCE="$(python3 -c 'import time; print(time.time())')"
export GPU_LAB_E2E_SAMPLES_SINCE

echo "checking cluster, monitoring, fabric exporter, and three-rank training"
check_normal_command_outputs gpu-lab-worker
exporter_pod="$(kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system get pods -l app.kubernetes.io/name=dcgm-exporter -o jsonpath='{.items[0].metadata.name}')"
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- ibstat
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- ibstatus
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- ibv_devinfo -v
gpu training status --namespace "$namespace"

gpu_wait_metric \
  gpu_lab_ib_port_up \
  'min(gpu_lab_ib_port_up) == 1' \
  "baseline: all synthetic IB ports up" 45 30
gpu_wait_metric \
  gpu_lab_ib_link_rate_gbps \
  'min(gpu_lab_ib_link_rate_gbps) >= 100' \
  "baseline: link rate at least 100 Gbps" 45 30
gpu_wait_metric \
  "$training_rank_selector" \
  "count($training_rank_selector == 1) == 3" \
  "baseline: three training ranks ready" 60 20
gpu_wait_metric \
  "$training_step_selector" \
  "count($training_step_selector >= 1) == 3" \
  "baseline: all ranks publish progress" 60 20
gpu_wait_metric \
  "$training_checkpoint_selector" \
  "count($training_checkpoint_selector >= 1) == 3" \
  "baseline: all ranks publish checkpoints" 60 20
baseline_step="$(gpu_read_fresh_metric "$training_step_selector" "min($training_step_selector)" 20)"
gpu_wait_metric_increase \
  "$training_step_selector" "min($training_step_selector)" "$baseline_step" \
  "baseline: training advances between fresh samples" 45 20

run_scenario \
  "ib-link-down" \
  'gpu_lab_ib_port_up{node="gpu-lab-worker2"}' \
  'min(gpu_lab_ib_port_up{node="gpu-lab-worker2"}) == 0' \
  "gpu_lab_training_fabric_fault_active{namespace=\"$namespace\"}" \
  "max(gpu_lab_training_fabric_fault_active{namespace=\"$namespace\"}) > 0"

run_scenario \
  "ib-rate-degraded" \
  'gpu_lab_ib_link_rate_gbps{node="gpu-lab-worker3"}' \
  'min(gpu_lab_ib_link_rate_gbps{node="gpu-lab-worker3"}) < 100' \
  "gpu_lab_training_fabric_delay_seconds{namespace=\"$namespace\"}" \
  "max(gpu_lab_training_fabric_delay_seconds{namespace=\"$namespace\"}) > 0"

run_scenario \
  "ib-symbol-errors" \
  'gpu_lab_ib_symbol_errors_total{node="gpu-lab-worker"}' \
  'max(gpu_lab_ib_symbol_errors_total{node="gpu-lab-worker"}) > 0' \
  "gpu_lab_training_fabric_errors_total{namespace=\"$namespace\"}" \
  "sum(gpu_lab_training_fabric_errors_total{namespace=\"$namespace\"}) > 0"

run_scenario \
  "rdma-retry-storm" \
  'gpu_lab_rdma_retries_total{node="gpu-lab-worker2"}' \
  'max(gpu_lab_rdma_retries_total{node="gpu-lab-worker2"}) > 0' \
  "gpu_lab_training_fabric_retries_total{namespace=\"$namespace\"}" \
  "sum(gpu_lab_training_fabric_retries_total{namespace=\"$namespace\"}) > 0"

run_scenario \
  "ib-congestion" \
  'gpu_lab_ib_xmit_wait_total{node="gpu-lab-worker3"}' \
  'max(gpu_lab_ib_xmit_wait_total{node="gpu-lab-worker3"}) > 0' \
  "gpu_lab_training_fabric_delay_seconds{namespace=\"$namespace\"}" \
  "max(gpu_lab_training_fabric_delay_seconds{namespace=\"$namespace\"}) > 0"

gpu training status --namespace "$namespace"
echo "gpu-lab five-scenario synthetic IB/RDMA recovery E2E passed"
