#!/usr/bin/env bash

gpu_e2e_init() {
  local script_name="${1:?E2E script name is required}"
  local log_root="${GPU_LAB_E2E_LOG_DIR:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/gpu-lab-e2e-logs}"

  mkdir -p "$log_root"
  E2E_LOG_DIR="$log_root"
  export GPU_LAB_E2E_LOG_DIR="$E2E_LOG_DIR"
  exec > >(tee -a "$E2E_LOG_DIR/${script_name}.log") 2>&1

  if [[ -n "${GPU_LAB_CLI:-}" ]]; then
    if [[ -x "$GPU_LAB_CLI" ]]; then
      GPU_LAB_CLI="$(cd -- "$(dirname -- "$GPU_LAB_CLI")" && pwd)/$(basename -- "$GPU_LAB_CLI")"
    elif command -v "$GPU_LAB_CLI" >/dev/null 2>&1; then
      GPU_LAB_CLI="$(command -v "$GPU_LAB_CLI")"
    else
      echo "GPU_LAB_CLI is not executable or present on PATH: $GPU_LAB_CLI" >&2
      return 1
    fi
    export GPU_LAB_CLI
    echo "using installed GPU Lab CLI: $GPU_LAB_CLI"
  else
    command -v go >/dev/null 2>&1 || {
      echo "go is required when GPU_LAB_CLI is not set" >&2
      return 1
    }
    E2E_TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gpu-lab-e2e.XXXXXX")"
    GPU_LAB_CLI="$E2E_TMP_DIR/gpu-lab"
    if [[ -z "${GPU_LAB_IMAGE_SOURCE+x}" ]]; then
      GPU_LAB_IMAGE_SOURCE=local
      export GPU_LAB_IMAGE_SOURCE
    fi
    echo "building source CLI to $GPU_LAB_CLI (image source: $GPU_LAB_IMAGE_SOURCE)"
    go build -trimpath -o "$GPU_LAB_CLI" ./cmd/gpu-lab
    export GPU_LAB_CLI
  fi
}

gpu() {
  "$GPU_LAB_CLI" "$@"
}

gpu_e2e_remove_temp() {
  if [[ -n "${E2E_TMP_DIR:-}" && -d "$E2E_TMP_DIR" ]]; then
    rm -rf -- "$E2E_TMP_DIR"
  fi
}

gpu_e2e_require_cluster_opt_in() {
  local test_name="${1:-E2E}"
  if [[ "${E2E_CLUSTER_CREATED_BY_RUN:-0}" == "1" || "${E2E_ALLOW_EXISTING_CLUSTER:-0}" == "1" ]]; then
    return 0
  fi
  echo "$test_name uses the existing gpu-lab cluster; set E2E_ALLOW_EXISTING_CLUSTER=1 to opt in" >&2
  return 2
}

gpu_e2e_guarded_query() {
  local selector="${1:?Prometheus selector is required}"
  local expression="${2:?Prometheus expression is required}"
  local max_age="${3:-30}"
  local samples_since="${GPU_LAB_E2E_SAMPLES_SINCE:-0}"
  local samples guard="" check

  # Each newline-delimited selector names one metric. timestamp() removes
  # __name__, so combining metric families first would create label collisions.
  while IFS= read -r samples; do
    [[ -n "$samples" ]] || continue
    check="(count($samples) > 0) and (count(($samples) < +Inf) == count($samples)) and (count(($samples) > -Inf) == count($samples)) and (time() - max(timestamp($samples)) >= 0) and (time() - min(timestamp($samples)) <= $max_age) and (min(timestamp($samples)) >= $samples_since)"
    guard="${guard:+$guard and }$check"
  done <<<"$selector"
  printf '((%s) and (%s))\n' "$expression" "$guard"
}

