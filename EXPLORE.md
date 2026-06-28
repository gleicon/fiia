# Explore: Fiia — Validating Assumptions & Candidate Approaches

> Generated from PRD + web research. Overwrite-safe scratchpad.

---

## Assumption Audit: What the PRD Got Wrong or Overstated

Before evaluating approaches, four assumptions in the PRD/SPEC require correction. These are not minor; two affect correctness, two affect security posture.

### ❌ GOGC=10 will cause GC thrashing (assumption: wrong)

GOGC=10 means GC runs when live heap grows by 10% — ~10× more frequently than default. On a small-heap, periodically-allocating daemon this causes GC thrashing: the runtime spends more time collecting than doing work. This is a **documented Go runtime failure mode** (golang/go#22743, #23044, #37927).

**Correct approach**: `GOMEMLIMIT=22MiB` + `GOGC=off` (or `GOGC=100`). The GC only fires when approaching the memory ceiling — exactly the right behavior for a memory-constrained daemon. The Go team now recommends this pattern explicitly.

### ❌ Cap'n Proto for 96-byte packets (assumption: over-engineered, wrong fit)

Cap'n Proto's advantages (zero-copy traversal, no decode step) only materialize on large, read-heavy payloads. At 96 bytes every 5 minutes, the dominant costs are TLS handshake and scheduling — not serialization. The Go Cap'n Proto library (`capnproto2`) is less maintained than Protobuf or MessagePack alternatives. Zero real-world agent systems at this scale use Cap'n Proto.

**Correct approach**: Protocol Buffers (broad Go ecosystem, mature tooling) or MessagePack (simpler, ~90ns/op). Either achieves smaller or equivalent wire size with better operational support.

### ⚠️ "mTLS is too complex at 5000 nodes" (assumption: overstated)

This is conditionally true *without automation*. With SPIFFE/SPIRE, Vault Agent PKI, or Teleport, mTLS at 5000+ nodes is the **industry-standard approach** — used by Cloudflare, HashiCorp, AWS App Mesh, and Teleport's own fleet product. Short-lived certificates rotate automatically; revocation is instantaneous.

HMAC per-node secrets avoid cert management but introduce equivalent bootstrapping complexity (how do you deliver 5000 unique secrets securely on first boot?), no forward secrecy, and no automatic rotation. The PRD trades one hard problem for another while getting weaker security.

**This is a real trade-off, not a clear win.** HMAC is simpler to implement; mTLS is more secure and operationally standard. The PRD presents this as a solved problem when it is an open design decision.

### ⚠️ Ansible --check --diff as reliable periodic audit (assumption: conditionally true)

Ansible check mode is **not designed as a continuous audit mechanism**. Known failure modes:
- Modules not declaring `check_mode: true` are silently skipped → false negatives
- Shell/command tasks cannot predict changes without `creates`/`removes` → unpredictable results  
- Dependency chains (task B uses variable from task A's result) break when A is skipped in check mode

At scale, raw subprocess invocation is fragile. Production deployments use AWX/Tower for scheduling and result aggregation. This is workable if playbooks are written check-mode-aware, but it is **not safe by default for arbitrary playbooks**.

---

## Is Fiia Reinventing an Existing System?

**No. The combination does not exist as a mature OSS project.**

Gap map across the space:

| Tool | Config Audit | Liveness Heartbeat | USE Telemetry | Idle RAM |
|------|-------------|-------------------|---------------|----------|
| Wazuh agent | Yes (SCA/FIM) | No | No | ~35–80 MB |
| osquery | Yes (SQL) | No | Partial | ~30–80 MB |
| Chef InSpec | Yes (Ruby DSL) | No | No | ~0 (no daemon) |
| Node Exporter | No | No | Yes | ~10–25 MB |
| Telegraf | No | No | Yes | ~30–50 MB |
| SaltStack minion | Yes (state) | Partial (ZMQ) | No | ~30–80 MB (leaky) |
| OTel Collector | No | Via OpAMP | Partial | ~80–150 MB |

Closest: Wazuh at 35 MB documented (50–80 MB observed) is a security/compliance tool with a heavy back-end (OpenSearch) and no USE telemetry. Osquery + Node Exporter is the common pairing but totals ~50–100 MB with two separate agents.

**Fiia fills a genuine gap**: unified config audit + liveness + USE telemetry in a single daemon under 25 MB. No OSS equivalent exists. The 25 MB target is the hard part — it requires a purpose-built agent (Go or C/Rust), not composition of existing tools.

---

## Candidate Approaches

### Option A — Build Fiia in Go, fix the wrong assumptions

Build as the PRD describes, correcting the four issues above: swap Cap'n Proto for Protobuf, swap GOGC=10 for GOMEMLIMIT+GOGC=off, treat HMAC vs mTLS as an explicit decision, and restrict Ansible check mode to check-mode-aware playbooks only.

**Trade-offs:**
- Go static binary achieves 25 MB target with discipline (GOMEMLIMIT, sync.Pool, no CGO)
- Protobuf: mature Go library, schema versioning, broader ecosystem
- HMAC: simpler implementation, no PKI dependency, weaker security posture
- Ansible dependency on each node adds ~50 MB disk, Python runtime, and reliability variance

**When to pick**: Core team already owns the Ansible playbook baseline; Ansible is already present on all nodes; PKI infrastructure does not exist.

**Effort**: High (purpose-built daemon). **Risk**: Medium — Go memory discipline is achievable but requires instrumentation to verify.

---

### Option B — Build Fiia in Go + SPIFFE/SPIRE for auth

Same as Option A but replace the HMAC per-node secret model with SPIFFE/SPIRE for automatic short-lived certificate issuance and rotation. Hub authenticates edge agents via X.509 SVID over mTLS.

**Trade-offs:**
- Eliminates secret bootstrapping problem and HMAC rotation risk
- SPIRE Server is a new infrastructure dependency (adds operational complexity if you don't already run it)
- Short-lived certs (1h TTL) require SPIRE Server HA — otherwise fleet agents can't renew and start rejecting connections
- Forward secrecy, automatic rotation, and instant revocation are genuine security wins
- Wire protocol remains Protobuf/MessagePack + TLS; no HMAC appended to frames

**When to pick**: Security team requires forward secrecy and auditability; team already operates Vault or Teleport; compliance requires certificate-based auth.

**Effort**: High + SPIRE operational overhead. **Risk**: Medium-High (SPIRE HA is a hard dependency).

---

### Option C — Replace Ansible check mode with a custom diff engine

Keep the Go daemon but drop the Ansible subprocess entirely. Instead: ship a compiled rule evaluator (based on OPA/Rego or CEL) that compares local file state against a SHA-256 manifest and structured baseline pulled from GitLab. No Python, no Ansible dependency on the edge node.

**Trade-offs:**
- Eliminates the Ansible subprocess reliability issues (hangs, false negatives, check_mode gaps)
- Eliminates Python runtime dependency (~50 MB disk per node)
- Eliminates 10-minute subprocess timeout as a latency ceiling — diff evaluation becomes sub-second
- Loses Ansible's rich module library (file, package, service, sysctl checks) — custom engine must reimplement each check type
- OPA/Rego evaluated in-process has a WASM runtime overhead (~5–8 MB additional RSS)
- Requires migrating existing playbook baseline to a new policy DSL — significant operational lift

**When to pick**: Ansible is not already deployed on fleet nodes; baseline playbooks are simple file/permission checks; latency on drift detection matters; engineering team can own a policy DSL migration.

**Effort**: Very High (policy engine + migration). **Risk**: High (custom audit engine surface area for false negatives).

---

### Option D — Compose: osquery + vmagent + custom heartbeat

Skip building a custom config audit engine. Deploy osquery (config audit via SQL), vmagent (USE telemetry via Node Exporter scrape forwarding), and a thin custom heartbeat daemon. Three separate processes.

**Trade-offs:**
- osquery is battle-tested at 1M+ nodes (Facebook); vmagent is lean (~15–30 MB)
- Gives up the 25 MB single-agent constraint — combined footprint ~70–120 MB
- osquery watchdog handles subprocess isolation automatically
- No custom audit logic to build or maintain
- Fleet manager required for osquery result aggregation (FleetDM, Kolide) — additional infrastructure
- Heartbeat daemon still needs building; three separate update/rollout pipelines

**When to pick**: 25 MB limit is negotiable; team prioritizes operational maturity over footprint; existing osquery investment or fleet management tooling.

**Effort**: Medium (integration, no core engine build). **Risk**: Low for osquery/vmagent; medium for heartbeat stitching.

---

## Open Questions the Choice Hinges On

OQ-A: Is the 25 MB RSS ceiling a hard constraint (systemd enforced, non-negotiable) or a target that could flex to 50 MB if the security or reliability trade-off justifies it? *Determines whether Option D is viable.*

OQ-B: Is Ansible already deployed on all 5,000 nodes? *If not, Option C or D become more attractive — adding Ansible to 5000 nodes is a significant rollout in itself.*

OQ-C: Does the team own a PKI or certificate automation tool (Vault, SPIRE, Teleport) today? *Determines whether Option B's auth model is an incremental investment or a greenfield dependency.*

OQ-D: What is the acceptable false-negative rate for drift detection? *Ansible check mode has known false-negative paths. If any missed drift is unacceptable, Option C (custom engine) or osquery (Option D) are more reliable.*

OQ-E: What does the GitLab baseline actually contain? If it is structured file checks and permissions only, a custom engine (Option C) is a realistic rewrite. If it contains complex Ansible module logic (packages, services, kernel parameters), rewriting it is prohibitive.

OQ-F: Does the hub need to store raw config diff content, or only drift status flags? If diffs contain sensitive config values, data residency/compliance requirements may constrain the hub's storage backend.

---

## Lean (operator's call)

Option A with the corrected assumptions is the fastest path that preserves the PRD's intent. The 25 MB constraint is the differentiating design goal; it rules out composition (D) and any Ruby/Python runtime approach. The Ansible subprocess risk (Option A vs C) is the most consequential open question — if the existing baseline playbooks use `shell`/`command` tasks heavily, Option C becomes necessary.

HMAC vs SPIFFE (A vs B) is worth a 30-minute conversation with the security team before committing to either.

---

*Run `/ds-grill-me` to work through OQ-A through OQ-F and lock a decision.*
