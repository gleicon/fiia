# Specification: Fiia — Distributed Configuration Audit & Fleet State Agent

## Problem

Large bare-metal and hypervisor fleets (5,000+ nodes) running active multi-tenant production workloads suffer from undetected configuration drift — nodes silently diverge from a declared GitLab baseline. Existing approaches (central SSH orchestration, active remediation loops) impose unacceptable interference risk on tenant workloads. Operators lack a passive, zero-impact mechanism to continuously audit host state, detect shadow infrastructure, and verify agent liveness at fleet scale.

---

## Scope

**In scope:**
- Edge daemon (Fiia agent) running on each fleet node
- Periodic configuration drift detection via local ansible-core in check-only mode
- Liveness heartbeat reporting to a central hub
- Host telemetry collection using USE method from Linux kernel virtual filesystems
- Central hub: heartbeat registry, drift log storage, operator dashboard
- CMDB integration for uninstrumented node detection
- Staged bootstrap deployment via Ansible
- One-way TLS + per-node HMAC-SHA256 transport security

**Out of scope:**
- Automated remediation or self-healing (strictly prohibited)
- mTLS client certificate management at edge
- Windows or non-Linux operating systems
- Real-time streaming metrics (this is periodic telemetry, not APM)
- Phase II inotify reactive mode (future extension, not in scope)

---

## Users

| Role | Goal |
|------|------|
| Platform/SRE Operator | Review drift reports and liveness status across the fleet without triggering any workload disruption |
| Security Engineer | Detect unauthorized config mutations and verify all nodes are instrumented and reporting |
| Infrastructure Engineer | Deploy and bootstrap Fiia onto new nodes with guaranteed non-interference |

---

## Functional Requirements

**Edge Daemon**

FR-1: The daemon SHALL execute `ansible-playbook --check --diff` against a locally cached GitLab configuration baseline every 15–30 minutes.

FR-2: The daemon SHALL apply a randomized splay jitter of 0–120 seconds before each audit cycle to prevent synchronized fleet-wide load spikes.

FR-3: The daemon SHALL kill the ansible subprocess with SIGKILL if it has not completed within 10 minutes.

FR-4: The daemon SHALL NOT write any configuration mutation to the host filesystem or execute any remediation action.

FR-5: The daemon SHALL send a heartbeat payload to the central hub every 5 minutes, decoupled from the audit cycle timer.

FR-6: The daemon SHALL implement the systemd watchdog protocol (Type=notify) so that systemd detects and logs a fatal error if the internal processing loop deadlocks or freezes.

FR-7: The daemon SHALL collect host telemetry using Brendan Gregg's USE method by parsing `/proc/stat`, `/proc/meminfo`, `/proc/diskstats`, `/proc/net/dev`, and `/proc/pressure/*` directly — no execution of system utility binaries.

FR-8: The daemon SHALL serialize all outbound payloads using MessagePack and append a 32-byte HMAC-SHA256 signature computed over the raw payload bytes using the per-node host secret.

FR-9: The daemon SHALL validate the central hub's TLS certificate against an embedded root CA string; the daemon carries no client certificate.

FR-10: On hub unreachability, the daemon SHALL retain at most one latest audit record in static runtime memory; it SHALL NOT accumulate a growing queue.

FR-10.a: If the kernel OOM-kills the ansible subprocess, the daemon SHALL emit an `AUDIT_RESOURCE_EXCEEDED` flag to the hub and return to sleep without crashing.

**Central Hub**

FR-11: The hub SHALL maintain a heartbeat registry mapping each node ID to its last-seen timestamp.

FR-12: The hub SHALL flag a node as `AGENT_PAUSED` or `AGENT_UNREACHABLE` if two consecutive expected heartbeat windows pass without a valid payload from that node.

FR-13: The hub SHALL sync the CMDB API once per hour to retrieve the full active server manifest (IPs and hostnames).

FR-14: The hub SHALL cross-reference the CMDB manifest against the heartbeat registry and flag any node present in CMDB but absent from the registry for 60+ consecutive minutes as `UNINSTRUMENTED_SERVER`.

FR-15: The hub SHALL reject any payload where HMAC validation fails and emit a security anomaly alert; the payload SHALL be dropped without processing.

**Deployment**

FR-16: The bootstrap playbook SHALL deploy the Fiia binary in staged batches using `serial: 5%` concurrency, partitioning the fleet by CMDB workload tags.

FR-17: The bootstrap playbook SHALL generate a unique cryptographically random per-node host secret and write it to `/etc/fiia/agent.toml` with permissions `0400` before starting the daemon.

