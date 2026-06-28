# Fiia — Design Decisions

---

## D-2: Authentication model

**Question:** HMAC per-node shared secret vs mTLS (SPIFFE/SPIRE/Vault)?

**Decision:** **HMAC-SHA256 per-node shared secret**, seeded by the Ansible bootstrap playbook — same pattern as SSH key distribution (delivered from outside in). No PKI dependency. Hub validates HMAC on every payload; mismatches are dropped and trigger a security anomaly alert.

**Rationale:** No existing PKI/Vault/SPIRE in the environment. SPIFFE/SPIRE noted as future direction but out of scope now. Agent is read-only; blast radius of a compromised node secret is limited to that node's telemetry. Ansible delivery of the secret is already part of the bootstrap flow, making this a zero-additional-dependency solution. Hub-side spoofing detection via HMAC validation is the hard requirement — met by this model.

**Implication:** PRD Section 7 HMAC design is correct. Secret stored at `/etc/fiia/agent.toml` (0400, root only). Hub maintains a per-node secret KV store for validation.

---

## D-3: Wire serialization format

**Question:** Cap'n Proto (PRD) vs Protocol Buffers vs MessagePack?

**Decision:** **MessagePack** using `vmihailenco/msgpack` (Go), map encoding (not array), with a `SchemaVersion uint8` field on every payload. Cap'n Proto is dropped.

**Rationale:** Cap'n Proto's zero-copy advantage only materializes on large read-heavy payloads — wasted at 96 bytes every 5 minutes. Its Go library is less mature. Protobuf requires `protoc` code generation. MessagePack with map encoding provides forward compatibility (new fields ignored by old decoders) and no codegen — just struct tags. Team controls both agent and hub, so compile-time contract enforcement is lower priority than simplicity. `SchemaVersion` field ensures clean degradation if payloads diverge.

**Implication:** Replace all Cap'n Proto references in SPEC with MessagePack. HMAC-SHA256 is computed over the raw MessagePack byte array before transmission. Hub reads `SchemaVersion` before decoding; unknown versions are dropped with a logged alert.

---

## D-4: Go GC tuning

**Question:** Should the spec lock in GOGC=10 (PRD) and GOMEMLIMIT=18MiB?

**Decision:** **Drop GOGC=10 entirely. Start MVP with `GOGC=off`.** Do not specify memory ceiling numbers in the spec — tune with real profiling data post-MVP. `sync.Pool` for buffer reuse stays.

