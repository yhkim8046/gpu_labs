#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

# shellcheck source=test/e2e/common.sh
source "$repo_root/test/e2e/common.sh"
gpu_e2e_init training

namespace="${GPU_LAB_TRAINING_NAMESPACE:-gpu-lab-demo}"
keep_job="${TRAINING_E2E_KEEP_JOB:-0}"
gpu_e2e_require_cluster_opt_in "training E2E"

collect_failure_state() {
  gpu_e2e_collect_failure_state training-failure
  kubectl --context gpu-lab --request-timeout=10s -n "$namespace" get statefulset,pods,configmap -o yaml >>"$E2E_LOG_DIR/training-failure-cluster-state.txt" 2>&1 || true
}

cleanup() {
  local status=$?
  trap - EXIT
  if [[ "$status" -ne 0 ]]; then
    collect_failure_state
  fi
  gpu training recover --namespace "$namespace" || true
  if [[ "$keep_job" != "1" ]]; then
    gpu training reset --namespace "$namespace" || true
  fi
  gpu_e2e_remove_temp
  exit "$status"
}

trap cleanup EXIT

step_selector="gpu_lab_training_step{namespace=\"$namespace\"}"
checkpoint_selector="gpu_lab_training_checkpoint_step{namespace=\"$namespace\"}"
rank_selector="gpu_lab_training_rank_up{namespace=\"$namespace\"}"

# Reset the named test workload first so restart counters and checkpoints are
# attributable to this run. The cluster itself is never removed here.
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

ready="$(kubectl --context gpu-lab --request-timeout=10s -n "$namespace" get statefulset gpu-lab-training -o jsonpath='{.status.readyReplicas}')"
[[ "$ready" == "3" ]]

nodes="$(kubectl --context gpu-lab --request-timeout=10s -n "$namespace" get pods -l app.kubernetes.io/name=gpu-lab-training -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u | wc -l | tr -d ' ')"
[[ "$nodes" == "3" ]]

gpu_wait_metric "$rank_selector" "count($rank_selector == 1) == 3" "three training ranks ready" 60 20
gpu_wait_metric "$step_selector" "count($step_selector >= 1) == 3" "all ranks publish positive training progress" 60 20
gpu_wait_metric "$checkpoint_selector" "count($checkpoint_selector >= 1) == 3" "all ranks publish an initial checkpoint" 60 20

straggler_selector="gpu_lab_training_straggler_active{namespace=\"$namespace\",rank=\"2\"}"
allreduce_selector="gpu_lab_training_allreduce_seconds{namespace=\"$namespace\"}"
gpu training inject straggler --namespace "$namespace" --rank 2 --delay 15s
gpu_wait_metric "$straggler_selector" "max($straggler_selector) == 1" "rank 2 straggler injection observed" 180 20
gpu_wait_metric "$allreduce_selector" "max($allreduce_selector) > 0" "AllReduce latency remains readable during straggler" 45 20

gpu training recover --namespace "$namespace"
gpu_wait_metric "$straggler_selector" "max($straggler_selector) == 0" "rank 2 straggler control cleared" 90 20
straggler_recovery_step="$(gpu_read_fresh_metric "$step_selector" "min($step_selector)" 20)"
straggler_recovery_checkpoint="$(gpu_read_fresh_metric "$checkpoint_selector" "min($checkpoint_selector)" 20)"
gpu_wait_metric_increase "$step_selector" "min($step_selector)" "$straggler_recovery_step" "all ranks resume after straggler recovery" 60 20
gpu_wait_metric_increase "$checkpoint_selector" "min($checkpoint_selector)" "$straggler_recovery_checkpoint" "all ranks checkpoint after straggler recovery" 90 20

