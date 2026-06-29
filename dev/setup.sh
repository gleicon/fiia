#!/usr/bin/env bash
# dev/setup.sh — detect VM backend, write dev/.backend for Makefile and dev/vm.sh
# Run: make dev-setup   OR   bash dev/setup.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_FILE="$SCRIPT_DIR/.backend"
VM_NAME="${DEV_VM_NAME:-fiia-dev}"
OS="$(uname -s)"

echo "=== fiia dev environment setup ==="
echo ""

# ── Linux: no VM needed ────────────────────────────────────────────────────────
if [[ "$OS" == "Linux" ]]; then
  echo "Linux detected — using local mode (ansible runs on this machine directly)."
  echo ""
  echo "Checking tools:"
  _ok=true
  for tool in python3 ansible-playbook docker; do
    if command -v "$tool" >/dev/null 2>&1; then
      echo "  [ok] $tool"
    else
      echo "  [--] $tool  (not found)"
      [[ "$tool" != "docker" ]] && _ok=false
    fi
  done
  echo ""
  if [[ "$_ok" == "false" ]]; then
    echo "Install missing tools, then re-run this script."
    exit 1
  fi
  {
    echo "DEV_VM_BACKEND := local"
    echo "DEV_VM_NAME    := local"
  } > "$BACKEND_FILE"
  echo "Written: $BACKEND_FILE"
  echo ""
  echo "Next: make dev-hub  (in one terminal)"
  echo "      make dev-deploy"
  exit 0
fi

# ── macOS: probe available backends ───────────────────────────────────────────
# Priority: Lima > Colima > Multipass > Apple Container
# Lima is preferred: full systemd, lightweight, and Colima wraps it anyway.
# Apple Container is listed last — experimental, no systemd, OCI-only.

echo "macOS detected. Probing VM backends (best → fallback):"
echo ""

BACKEND=""

# Lima (recommended — full systemd, lightweight VM)
if command -v limactl >/dev/null 2>&1; then
  VER="$(limactl --version 2>/dev/null | head -1 || echo '')"
  echo "  [ok] Lima  $VER  ← recommended"
  BACKEND="${BACKEND:-lima}"
fi

# Colima (Lima + Docker daemon; also fine)
if command -v colima >/dev/null 2>&1; then
  VER="$(colima version 2>/dev/null | head -1 || echo '')"
  echo "  [ok] Colima  $VER"
  BACKEND="${BACKEND:-colima}"
fi

# Multipass
if command -v multipass >/dev/null 2>&1; then
  VER="$(multipass version 2>/dev/null | head -1 || echo '')"
  echo "  [ok] Multipass  $VER"
  BACKEND="${BACKEND:-multipass}"
fi

# Apple Container (experimental — no systemd; use for OCI workloads only)
if command -v container >/dev/null 2>&1; then
  VER="$(container --version 2>/dev/null | head -1 || echo '')"
  echo "  [ok] Apple Container  $VER  (experimental, no systemd)"
  BACKEND="${BACKEND:-apple-container}"
fi

echo ""

# Docker (needed for collection tests regardless of VM choice)
DOCKER_OK=false
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  echo "  [ok] Docker daemon reachable (required for: make dev-collection-test)"
  DOCKER_OK=true
else
  echo "  [--] Docker daemon not reachable (make dev-collection-test will not work)"
  echo "       Install Docker Desktop, OrbStack, or start Colima."
fi
echo ""

if [[ -z "$BACKEND" ]]; then
  echo "ERROR: no VM backend found. Install one of:"
  echo ""
  echo "  Lima (recommended):"
  echo "    brew install lima"
  echo ""
  echo "  Colima (Docker included):"
  echo "    brew install colima && colima start"
  echo ""
  echo "  Apple Container:"
  echo "    https://github.com/apple/container"
  exit 1
fi

echo "Selected backend: $BACKEND (VM name: $VM_NAME)"
echo ""

# Write .backend (Makefile include format)
{
  echo "DEV_VM_BACKEND := $BACKEND"
  echo "DEV_VM_NAME    := $VM_NAME"
} > "$BACKEND_FILE"
echo "Written: $BACKEND_FILE"
echo ""

# ── next-step guidance ─────────────────────────────────────────────────────────
case "$BACKEND" in
lima)
  if limactl list -f '{{.Name}}' 2>/dev/null | grep -q "^${VM_NAME}$"; then
    STATUS="$(limactl list 2>/dev/null | awk -v n="$VM_NAME" '$1==n{print $2}')"
    echo "Lima VM '$VM_NAME' exists (status: ${STATUS:-unknown})"
    if [[ "${STATUS:-}" == "Running" ]]; then
      echo "Ready. Run: make dev-hub  (terminal 1)  &&  make dev-deploy"
    else
      echo "Next: make dev-vm-start"
    fi
  else
    echo "Lima VM '$VM_NAME' does not exist yet."
    echo "Next: make dev-vm-create  &&  make dev-vm-start  &&  make dev-deploy"
  fi
  ;;
colima)
  if colima status >/dev/null 2>&1; then
    echo "Colima is running. Ready."
    echo "Next: make dev-hub  (terminal 1)  &&  make dev-deploy"
  else
    echo "Next: make dev-vm-start  &&  make dev-deploy"
  fi
  ;;
apple-container)
  echo "Apple Container backend selected."
  echo "Note: apple-container support is experimental."
  echo "Next: make dev-vm-create  &&  make dev-vm-start  &&  make dev-deploy"
  ;;
multipass)
  if multipass list 2>/dev/null | grep -q "$VM_NAME"; then
    echo "Multipass VM '$VM_NAME' exists."
  else
    echo "Next: make dev-vm-create  &&  make dev-vm-start  &&  make dev-deploy"
  fi
  ;;
esac

echo ""
[[ "$DOCKER_OK" == "true" ]] && echo "Collection test: make dev-collection-test"