FR-18: The bootstrap playbook SHALL execute an initial dry-run handshake to confirm the node registers in the central hub registry before releasing the deployment lock.

---

## Non-Functional Requirements

NFR-1: **CPU** — The edge daemon process tree SHALL consume ≤1.0% of one CPU core (enforced via `CPUQuota=1%` in the systemd unit).

NFR-2: **Memory** — The edge daemon process SHALL consume ≤25MB RSS at all times (enforced via `MemoryMax=25M` in the systemd unit; Go runtime configured with `GOMEMLIMIT=18MiB`, `GOGC=10`).

NFR-3: **Disk I/O** — The daemon SHALL generate ≤500 KB/s disk read+write throughput combined (enforced via `IOReadBandwidthMax` and `IOWriteBandwidthMax` in the systemd unit).

NFR-4: **Scheduling** — The daemon SHALL run at Nice=19 and `IOSchedulingClass=idle` so all customer workloads preempt it unconditionally.

NFR-5: **Heartbeat latency** — Heartbeat packets SHALL be dispatched within 30 seconds of the scheduled 5-minute interval under normal conditions.

NFR-6: **Wire frame size** — A standard heartbeat telemetry payload (NodeID + Timestamp + Metrics + Status) SHALL be ≤96 bytes packed.

NFR-7: **Fleet aggregate throughput (steady state)** — At 5,000 nodes × 1 heartbeat per 300 seconds, aggregate hub ingress SHALL average ≤1.6 KB/sec.

NFR-8: **Fleet aggregate throughput (peak drift event)** — With splay jitter, peak aggregate hub ingress during a mass drift event across all 5,000 nodes SHALL not exceed 2.2 MB/sec.

NFR-9: **Hub availability** — The central hub SHALL be deployed as a clustered HA configuration targeting ≥99.9% monthly uptime.

NFR-10: **Deployment non-interference** — The staged rollout SHALL not cause measurable latency degradation (>5% P99 increase) on tenant workloads on any node during installation.

---

## Interfaces

**Edge Daemon**
- Binary: statically compiled Go executable, managed as a systemd service unit
- Config file: `/etc/fiia/agent.toml` (root:root, 0400) — contains hub endpoint, embedded root CA, per-node HMAC secret, GitLab baseline cache path
- Outbound transport: MessagePack struct over TCP/TLS 1.3 to hub endpoint
- Local subprocess: `/usr/bin/ansible-playbook --check --diff <playbook>` forked as sandboxed child process
- Kernel VFS reads: `/proc/stat`, `/proc/meminfo`, `/proc/diskstats`, `/proc/net/dev`, `/proc/pressure/{cpu,memory,io}`, `/proc/vmstat`
- systemd integration: `sd_notify(WATCHDOG=1)` keepalive, `sd_notify(READY=1)` on startup

**Central Hub**
- Inbound: TLS 1.3 listener accepting MessagePack frames with trailing HMAC-SHA256
- CMDB integration: REST API pull (hourly), returns JSON manifest of active IPs/hostnames
- Operator dashboard: web UI displaying heartbeat registry state, drift log, `UNINSTRUMENTED_SERVER` and `AGENT_PAUSED` alerts
- Storage: key-value store for per-node HMAC secrets; time-series or relational store for heartbeat registry and drift logs

**External Systems**
- GitLab: configuration baseline repository — pulled/cached to each edge node
- CMDB API: asset source of truth, queried hourly by hub
- Ansible Core: local binary on edge node, invoked read-only in check mode

---

## Constraints

- **Language**: Go (statically compiled, CGO disabled)
- **Platform**: Linux only; requires systemd, cgroups v1/v2, `/proc`, `/sys`
- **Serialization**: MessagePack (`vmihailenco/msgpack/v5`, map encoding); `sync.Pool` buffers for the `/proc` read path
- **Runtime flags**: `GOMEMLIMIT=18MiB`, `GOGC=10`; `sync.Pool` required for all reusable buffers
- **No mTLS**: edge nodes carry no client certificates; server-only TLS + HMAC is the auth model
- **No auto-remediation**: the daemon has no write path to host configuration at any layer
- **No dynamic memory accumulation**: all hot-path allocations must use pre-allocated pools; the daemon must not grow RSS over time

---

## Acceptance Criteria

AC-1: Given a running daemon under steady workload, when measured over 10 minutes, then RSS SHALL remain ≤25MB and CPU usage SHALL remain ≤1.0% of one core.
*Covers FR-4 (no mutation), NFR-1, NFR-2*

AC-2: Given the daemon is running, when 5 minutes elapse, then a heartbeat packet SHALL arrive at the hub within 30 seconds of the scheduled time.
*Covers FR-5, NFR-5*