rank_one_step="gpu_lab_training_step{namespace=\"$namespace\",rank=\"1\"}"
rank_one_checkpoint="gpu_lab_training_checkpoint_step{namespace=\"$namespace\",rank=\"1\"}"
rank_one_restarts="gpu_lab_training_restarts_total{namespace=\"$namespace\",rank=\"1\"}"
step_before_crash="$(gpu_read_fresh_metric "$rank_one_step" "max($rank_one_step)" 20)"
checkpoint_before_crash="$(gpu_read_fresh_metric "$rank_one_checkpoint" "max($rank_one_checkpoint)" 20)"

gpu training inject worker-crash --namespace "$namespace" --rank 1
restarts=""
for _ in {1..48}; do
  restarts="$(kubectl --context gpu-lab --request-timeout=10s -n "$namespace" get pod gpu-lab-training-1 -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || true)"
  if [[ "$restarts" =~ ^[0-9]+$ ]] && (( restarts >= 1 )); then
    break
  fi
  sleep 5
done
if [[ ! "$restarts" =~ ^[0-9]+$ ]] || (( restarts < 1 )); then
  echo "training rank 1 did not restart after worker-crash injection" >&2
  exit 1
fi
kubectl --context gpu-lab --request-timeout=10s -n "$namespace" wait --for=condition=Ready pod/gpu-lab-training-1 --timeout=2m
gpu_wait_metric "$rank_one_restarts" "max($rank_one_restarts) >= 2" "rank 1 restart counter advanced" 60 20
gpu_wait_metric "$rank_one_step" "max($rank_one_step) >= $step_before_crash" "rank 1 resumed from its last checkpoint" 60 20

gpu training recover --namespace "$namespace"
gpu_wait_metric "$rank_selector" "count($rank_selector == 1) == 3" "all three ranks return after worker crash" 90 20
gpu_wait_metric "$rank_one_checkpoint" "max($rank_one_checkpoint) >= $checkpoint_before_crash" "rank 1 checkpoint restored from before its crash" 60 20
crash_recovery_step="$(gpu_read_fresh_metric "$rank_one_step" "max($rank_one_step)" 20)"
crash_recovery_checkpoint="$(gpu_read_fresh_metric "$rank_one_checkpoint" "max($rank_one_checkpoint)" 20)"
gpu_wait_metric_increase "$rank_one_step" "max($rank_one_step)" "$crash_recovery_step" "rank 1 resumes progress from its checkpoint" 60 20
gpu_wait_metric_increase "$rank_one_checkpoint" "max($rank_one_checkpoint)" "$crash_recovery_checkpoint" "rank 1 writes a newer checkpoint after restart" 90 20

# A coordinator restart discards the in-memory reduction cache. All healthy
# peers must join the replacement session and continue from a common checkpoint.
rank_zero_restarts="gpu_lab_training_restarts_total{namespace=\"$namespace\",rank=\"0\"}"
coordinator_restarts="$(gpu_read_fresh_metric "$rank_zero_restarts" "max($rank_zero_restarts)" 20)"
gpu training inject worker-crash --namespace "$namespace" --rank 0
gpu_wait_metric "$rank_zero_restarts" "max($rank_zero_restarts) > $coordinator_restarts" "coordinator restarted from its checkpoint" 240 20
gpu training recover --namespace "$namespace"
gpu_wait_metric "$rank_selector" "count($rank_selector == 1) == 3" "all ranks rejoin the replacement coordinator" 120 20
coordinator_recovery_step="$(gpu_read_fresh_metric "$step_selector" "min($step_selector)" 20)"
coordinator_recovery_checkpoint="$(gpu_read_fresh_metric "$checkpoint_selector" "min($checkpoint_selector)" 20)"
gpu_wait_metric_increase "$step_selector" "min($step_selector)" "$coordinator_recovery_step" "all ranks progress after coordinator replacement" 90 20
gpu_wait_metric_increase "$checkpoint_selector" "min($checkpoint_selector)" "$coordinator_recovery_checkpoint" "all ranks checkpoint after coordinator replacement" 90 20

gpu training status --namespace "$namespace"
echo "gpu-lab three-rank distributed-training recovery E2E passed"
