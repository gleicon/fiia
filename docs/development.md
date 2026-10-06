# Fiia — Development

## Prerequisites

Go 1.26+, Ansible (`brew install ansible`), Docker. Lima/Colima/Multipass for
the VM dev loop (`brew install lima`); on Linux no VM is needed.

## Flags

| Flag | Meaning |
|------|---------|
| `-manifest PATH` | Check target, or `-write-manifest` destination |
| `-files a,b` | Absolute file paths to hash |
| `-packages a,b` | Names (`name` or `name=version` to pin) |
| `-services a,b` | Service names (actual state is recorded) |
| `-snapshot` | Also record all installed packages / active services; later additions flag `pkg:unauthorized:*` / `svc:unauthorized:*` |
| `-scan-playbook a.yml,b.yml` | Extract managed paths from playbooks (feeds `-write-manifest`, or prints them alone) |
| `-format text\|json` | `-check` output (default `text`); exits 0 clean / 1 drift / 2 error |
| `-node-id ID` | Report label (default: hostname) |
| `-config PATH` | Agent TOML (`manifest_path` is the only required field) |
| `-otlp-endpoint URL` | OTLP/HTTP receiver (default: `OTEL_EXPORTER_OTLP_ENDPOINT` env, else stdout) |
| `-remediate` | Enforce the manifest (remove unauthorized packages/services, fix declared service states). **OFF by default** — the standard fix is re-running the provisioning playbook (IaS); files are never auto-restored by the agent. |

## Manifest sources

Module (runs on the target over plain SSH, last task in the play), CLI
(`-write-manifest`), or scanner (`-scan-playbook`) — all three produce the
same file. Rule of thumb: track what the playbook manages (`template:` /
`copy:` / `lineinfile:` destinations); that's the drift surface.

> **Golden rule:** fiia checks only what the manifest documents. Snapshot mode
> records all packages + services automatically; **files are never
> snapshotted** — document each file you care about in Ansible
> (`copy:`/`template:`/`lineinfile:`, derivable by `-scan-playbook`) or list it
> directly in the manifest. *If it matters, promise it.*

The scanner follows tasks, handlers, blocks, static includes, play `vars:`
+ `vars_files:`, and static `loop:` over `{{ item }}`. What it can't resolve
(templated paths, dynamic includes, `unarchive` members, `state=absent`) is
reported on stderr, never silently dropped — review the warnings.

Package/service checks use the native manager: dpkg→rpm + systemctl on
Linux, Homebrew on macOS. Generation and checking share the lookups.

## OTel signals

`fiia.alive` gauge (missing series = silent node), `fiia.drift.status`
(0/1/2), `fiia.drift.details` gauge while drifting (deviation list as the
`deviations` label — fleet view), `fiia.checks.total` counter, one log record
per check (`fiia.status`, `fiia.deviations[]`, `fiia.manifest_generated_at`).

## Dev loop (VM)

```sh
make dev-init          # backend + VM, first time only
make dev-deploy        # build, provision, record manifest, start service
make dev-check-drift   # on-node verdict + recent log
make dev-drift         # corrupt sentinel + motd
make dev-restore       # restore baseline
make test-linux        # run tests on the VM (or locally on Linux)
```

The agent binary is the single manifest generator — Ansible invokes
`-write-manifest` directly (the `fiia.fleet.agent` role does this as its last
step).

## E2E over Ansible + Docker

`make e2e-ansible` (or `./e2e/ansible/run.sh`) provisions a throwaway
`ubuntu:24.04` container entirely through Ansible — nothing on the host or any
VM is changed (no sudo, no systemd, no daemon; the container is removed and
asserted gone on exit). It exercises the real provisioning path: agent install,
baseline files, manifest recording via the agent binary, then drift and
detection.

Stages:

0. `-scan-playbook` on the host (read-only) — asserts `scan-source.yml` yields
   `/etc/fiia/sentinel` and `/etc/motd`.
1. `e2e/ansible/bootstrap.yml` — install `fiia-agent`, write the baseline files
   (content from the shared `e2e/baseline/sentinel.txt`), record a **snapshot**
   manifest with `-write-manifest`, assert `-check` exits 0.
