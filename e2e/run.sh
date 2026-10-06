#!/usr/bin/env bash
# E2E: agent daemon -> OTel collector, clean -> file drift -> package drift -> restored.
# Usage: ./e2e/run.sh  (from the repo root)
# Stages assert against the collector's file exporter output.
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$E2E_DIR/out/telemetry.json"
# Prefer the v2 plugin (`docker compose`); fall back to standalone
# docker-compose v1. DOCKER_API_VERSION pins client negotiation for old
# compose installs; harmless when the client is already new.
export DOCKER_API_VERSION="${DOCKER_API_VERSION:-1.44}"
if command -v docker-compose >/dev/null 2>&1; then
  COMPOSE="docker-compose -f $E2E_DIR/docker-compose.yml"
else
  COMPOSE="docker compose -f $E2E_DIR/docker-compose.yml"
fi

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; exit 1; }

# $1 = wanted gauge value ("0" or "1"), $2 = timeout seconds, $3 = stage label
wait_for_status() {
  local want="$1" timeout="$2" label="$3" waited=0
  while [ "$waited" -lt "$timeout" ]; do
    if [ -f "$OUT" ] && python3 - "$OUT" "$want" << 'EOF' > /dev/null 2>&1
import json, sys
path, want = sys.argv[1], sys.argv[2]
with open(path) as fh:
    blob = fh.read()
for line in blob.splitlines():
    try:
        doc = json.loads(line)
    except ValueError:
        continue
    for rm in doc.get("resourceMetrics", []):
        for sm in rm.get("scopeMetrics", []):
            for m in sm.get("metrics", []):
                if m.get("name") != "fiia.drift.status":
                    continue
                data = m.get("gauge", {}).get("dataPoints", [])
                if any(dp.get("asInt") == want for dp in data):
                    sys.exit(0)
sys.exit(1)
EOF
    then
      pass "$label"
      return 0
    fi
    sleep 2
    waited=$((waited + 2))
  done
  fail "$label (no fiia.drift.status=$want within ${timeout}s)"
}

# $1 = timeout seconds, $2 = stage label — liveness gauge must be present
wait_for_alive() {
  local timeout="$1" label="$2" waited=0
  while [ "$waited" -lt "$timeout" ]; do
    if [ -f "$OUT" ] && python3 - "$OUT" << 'EOF' > /dev/null 2>&1
import json, sys
with open(sys.argv[1]) as fh:
    for line in fh:
        try:
            doc = json.loads(line)
        except ValueError:
            continue
        for rm in doc.get("resourceMetrics", []):
            for sm in rm.get("scopeMetrics", []):
                for m in sm.get("metrics", []):
                    if m.get("name") == "fiia.alive" and any(
                            dp.get("asInt") == "1"
                            for dp in m.get("gauge", {}).get("dataPoints", [])):
                        sys.exit(0)
sys.exit(1)
EOF
    then
      pass "$label"
      return 0
    fi
    sleep 2
    waited=$((waited + 2))
  done
  fail "$label (no fiia.alive series within ${timeout}s)"
}

# $1 = regex pattern, $2 = timeout seconds, $3 = stage label
wait_for_deviation() {
  local pattern="$1" timeout="$2" label="$3" waited=0
  while [ "$waited" -lt "$timeout" ]; do
    if [ -f "$OUT" ] && grep -q "$pattern" "$OUT" 2>/dev/null; then
      pass "$label"
      return 0
    fi
    sleep 2
    waited=$((waited + 2))
  done
  fail "$label (no '$pattern' in log records within ${timeout}s)"
}

cleanup() { $COMPOSE down -v > /dev/null 2>&1 || true; }
trap cleanup EXIT

echo "=== build + start ==="
rm -rf "$E2E_DIR/out"
mkdir -p "$E2E_DIR/out"
$COMPOSE up -d --build --quiet-pull

echo "=== stage 1: baseline reads clean + heartbeat exported ==="
wait_for_status 0 90 "clean verdict exported (fiia.drift.status=0)"
wait_for_alive 30 "liveness gauge exported (fiia.alive=1)"

echo "=== stage 2: introduce file drift ==="
$COMPOSE exec -T agent sh -c 'echo "# tamper" >> /etc/fiia/sentinel'
wait_for_status 1 90 "drift verdict exported (fiia.drift.status=1)"
wait_for_deviation "hash_mismatch" 30 "log record carries hash_mismatch deviation"

echo "=== stage 3: introduce package drift (snapshot-unauthorized) ==="
$COMPOSE exec -T agent sh -c 'apt-get update -q && apt-get install -y -q vim-tiny'
wait_for_deviation "pkg:unauthorized:vim-tiny" 60 \
  "log record carries pkg:unauthorized:vim-tiny deviation"

echo "=== stage 4: restore baseline ==="
AGENT_CTR="$($COMPOSE ps -q agent 2>/dev/null)"
docker cp "$E2E_DIR/baseline/sentinel.txt" "$AGENT_CTR:/etc/fiia/sentinel"
$COMPOSE exec -T agent sh -c 'apt-get purge -y -q vim-tiny'
wait_for_status 0 90 "clean verdict exported again (fiia.drift.status=0)"

echo ""
echo "E2E: all stages passed"