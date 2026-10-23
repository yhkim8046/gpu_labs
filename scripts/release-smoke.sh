#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  printf 'usage: %s <binary-directory> <version-or-tag>\n' "${0##*/}" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
bin_dir=$1
expected_version=${2#v}
[[ -d "$bin_dir" && -n "$expected_version" ]] || usage

required_binaries=(gpu gpu-lab nvidia-smi ibstat ibstatus ibv_devinfo)
for binary in "${required_binaries[@]}"; do
  path="${bin_dir%/}/$binary"
  if [[ ! -f "$path" || ! -x "$path" ]]; then
    printf 'release smoke: missing or non-executable binary: %s\n' "$path" >&2
    exit 1
  fi
done

help_output="$("${bin_dir%/}/gpu" --help)"
grep -Fq 'Usage:' <<<"$help_output"
grep -Fq 'gpu scenario list' <<<"$help_output"

alias_help="$("${bin_dir%/}/gpu-lab" --help)"
grep -Fq 'Usage:' <<<"$alias_help"

version_output="$("${bin_dir%/}/gpu" version)"
if [[ ! $version_output =~ ^gpu-lab[[:space:]]+([^[:space:]]+)[[:space:]]+\(commit[[:space:]]+([^,[:space:]]+),[[:space:]]+built ]]; then
  printf 'release smoke: unexpected version output: %s\n' "$version_output" >&2
  exit 1
fi
reported_version=${BASH_REMATCH[1]}
commit=${BASH_REMATCH[2]}
if [[ "$reported_version" != "$expected_version" ]]; then
  printf 'release smoke: expected version %s, got %s\n' "$expected_version" "$reported_version" >&2
  exit 1
fi
if [[ -z "$commit" || "$commit" == unknown ]]; then
  printf 'release smoke: release commit is missing or unknown: %s\n' "$version_output" >&2
  exit 1
fi

scenario_output="$("${bin_dir%/}/gpu" scenario list)"
scenario_count="$(awk 'NF { count++ } END { print count + 0 }' <<<"$scenario_output")"
if [[ "$scenario_count" != 22 ]]; then
  printf 'release smoke: expected 22 built-in scenarios, found %s\n' "$scenario_count" >&2
  exit 1
fi

training_help="$("${bin_dir%/}/gpu" training --help)"
grep -Fq 'gpu training run' <<<"$training_help"
grep -Fq 'gpu training recover' <<<"$training_help"

recovery_help="$("${bin_dir%/}/gpu" verify --recovery --help)"
grep -Fq 'gpu verify --recovery [fault]' <<<"$recovery_help"
grep -Fq 'known fabric faults:' <<<"$recovery_help"

inspect_help="$("${bin_dir%/}/gpu" inspect --help)"
grep -Fq 'gpu inspect' <<<"$inspect_help"
grep -Fq 'synthetic' <<<"$inspect_help"

printf 'release smoke passed: version=%s commit=%s binaries=%d scenarios=%s\n' \
  "$expected_version" "$commit" "${#required_binaries[@]}" "$scenario_count"
