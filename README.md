# Fiia — Fleet Configuration Audit Agent

Passive drift detection for bare-metal and hypervisor fleets. Two binaries, no auto-remediation.

## How it works

- **fiia-agent** — locked-down systemd unit on every node. Sends a 5-minute heartbeat with USE-method metrics. Every 20 minutes reads a JSON manifest (generated at provisioning time by the `fiia.fleet.manifest` ansible module) and checks files, packages, and services against the declared baseline — natively in Go, no ansible subprocess at audit time. Never modifies anything.

- **fiia-hub** — validates per-node HMAC-SHA256 before decoding any payload. Maintains heartbeat registry, detects silent nodes, flags nodes in inventory that never reported, exposes Prometheus metrics and a REST API.

## Quick start (local dev)

**Prerequisites:** Go 1.24+, Lima (`brew install lima`), Ansible (`brew install ansible`), Docker.

```sh
# First time: set up VM
make dev-init          # detects backend, creates and starts Lima VM

# Two terminals:
make dev-hub           # terminal 1 — starts hub on :9443 (ingest) / :9091 (API) / :9090 (metrics)
make dev-deploy        # terminal 2 — cross-compiles, provisions VM, starts agent
```

Verify:
```sh
make dev-run           # systemctl status fiia-agent (in VM)
curl http://localhost:9091/nodes
```

Simulate drift:
```sh
make dev-drift         # corrupt managed files on VM
make dev-check-drift   # query hub for alerts
make dev-restore       # restore baseline
```

**Linux:** `make dev-init` auto-selects local mode — no VM needed.

## Adding drift monitoring to an existing playbook

```yaml
# last task in your provisioning play
- name: Update fiia drift manifest
  fiia.fleet.manifest:
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

See [`ansible/collections/fiia/fleet/roles/agent/README.md`](ansible/collections/fiia/fleet/roles/agent/README.md) for collection install and full variable reference.

## Docs

| | |
|-|-|
| [docs/development.md](docs/development.md) | Full dev workflow, Makefile reference, configuration |
| [docs/architecture.md](docs/architecture.md) | Component design, data flows, dependency rules |
| [docs/protocol.md](docs/protocol.md) | Wire format, HMAC authentication, schema versioning |
| [docs/step-ca-setup.md](docs/step-ca-setup.md) | Production TLS CA setup |
| [DECISIONS.md](DECISIONS.md) | Design decisions with rationale |
