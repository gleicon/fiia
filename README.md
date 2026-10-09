# Fiia

Configuration drift detection for Ansible-managed servers. `fiia-agent` records
what a playbook provisions (files, packages, services) in a manifest and checks
the node against it on an interval, reporting verdicts and liveness over
OpenTelemetry. No hub, no secrets.

## Quick start

```sh
# record the baseline after provisioning
fiia-agent -write-manifest -manifest /etc/fiia/manifest.json \
  -files /etc/nginx/nginx.conf -packages nginx -services nginx -snapshot

# check for drift
fiia-agent -check -manifest /etc/fiia/manifest.json   # exit 0 clean, 1 drift, 2 error
```

Run the record step as the last task in your play. The `fiia.fleet.agent` role
does this via its `fiia_manifest_*` variables; directly it looks like:

```yaml
- name: Update fiia drift manifest
  ansible.builtin.command:
    argv: [fiia-agent, -write-manifest, -manifest, /etc/fiia/manifest.json,
           -files, /etc/nginx/nginx.conf,/etc/ssh/sshd_config, -snapshot]
```

Golden rule: fiia checks only what the manifest documents. Packages and
services are snapshotted automatically; files are never snapshotted, so
document every file you want checked. If it matters, promise it.

The agent runs as a systemd daemon (`-config /etc/fiia/agent.toml`, only
`manifest_path` required) or one-shot (`-check`). Each check emits
`fiia.alive`, `fiia.drift.status` (0/1/2), `fiia.drift.details`, and
`fiia.checks.total` to stdout or OTLP/HTTP, plus a log record of the
deviations.

## Docs

| | |
|-|-|
| [docs/development.md](docs/development.md) | Flags, config, OTel signals, E2E |
| [docs/ansible-interception.md](docs/ansible-interception.md) | Adopt fiia with existing cookbooks; record what Ansible changes |
| [docs/drift-operations.md](docs/drift-operations.md) | Find/fix drift, fleet queries, remediation |
| [docs/architecture.md](docs/architecture.md) | Components and data flows |
| [docs/otel-prometheus-grafana.md](docs/otel-prometheus-grafana.md) | Live observability stack and how to build your own |
| [Role README](ansible/collections/fiia/fleet/roles/agent/README.md) | Ansible install and variables |

Local E2E over Docker: `make e2e`, `make e2e-ansible`, `make e2e-systemd`,
`make e2e-live` (tear down with `make e2e-live-down`).