gpu_e2e_query_value() {
  local selector="${1:?Prometheus selector is required}"
  local expression="${2:?Prometheus expression is required}"
  local max_age="${3:-30}"
  local query encoded response api_path

  query="$(gpu_e2e_guarded_query "$selector" "$expression" "$max_age")"
  encoded="$(python3 -c 'import sys; from urllib.parse import quote_plus; print(quote_plus(sys.argv[1]))' "$query")"
  api_path="/api/v1/namespaces/gpu-lab-monitoring/services/gpu-lab-monitoring-kube-pr-prometheus:9090/proxy/api/v1/query?query=$encoded"

  if ! response="$(kubectl --context gpu-lab --request-timeout=10s get --raw "$api_path")"; then
    echo "bounded Prometheus query failed: $expression" >&2
    return 1
  fi

  python3 -c '
import json, math, sys, time

try:
    payload = json.load(sys.stdin)
    if payload.get("status") != "success":
        raise ValueError(payload.get("error") or "Prometheus returned a non-success status")
    data = payload.get("data", {})
    kind = data.get("resultType")
    result = data.get("result")
    if kind == "vector":
        if not isinstance(result, list) or len(result) != 1:
            raise ValueError("query must return exactly one fresh sample")
        pair = result[0].get("value")
    elif kind == "scalar":
        pair = result
    else:
        raise ValueError("query returned an unsupported result type")
    if not isinstance(pair, list) or len(pair) < 2:
        raise ValueError("query result is missing its timestamp or value")
    observed_at = float(pair[0])
    value = float(pair[1])
    age = time.time() - observed_at
    if not math.isfinite(observed_at) or not math.isfinite(value):
        raise ValueError("query result contains a non-finite timestamp or value")
    if age < -30 or age > 30:
        raise ValueError("Prometheus query response timestamp is stale or in the future")
    print(format(value, ".17g"))
except (ValueError, TypeError, KeyError, json.JSONDecodeError) as error:
    print("invalid Prometheus result: " + str(error), file=sys.stderr)
    sys.exit(1)
' <<<"$response"
}

gpu_wait_metric() {
  local selector="${1:?Prometheus selector is required}"
  local expression="${2:?Prometheus condition is required}"
  local description="${3:?Description is required}"
  local timeout="${4:-60}"
  local max_age="${5:-30}"
  local deadline=$((SECONDS + timeout))
  local error_file value
  error_file="$(mktemp "${TMPDIR:-/tmp}/gpu-lab-e2e-query.XXXXXX")"

  while :; do
    if value="$(gpu_e2e_query_value "$selector" "$expression" "$max_age" 2>"$error_file")"; then
      echo "ok: $description (value=$value)"
      rm -f -- "$error_file"
      return 0
    fi
    if (( SECONDS >= deadline )); then
      echo "timed out after ${timeout}s: $description" >&2
      cat "$error_file" >&2
      rm -f -- "$error_file"
      return 1
    fi
    sleep "${GPU_LAB_E2E_POLL_INTERVAL:-5}"
  done
}

gpu_read_fresh_metric() {
  local selector="${1:?Prometheus selector is required}"
  local aggregate="${2:?Prometheus aggregate is required}"
  local max_age="${3:-30}"
  gpu_e2e_query_value "$selector" "$aggregate" "$max_age"
}

gpu_wait_metric_increase() {
  local selector="${1:?Prometheus selector is required}"
  local aggregate="${2:?Prometheus aggregate is required}"
  local previous="${3:?Starting value is required}"
  local description="${4:?Description is required}"
  local timeout="${5:-45}"
  local max_age="${6:-30}"
  local deadline=$((SECONDS + timeout))
  local error_file value
  error_file="$(mktemp "${TMPDIR:-/tmp}/gpu-lab-e2e-progress.XXXXXX")"

  while :; do
    if value="$(gpu_read_fresh_metric "$selector" "$aggregate" "$max_age" 2>"$error_file")"; then
      if python3 -c 'import math,sys; a=float(sys.argv[1]); b=float(sys.argv[2]); sys.exit(0 if math.isfinite(a) and math.isfinite(b) and b > a else 1)' "$previous" "$value"; then
        echo "ok: $description advanced $previous -> $value"
        rm -f -- "$error_file"
        return 0
      fi
    fi
    if (( SECONDS >= deadline )); then
      echo "timed out after ${timeout}s waiting for fresh monotonic progress: $description (baseline=$previous, latest=${value:-unavailable})" >&2
      cat "$error_file" >&2
      rm -f -- "$error_file"
      return 1
    fi
    sleep "${GPU_LAB_E2E_POLL_INTERVAL:-5}"
  done
}

gpu_e2e_collect_failure_state() {
  local suffix="${1:-failure}"
  local output="$E2E_LOG_DIR/${suffix}-cluster-state.txt"
  {
    echo "=== kubectl context ==="
    kubectl --context gpu-lab --request-timeout=10s get nodes -o wide || true
    echo "=== all pods ==="
    kubectl --context gpu-lab --request-timeout=10s get pods -A -o wide || true
    echo "=== recent events ==="
    kubectl --context gpu-lab --request-timeout=10s get events -A --sort-by=.lastTimestamp || true
    echo "=== monitoring resources ==="
    kubectl --context gpu-lab --request-timeout=10s get prometheusrule,servicemonitor -A || true
  } >>"$output" 2>&1
  if command -v kind >/dev/null 2>&1 && kind get clusters 2>/dev/null | grep -Fxq gpu-lab; then
    kind export logs "$E2E_LOG_DIR/kind-logs-$suffix" --name gpu-lab >>"$output" 2>&1 || true
  fi
  echo "failure diagnostics saved to $output" >&2
}
