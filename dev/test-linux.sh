#!/usr/bin/env bash
# dev/test-linux.sh — compile test binaries on macOS, run them on the Linux VM.
#
# Usage:
#   dev/test-linux.sh                         # test all packages
#   dev/test-linux.sh ./internal/agent/audit  # test one package
#   dev/test-linux.sh -v ./...                # pass flags to go test
#
# Requires: running Lima or Colima VM (make dev-vm-start).
# Go does not need to be installed on the VM — test binaries are compiled locally.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── load backend ────────────────────────────────────────────────────────────────
BACKEND_FILE="$SCRIPT_DIR/.backend"
if [[ -z "${DEV_VM_BACKEND:-}" ]]; then
  if [[ ! -f "$BACKEND_FILE" ]]; then
    echo "ERROR: dev/.backend not found. Run: make dev-setup" >&2
    exit 1
  fi
  DEV_VM_BACKEND="$(grep '^DEV_VM_BACKEND' "$BACKEND_FILE" | sed 's/DEV_VM_BACKEND[[:space:]]*:=[[:space:]]*//')"
  DEV_VM_NAME="$(grep '^DEV_VM_NAME' "$BACKEND_FILE" | sed 's/DEV_VM_NAME[[:space:]]*:=[[:space:]]*//' || echo 'fiia-dev')"
fi

if [[ "$DEV_VM_BACKEND" == "local" ]]; then
  echo "local mode: running go test directly (no VM)"
  cd "$REPO_ROOT"
  exec go test "${@:-./.../}"
fi

# ── resolve VM shell command ────────────────────────────────────────────────────
case "$DEV_VM_BACKEND" in
  lima)   VM_SHELL="limactl shell ${DEV_VM_NAME:-fiia-dev} --" ;;
  colima) VM_SHELL="colima ssh --" ;;
  *)      echo "ERROR: backend '$DEV_VM_BACKEND' not supported for test-linux.sh" >&2; exit 1 ;;
esac

# ── detect VM arch ─────────────────────────────────────────────────────────────
VM_ARCH=$($VM_SHELL uname -m 2>/dev/null)
case "$VM_ARCH" in
  aarch64|arm64) GOARCH=arm64 ;;
  *)             GOARCH=amd64 ;;
esac

# ── parse args ─────────────────────────────────────────────────────────────────
# Separate go test flags (start with -) from package patterns.
GOTEST_FLAGS=()
PACKAGES=()
for arg in "$@"; do
  if [[ "$arg" == -* ]]; then
    GOTEST_FLAGS+=("$arg")
  else
    PACKAGES+=("$arg")
  fi
done
[[ ${#PACKAGES[@]} -eq 0 ]] && PACKAGES=("./...")

TMPDIR_VM="/tmp/fiia-test-$$"

# ── colima: write SSH config once for scp use ──────────────────────────────────
COLIMA_SSH_CFG=""
if [[ "$DEV_VM_BACKEND" == "colima" ]]; then
  COLIMA_SSH_CFG=$(mktemp)
  colima ssh-config 2>/dev/null > "$COLIMA_SSH_CFG"
  colima ssh -- mkdir -p "$TMPDIR_VM"
  trap 'rm -f "$COLIMA_SSH_CFG"' EXIT
fi

# ── lima: create tmpdir upfront ────────────────────────────────────────────────
if [[ "$DEV_VM_BACKEND" == "lima" ]]; then
  $VM_SHELL mkdir -p "$TMPDIR_VM"
fi

# ── compile and run each package ───────────────────────────────────────────────
cd "$REPO_ROOT"
FAILED=0

for pkg in "${PACKAGES[@]}"; do
  # Expand glob packages to a list
  pkg_list=$(go list "$pkg" 2>/dev/null) || { echo "SKIP (no Go files): $pkg"; continue; }
  for p in $pkg_list; do
    bin_name="$(echo "$p" | tr '/' '_').test"
    local_bin="/tmp/${bin_name}"

    echo "── $p ──"
    GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 \
      go test -c -o "$local_bin" "$p" 2>&1 || { echo "COMPILE FAILED: $p"; FAILED=1; continue; }

    # go test -c exits 0 but writes no binary when there are no test files.
    if [[ ! -f "$local_bin" ]]; then
      echo "  (no test files)"
      continue
    fi

    # Copy binary (and testdata/ if present) to VM
    pkg_src_dir="$(go list -f '{{.Dir}}' "$p" 2>/dev/null)"
    case "$DEV_VM_BACKEND" in
      lima)
        limactl copy "$local_bin" "${DEV_VM_NAME:-fiia-dev}:${TMPDIR_VM}/${bin_name}"
        if [[ -d "${pkg_src_dir}/testdata" ]]; then
          $VM_SHELL mkdir -p "${TMPDIR_VM}/testdata"
          for f in "${pkg_src_dir}"/testdata/*; do
            limactl copy "$f" "${DEV_VM_NAME:-fiia-dev}:${TMPDIR_VM}/testdata/$(basename "$f")"
          done
        fi
        ;;
      colima)
        scp -q -F "$COLIMA_SSH_CFG" "$local_bin" "colima:${TMPDIR_VM}/${bin_name}"
        if [[ -d "${pkg_src_dir}/testdata" ]]; then
          $VM_SHELL mkdir -p "${TMPDIR_VM}/testdata"
          scp -q -F "$COLIMA_SSH_CFG" -r "${pkg_src_dir}/testdata/." "colima:${TMPDIR_VM}/testdata/"
        fi
        ;;
    esac

    $VM_SHELL chmod +x "${TMPDIR_VM}/${bin_name}"
    $VM_SHELL sh -c "cd ${TMPDIR_VM} && ./${bin_name} ${GOTEST_FLAGS[*]:--test.v}" || FAILED=1
    rm -f "$local_bin"
  done
done

# cleanup
$VM_SHELL rm -rf "$TMPDIR_VM" 2>/dev/null || true

exit $FAILED
