#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

namespace="${GPU_LAB_TRAINING_NAMESPACE:-gpu-lab-demo}"
image="${GPU_LAB_TRAINING_IMAGE:-gpu-lab:dev}"
keep_job="${TRAINING_E2E_KEEP_JOB:-0}"

cleanup() {
  status=$?
  go run ./cmd/gpu-lab training recover --namespace "$namespace" || true
  if [[ "$keep_job" != "1" ]]; then
    go run ./cmd/gpu-lab training reset --namespace "$namespace" || true
  fi
  exit "$status"
}

wait_metric() {
  local pod="$1"
  local pattern="$2"
  for _ in {1..36}; do
    if kubectl --context gpu-lab get --raw "/api/v1/namespaces/${namespace}/pods/${pod}:9401/proxy/metrics" | grep -Eq "$pattern"; then
      return 0
    fi
    sleep 5
  done
  return 1
}

trap cleanup EXIT

go run ./cmd/gpu-lab training run --workers 3 --image "$image" --namespace "$namespace" --wait --timeout 5m

ready="$(kubectl --context gpu-lab -n "$namespace" get statefulset gpu-lab-training -o jsonpath='{.status.readyReplicas}')"
[[ "$ready" == "3" ]]

nodes="$(kubectl --context gpu-lab -n "$namespace" get pods -l app.kubernetes.io/name=gpu-lab-training -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u | wc -l | tr -d ' ')"
[[ "$nodes" == "3" ]]

wait_metric gpu-lab-training-0 '^gpu_lab_training_step.* [1-9][0-9]*$'

go run ./cmd/gpu-lab training inject straggler --namespace "$namespace" --rank 2 --delay 2s
wait_metric gpu-lab-training-2 '^gpu_lab_training_straggler_active.* 1$'
wait_metric gpu-lab-training-0 '^gpu_lab_training_allreduce_seconds.* [1-9][0-9]*(\.[0-9]+)?$'

go run ./cmd/gpu-lab training recover --namespace "$namespace"
wait_metric gpu-lab-training-2 '^gpu_lab_training_straggler_active.* 0$'

go run ./cmd/gpu-lab training inject worker-crash --namespace "$namespace" --rank 1
for _ in {1..36}; do
  restarts="$(kubectl --context gpu-lab -n "$namespace" get pod gpu-lab-training-1 -o jsonpath='{.status.containerStatuses[0].restartCount}')"
  if [[ "$restarts" -ge 1 ]]; then
    break
  fi
  sleep 5
done
[[ "$restarts" -ge 1 ]]
kubectl --context gpu-lab -n "$namespace" wait --for=condition=Ready pod/gpu-lab-training-1 --timeout=2m
wait_metric gpu-lab-training-1 '^gpu_lab_training_restarts_total.* [2-9][0-9]*$'

go run ./cmd/gpu-lab training recover --namespace "$namespace"
go run ./cmd/gpu-lab training status --namespace "$namespace"

echo "gpu-lab distributed-training e2e passed"
