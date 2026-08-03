#!/usr/bin/env bash

set -Eeuo pipefail

readonly DEFAULT_REPOSITORY="yhkim8046/gpu_labs"
readonly REQUIRED_BINARIES=(gpu gpu-lab nvidia-smi)

die() {
  printf 'gpu-lab installer: %s\n' "$*" >&2
  exit 1
}

on_error() {
  local exit_code=$?
  printf 'gpu-lab installer: installation stopped at line %s (exit %s)\n' "${BASH_LINENO[0]}" "$exit_code" >&2
  exit "$exit_code"
}

trap on_error ERR

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

download() {
  local url=$1
  local destination=$2
  if ! curl --fail --silent --show-error --location --retry 3 --retry-delay 1 \
    --output "$destination" "$url"; then
    die "download failed: ${url}"
  fi
}

detect_os() {
  case "$(uname -s)" in
    Darwin) printf 'darwin\n' ;;
    Linux) printf 'linux\n' ;;
    *) die "unsupported operating system: $(uname -s); use macOS, Linux, or WSL2" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    arm64|aarch64) printf 'arm64\n' ;;
    amd64|x86_64) printf 'amd64\n' ;;
    *) die "unsupported CPU architecture: $(uname -m); use arm64 or amd64" ;;
  esac
}

sha256_file() {
  local file=$1
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return
  fi
  die "required command not found: shasum or sha256sum"
}

resolve_latest_tag() {
  local repository=$1
  local metadata
  local tag

  metadata="$(curl --fail --silent --show-error --location --retry 3 \
    --header 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/${repository}/releases/latest")"
  tag="$(printf '%s\n' "$metadata" | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [[ -n "$tag" ]] || die "could not determine the latest GitHub Release for ${repository}"
  printf '%s\n' "$tag"
}

install_binary() {
  local source=$1
  local destination_dir=$2
  local binary=$3

  if [[ -w "$destination_dir" ]]; then
    install -m 0755 "$source" "$destination_dir/$binary"
  elif command -v sudo >/dev/null 2>&1; then
    sudo install -m 0755 "$source" "$destination_dir/$binary"
  else
    die "cannot write ${destination_dir}; rerun with GPU_LAB_INSTALL_DIR=\"${HOME}/.local/bin\""
  fi
}

main() {
  require_command curl
  require_command tar
  require_command awk
  require_command sed
  require_command mktemp
  require_command install

  local repository="${GPU_LAB_REPOSITORY:-$DEFAULT_REPOSITORY}"
  local requested_version="${GPU_LAB_VERSION:-latest}"
  local install_dir="${GPU_LAB_INSTALL_DIR:-/usr/local/bin}"
  local os
  local arch
  local tag
  local version
  local asset
  local base_url
  local temporary_dir
  local archive
  local checksums
  local extract_dir
  local expected_checksum
  local actual_checksum
  local binary

  [[ -n "$repository" ]] || die "GPU_LAB_REPOSITORY cannot be empty"
  [[ -n "$install_dir" && "$install_dir" != "/" ]] || die "GPU_LAB_INSTALL_DIR must be a non-root directory"

  os="$(detect_os)"
  arch="$(detect_arch)"

  if [[ "$requested_version" == "latest" ]]; then
    tag="$(resolve_latest_tag "$repository")"
  else
    tag="$requested_version"
    [[ "$tag" == v* ]] || tag="v${tag}"
  fi
  version="${tag#v}"
  [[ -n "$version" ]] || die "invalid release version: ${tag}"

  asset="gpu-lab_${version}_${os}_${arch}.tar.gz"
  base_url="https://github.com/${repository}/releases/download/${tag}"
  temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/gpu-lab-install.XXXXXX")"
  trap 'rm -rf -- "$temporary_dir"' EXIT

  archive="${temporary_dir}/${asset}"
  checksums="${temporary_dir}/checksums.txt"
  extract_dir="${temporary_dir}/extracted"
  mkdir -p "$extract_dir"

  printf 'Downloading GPU Lab %s for %s/%s...\n' "$version" "$os" "$arch"
  download "${base_url}/${asset}" "$archive"
  download "${base_url}/checksums.txt" "$checksums"

  expected_checksum="$(awk -v asset="$asset" '$2 == asset { print $1; exit }' "$checksums")"
  [[ -n "$expected_checksum" ]] || die "checksums.txt does not contain ${asset}"
  actual_checksum="$(sha256_file "$archive")"
  [[ "$actual_checksum" == "$expected_checksum" ]] || die "checksum verification failed for ${asset}"

  tar -xzf "$archive" -C "$extract_dir"
  for binary in "${REQUIRED_BINARIES[@]}"; do
    [[ -f "${extract_dir}/${binary}" ]] || die "release ${tag} does not contain ${binary}; publish a complete release before installing"
  done

  if [[ ! -d "$install_dir" ]]; then
    if mkdir -p "$install_dir" 2>/dev/null; then
      :
    elif command -v sudo >/dev/null 2>&1; then
      sudo mkdir -p "$install_dir"
    else
      die "cannot create ${install_dir}; rerun with GPU_LAB_INSTALL_DIR=\"${HOME}/.local/bin\""
    fi
  fi

  for binary in "${REQUIRED_BINARIES[@]}"; do
    install_binary "${extract_dir}/${binary}" "$install_dir" "$binary"
  done

  printf 'Installed GPU Lab %s to %s\n' "$version" "$install_dir"
  "${install_dir}/gpu" version

  case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *)
      printf '\nAdd this directory to PATH before running gpu:\n'
      printf '  export PATH="%s:$PATH"\n' "$install_dir"
      ;;
  esac
}

main "$@"
