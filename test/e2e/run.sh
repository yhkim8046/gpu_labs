#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

# shellcheck source=test/e2e/common.sh
source "$repo_root/test/e2e/common.sh"
gpu_e2e_init run

keep_cluster="${E2E_KEEP_CLUSTER:-0}"
cluster_owned=0
cluster_preflight_refused=0

gpu_fault_scenarios=(
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

expected_scenarios=(
  normal
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
  thermal-escalation
  ib-link-down
  ib-rate-degraded
  ib-symbol-errors
  rdma-retry-storm
  ib-congestion
)

collect_failure_state() {
  gpu_e2e_collect_failure_state run-failure
}

cleanup() {
  local status=$?
  trap - EXIT
  if [[ "$status" -ne 0 && "$cluster_preflight_refused" != "1" ]]; then
    collect_failure_state
  fi
  if [[ "$cluster_owned" == "1" && "$keep_cluster" != "1" ]] && command -v kind >/dev/null 2>&1; then
    if kind get clusters 2>/dev/null | grep -Fxq gpu-lab; then
      kind delete cluster --name gpu-lab || echo "failed to delete runner-owned gpu-lab cluster" >&2
    fi
  fi
  gpu_e2e_remove_temp
  exit "$status"
}

trap cleanup EXIT

command -v kind >/dev/null 2>&1 || {
  echo "kind is required by the full E2E runner" >&2
  exit 1
}
if ! kind_clusters="$(kind get clusters)"; then
  cluster_preflight_refused=1
  echo "could not inspect kind clusters; refusing to assume cluster ownership" >&2
  exit 1
fi
if grep -Fxq gpu-lab <<<"$kind_clusters"; then
  if [[ "${E2E_ALLOW_EXISTING_CLUSTER:-0}" != "1" ]]; then
    cluster_preflight_refused=1
    echo "kind cluster gpu-lab already exists; set E2E_ALLOW_EXISTING_CLUSTER=1 to opt in to reuse" >&2
    exit 2
  fi
  echo "reusing explicitly approved existing gpu-lab cluster; it will not be deleted"
  export E2E_CLUSTER_CREATED_BY_RUN=0
else
  # This name was absent at preflight, so cleanup owns only the cluster this
  # run creates, including a partially bootstrapped cluster after a failure.
  cluster_owned=1
  export E2E_CLUSTER_CREATED_BY_RUN=1
fi

actual_scenarios="$(gpu scenario list | LC_ALL=C sort)"
expected_scenario_list="$(printf '%s\n' "${expected_scenarios[@]}" | LC_ALL=C sort)"
if [[ "$actual_scenarios" != "$expected_scenario_list" ]]; then
  echo "expected exactly 22 built-in scenarios; got:" >&2
  printf '%s\n' "$actual_scenarios" >&2
  exit 1
fi

gpu doctor
gpu create
gpu helm catalog
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
gpu helm install all
gpu helm list --all-namespaces
gpu doctor

gpu verify normal
gpu inspect --mode synthetic --json --timeout 30s >"$E2E_LOG_DIR/inspection.json"
python3 - "$E2E_LOG_DIR/inspection.json" <<'PY'
import json, sys
with open(sys.argv[1]) as source:
    report = json.load(source)
assert report['mode'] == 'synthetic' and report['unofficial'] is True, report
identities = [check for check in report['checks'] if check['name'].endswith(' synthetic identity')]
assert len(identities) == 24, identities
assert all(check['status'] not in ('fail', 'unavailable') for check in report['checks']), report
print('inspection JSON: 24 synthetic GPUs and no unavailable/failed checks')
PY
gpu nvidia-smi --query-gpu=temperature.gpu,memory.used,utilization.gpu --format=csv,noheader,nounits >"$E2E_LOG_DIR/gpu.csv"
python3 - "$E2E_LOG_DIR/gpu.csv" <<'PY'
import csv, math, sys
with open(sys.argv[1]) as source:
    rows = list(csv.reader(source))
assert len(rows) == 24 and all(len(row) == 3 for row in rows), rows
assert all(math.isfinite(float(value.strip())) for row in rows for value in row), rows
print('CSV stdout: 24 rows and 3 finite numeric columns')
PY
gpu nvidia-smi --list-gpus
exporter_pod="$(kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system get pods -l app.kubernetes.io/name=dcgm-exporter -o jsonpath='{.items[0].metadata.name}')"
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- nvidia-smi --list-gpus
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- ibstat
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- ibstatus
kubectl --context gpu-lab --request-timeout=10s -n gpu-lab-system exec "$exporter_pod" -- ibv_devinfo -v

for scenario_name in "${gpu_fault_scenarios[@]}"; do
  echo "== GPU scenario: $scenario_name =="
  gpu scenario run "$scenario_name"
  gpu verify "$scenario_name"
  if [[ "$scenario_name" == "xid-79" ]]; then
    xid_errors="$(gpu nvidia-smi --query-gpu=xid.errors --format=csv,noheader,nounits | sort -u)"
    grep -qx '79' <<<"$xid_errors"
  fi
  gpu scenario reset
  gpu verify normal
done

# Assert every declared phase and its readings from fresh Prometheus samples.
echo "== thermal-escalation timeline =="
gpu scenario run thermal-escalation
gpu_wait_metric \
  'gpu_lab_scenario_phase{scenario="thermal-escalation",phase="baseline"}' \
  'count(gpu_lab_scenario_phase{scenario="thermal-escalation",phase="baseline"} == 1) == 3' \
  'all exporters adopted thermal baseline' 20 20
gpu_wait_metric \
  gpu_lab_gpu_temperature_celsius \
  'max(gpu_lab_gpu_temperature_celsius) == 45' \
  'thermal baseline at t=0s' 20 45
gpu_wait_metric \
  gpu_lab_gpu_utilization_percent \
  'max(gpu_lab_gpu_utilization_percent) == 15' \
  'thermal baseline utilization' 20 45
gpu_wait_metric \
  $'gpu_lab_gpu_memory_used_bytes\ngpu_lab_gpu_memory_total_bytes' \
  'max(abs(100 * gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes - 20)) < 0.000001' \
  'thermal baseline memory use' 20 45
gpu_wait_metric \
  gpu_lab_gpu_power_watts \
  'max(gpu_lab_gpu_power_watts) == 80' \
  'thermal baseline power' 20 45
gpu_wait_metric \
  gpu_lab_gpu_throttle_active \
  'max(gpu_lab_gpu_throttle_active) == 0' \
  'thermal baseline throttle inactive' 20 45
gpu_wait_metric \
  gpu_lab_gpu_temperature_celsius \
  'max(gpu_lab_gpu_temperature_celsius) >= 82' \
  'thermal warning phase at t=30s' 60 45
gpu_wait_metric \
  gpu_lab_gpu_utilization_percent \
  'max(gpu_lab_gpu_utilization_percent) == 88' \
  'thermal warning utilization' 20 45
gpu_wait_metric \
  $'gpu_lab_gpu_memory_used_bytes\ngpu_lab_gpu_memory_total_bytes' \
  'max(abs(100 * gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes - 60)) < 0.000001' \
  'thermal warning memory use' 20 45
gpu_wait_metric \
  gpu_lab_gpu_power_watts \
  'max(gpu_lab_gpu_power_watts) == 210' \
  'thermal warning power' 20 45
gpu_wait_metric \
  gpu_lab_gpu_temperature_celsius \
  'max(gpu_lab_gpu_temperature_celsius) >= 96' \
  'thermal critical phase at t=60s' 50 45
gpu_wait_metric \
  gpu_lab_gpu_utilization_percent \
  'max(gpu_lab_gpu_utilization_percent) == 62' \
  'thermal critical utilization' 20 45
gpu_wait_metric \
  $'gpu_lab_gpu_memory_used_bytes\ngpu_lab_gpu_memory_total_bytes' \
  'max(abs(100 * gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes - 74)) < 0.000001' \
  'thermal critical memory use' 20 45
gpu_wait_metric \
  gpu_lab_gpu_power_watts \
  'max(gpu_lab_gpu_power_watts) == 245' \
  'thermal critical power' 20 45
gpu_wait_metric \
  'gpu_lab_gpu_throttle_active{reason="thermal"}' \
  'max(gpu_lab_gpu_throttle_active{reason="thermal"}) == 1' \
  'thermal critical throttle active' 50 45
gpu verify thermal-escalation
gpu_wait_metric \
  gpu_lab_gpu_temperature_celsius \
  'max(gpu_lab_gpu_temperature_celsius) == 45' \
  'thermal recovery phase at t=120s' 90 45
gpu_wait_metric \
  gpu_lab_gpu_utilization_percent \
  'max(gpu_lab_gpu_utilization_percent) == 15' \
  'thermal recovery utilization' 20 45
gpu_wait_metric \
  $'gpu_lab_gpu_memory_used_bytes\ngpu_lab_gpu_memory_total_bytes' \
  'max(abs(100 * gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes - 20)) < 0.000001' \
  'thermal recovery memory use' 20 45
gpu_wait_metric \
  gpu_lab_gpu_power_watts \
  'max(gpu_lab_gpu_power_watts) == 80' \
  'thermal recovery power' 20 45
gpu_wait_metric \
  gpu_lab_gpu_throttle_active \
  'max(gpu_lab_gpu_throttle_active) == 0' \
  'thermal recovery throttle inactive' 20 45
gpu_wait_metric \
  'gpu_lab_scenario_phase{scenario="thermal-escalation",phase="recovery"}' \
  'count(gpu_lab_scenario_phase{scenario="thermal-escalation",phase="recovery"} == 1) == 3' \
  'all exporters adopted thermal recovery' 30 20
gpu verify thermal-escalation
gpu scenario reset
gpu verify normal

./test/e2e/training.sh
./test/e2e/ib.sh

echo "gpu-lab full E2E passed: all 22 built-ins, thermal timeline, training recovery, and five fabric recovery drills"