1b. Scan-driven manifest — the agent generates a manifest from `scan-source.yml`
   (`-write-manifest -scan-playbook`) and `-check` reads it clean; proves the
   scan→manifest path end to end.
2. `e2e/ansible/drift.yml` — corrupt the sentinel (`file:hash_mismatch`) and
   install `curl` (`pkg:unauthorized:curl` against the snapshot); assert both,
   then restore (purge exactly the unauthorized packages parsed from the
   verdict) and assert clean again.

The target is `hosts: all` with `-i <container>,` and
`ansible_connection=community.docker.docker`; the playbooks run as root inside
the container (no `become`, the base image has no sudo). `python3` +
`python3-apt` are installed in the container (Ansible interpreter + apt module).

### Collection path quirk (ansible-core 2.21+)

Ansible resolves collections from `ANSIBLE_COLLECTIONS_PATH` (singular) and
requires the `<path>/ansible_collections/<ns>/<name>` layout. The repo keeps
the source at `ansible/collections/fiia/fleet`, so `run.sh` stages a symlink
tree at `e2e/ansible/collections/` (gitignored) and points Ansible at it. Both
env var spellings are exported (the older plural is inert on 2.21+; the
singular is inert pre-2.21).

## E2E over systemd + Docker

`make e2e-systemd` (or `./e2e/systemd/run.sh`) runs the **agent role** against a
real systemd target (`e2e/systemd/Dockerfile`: ubuntu 24.04 + systemd + dbus,
run privileged with `--cgroupns=host`) — no host or VM changes.

- Stage 1 `provision.yml` — creates a tracked `fiia-test.service`, applies the
  full `fiia.fleet.agent` role, then asserts: service **active** (Type=notify ⇒
  sd_notify `READY=1` was accepted), watchdog 120s, unit/logrotate/config/
  manifest present, baseline clean.
- Stage 2 `drift.yml` — stops + disables `fiia-test.service` and corrupts the
  sentinel; asserts the **running daemon** reports `DRIFT` in its log and that
  `-check` sees `file:hash_mismatch`, `svc:inactive`, `svc:disabled`; restores
  and asserts the daemon reports clean and stays active (watchdog didn't kill
  it). Final section injects a fake `systemctl` that cannot confirm state
  (rc=4) and asserts the agent **alerts `svc:unverifiable`** with remediation
  guidance instead of falsely reporting `svc:inactive`/`svc:disabled`.

No OS changes are needed to run the agent as the unprivileged `fiia` service
account: the role chowns the manifest to `fiia` (still 0400), and service
checks work with the standard systemd/D-Bus on a normal server (the suite's
minimal container image installs `dbus` for this reason).

## E2E live observability (Prometheus + Grafana)

`make e2e-live` (`./e2e/observe/run.sh`) runs the daemon E2E against a minimal
OTel stack and **keeps it running** so you can watch drift happen in real time:

```sh
make e2e-live        # start, drive clean -> drift -> restored, leave stack up
make e2e-live-down   # tear the stack down
```

While it runs:

| Service | URL | Notes |
|---|---|---|
| Grafana | http://localhost:3000 | admin / admin; provisioned dashboard *Fiia — drift detection* + Unified Alerting rules |
| Prometheus | http://localhost:9090 | `/alerts` shows `FiiaDriftDetected` firing on drift, `FiiaNodeSilent` on missing heartbeat |
| Collector | http://localhost:9464/metrics | `fiia_alive`, `fiia_drift_status`, `fiia_checks_total` |
| Webhook | make e2e-live-webhook | prints Grafana alert notifications as they fire |

Stack: collector (OTLP/HTTP → debug + file + **Prometheus** exporter) +
`prom/prometheus:v2.55.0` (scrape + alert rules `e2e/observe/rules.yml`) +
`grafana/grafana:11.3.0` (provisioned datasource, dashboard, Unified Alerting
rules + webhook contact point). The scenario auto-drifts the sentinel, waits
for `fiia_drift_status` to flip 0→1 and the alerts to fire, then restores and
watches them resolve.

See [otel-prometheus-grafana.md](otel-prometheus-grafana.md) for the full
breakdown and how to reuse the config for your own environment. For the
operator flow (find the drift, fix it, fleet queries, remediation) see
[drift-operations.md](drift-operations.md).

