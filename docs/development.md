# Fiia development and usage

## Prerequisites

Go 1.26+, Ansible (`brew install ansible`), Docker. Lima/Colima/Multipass for
the VM dev loop (`brew install lima`); on Linux no VM is needed.

## Flags

| Flag | Meaning |
|------|---------|
| `-manifest PATH` | Check target, or `-write-manifest` destination |
| `-files a,b` | Absolute file paths to hash |
| `-packages a,b` | Package names (`name` or `name=version` to pin) |
| `-services a,b` | Service names (actual state is recorded) |
| `-snapshot` | Also record all installed packages and active services; later additions flag `pkg:unauthorized:*` / `svc:unauthorized:*` |
| `-scan-playbook a.yml` | Extract managed paths from playbooks (feeds `-write-manifest`, or prints them alone) |
| `-format text\|json` | `-check` output (default `text`); exits 0 clean / 1 drift / 2 error |
| `-node-id ID` | Report label (default: hostname) |
| `-config PATH` | Agent TOML (`manifest_path` is the only required field) |
| `-otlp-endpoint URL` | OTLP/HTTP receiver (default: `OTEL_EXPORTER_OTLP_ENDPOINT` env, else stdout) |
| `-remediate` | Enforce the manifest (remove unauthorized packages/services, fix declared service states). Off by default; the standard fix is re-running the provisioning playbook. Files are never auto-restored. |
| `-record-begin` | Index the file tree under `-dirs` into `-data` before a provisioning run |
| `-record-end` | Diff `-data` against a fresh index and merge the changed files into `-manifest` (the ansible inventory) |
| `-data PATH` | Record index path (record-begin writes it, record-end reads it) |
| `-dirs a,b` | Record roots to index |

## Manifest sources

The agent binary produces the manifest three ways: as the last task in the
play (via `ansible -m command`, plain SSH, no daemon), from the CLI
(`-write-manifest`), or from the playbook scanner (`-scan-playbook`). They all
build the same file. Track what the playbook manages (`template:` / `copy:` /
`lineinfile:` destinations); that is the drift surface.

Golden rule: fiia checks only what the manifest documents. Packages and
services are snapshotted automatically; files are never snapshotted, so
document every file you want checked in Ansible or directly in the manifest.
If it matters, promise it.

The scanner follows tasks, handlers, blocks, static includes, play `vars:` and
`vars_files:`, and static `loop:` over `{{ item }}`. What it cannot resolve
(templated paths, dynamic includes, `unarchive` members, `state=absent`) is
reported on stderr. Review the warnings.

Package and service checks use the native manager: dpkg/rpm + systemctl on
Linux, Homebrew on macOS. Generation and checking share the lookups.

## OTel signals

`fiia.alive` gauge (missing series = silent node), `fiia.drift.status` (0/1/2),
`fiia.drift.details` gauge while drifting (deviation list as the `deviations`
label, for a fleet view), `fiia.checks.total` counter, one log record per check
(`fiia.status`, `fiia.deviations[]`, `fiia.manifest_generated_at`).

## Config

```toml
[agent]
node_id               = "web-01"              # optional, defaults to hostname
manifest_path         = "/etc/fiia/manifest.json"
heartbeat_interval_sec = 60
audit_interval_sec    = 1200
audit_jitter_max_sec  = 120
# otlp_endpoint = "http://otelcol.internal:4318"  # omit for stdout export
# remediate     = false  # only enforces packages & services in the manifest
#                        # snapshot. Re-run the provisioning playbook instead.
# check         = ["sha256", "mode"]   # file attributes to compare; add "size"
#                                      # or "mtime" to check those too
# fast_scan     = false  # trust size+mtime and skip the sha256 for unchanged
#                        # files. ~6x faster checks on large inventories;
#                        # opt-in because it trusts stat as the change signal.
```

## E2E over Docker

All suites run in Docker with no host or VM changes.

| Target | What it runs |
|--------|--------------|
| `make e2e` | Agent daemon to OTel collector; clean, file drift, package drift, restore |
| `make e2e-ansible` | Provision a throwaway container with Ansible (`-write-manifest`, snapshot, `-remediate`), scan-playbook, drift, restore |
| `make e2e-systemd` | The `fiia.fleet.agent` role in a real systemd container; service and file drift, permission alert |
| `make e2e-live` | Daemon to collector to Prometheus to Grafana; leaves the stack running (tear down with `make e2e-live-down`) |

The agent binary is the single manifest generator; Ansible invokes
`-write-manifest` directly (the `agent` role does this as its last step).

## Makefile

```
build test test-linux
e2e e2e-ansible e2e-systemd e2e-live e2e-live-down e2e-live-webhook
dev-init dev-setup dev-vm-create dev-vm-start dev-vm-stop dev-vm-status
dev-build dev-inventory dev-deploy
dev-run dev-logs dev-journal dev-watch dev-stop
dev-drift dev-restore dev-check-drift
```