# Fiia — Plan

## Now

**State:** Increments 1–5 fully implemented and end-to-end tested on Colima dev VM. Drift detection pipeline verified: `make dev-drift` → audit cycle → `DRIFT_DETECTED` in hub → `make dev-restore` → `OK` → alert cleared. All production deployment artifacts (bootstrap playbooks, systemd unit, logrotate) correct and deployed. Increment 6 (resilience + remote control) is designed (D-13 through D-19 in DECISIONS.md) and partially stubbed (`PayloadTypeAck`/`PayloadTypeCommand` reserved in wire).

**Next:** Run `/ds-project-map` to refresh PROJECT.md (stale: Go version, CPUQuota/MemoryMax values, missing packages, deploy/ansible description). Then start Increment 6 implementation — begin with `internal/agent/queue` (disk-backed ring buffer, D-13).

**Open questions:**
- Tiger Style in PROJECT.md marked "active for this session" — keep as permanent project constraint, update wording, or drop?

**Watch:** `make dev-watch` not yet tested live; confirm awk label parsing works with actual `tail -f` multi-file output.

---

## Roadmap

### Increment 1: Walking skeleton
_Goal: agent sends heartbeat → hub validates HMAC → `/metrics` shows node count → Grafana scrapes it._

- [x] `internal/wire`: `HeartbeatPayload` struct with MessagePack tags, `Encode`, `Decode`, `Sign`, `Verify`
- [x] `internal/hub/store`: `Store` interface, SQLite implementation (`modernc.org/sqlite`), `goose` migration 001 (nodes + secrets tables)
- [x] `internal/hub/registry`: in-memory node state map, `Update(nodeID, timestamp)`, stub expiry check
- [x] `internal/hub/ingest`: TLS 1.3 listener, frame split (payload | 32-byte sig), node secret lookup, HMAC verify before decode, route heartbeat to registry
- [x] `internal/hub/metrics`: Prometheus `/metrics` handler, `fiia_nodes_alive` gauge
- [x] `internal/agent/transport`: TLS 1.3 client with embedded root CA, frame = MessagePack bytes + HMAC-SHA256 appended, send with no retry queue
- [x] `internal/agent/heartbeat`: 5-minute ticker, build `HeartbeatPayload` (empty metrics), call transport, `sd_notify(WATCHDOG=1)` on watchdog ticker
- [x] `internal/agent/config` + `internal/hub/config`: TOML loaders for agent and hub configs
- [x] `cmd/agent` + `cmd/hub`: wire packages, `sd_notify(READY=1)`, `SIGTERM` shutdown
- [x] `dev/ca/`: generate and commit dev root CA + hub leaf cert; hub and agent use dev CA in non-production builds
- [x] Integration smoke test: agent → hub heartbeat received, registry updated, `/metrics` returns `fiia_nodes_alive_total 1`

---

### Increment 2: USE telemetry
_Goal: heartbeats carry real host resource metrics visible in Grafana._

- [x] `internal/agent/telemetry`: `/proc/stat` CPU utilisation + PSI saturation parser
- [x] `internal/agent/telemetry`: `/proc/meminfo` memory utilisation + PSI saturation parser
- [x] `internal/agent/telemetry`: `/proc/diskstats` + `/proc/pressure/io` disk utilisation + saturation parser
- [x] `internal/agent/telemetry`: `/proc/net/dev` network utilisation + error counter parser
- [x] `sync.Pool` buffer reuse in all parsers (no per-call allocations on read buffer)
- [x] Enrich `HeartbeatPayload.Metrics` field; `NodeState` stores `wire.USEMetrics` per node
- [x] `internal/hub/metrics`: per-node USE gauges via custom `prometheus.Collector` (label `node_id`)
- [x] Unit tests: parser outputs match known `/proc` fixture files

---

### Increment 3: Audit and drift detection
_Goal: nodes running drifted configs appear in `/alerts` and Grafana._

- [x] `internal/wire`: add `DriftPayload` struct (`SchemaVersion`, `NodeID`, `Timestamp`, `Status`, `TasksChanged []string`)
- [x] `internal/hub/store`: `drift_events` table, `goose` migration 002; `AppendDrift`, `GetDriftEvents` on `Store` interface
- [x] `internal/agent/audit`: fork `ansible-playbook --check --diff` under `context.WithTimeout(10 * time.Minute)`
- [x] `internal/agent/audit`: capture stdout to `/var/log/fiia/drift.log` (append); parse exit code 0/2/timeout/OOM
- [x] `internal/agent/audit`: OOM exit writes `AUDIT_RESOURCE_EXCEEDED` status; transport retry slot (`atomic.Pointer[pendingFrame]`) holds unsent frame
- [x] Audit goroutine in `cmd/agent`: sleep with base + `rand[0,120]s` jitter, call `audit.Run()`, send `DriftPayload` via `transport.SendAuditResult`
- [x] Hub ingest: handle `DriftPayload` route → `store.AppendDrift()`
- [x] Hub ingest: unknown `SchemaVersion` → `PeekPayloadType` returns error; ingest logs and drops
- [x] `internal/hub/api`: `GET /nodes` (list with status), `GET /nodes/{id}/status`, `GET /alerts` (JSON)
- [x] `internal/hub/metrics`: `fiia_drift_events_total` counter, `fiia_nodes_paused` gauge
- [x] Integration test: `TestIntegrationDriftPayload` — sends drift frame; hub stores it; counter incremented