> The live stack publishes ports 4318/9464/9090/3000, so it conflicts with a
> concurrent `make e2e` — run `make e2e-live-down` first.

### Resources (Colima)

Run the suites one at a time. The default Colima VM (2 CPU / 2 GiB) is enough
for any single suite but can OOM the docker daemon if several run at once or
the live stack runs alongside the systemd suite. Bump the VM for concurrent
use: `colima start --cpu 4 --memory 6`. CI runners are unaffected.

### Systemd boot determinism

The suite's image (`e2e/systemd/Dockerfile`) blanks `/etc/fstab`, disables the
GPT auto-mount generator, and masks container-irrelevant units (logind, getty,
disk scrub/utmp/pcrphase) so `is-system-running` settles deterministically. The
container is started with **only** the cgroup mount (`--privileged
--cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw`): mounting the host's
`/run` leaks the host's generated systemd units into the container and lets the
container write into the host's `/run`, which stalls the boot.

## E2E over Docker

`make e2e` (or `./e2e/run.sh`, from the repo root) runs the full daemon path
against real containers — no VM or SSH. The agent runs in a Debian image and
exports to an OTel Collector; the script asserts on the collector's output.

```sh
make e2e            # builds e2e/Dockerfile.agent, starts compose, asserts
```

Stages:

1. Build + start. The image build also records the baseline manifest
   (`/etc/fiia/sentinel`, `/etc/motd`; package `bash`, **snapshot mode**).
2. Clean verdict — collector must export `fiia.drift.status=0`; the
   `fiia.alive` liveness gauge must be present.
3. File drift — appends a line to the sentinel, expects `fiia.drift.status=1`
   and a log record carrying the `hash_mismatch` deviation.
4. Package drift — installs `vim-tiny`, expects `pkg:unauthorized:vim-tiny`
   (snapshot-unauthorized).
5. Restore — copies the shared baseline sentinel in and purges `vim-tiny`,
   expects `fiia.drift.status=0` again.

Layout: `e2e/Dockerfile.agent` (multi-stage build), `e2e/agent.toml`
(`node_id=e2e-node`, heartbeat 5s / audit 10s), `e2e/otelcol.yaml`
(OTLP/HTTP :4318 → debug + file exporters, pinned to
`otel/opentelemetry-collector:0.162.0`), `e2e/docker-compose.yml` (collector +
agent, host `e2e/out/` mounted), `e2e/run.sh` (driver — parses
`telemetry.json` with python3, prefers `docker compose` v2 and falls back to
standalone `docker-compose` v1 with `DOCKER_API_VERSION=1.44`).

### Why the exporter sends to `/v1/<signal>`

This test is what caught the OTLP 404. `otlploghttp.WithEndpointURL`
(`otlploghttp@v0.23.0` / `otlpmetrichttp@v0.43.0`) replaces the SDK's default
`/v1/logs` (resp. `/v1/metrics`) path with the URL's empty path, so the
collector 404s and nothing is recorded. `internal/agent/otel` therefore passes
host and signal path separately (`WithEndpoint(hostport)` +
`WithURLPath("/v1/…")`). The E2E would catch a regression, since it asserts on
real collector output.

### Shortcomings

- Compose: prefers `docker compose` v2, falls back to standalone v1 with
  `DOCKER_API_VERSION=1.44` (old compose clients negotiate a too-old API on
  this box); compose file pins schema `3.8`.
- Collector image pinned (`otel/opentelemetry-collector:0.162.0`), so results
  are reproducible unless the pin is bumped.
- Host needs `python3` for the telemetry assertions.
- The agent exports to base-endpoint `/v1/metrics` and `/v1/logs`;
  per-signal `OTEL_EXPORTER_OTLP_*_ENDPOINT` env vars are not honored.
- Fixed polling timeouts per stage; slow CI runners may need more.
- systemd suite needs a privileged container (`--cgroupns=host`); CI runners
  support it, but not all sandboxed Docker environments do.

## Recent changes

- E2E suites: `make e2e` (daemon → collector), `make e2e-ansible` (Ansible over
  Docker), `make e2e-systemd` (agent role over systemd), `make e2e-live`
  (daemon → collector → Prometheus → Grafana). Wired into CI as a separate
  `e2e` job.
