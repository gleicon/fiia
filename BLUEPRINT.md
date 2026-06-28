# Blueprint: Fiia

> Target architecture. Reports only — changes nothing. Hand to `/ds-roadmap` to build.

---

## Shape

**Two static Go binaries in a monorepo.** `fiia-agent` runs on every fleet node; `fiia-hub` runs on one or more central nodes. They share exactly one internal package: the wire format. All other code is private to each binary. No framework, no service mesh, no queue. A single `Store` interface and a single `InventoryReader` interface are the two seams for future upgrades.

**Tier: walking skeleton → full MVP.** Skeleton proves the pipe (heartbeat → hub → Prometheus). MVP adds audit, drift events, inventory reconciliation, and alerts.

**Scale this is designed for:** 5,000 nodes × 1 heartbeat / 5 min = 16.6 req/sec steady state. 2.2 MB/sec peak (mass drift event with jitter). SQLite is adequate; Postgres is the known upgrade path when hub goes HA.

---

## Repository Layout

```
fiia/
├── cmd/
│   ├── agent/           # agent binary entrypoint
│   └── hub/             # hub binary entrypoint
├── internal/
│   ├── wire/            # SHARED: payload structs, MessagePack encode/decode, HMAC sign/verify
│   ├── agent/
│   │   ├── config/      # TOML config loading and validation
│   │   ├── telemetry/   # /proc + /sys parsers (USE method)
│   │   ├── audit/       # ansible subprocess: fork, timeout, SIGKILL, diff capture
│   │   ├── heartbeat/   # heartbeat goroutine: collect metrics, build payload, send
│   │   └── transport/   # TLS client: embed root CA, dial hub, send signed frames
│   └── hub/
│       ├── config/      # TOML config loading and validation
│       ├── ingest/      # TLS listener: accept, HMAC validate, route to registry or store
│       ├── registry/    # heartbeat registry: node state, last-seen, alert flags
│       ├── inventory/   # InventoryReader interface, CSV impl, reconciler goroutine
│       ├── store/       # Store interface, SQLite impl, goose migrations
│       ├── metrics/     # Prometheus /metrics endpoint (fleet aggregates)
│       └── api/         # REST API: /nodes, /nodes/{id}/status, /alerts
├── dev/
│   ├── ca/              # pre-generated dev root CA + hub cert (committed to repo)
│   └── inventory.csv    # sample dev node inventory
├── deploy/
│   └── ansible/         # bootstrap playbook, systemd unit templates, step-ca setup docs
└── go.mod               # single module: fiia
```

---

## Modules / Boundaries

### `internal/wire` — Wire contract
**Owns:** `HeartbeatPayload` and `DriftPayload` structs (MessagePack-tagged), `Sign(secret, payload) []byte`, `Verify(secret, payload, sig) bool`, `Encode`, `Decode`.  
**Depends on:** nothing internal.  
**Why it exists:** FR-8, D-3. Single source of truth for the payload schema. Any schema change touches one package; mismatches are visible immediately.

---

### `internal/agent/config` — Agent configuration
**Owns:** TOML config struct for agent: hub endpoint, `ca_cert_path`, `hmac_secret` (loaded from `/etc/fiia/agent.toml`), playbook path, log paths.  
**Depends on:** nothing internal.

---

### `internal/agent/telemetry` — USE metric collection
**Owns:** Parsers for `/proc/stat`, `/proc/meminfo`, `/proc/diskstats`, `/proc/net/dev`, `/proc/pressure/{cpu,memory,io}`, `/proc/vmstat`. Returns a `USESnapshot` value type (no pointers, no allocation on hot path). Uses `sync.Pool` for read buffers.  
**Depends on:** nothing internal.  
**Why it exists:** FR-7. Direct `/proc` reads, no subprocess execution.

---

### `internal/agent/audit` — Ansible subprocess manager
**Owns:** Fork `ansible-playbook --check --diff` under `context.WithTimeout(10 * time.Minute)`. Capture stdout to `/var/log/fiia/drift.log` (append, not truncate). Parse exit code: `0` = clean, `2` = drift detected, killed = timeout. Detect OOM exit. Return `AuditResult{Status, TasksChanged []string}`.  
**Depends on:** `internal/agent/config`.  
**Why it exists:** FR-1, FR-2, FR-3, FR-10.a, D-6. Subprocess isolation via cgroup inheritance + timeout + `fiia` user (D-10).