**Rationale:** GOGC=10 is actively harmful — causes GC thrashing on small heaps (golang/go#22743, #37927). GOGC=off is the correct default for a memory-constrained daemon: GC only fires via GOMEMLIMIT pressure. Specific MB thresholds (18 MiB, 22 MiB) are implementation details that belong in systemd unit and runtime config after profiling real workloads, not in the spec. Premature optimization of GC knobs before MVP violates first principles.

**Implication:** Remove GOGC=10 and GOMEMLIMIT=18MiB from SPEC NFRs. Spec says "Go runtime configured to avoid GC thrashing; specific parameters determined by profiling." Systemd MemoryMax enforcement remains as the hard kernel ceiling.

---

## D-5: GitLab baseline sync to edge node

**Question:** Who pulls the configuration baseline to each edge node — agent, hub, or Ansible?

**Decision:** **Ansible delivers the baseline at deploy time; agent uses what is already locally cached.** The agent has no GitLab credentials and makes no outbound connections except to the hub for reporting. A separate systemd timer (not the audit daemon) handles periodic `git pull` of the cached repo if baseline updates are needed. Agent enforces what Ansible last applied — influenced from within, not from outside.

**Rationale:** Keeps the agent's trust surface minimal: it reads local state, reports outward, and cannot be instructed by external systems. Aligns with the "zero autonomy" mandate. Ansible is already the mechanism that owns baseline delivery; keeping it that way avoids a new distribution dependency. Changeset pull from GitLab or hub is a future extension, not MVP scope.

**Implication:** Agent config references a local filesystem path for the playbook directory. No GitLab token in the agent. Baseline updates are an operational concern handled outside the agent binary.

---

## D-6: Ansible baseline audit scope

**Question:** Must the baseline be declarative-only, or can it contain shell/command tasks?

**Decision:** **Spec-level constraint: baseline playbooks used for audit MUST be declarative-only** (no `shell`, `command`, `raw` tasks). If operational playbooks contain shell tasks, the audit baseline is a separate, declarative-only subset. Fallback for unavoidable shell-task paths: record SHA-256 checksums of affected filesystem paths at deploy time; agent compares checksums on each audit cycle as a supplementary check outside of Ansible check mode.

**Rationale:** Ansible check mode is unreliable for shell/command tasks — produces false positives and false negatives. Declarative modules (file, template, sysctl, service, package) implement check mode correctly and produce reliable signal. The checksum fallback is a corner case escape hatch, not the primary mechanism. Working at spec level is the right place to enforce this — not in runtime code.

**Implication:** FR-1 in SPEC gets a constraint: "baseline playbooks used for audit MUST consist of declarative Ansible modules only." Checksum-based supplementary audit for shell-task paths documented as a future extension (FR-1.a).

---

## D-7: Hub storage backend

**Question:** SQLite vs PostgreSQL vs Redis+Postgres for heartbeat registry and drift logs?

**Decision:** **SQLite (`modernc.org/sqlite`, pure Go) for MVP. PostgreSQL for production/HA.** Storage is wrapped behind a repository interface (`database/sql`) from day one — Postgres is a driver swap + migration, not a rewrite. Schema migrations managed with `goose` from the start.

**Rationale:** External DB dependency adds friction during development. 8 writes/sec is well within SQLite's range for a single-hub MVP. Pure Go SQLite avoids CGO and keeps the hub build clean. Redis adds operational complexity for a problem that doesn't exist at this write rate.

**Hard constraint:** SQLite cannot be clustered. Hub HA requires Postgres. This migration is planned and known — not a surprise. When hub goes to multi-node, switch the driver.

**Implication:** Hub repository layer must be interface-driven from the start. No raw SQL scattered across handlers — all storage access through a `Store` interface with a SQLite implementation initially.

---

## D-8: Operator dashboard

**Question:** Build a custom UI, expose a REST API, or integrate with existing tooling?

**Decision:** **API-first. No custom UI for MVP.** Hub exposes: (a) a Prometheus `/metrics` endpoint for fleet-level aggregates (nodes alive, drift count, alert counts, last-seen age histograms) — consumed by the team's existing Grafana via Prometheus datasource, no plugin required; (b) a REST API (`/nodes`, `/nodes/{id}/status`, `/alerts`) for per-node detail queries via curl or scripting.

**Rationale:** Team already runs Grafana. Prometheus scrape is the zero-friction integration path — standard datasource, no custom plugin. Custom UI built before operators have used the system is premature. Ship the data layer first; UI decisions can follow real operator feedback.

**Implication:** Hub must expose `/metrics` (Prometheus text format) alongside its ingest endpoint. REST API serves node/alert detail. No HTML rendering in the hub binary for MVP.

---

## D-9: Drift diff storage

**Question:** Does the hub store raw Ansible diff output, or status flags only?

**Decision:** **Hub stores status flags + task metadata only** (node ID, timestamp, playbook, task name, changed=true/false). **Raw diffs are written to a local file on the edge node** for admin access when needed. Log rotation is mandatory on the edge node to prevent disk fill.

**Rationale:** Raw diffs may contain sensitive values (SSH keys, connection strings, TLS certs). Storing them centrally creates a sensitive data concentration requiring encryption at rest, access controls, and retention policy. Keeping diffs local avoids that entirely — admin SSHs to the drifted node if they need the actual diff content. Hub remains a status/alert system, not a config data store.

**Implication:** Agent writes diff output to a local file (e.g., `/var/log/fiia/drift.log`) managed by logrotate or systemd journal with `MaxRetentionSec` / `SystemMaxUse` bounds. Hub payload for a drift event: `{schema_version, node_id, timestamp, status: "DRIFT", tasks_changed: ["task_name_1", ...]}` — no diff content.

---

## D-10: Ansible subprocess isolation

**Question:** User namespaces / sandbox, or rely on OS-level controls already in place?

**Decision:** **No user namespaces for MVP.** Isolation via three layers already in the design: (a) cgroup inheritance — the entire process tree including forks lives under the parent's systemd cgroup, so `MemoryMax`/`CPUQuota` applies automatically; (b) `context.WithTimeout` + SIGKILL at 10 minutes covers hangs; (c) a dedicated `fiia` system user (no sudo, no write access to system config paths) — Ansible forked as `fiia` physically cannot write to `/etc/` without failing.

**Rationale:** Ansible already runs on these nodes. Adding namespace isolation fights the tool (Ansible needs real filesystem read access across the system). The `fiia` system user approach achieves the same write-prevention guarantee without kernel version dependencies. Document the minimum-privilege user setup and provide a recommended `useradd` invocation in the deployment playbook. User namespace hardening is a post-MVP extension.

**Implication:** Bootstrap playbook creates `fiia` system user with no login shell, no sudo rights, no write access to audited config paths. Agent binary and config owned by root; agent runs as `fiia`. Drift log directory (`/var/log/fiia/`) owned by `fiia` (write access needed for local diff logs only).

---

## D-11: Hub TLS certificate authority

**Question:** Internal CA already exists, or net-new?

**Decision:** **Net-new. Use `step-ca` (Smallstep)** as the internal CA — single binary, runs on the hub cluster, auto-renews hub certificates. Root CA certificate has a 10-year lifetime (stable for agent embedding); hub leaf certificate rotates annually automatically. Agents embed the root CA PEM at deploy time (written to `/etc/fiia/agent.toml` by the bootstrap playbook). **A dev mode is supported** using a pre-generated development root CA and matching dev hub certificate committed to the repository — agents in dev mode embed the dev root CA and perform full cert chain validation against it. TLS certificate verification is NEVER skipped; `InsecureSkipVerify` is not a supported configuration.

**Rationale:** No existing PKI. `step-ca` is the lowest-friction path: one binary on the hub, no additional infrastructure. Root cert stability (10-year TTL) means agents don't need updating when the hub cert rotates. Dev CA (committed to repo) lowers the barrier for local development without creating a MITM window — full chain validation is maintained in all modes. `InsecureSkipVerify` is explicitly forbidden: it defeats the one-way TLS protection entirely, allowing any attacker on the same network to impersonate the hub.

**Implication:** Hub startup requires `step-ca` running and reachable (or pre-issued cert on disk). Bootstrap playbook writes root CA PEM to agent config. Repository ships a `dev/` directory with a pre-generated dev CA + hub cert for local development. Agent config has a `ca_cert_path` field; dev and prod differ only in which CA cert is referenced — the validation logic is identical.

---

## D-12: Node inventory / CMDB integration

**Question:** Do you have a CMDB with an API, or should node inventory use a simpler source of truth?

**Decision:** **MVP: CSV file of expected node IPs/hostnames.** Hub reads the CSV on startup and on file change (inotify watch on the file). Any node in the CSV absent from the heartbeat registry for 60+ minutes is flagged `UNINSTRUMENTED_SERVER`. **Future: NetBox REST API** — hub queries NetBox's `/api/dcim/devices/` endpoint hourly; same reconciliation logic, different reader. The inventory reader is interface-driven so the CSV and NetBox implementations are swappable.

**Rationale:** No CMDB or authoritative API exists today. CSV is zero-dependency and immediately operational. NetBox is the target CMDB; its REST API is well-documented and stable — the integration path is clear. Interface-driven reader means the migration is a new implementation, not a refactor.

**Implication:** Hub has an `InventoryReader` interface with a `ListNodes() ([]Node, error)` method. CSV implementation reads from a configurable path. NetBox implementation is a future addition. Hub config specifies `inventory_source: csv` (path) or `inventory_source: netbox` (URL + API key).

---

## D-1: Memory ceiling and dependency model

**Question:** Is the 25 MB RSS ceiling hard, or is the real constraint something else?

**Decision:** The primary constraint is **no new tool dependencies on edge nodes**, not a specific MB number. Ansible is already present fleet-wide. The agent must be a single purpose-built binary that uses only what is already installed (Ansible, systemd). Small footprint is a goal; avoiding additional installs is the hard rule.

**Rationale:** Fleet runs active multi-tenant customer workloads. Installing new runtimes or agents (osquery, Telegraf, Python packages) on 5000+ production nodes is an unacceptable operational and interference risk. Ansible pre-existence is a confirmed fact.

**Implication:** Options C (custom diff engine, drops Ansible) and D (osquery + vmagent) are eliminated. Base is Option A or B: Go binary + existing Ansible.

---

## D-13: Store-and-forward queue for unsent payloads

**Question:** If the agent crashes between audit completing and sending the result, or the hub is unreachable for hours/days, drift events are silently lost. Should fiia have a local persistence layer?

**Decision:** **Yes — disk-backed ring buffer in `/var/lib/fiia/queue/`**, 64 entries max, msgpack-encoded, append-only. Agent writes audit results and heartbeat payloads to the queue on completion; sender reads from queue head and advances the pointer on successful ACK from hub. On agent startup, queue is replayed from last unsent position.

**Rationale:** Silent loss is the worst failure mode for a security-oriented audit tool — a crash-during-audit or hub-maintenance window makes the system appear healthy when it is not. A bounded ring buffer (not unlimited growth) prevents disk fill on extended partitions. 64 entries × ~200 bytes = ~12KB max — negligible footprint.

**Implication:** New `internal/agent/queue` package. Queue writes happen before network send. Sender advances read pointer only on hub ACK (TCP close is not sufficient — hub must echo a success frame). Hub adds a lightweight ACK frame to the wire protocol.

---

## D-14: AUDIT_TIMEOUT diagnostic signal

**Question:** `AUDIT_TIMEOUT` is ambiguous — covers both "playbook ran but overran the deadline" and "ansible was CPU-throttled to death by cgroup." Should the status be split?

**Decision:** **Do not split the status code.** Agent cannot reliably distinguish causes from inside the cgroup. Fix: log elapsed wall-clock time when timeout fires. Repeated `AUDIT_TIMEOUT` with short elapsed time (< 30s) is the operator signal for resource throttling. Root fix is correct resource limits (`CPUQuota=10%`, `audit_timeout_sec` >= 5× expected ansible wall time).

**Rationale:** A new status code `AUDIT_THROTTLED` would require the agent to inspect cgroup stats or parse kernel messages — fragile and kernel-version-dependent. The elapsed time in the log line gives the same diagnostic information without protocol complexity. Two consecutive `AUDIT_TIMEOUT` events on a freshly-deployed node is already a strong signal that resource limits are wrong.

**Implication:** `audit.Run()` logs `audit: timeout after %.1fs` when context deadline is exceeded. Operator runbook: if `AUDIT_TIMEOUT` repeats and elapsed < 60s, check `CPUQuota` in the systemd unit.

---

## D-15: Audit scheduling during hub disconnection

**Question:** Should the agent slow down or stop audits when the hub is unreachable, or keep auditing at the normal interval?

**Decision:** **Keep auditing at normal interval regardless of hub connectivity.** Audit is a local operation (ansible check-mode against local filesystem) with no hub dependency. Results queue locally (D-13 store-and-forward). On hub reconnect, queue drains in arrival order — full audit trail across the disconnection window is preserved.

**Rationale:** The audit's value is detecting drift, not reporting it. Slowing audits during a network partition means arriving back online with a stale picture of node state — exactly the wrong behavior for a security tool. A spaceship or remote node may be disconnected for days; the hub should see everything that happened, in order, when it comes back. The queue bound (64 entries) caps local storage; entries older than the ring size are overwritten, which is acceptable — the most recent N audits matter most.

**Implication:** Heartbeat backoff (D-13 send retry) and audit scheduling are independent. Audit loop uses its own timer unaffected by transport state. Queue is the coupling point between audit and transport.

---

## D-16: ansible.cfg reliability

**Question:** The bootstrap-deployed `/var/lib/fiia/ansible.cfg` could go missing (node drift, manual delete, fresh OS install). Should the agent depend on it being present, or ensure the settings are always applied?

**Decision:** **Agent writes a known-good `ansible.cfg` to `/var/lib/fiia/.ansible/audit.cfg` before each audit invocation** and sets `ANSIBLE_CONFIG` to that path. The bootstrap-deployed `/var/lib/fiia/ansible.cfg` remains for manual `ansible-playbook` runs but is not a runtime dependency. No temp files — fixed path, overwritten each run.

**Rationale:** Reliability, not security hardening. If the file is missing for any reason (drift, fresh OS, manual cleanup) the audit silently degrades: fact gathering hangs, `AUDIT_TIMEOUT` every cycle, zero diagnostic signal. Writing the config from code before each run costs nothing and eliminates this failure mode entirely. Not over-engineering — this is the same pattern as writing a pid file or socket path before use.

**Implication:** `audit.Run()` calls `writeAnsibleCfg(cfg)` before constructing the exec.Cmd. `ANSIBLE_CONFIG` in `cmd.Env` points to `/var/lib/fiia/.ansible/audit.cfg`. Bootstrap-deployed file kept for operator convenience.

---

## D-17: Ansible startup smoke check

**Question:** Systemd constraint interactions (`ProtectSystem=strict`, `NoNewPrivileges`, `CPUQuota`) silently compose into failure modes only visible as `AUDIT_TIMEOUT` at the hub. Should the agent validate the ansible invocation is possible before entering its normal loop?

**Decision:** **Yes — run `ansible-playbook --version` once at agent startup** (5s timeout, same environment as real audits). If it fails: log a clear error with the failure output and emit `AUDIT_ERROR` as the first drift payload so the hub immediately sees the node is broken. Does not re-run on every restart — only on first start (no prior successful audit in queue). Skipped if `ansible_playbook_path` is empty.

**Rationale:** The hub shows `AUDIT_TIMEOUT` every 20 minutes with no hint that the root cause is a misconfigured systemd unit. A startup smoke check surfaces the failure in the first 5 seconds of the service starting — operator sees it in `journalctl` and in the hub's drift events immediately. Cost: one extra `ansible-playbook --version` invocation on startup.

**Implication:** `audit.Probe(cfg)` function runs `ansible-playbook --version` under same env as `audit.Run()`. Called from `cmd/agent` after config load, before starting the audit goroutine.

---

## D-18: Hub-to-agent command channel

**Question:** The audit interval is purely agent-driven (timer + jitter). Should the hub be able to trigger an on-demand audit — e.g. after pushing a config change — without waiting up to 20 minutes?

**Decision:** **Not implemented for MVP, but wire protocol reserves the capability now.** Add `PayloadTypeCommand` to the wire's payload type enum. Agent reads a response frame after each heartbeat send; hub currently always sends an empty ACK frame (`PayloadTypeAck`). No behavior change today. When the feature is built: hub sends `{type: "audit_now"}` and agent drains its audit timer.

**Full remote control scope (backlog, Increment 6):** on-demand audit trigger, configurable interval override, remote config push (baseline playbook update), graceful restart signal. All carried over the existing TLS connection as hub→agent command frames — no new ports or protocols.

**Rationale:** Wire protocol changes are the hardest to evolve after deployment (both ends must agree on schema version). Reserving the frame type now costs nothing and avoids a flag day later. The agent already opens a persistent TLS connection per heartbeat — adding a read for a response frame is a one-line change.

**Implication:** `wire.PayloadTypeCommand` and `wire.PayloadTypeAck` constants added now. Transport layer reads one optional response frame after each heartbeat send (with short timeout — ignore if none arrives). Hub sends `PayloadTypeAck` unconditionally after storing the heartbeat.

---

## D-19: Dev watch target

**Question:** The dev loop (make dev-drift → wait 120s → make dev-check-drift) is opaque while waiting. Should there be a combined log stream for the dev loop?

**Decision:** **`make dev-watch` using only `tail -f` + `awk`** — no new tool dependencies. Tails agent.log and drift.log on the VM simultaneously; awk strips the full path from `==> filename <==` headers to a short prefix per line.

**Rationale:** Standard tools only. `tail -f` on multiple files produces `==> filename <==` headers on source switches — awk parses those to label each line. Works everywhere, no install required.

**Implication:** One new Makefile target. No new dependencies.