- Agent permission signals (no OS changes required):
  - `svc:unverifiable` deviation when systemctl cannot confirm a service state
    (denied manager query), with remediation guidance — instead of a false
    `svc:inactive`/`svc:disabled`.
  - `CHECK_ERROR` verdict + periodic OTel status-2 emissions when the manifest
    is unreadable (permission), instead of a silent node.
  - `-check` distinguishes "manifest missing" from "manifest unreadable
    (permission)".
  - `fiia.drift.status` gauge carries no per-status label so Prometheus sees a
    single series (0/1/2).
- Fleet-visible drift detail: **`fiia.drift.details`** metric emitted while
  drifting, carrying the deviation list as the `deviations` label — a central
  Prometheus/Grafana shows *what* drifted on every node without logging in
  (see [drift-operations.md](drift-operations.md)).
- **Auto-remediation is opt-in and OFF by default**: `-remediate` (one-shot) or
  `remediate = true` (config/daemon) enforces the manifest — removes
  unauthorized packages/services, fixes declared service states. Files are
  never auto-restored (IaS restores content). Default posture = detect + fix
  by re-running the provisioning playbook.
- `docs/drift-operations.md` documents the operator flow (find → fix → verify,
  deleted files, fleet queries, promises model).
- **Single manifest generator**: removed the `fiia.fleet.manifest` Python module
  and `make dev-collection-test`. The agent binary is the only implementation —
  Ansible invokes `fiia-agent -write-manifest` (the `agent` role does this as
  its last step via `fiia_manifest_*` variables). No Python↔Go duplication to
  keep in sync.
- E2E fixes surfaced by these suites:
  - Role: manifest chowned to the `fiia` user after recording (was 0400 root
    ⇒ the daemon could never read it and silently disabled checks).
  - Role: unit `After=dbus.service`; the systemd suite container installs
    `dbus` (standard on real servers, so no manual OS change is needed).
- `e2e/baseline/sentinel.txt` is the single source of truth for the baseline
  used by all suites.
- `e2e/out/`, `e2e/ansible/{bin,collections}/`, `e2e/systemd/{bin,collections}/`
  gitignored.
- `.github/workflows/ci.yml`: Go `1.24` → `1.26`; added the `e2e` job.
- `deploy/ansible/inventory/dev.ini` untracked (host-specific; regenerate with
  `make dev-inventory`); stray `fiia-agent-linux-*` binaries removed.
- Hub-era leftovers removed (no hub): `cmd/hub`, `dev/hub.toml`,
  `dev/gen_certs`, `dev/baseline.yml`, `dev/agent.toml`,
  `docs/{prd,protocol,step-ca-setup}.md`.
- Hub-era planning/spec docs deleted (BLUEPRINT, DECISIONS, EXPLORE, PLAN,
  SPEC, and the old `.project/PROJECT.md`); the live doc set is `docs/` + the
  README, and `.project/PROJECT.md` reflects the current single-binary
  architecture.

## Makefile

```
build test test-linux
e2e e2e-ansible e2e-systemd e2e-live e2e-live-down
dev-init dev-setup dev-vm-create dev-vm-start dev-vm-stop dev-vm-status
dev-build dev-inventory dev-deploy
dev-run dev-logs dev-journal dev-watch dev-stop
dev-drift dev-restore dev-check-drift
```

## Config

```toml
[agent]
node_id               = "web-01"              # optional, defaults to hostname
manifest_path         = "/etc/fiia/manifest.json"
heartbeat_interval_sec = 60
audit_interval_sec    = 1200
audit_jitter_max_sec  = 120
# otlp_endpoint = "http://otelcol.internal:4318"  # omit for stdout export
# remediate     = false  # !!! WARNING: only enforces packages & services in the
#                        # manifest snapshot; does NOT restore users, cron,
#                        # firewall, sysctl or other Ansible-managed state.
#                        # Re-run the provisioning playbook for a complete fix.
```

`fiia.drift.details` is emitted while drifting with the deviation list as the
`deviations` label — that's how a fleet sees *what* drifted centrally (see
[drift-operations.md](drift-operations.md)).

## Security

No inbound connections, no writes to host config, `agent.toml` mode 0400,
manifest verified read-only.
