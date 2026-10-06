# Fiia — Configuration Drift Detection for Ansible-Managed Servers

You provision with Ansible. Fiia checks that servers still match what you
provisioned — files, packages, services — and reports to your OpenTelemetry
pipeline. No hub, no secrets. Detection first; remediation is an explicit
opt-in (`-remediate` / `remediate = true`), never the default.

## Quick start

```sh
# 1. Record the baseline after provisioning.
fiia-agent -write-manifest -manifest /etc/fiia/manifest.json \
  -files /etc/nginx/nginx.conf,/etc/ssh/sshd_config \
  -packages nginx,openssh-server \
  -services nginx,ssh -snapshot

# 2. Check for drift any time.
fiia-agent -check -manifest /etc/fiia/manifest.json
# OK: no drift (files=2 packages=2 services=2)   → exit 0
# DRIFT: 1 deviation(s)                          → exit 1
```

Have playbooks? Derive the file list from them instead of writing it by hand:

```sh
fiia-agent -write-manifest -manifest /etc/fiia/manifest.json \
  -scan-playbook site.yml -packages nginx -services nginx -snapshot
```

Last task in your play works too (runs over plain SSH, no daemon — the agent
binary is the manifest generator, invoked directly by Ansible):

```yaml
- name: Update fiia drift manifest
  ansible.builtin.command:
    argv: ["/usr/local/bin/fiia-agent", -write-manifest,
           -manifest, /etc/fiia/manifest.json,
           -files, /etc/nginx/nginx.conf,/etc/ssh/sshd_config,
           -packages, nginx,openssh-server,
           -services, nginx,ssh, -snapshot]
```

The `fiia.fleet.agent` role does this for you via its `fiia_manifest_*`
variables (files, packages, services, snapshot) as the last provisioning step.

## How it runs

> **Golden rule: fiia only checks what the manifest documents.** The manifest is
> a curated promise, not an audit log or a full OS snapshot. Packages and
> services are snapshotted automatically (snapshot mode) so additions are
> caught; **files are never snapshotted** — a file is checked only if you
> document it in Ansible (`copy:`/`template:`/`lineinfile:`, picked up by
> `-scan-playbook`) or list it directly in the manifest. *If it matters, promise
> it.*

| Mode | Command | Use |
|------|---------|-----|
| Daemon | `fiia-agent -config /etc/fiia/agent.toml` | systemd: heartbeats + checks to OTel |
| One-shot | `fiia-agent -check …` | cron, CI, `ansible -m command`, by hand |
| Generate | `fiia-agent -write-manifest …` | record baseline after provisioning |
| Scan | `fiia-agent -scan-playbook …` | list what a playbook manages |

Every check emits to OTel (stdout JSON by default, OTLP/HTTP when configured):
`fiia.alive` (alert on missing series = silent node), `fiia.drift.status`
(0 clean / 1 drift / 2 error), `fiia.drift.details` (while drifting, carries
the deviations as labels), `fiia.checks.total`, plus a log record per check.
Exit codes for `-check`: 0 / 1 / 2.

```toml
# /etc/fiia/agent.toml — manifest_path is the only required field
[agent]
manifest_path = "/etc/fiia/manifest.json"
# node_id, heartbeat_interval_sec, audit_interval_sec, otlp_endpoint …
```

## Docs

| | |
|-|-|
| [docs/development.md](docs/development.md) | Flags, OTel signals, dev loop, config, E2E |
| [docs/drift-operations.md](docs/drift-operations.md) | Find/fix drift, fleet queries, remediation & promises |
| [docs/architecture.md](docs/architecture.md) | Components, data flows, rules |
| [docs/otel-prometheus-grafana.md](docs/otel-prometheus-grafana.md) | Live observability stack + how to build your own |
| [Role README](ansible/collections/fiia/fleet/roles/agent/README.md) | Ansible install + variables |

Local E2E over Docker (no VM): `make e2e` — full daemon → collector flow with
drift assertions; `make e2e-ansible` — the same drift story driven entirely by
Ansible against a throwaway container; `make e2e-systemd` — the agent role +
service drift against a real systemd container; `make e2e-live` — daemon →
collector → Prometheus → Grafana with a live dashboard and alert rules (tear
down with `make e2e-live-down`). See [docs/development.md](docs/development.md).