---

### `internal/agent/transport` — Hub transport client
**Owns:** TLS 1.3 dial with embedded root CA (`tls.Config{RootCAs: ...}`). Frame = MessagePack bytes + 32-byte HMAC-SHA256 appended. Single send: no retry loop, no queue. On failure: updates the single `pendingRecord` atomic slot (one record max — FR-10). `InsecureSkipVerify` is not a field in this struct.  
**Depends on:** `internal/wire`, `internal/agent/config`.

---

### `internal/agent/heartbeat` — Heartbeat goroutine
**Owns:** Ticker at 5-minute interval (FR-5). On each tick: call `telemetry.Collect()`, build `HeartbeatPayload`, call `transport.Send()`. Sends `sd_notify("WATCHDOG=1")` on the watchdog ticker (FR-6). Decoupled from audit goroutine — no shared channel between them.  
**Depends on:** `internal/agent/telemetry`, `internal/agent/transport`, `internal/wire`.

---

### `cmd/agent` — Agent entrypoint
**Owns:** `main()`: load config, open log file, `sd_notify("READY=1")`, launch heartbeat goroutine, launch audit goroutine, block on `SIGTERM`. No logic of its own — wires the packages.  
**Depends on:** all `internal/agent/*`.

---

### `internal/hub/ingest` — Payload ingest (security boundary)
**Owns:** TLS 1.3 listener. Per-connection: read frame bytes, split at `len(frame)-32` → payload | signature. Extract `node_id` from raw bytes (first field, always present). Lookup `hmac_secret` from `Store`. Call `wire.Verify()`. **Drop and alert if invalid (FR-15) — before any MessagePack decode.** Decode `SchemaVersion`; drop with alert if unknown. Route: heartbeat → `registry.Update()`, drift → `store.AppendDrift()`.  
**Depends on:** `internal/wire`, `internal/hub/registry`, `internal/hub/store`.  
**Why this order:** HMAC validation before decode prevents malformed-payload panics. Security boundary is explicit.

---

### `internal/hub/registry` — Heartbeat registry
**Owns:** In-memory map of `node_id → NodeState{LastSeen, Status, AlertFlags}` backed by periodic flush to `Store`. Expiry check goroutine runs every minute: two missed heartbeat windows → set `AGENT_PAUSED`. Provides `Update(nodeID, timestamp)`, `GetAll() []NodeState`, `GetAlerts() []Alert`.  
**Depends on:** `internal/hub/store`.  
**Why in-memory:** Heartbeat reads (every 5 min × 5000 nodes = 16.6/sec) need sub-millisecond response. SQLite write of last-seen timestamp is the durable record; in-memory map is the hot path.

---

### `internal/hub/inventory` — Node inventory and reconciler
**Owns:** `InventoryReader` interface: `ListNodes() ([]Node, error)`. CSV implementation: reads a file, returns `[]Node{IP, Hostname}`. Reconciler goroutine: runs every 60 minutes, calls `ListNodes()`, cross-references against `registry.GetAll()`, flags nodes absent for 60+ minutes as `UNINSTRUMENTED_SERVER` in the store (FR-13, FR-14, D-12).  
**Depends on:** `internal/hub/store`, `internal/hub/registry`.  
**Seam:** `InventoryReader` interface. NetBox implementation is a new struct, zero other changes.

---

### `internal/hub/store` — Storage
**Owns:** `Store` interface: `UpdateHeartbeat`, `AppendDrift`, `GetNodeSecret`, `SetAlert`, `GetAlerts`, `GetNodes`. SQLite implementation using `modernc.org/sqlite` (pure Go). Schema migrations via `goose` (embedded). No raw SQL outside this package.  
**Depends on:** nothing internal.  
**Seam:** `Store` interface. Postgres implementation = new struct + connection string. Migration files are compatible with both drivers.

---