---

### Increment 4: Inventory reconciliation
_Goal: nodes in the CSV that never reported are flagged in Grafana and `/alerts`._

- [x] `internal/hub/store`: `alerts` table in migration 001; `SetAlert`, `GetAlerts`, `ClearAlert`, `CountNodesWithStatus` on `Store` interface
- [x] `internal/hub/inventory`: `InventoryReader` interface (`ListNodes() ([]Node, error)`)
- [x] `internal/hub/inventory`: CSV implementation (newline-delimited, `#`-comments, `hostname[,alias]` format)
- [x] `internal/hub/inventory`: reconciler goroutine — configurable ticker, runs immediately on start, flags `UNINSTRUMENTED_SERVER`
- [x] `internal/hub/registry`: expiry goroutine — per-minute tick, two missed heartbeat windows → `AGENT_PAUSED` / `AGENT_UNREACHABLE`
- [x] `internal/hub/metrics`: `fiia_nodes_uninstrumented` gauge, `fiia_nodes_unreachable` gauge
- [x] Hub config: `inventory_csv_path` + `reconcile_interval_sec` fields
- [x] Integration test: `TestIntegrationInventoryReconciler` — absent CSV node flagged `UNINSTRUMENTED_SERVER`

---

### Increment 5: Production deployment
_Goal: deployable to a real fleet subset via staged Ansible rollout._

- [x] `deploy/ansible/bootstrap.yml`: creates `fiia` system user (no login shell, no sudo), `serial: 5%`
- [x] Bootstrap playbook: HMAC secret from inventory var, writes `agent.toml.j2` to `/etc/fiia/agent.toml` (0400, `fiia`-owned)
- [x] Bootstrap playbook: installs binary + `fiia-agent.service.j2` with `CPUQuota=10%`, `MemoryMax=256M`, `IOBandwidth=512K`, `Nice=19`, `IOSchedulingClass=idle`, `WatchdogSec=120`
- [x] Bootstrap playbook: deploys `fiia_ca_cert` PEM to `/etc/fiia/root_ca.pem`
- [x] Bootstrap playbook: dry-run handshake polls `GET /nodes/{id}/status` with retries before releasing
- [x] `step-ca` setup: documented in `docs/step-ca-setup.md`
- [x] Logrotate: `deploy/ansible/files/fiia-logrotate` — daily, 7-day, compress, HUP on rotate
- [x] Systemd watchdog: `sdnotify` package sends `WATCHDOG=1` on heartbeat ticker; `WatchdogSec=120` in unit
- [x] Bootstrap playbook: deploy `ansible.cfg` to `/var/lib/fiia/ansible.cfg` (`gathering=explicit`, tmp paths); service `Environment=ANSIBLE_CONFIG=` points to it
- [x] `dev/gen_certs`: idempotent — skip if certs present; `--force` flag for intentional rotation (`make dev-certs-force`)
- [x] `internal/agent/heartbeat`: adaptive backoff on hub failure — 5 → 10 → 20 → 40s cap, resets to normal on success; watchdog ticker remains fixed
- [x] `internal/hub/ingest`: log each heartbeat (node, CPU/mem/disk %); log each drift payload (node, status, changed tasks)
- [x] `internal/hub/registry`: fleet summary log on every expiry tick — total/alive/paused/unreachable/drift counts
- [x] `internal/hub/ingest`: `DRIFT_DETECTED` audit result raises alert; `OK` audit result clears it
- [x] `internal/hub/ingest`: heartbeat arrival clears `AGENT_UNREACHABLE` and `AGENT_PAUSED` alerts
- [x] `audit.Probe()`: startup smoke check — runs `ansible-playbook --version` once; emits `AUDIT_ERROR` to hub immediately if ansible is broken in service environment
- [x] `audit.Run()`: writes `/var/lib/fiia/.ansible/audit.cfg` before each invocation (gathering=explicit, tmp paths) — no dependency on bootstrap-deployed file
- [x] `audit.Run()`: logs elapsed wall-clock time on timeout and completion for CPUQuota diagnostics
- [ ] End-to-end test on staging fleet: deploy to 5% batch, confirm no P99 latency regression, heartbeats in Grafana

---

### Increment 6: Resilience and remote control
_Goal: agent survives extended hub outages; hub can trigger on-demand audits without waiting for the timer._

- [x] `wire`: add `PayloadTypeCommand` and `PayloadTypeAck` frame types (hub→agent direction, reserved — not yet consumed)
- [ ] `internal/hub/ingest`: send `PayloadTypeAck` frame after storing each heartbeat
- [ ] `internal/agent/transport`: read one optional response frame after each heartbeat send (short timeout; ignore if none)
- [ ] `internal/agent/queue`: disk-backed ring buffer (64 entries, msgpack, `/var/lib/fiia/queue/`) — write before send, advance read pointer on ACK
- [ ] Agent startup: replay unsent queue entries before entering normal loop
- [ ] Hub API: `POST /nodes/{id}/audit_now` — enqueues a `PayloadTypeCommand{type:"audit_now"}` frame, delivered on next heartbeat ACK
- [ ] Agent: on receiving `audit_now` command, drain audit timer and run immediately
- [ ] Hub API: `POST /nodes/{id}/config` — push updated baseline playbook path or interval override
- [ ] Agent: `SIGTERM` from hub command (`graceful_restart`) — clean shutdown, systemd restarts
