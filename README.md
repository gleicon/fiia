# Fiia — Fleet Configuration Audit Agent

Passive drift detection for bare-metal and VM fleets. Two statically-linked binaries, no auto-remediation.

## What it does

- Detects configuration drift: file checksums, package versions, service states — checked natively in Go every 20 minutes against a JSON manifest written at provisioning time.
- Optional **snapshot mode** captures the full package and service list at provision time; any unauthorized addition is flagged at audit time without a separate security scanner.
- Raises alerts when nodes go silent, have HMAC mismatches, drift from baseline, or have stale manifests (>90 days unregenerated).
- Pushes alert events to any webhook (Slack, PagerDuty, Grafana Alertmanager) on set and clear.
- Exposes Prometheus metrics and a REST API on a single port; no separate metrics server.

## Architecture

```
[edge node]                               [hub]

fiia-agent (systemd, locked-down unit)   fiia-hub
  heartbeat goroutine (5 min)              ingest :9443 (TLS)
    collect USE metrics (/proc)              HMAC verify before decode
    send HeartbeatPayload ─────────────→     registry.Update
                                             async DB write queue
  audit goroutine (20 min ± jitter)        api :9091
    read /etc/fiia/manifest.json              GET /nodes /alerts /metrics
    check files (SHA256), packages,           POST /nodes/{id}/enroll
      services (systemctl), snapshot          POST /nodes/{id}/audit_now
    send DriftPayload ─────────────────→     store.AppendDrift
                                             store.SetAlert / ClearAlert
  store-and-forward queue (disk)           → webhook push (async)
    write before send; advance on ACK
```

Payloads are MessagePack + HMAC-SHA256 over TLS 1.3. Hub validates HMAC **before** decoding — no exceptions.

## Quick start (local dev)

**Prerequisites:** Go 1.24+, Lima (`brew install lima`), Ansible (`brew install ansible`), Docker.

```sh
make dev-init          # first time: detect backend, create + start Lima VM
make dev-hub           # terminal 1: start hub  (ingest :9443, API+metrics :9091)
make dev-deploy        # terminal 2: build, provision VM, start agent
```

Verify:
```sh
make dev-run           # systemctl status fiia-agent (in VM)
curl http://localhost:9091/nodes
curl http://localhost:9091/metrics | grep fiia_nodes
```

Simulate drift:
```sh
make dev-drift         # corrupt managed files on VM
make dev-check-drift   # query hub for alerts
make dev-restore       # restore baseline
```

Run tests on Linux (cross-compiled from macOS, executed on Lima VM):
```sh
make test-linux
make test-linux ARGS="./internal/agent/audit -v"
```

**Linux:** `make dev-init` auto-selects local mode — no VM needed.

## Adding drift monitoring to an existing playbook

```yaml
# last task in your provisioning play
- name: Update fiia drift manifest
  fiia.fleet.manifest:
    mode: snapshot          # records full pkg + service list; flags unauthorized additions
    files:
      - /etc/nginx/nginx.conf
      - /etc/ssh/sshd_config
    packages:
      - nginx
      - openssh-server
    services:
      - nginx
      - { name: ssh, running: true, enabled: true }
```

`mode: declared` (default) checks only listed items. `mode: snapshot` additionally flags any package or service that appears after provisioning.

See [`ansible/collections/fiia/fleet/roles/agent/README.md`](ansible/collections/fiia/fleet/roles/agent/README.md) for collection install and full variable reference.

## Alerts

| Alert | Condition | Cleared |
|-------|-----------|---------|
| `DRIFT_DETECTED` | Manifest check finds deviations | Next clean audit |
| `MANIFEST_STALE` | `manifest.generated_at` > 90 days | Next audit with fresh manifest |
| `HMAC_MISMATCH` | Frame received with invalid HMAC | Manual |
| `AGENT_PAUSED` | Node silent > paused threshold | Next heartbeat |
| `AGENT_UNREACHABLE` | Node silent > unreachable threshold | Next heartbeat |
| `UNINSTRUMENTED_SERVER` | In inventory, never reported | First heartbeat |

## Docs

| | |
|-|-|
| [docs/development.md](docs/development.md) | Dev workflow, Makefile reference, configuration |
| [docs/architecture.md](docs/architecture.md) | Component design, data flows, dependency rules |
| [docs/protocol.md](docs/protocol.md) | Wire format, HMAC authentication, schema versioning |
| [docs/step-ca-setup.md](docs/step-ca-setup.md) | Production TLS CA setup |
| [DECISIONS.md](DECISIONS.md) | Design decisions with rationale |