### `internal/hub/metrics` — Prometheus exporter
**Owns:** `/metrics` HTTP handler. Gauges: `fiia_nodes_alive`, `fiia_nodes_paused`, `fiia_nodes_unreachable`, `fiia_nodes_uninstrumented`, `fiia_drift_events_total`. Histograms: `fiia_last_seen_age_seconds`. Reads from `registry` and `store`.  
**Depends on:** `internal/hub/registry`, `internal/hub/store`.  
**Why it exists:** D-8. Grafana Prometheus datasource integration. No custom UI.

---

### `internal/hub/api` — REST API
**Owns:** `GET /nodes` (list all with status), `GET /nodes/{id}/status` (single node detail), `GET /alerts` (current active alerts). JSON responses. No auth on MVP (internal network only — document this).  
**Depends on:** `internal/hub/registry`, `internal/hub/store`.

---

### `cmd/hub` — Hub entrypoint
**Owns:** `main()`: load config, init store + run migrations, start ingest listener, start registry expiry goroutine, start inventory reconciler goroutine, start metrics + API HTTP servers, block on `SIGTERM`. No logic of its own.  
**Depends on:** all `internal/hub/*`.

---

## Dependency Rules

```
cmd/agent  →  internal/agent/*  →  internal/wire
cmd/hub    →  internal/hub/*    →  internal/wire
                                →  internal/hub/store (leaf)

internal/hub/ingest    →  registry, store, wire
internal/hub/registry  →  store
internal/hub/inventory →  registry, store
internal/hub/metrics   →  registry, store
internal/hub/api       →  registry, store
```

**Rules:**
- `cmd/agent` imports NOTHING from `internal/hub/*`. Ever.
- `cmd/hub` imports NOTHING from `internal/agent/*`. Ever.
- `internal/wire` imports NOTHING internal. It is the only shared leaf.
- `internal/hub/store` imports NOTHING else internal. It is a leaf.
- No cycles. Enforced by Go's import graph (circular imports fail compilation).

---

## Seams

| Seam | Location | What flexes later |
|------|----------|------------------|
| Wire format | `internal/wire` | Swap MessagePack → Protobuf: only this package changes |
| Storage backend | `internal/hub/store.Store` interface | CSV → Postgres: new struct, same interface |
| Inventory source | `internal/hub/inventory.InventoryReader` | CSV → NetBox: new struct, same interface |
| TLS CA | `ca_cert_path` in agent config | Dev CA → prod CA: config field only |
| Ansible command | injectable in `audit.Runner` | Swap for test stub in unit tests |

---

## Critical Path: Hub Ingest

```
TCP accept
  → TLS handshake (server cert validated by agent against embedded root CA)
  → read all bytes (with read deadline)
  → split: payload_bytes = frame[:len-32], sig = frame[len-32:]
  → peek node_id from payload_bytes (first msgpack field, no full decode)
  → store.GetNodeSecret(node_id)
  → wire.Verify(secret, payload_bytes, sig)
      → DROP + alert if invalid          ← security boundary
  → wire.Decode(payload_bytes) → struct
  → read SchemaVersion
      → DROP + alert if unknown version
  → route on payload type:
      HEARTBEAT → registry.Update(node_id, now)
      DRIFT     → store.AppendDrift(node_id, tasks_changed, now)
```

This path MUST NOT decode MessagePack before HMAC verification. Order is fixed.

---

## Agent Goroutine Model

```
main()
  ├── watchdog ticker (every 25s) → sd_notify("WATCHDOG=1")
  ├── heartbeat goroutine
  │     every 5 min:
  │       telemetry.Collect() → USESnapshot
  │       build HeartbeatPayload
  │       transport.Send() → drop on failure, update pendingRecord slot
  │
  └── audit goroutine
        sleep (15-30 min base + rand[0,120]s jitter)
        audit.Run() → context.WithTimeout(10min)
          fork ansible-playbook --check --diff
          capture stdout → /var/log/fiia/drift.log (logrotate managed)
          on exit:
            code 0  → send CLEAN heartbeat
            code 2  → build DriftPayload{tasks_changed} → send
            timeout → log AUDIT_TIMEOUT, continue
            OOM     → write AUDIT_RESOURCE_EXCEEDED to pendingRecord slot
```

**Shared state between goroutines:** exactly one `atomic.Pointer[PendingRecord]`. Not a channel, not a slice — a single overwrite slot. Prevents memory accumulation on hub outage (FR-10).

---

