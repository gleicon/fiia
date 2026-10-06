#!/usr/bin/env bash
# Live Fiia E2E: daemon -> OTel collector -> Prometheus (data + alert rules) ->
# Grafana (dashboard). The stack STAYS UP so you can watch drift happen live.
#
#   make e2e-live          run the live scenario and leave the stack running
#   make e2e-live-down     tear the live stack down
#
# Watch while it runs:
#   Dashboard : http://localhost:3000  (user admin / password admin)
#   Prometheus: http://localhost:9090  (/alerts shows FiiaDriftDetected firing)
#
# The script drives the agent through clean -> file drift -> restored, and
# prints each verdict as it flips on the dashboard.
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/.." && pwd)"
BASELINE="$REPO_ROOT/e2e/baseline/sentinel.txt"
PROJECT="fiia-observe"

export DOCKER_API_VERSION="${DOCKER_API_VERSION:-1.44}"
COMPOSE="docker compose -p $PROJECT -f $E2E_DIR/docker-compose.yml -f $E2E_DIR/docker-compose.observe.yml"

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; exit 1; }

if [ "${1:-}" = "down" ]; then
  $COMPOSE down -v --remove-orphans
  echo "live stack torn down"
  exit 0
fi

# value of a single-instant Prometheus query; empty when no data yet
prom_value() {
  curl -sf --max-time 3 "http://localhost:9090/api/v1/query?query=$1" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); r=d.get("data",{}).get("result",[]); print(r[0]["value"][1] if r else "")' 2>/dev/null || true
}

alert_state() {
  curl -sf --max-time 3 "http://localhost:9090/api/v1/alerts" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(",".join(sorted(a["labels"].get("alertname","") for a in d["data"]["alerts"] if a["state"]=="firing")))' 2>/dev/null || true
}

wait_for() {
  local desc="$1" expr="$2" want="$3" timeout="$4" got="" i
  for i in $(seq 1 $((timeout / 2))); do
    got="$(prom_value "$expr")"
    [ "$got" = "$want" ] && { pass "$desc"; return 0; }
    sleep 2
  done
  fail "$desc (expected $expr=$want, last=$got)"
}

echo "=== (re)start live stack ($PROJECT) ==="
$COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
$COMPOSE up -d --build --quiet-pull

echo "=== wait for Prometheus to see the agent ==="
wait_for "fiia.alive present in Prometheus" "fiia_alive" "1" 120

echo "=== stage 1: baseline reads clean ==="
wait_for "clean verdict (fiia_drift_status=0)" "fiia_drift_status" "0" 120

echo ""
echo "Watch the dashboard now:  http://localhost:3000  (admin/admin)"
echo "Prometheus alerts:        http://localhost:9090/alerts"
echo ""

echo "=== stage 2: introduce drift (watch the dashboard flip) ==="
$COMPOSE exec -T agent sh -c 'echo "# tamper" >> /etc/fiia/sentinel'
wait_for "drift verdict (fiia_drift_status=1)" "fiia_drift_status" "1" 120
i=0
while [ $i -lt 30 ]; do
  a="$(alert_state)"
  echo "  FiiaDriftDetected alert state: $([ -n "$a" ] && echo FIRING || echo pending/resolved)"
  case "$a" in *FiiaDriftDetected*) break ;; esac
  sleep 2; i=$((i + 1))
done

echo "=== stage 3: restore baseline (watch it go clean again) ==="
AGENT_CTR="$($COMPOSE ps -q agent)"
docker cp "$BASELINE" "$AGENT_CTR:/etc/fiia/sentinel"
wait_for "clean verdict again (fiia_drift_status=0)" "fiia_drift_status" "0" 120
i=0
while [ $i -lt 30 ]; do
  a="$(alert_state)"
  case "$a" in *FiiaDriftDetected*) sleep 2; i=$((i + 1)) ;; *) break ;; esac
done
echo "  FiiaDriftDetected alert: resolved (no longer firing)"

echo ""
echo "E2E live: all stages passed — stack is still running."
echo ""
echo "  Dashboard : http://localhost:3000  (admin/admin)  dashboard 'Fiia — drift detection'"
echo "  Prometheus: http://localhost:9090  (/alerts, /query)"
echo "  Collector : http://localhost:9464/metrics"
echo ""
echo "Tear down when done:  make e2e-live-down"