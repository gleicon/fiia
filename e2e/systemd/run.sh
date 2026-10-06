#!/usr/bin/env bash
# Fiia E2E driven by Ansible against a systemd-capable Docker container.
# Nothing on the host or any VM is modified.
#
#   stage 1  provision.yml  -> tracked service + full fiia.fleet.agent role;
#                              asserts active (sd_notify), watchdog 120s,
#                              unit/logrotate/config/manifest present
#   stage 2  drift.yml      -> stop+disable service, drift sentinel; asserts the
#                              running daemon reports DRIFT; restores; asserts clean
#
# Usage: ./e2e/systemd/run.sh  (from the repo root)  |  make e2e-systemd
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"
BIN_DIR="$E2E_DIR/bin"
BASELINE="$REPO_ROOT/e2e/baseline/sentinel.txt"
CTR="fiia-e2e-systemd"
IMG="fiia-e2e-systemd:local"

require() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: $1 not found"; exit 1; }; }

require docker
require ansible-playbook

if ! ansible-galaxy collection list 2>/dev/null | grep -q 'community\.docker'; then
  echo "installing community.docker collection..."
  ansible-galaxy collection install community.docker
fi

export DOCKER_API_VERSION="${DOCKER_API_VERSION:-1.44}"

# Expose the source fiia.fleet collection (singular env for ansible-core 2.21+,
# plural for older cores; one is always inert).
COLL_STAGE="$E2E_DIR/collections"
mkdir -p "$COLL_STAGE/ansible_collections/fiia"
ln -sfn "$REPO_ROOT/ansible/collections/fiia/fleet" "$COLL_STAGE/ansible_collections/fiia/fleet"
export ANSIBLE_COLLECTIONS_PATH="$COLL_STAGE"
export ANSIBLE_COLLECTIONS_PATHS="$COLL_STAGE"

case "$(docker info --format '{{.Architecture}}' 2>/dev/null)" in
  x86_64 | amd64) GOARCH=amd64 ;;
  aarch64 | arm64) GOARCH=arm64 ;;
  *) GOARCH=amd64 ;;
esac

echo "=== build linux/$GOARCH agent (repo-local, gitignored) ==="
mkdir -p "$BIN_DIR"
(cd "$REPO_ROOT" && GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 go build -o "$BIN_DIR/fiia-agent" ./cmd/agent)

echo "=== build systemd target image ($IMG) ==="
docker build -q -t "$IMG" "$E2E_DIR" >/dev/null

echo "=== start privileged systemd container $CTR ==="
# NOTE: only the cgroup mount is passed in. Mounting the host's /run (or /tmp)
# leaks the host's generated systemd units into the container (boot.mount etc.,
# which never settle and stall the boot) and lets the container write into the
# host's /run — both caused "initializing"/"maintenance" boot states.
docker rm -f "$CTR" >/dev/null 2>&1 || true
docker run -d --name "$CTR" --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  "$IMG" >/dev/null

echo "=== wait for systemd to be ready ==="
state=""
for i in $(seq 1 45); do
  state="$(docker exec "$CTR" systemctl is-system-running 2>/dev/null || true)"
  case "$state" in running | degraded) break ;; esac
  sleep 2
done
if [ "$state" != running ] && [ "$state" != degraded ]; then
  echo "FAIL: systemd not ready (state=$state)"
  docker logs "$CTR" 2>&1 | tail -n 20
  docker rm -f "$CTR" >/dev/null 2>&1 || true
  exit 1
fi

ANSIBLE_ARGS=(
  -i "$CTR,"
  -e "ansible_connection=community.docker.docker"
  -e "ansible_python_interpreter=/usr/bin/python3"
  -e "agent_binary=$BIN_DIR/fiia-agent"
  -e "baseline_file=$BASELINE"
)

finish() {
  docker rm -f "$CTR" >/dev/null 2>&1 || true
  rm -rf "$COLL_STAGE"
  if docker ps -a --format '{{.Names}}' | grep -q "^$CTR$"; then
    echo "FAIL: container $CTR not removed"
    exit 1
  fi
}
trap finish EXIT

echo ""
echo "=== stage 1: provision agent + tracked service via the fiia.fleet.agent role ==="
ansible-playbook "${ANSIBLE_ARGS[@]}" "$E2E_DIR/provision.yml"

echo ""
echo "=== stage 2: drift service + file, daemon detects, restore ==="
ansible-playbook "${ANSIBLE_ARGS[@]}" "$E2E_DIR/drift.yml"

echo ""
echo "E2E (systemd over Docker): all stages passed"