## Build Order

### Increment 1 — Walking skeleton
Proves the pipe works end-to-end.

1. `internal/wire`: `HeartbeatPayload` struct, MessagePack encode/decode, HMAC sign/verify
2. `internal/hub/store`: SQLite schema (nodes, secrets), `goose` migration 001
3. `internal/hub/registry`: in-memory map, `Update()`, stub expiry
4. `internal/hub/ingest`: TLS listener, HMAC validate, route to registry
5. `internal/hub/metrics`: `fiia_nodes_alive` gauge only
6. `internal/agent/transport`: TLS dial, send signed frame
7. `internal/agent/heartbeat`: 5-min ticker, hardcoded empty metrics, send
8. `cmd/agent` + `cmd/hub`: wire it together
9. `dev/ca/`: committed dev CA + hub cert

**Deliverable:** agent sends heartbeat → hub validates → `/metrics` shows node count → Grafana scrapes it. No audit, no drift, no inventory. Proves the entire security and transport path.

---

### Increment 2 — USE telemetry
Adds real metrics to heartbeats.

1. `internal/agent/telemetry`: `/proc` parsers for CPU, memory, disk, net, PSI
2. Enrich `HeartbeatPayload.Metrics` field
3. Add telemetry gauges to `internal/hub/metrics`

**Deliverable:** Grafana shows real host resource metrics per node.

---

### Increment 3 — Audit and drift detection
Adds the core config audit function.

1. `internal/wire`: add `DriftPayload` struct
2. `internal/hub/store`: add `drift_events` table, `goose` migration 002
3. `internal/agent/audit`: subprocess manager, timeout, diff capture to log
4. Connect audit goroutine in `cmd/agent`
5. Hub ingest: handle `DriftPayload` route
6. `internal/hub/api`: `GET /nodes`, `GET /nodes/{id}/status`, `GET /alerts`
7. Add `fiia_drift_events_total` counter to metrics

**Deliverable:** Nodes running drifted configs show up in `/alerts` and Grafana.

---

### Increment 4 — Inventory reconciliation
Adds uninstrumented node detection.

1. `internal/hub/inventory`: `InventoryReader` interface + CSV implementation
2. Reconciler goroutine: 60-min ticker, cross-reference, flag `UNINSTRUMENTED_SERVER`
3. `internal/hub/store`: add `alerts` table, `goose` migration 003
4. Add `fiia_nodes_uninstrumented` gauge to metrics

**Deliverable:** Nodes in the CSV that never reported are flagged in Grafana and `/alerts`.

---

### Increment 5 — Production deployment
Operational hardening.

1. `deploy/ansible/`: bootstrap playbook, `fiia` system user, systemd unit with cgroup limits
2. `step-ca` setup documentation + hub cert issuance
3. Logrotate config for `/var/log/fiia/drift.log`
4. Registry expiry goroutine: two missed windows → `AGENT_PAUSED` / `AGENT_UNREACHABLE`

**Deliverable:** Deployable to a subset of the real fleet via `serial: 5%`.

---

## Deferred (with trigger condition)

| What | Trigger to add it |
|------|-----------------|
| Postgres `Store` implementation | Hub goes multi-node for HA (NFR-9) |
| NetBox `InventoryReader` implementation | NetBox deployed and API accessible (D-12) |
| SHA-256 checksum fallback for shell tasks | Audit baseline must include shell tasks (D-6) |
| User namespace subprocess sandboxing | Security hardening pass post-MVP (D-10) |
| Hub REST API auth | Hub exposed outside trusted internal network |
| SPIFFE/SPIRE mTLS | PKI infrastructure exists and forward secrecy required (D-2) |
| Phase II inotify reactive audit | Periodic 15-30 min cycle latency insufficient (SPEC out of scope) |
| Hub custom operator UI | Operators report Grafana is insufficient for their workflow (D-8) |

---

## Alternative Considered

**Two separate repositories** (one for agent, one for hub, shared wire types as a published Go module).

Rejected: at this team size and stage, a published wire module creates a coordination overhead without benefit. Any schema change requires a version bump, a module release, and two PR updates across two repos. The monorepo makes the forced coordination visible — a single `internal/wire` change and both binaries recompile together, catching mismatches at build time.