AC-3: Given a heartbeat packet, when the hub receives it, then the serialized MessagePack payload with trailing HMAC-SHA256 SHALL be ≤96 bytes.
*Covers FR-8, NFR-6*

AC-4: Given an ansible subprocess is forked, when 10 minutes pass without subprocess exit, then the daemon SHALL deliver SIGKILL to the subprocess and continue running without crashing.
*Covers FR-3*

AC-5: Given a node stops sending heartbeats, when two consecutive expected heartbeat windows pass (≥10 minutes), then the hub SHALL display the node as `AGENT_PAUSED` or `AGENT_UNREACHABLE` on the operator dashboard.
*Covers FR-12*

AC-6: Given a CMDB sync completes, when a node IP is present in the CMDB manifest but has no heartbeat record in the registry for ≥60 minutes, then the hub SHALL flag that node as `UNINSTRUMENTED_SERVER`.
*Covers FR-13, FR-14*

AC-7: Given a payload arrives at the hub with a tampered HMAC, when the hub validates the signature, then the payload SHALL be dropped without processing and a security anomaly alert SHALL be recorded.
*Covers FR-15*

AC-8: Given the hub endpoint is unreachable, when the daemon completes multiple audit cycles, then daemon RSS SHALL not grow beyond 25MB and only the latest single audit record SHALL be retained in memory.
*Covers FR-10*

AC-9: Given the kernel OOM-kills the ansible subprocess, when the daemon detects the child exit, then it SHALL emit `AUDIT_RESOURCE_EXCEEDED` to the hub on next successful connection and return to the idle sleep state.
*Covers FR-10.a*

AC-10: Given the daemon starts an audit cycle, when measured across 100 cycles across a 5,000-node fleet, then the audit start times SHALL be uniformly distributed within a 120-second window relative to the base timer.
*Covers FR-2*

AC-11: Given a fleet-wide configuration drift event, when all 5,000 nodes report drift payloads within their jitter windows, then aggregate hub ingress SHALL not exceed 2.2 MB/sec.
*Covers NFR-8*

AC-12: Given a fresh node bootstrap, when the Ansible deployment playbook runs, then the playbook SHALL complete with `serial: 5%` batching and the node SHALL appear in the hub heartbeat registry before the playbook releases the deployment lock.
*Covers FR-16, FR-17, FR-18*

**FR → AC Coverage Map**

| FR | AC |
|----|-----|
| FR-1 | AC-4 (subprocess lifecycle), AC-10 (cycle timing) |
| FR-2 | AC-10 |
| FR-3 | AC-4 |
| FR-4 | AC-1 (no write path validated indirectly by no drift in --check mode) |
| FR-5 | AC-2 |
| FR-6 | manual: systemd watchdog verified by killing the main loop goroutine in a test build |
| FR-7 | AC-1 (daemon stays in bounds parsing /proc), unit test on parser outputs |
| FR-8 | AC-3, AC-7 |
| FR-9 | integration test: hub with wrong cert causes daemon to reject connection |
| FR-10 | AC-8 |
| FR-10.a | AC-9 |
| FR-11 | AC-5 |
| FR-12 | AC-5 |
| FR-13 | AC-6 |
| FR-14 | AC-6 |
| FR-15 | AC-7 |
| FR-16 | AC-12 |
| FR-17 | AC-12 |
| FR-18 | AC-12 |

---

## Open Questions

OQ-1: What is the authoritative GitLab sync mechanism for the edge node — does the daemon pull from GitLab directly, or does the hub distribute cached baseline artifacts to nodes? (PRD says "pulled/cached" but does not specify the puller or cadence.)

OQ-2: ~~Resolved~~ Wire format uses MessagePack with a `schema_version` uint8 field; the `internal/wire` package owns the schema. Both binaries share `internal/wire` — a schema change requires updating both binaries atomically.

OQ-3: What CMDB API authentication mechanism is used (API key, OAuth, mTLS)? Hub CMDB integration cannot be designed without this.

OQ-4: Is the operator dashboard a new build or an integration with an existing internal tool (e.g., Grafana, internal NOC platform)?

OQ-5: What is the hub's storage backend for the heartbeat registry and drift logs — existing infrastructure (PostgreSQL, ClickHouse, Redis) or greenfield?

OQ-6: What is the maximum tolerated hub-side processing latency for HMAC validation under peak drift load (2.2 MB/sec, ~43,000 packets/sec)?

OQ-7: Are there compliance or data sovereignty constraints on where drift log data (which may contain config file contents) can be stored or transmitted?
