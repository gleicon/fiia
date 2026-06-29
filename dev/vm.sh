#!/usr/bin/env bash
# dev/vm.sh — VM lifecycle and shell wrapper
#
# Usage: dev/vm.sh <command> [args...]
#
# Commands:
#   create       create the VM (first-time setup)
#   start        start the VM
#   stop         stop the VM
#   shell <cmd>  run a command inside the VM (or on this host in local mode)
#   ssh-config   emit SSH config for the VM (stdout)
#   arch         print the VM's CPU architecture (uname -m)
#   host         print the hostname the VM uses to reach the macOS host
#   node-id      print the default dev node ID
#   status       print current backend and VM status
#
# Backend is read from dev/.backend (written by dev/setup.sh / make dev-setup).
# Override: DEV_VM_BACKEND=colima dev/vm.sh start
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_FILE="$SCRIPT_DIR/.backend"

# ── load backend config ────────────────────────────────────────────────────────
if [[ -z "${DEV_VM_BACKEND:-}" ]]; then
  if [[ ! -f "$BACKEND_FILE" ]]; then
    echo "ERROR: dev/.backend not found. Run: make dev-setup" >&2
    exit 1
  fi
  # parse Makefile := assignment syntax
  DEV_VM_BACKEND="$(grep '^DEV_VM_BACKEND' "$BACKEND_FILE" | sed 's/DEV_VM_BACKEND[[:space:]]*:=[[:space:]]*//')"
  DEV_VM_NAME="$(grep '^DEV_VM_NAME' "$BACKEND_FILE" | sed 's/DEV_VM_NAME[[:space:]]*:=[[:space:]]*//' || echo 'fiia-dev')"
fi

VM_NAME="${DEV_VM_NAME:-fiia-dev}"
CMD="${1:-help}"
shift || true

# ── dispatch ───────────────────────────────────────────────────────────────────
case "$DEV_VM_BACKEND" in

# ── Lima ──────────────────────────────────────────────────────────────────────
lima)
  case "$CMD" in
    create)
      limactl create --name "$VM_NAME" template://ubuntu-lts --cpus 2 --memory 2
      echo "VM created. Start with: make dev-vm-start"
      ;;
    start)      exec limactl start "$VM_NAME" ;;
    stop)       exec limactl stop "$VM_NAME" ;;
    shell)      exec limactl shell "$VM_NAME" -- "$@" ;;
    ssh-config) exec limactl show-ssh --format config "$VM_NAME" ;;
    arch)       exec limactl shell "$VM_NAME" -- uname -m ;;
    host)       echo "host.lima.internal" ;;
    node-id)    echo "lima-dev" ;;
    status)
      echo "backend: lima"
      limactl list 2>/dev/null | grep -E "^NAME|^${VM_NAME}" || echo "(VM not found)"
      ;;
    *) echo "unknown command: $CMD" >&2; exit 1 ;;
  esac
  ;;

# ── Colima ────────────────────────────────────────────────────────────────────
colima)
  case "$CMD" in
    create)     echo "Colima creates the VM on first start. Run: make dev-vm-start" ;;
    start)      exec colima start --cpu 2 --memory 2 --disk 10 ;;
    stop)       exec colima stop ;;
    shell)      exec colima ssh -- "$@" ;;
    ssh-config) exec colima ssh-config ;;
    arch)       exec colima ssh -- uname -m ;;
    host)       echo "host.lima.internal" ;;
    node-id)    echo "colima-dev" ;;
    status)
      echo "backend: colima"
      colima status 2>&1 || echo "(not running)"
      ;;
    *) echo "unknown command: $CMD" >&2; exit 1 ;;
  esac
  ;;

# ── Apple Container ───────────────────────────────────────────────────────────
apple-container)
  case "$CMD" in
    create)
      container system start 2>/dev/null || true
      container run --name "$VM_NAME" --detach ghcr.io/apple/swift:latest sleep infinity
      echo "Note: apple-container support is experimental. systemd not available."
      ;;
    start)      container system start ;;
    stop)       container stop "$VM_NAME" 2>/dev/null && container system stop ;;
    shell)      exec container exec "$VM_NAME" "$@" ;;
    ssh-config) echo "# apple-container does not use SSH; ansible_connection=docker" ;;
    arch)       container exec "$VM_NAME" uname -m ;;
    host)       echo "host.containers.internal" ;;
    node-id)    echo "container-dev" ;;
    status)
      echo "backend: apple-container"
      container list 2>/dev/null | grep "$VM_NAME" || echo "(not running)"
      ;;
    *) echo "unknown command: $CMD" >&2; exit 1 ;;
  esac
  ;;

# ── Multipass ─────────────────────────────────────────────────────────────────
multipass)
  case "$CMD" in
    create)
      multipass launch --name "$VM_NAME" --cpus 2 --memory 2G --disk 10G lts
      echo "VM created. Start with: make dev-vm-start (already started)"
      ;;
    start)      exec multipass start "$VM_NAME" ;;
    stop)       exec multipass stop "$VM_NAME" ;;
    shell)      exec multipass exec "$VM_NAME" -- "$@" ;;
    ssh-config) multipass info "$VM_NAME" --format json \
                  | python3 -c "
import sys,json
d=json.load(sys.stdin)['info']['$VM_NAME']
ip=d['ipv4'][0]
print(f'Host {VM_NAME}\n  HostName {ip}\n  User ubuntu\n  StrictHostKeyChecking no')
" ;;
    arch)       exec multipass exec "$VM_NAME" -- uname -m ;;
    host)
      multipass info "$VM_NAME" --format json 2>/dev/null \
        | python3 -c "import sys,json; print(json.load(sys.stdin)['info']['${VM_NAME}']['ipv4'][0])" \
        2>/dev/null || echo "localhost"
      ;;
    node-id)    echo "multipass-dev" ;;
    status)
      echo "backend: multipass"
      multipass list 2>/dev/null | grep -E "^Name|^${VM_NAME}" || echo "(VM not found)"
      ;;
    *) echo "unknown command: $CMD" >&2; exit 1 ;;
  esac
  ;;

# ── Local (Linux / no VM) ─────────────────────────────────────────────────────
local)
  case "$CMD" in
    create|start|stop) echo "local mode: no VM" ;;
    shell)      exec "$@" ;;
    ssh-config) true ;;
    arch)       exec uname -m ;;
    host)       echo "localhost" ;;
    node-id)    echo "local-dev" ;;
    status)     echo "backend: local (no VM, running on this machine)" ;;
    *) echo "unknown command: $CMD" >&2; exit 1 ;;
  esac
  ;;

*)
  echo "ERROR: unknown backend '$DEV_VM_BACKEND'. Run: make dev-setup" >&2
  exit 1
  ;;
esac
