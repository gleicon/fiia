# Fiia — Development

## Prerequisites

| Tool | Install |
|------|---------|
| Go 1.24+ | [go.dev](https://go.dev/dl/) |
| Lima | `brew install lima` |
| Ansible | `brew install ansible` |
| Docker | Docker Desktop, OrbStack, or Colima |

Colima or Multipass also work as VM backends. On Linux no VM is needed.

## Getting started

```sh
make dev-init          # detect backend, create + start Lima VM (first time only)
make dev-hub           # terminal 1: start hub
make dev-deploy        # terminal 2: build agent, provision VM, start service
```

That's it. `dev-init` is a one-shot; skip it on subsequent sessions — just `make dev-vm-start` if the VM is stopped.

### Verify

```sh
make dev-run           # systemctl status fiia-agent in VM
curl http://localhost:9091/nodes
curl http://localhost:9091/nodes/<node-id>/status   # node-id: run dev/vm.sh node-id
curl http://localhost:9090/metrics | grep fiia_nodes_alive
```

### Test drift detection

```sh
make dev-drift         # corrupt managed files (sentinel + motd)
# wait ~120s for next audit cycle
make dev-check-drift   # query hub: node status, drift events, alerts
make dev-restore       # restore baseline
```

## Ansible collection test (no VM needed)

Tests the `fiia.fleet.manifest` module against a real Ubuntu container:

```sh
make dev-collection-test
```

Requires Docker. Installs `community.docker` ansible collection automatically if missing.

## Makefile reference

```
build                   build fiia-agent and fiia-hub for host OS
test                    run go test ./...

dev-init                first-time setup: detect backend, create VM, start VM
dev-setup               detect VM backend only (writes dev/.backend)
dev-vm-create           create the VM
dev-vm-start            start the VM
dev-vm-stop             stop the VM
dev-vm-status           print backend and VM status

dev-hub                 generate dev certs + seed node secret + start hub
dev-build               cross-compile agent for VM arch
dev-certs               generate dev/ca/ TLS certs (no-op if present)
dev-certs-force         force-rotate certs (restart hub + redeploy agents after)
dev-deploy              build + inventory + ansible bootstrap playbook
dev-collection-test     test fiia.fleet.manifest module via Docker

dev-run                 systemctl status fiia-agent in VM
dev-logs                tail /var/log/fiia/agent.log
dev-drift-log           tail /var/log/fiia/drift.log
dev-journal             journalctl -f for fiia-agent
dev-watch               agent.log + drift.log side-by-side
dev-stop                stop fiia-agent in VM

dev-drift               corrupt managed files (triggers drift detection)
dev-restore             restore managed files to baseline
dev-check-drift         query hub: node status, drift events, alerts
```

## REST API

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/nodes` | All nodes and last-seen timestamps |
| `GET` | `/nodes/{id}/status` | Single node status |
| `GET` | `/nodes/{id}/drift` | Last 50 drift events |
| `GET` | `/alerts` | All active alerts |
| `POST` | `/nodes/{id}/audit` | Trigger immediate audit |
| `POST` | `/nodes/{id}/config` | Push config update |

### Alert types

| Alert | Raised when | Cleared when |
|-------|-------------|--------------|
| `DRIFT_DETECTED` | Manifest check finds deviations | Next clean audit |
| `HMAC_MISMATCH` | Frame received with invalid HMAC | Manual |
| `AGENT_PAUSED` | Node silent > paused threshold | Next heartbeat |
| `AGENT_UNREACHABLE` | Node silent > unreachable threshold | Next heartbeat |
| `UNINSTRUMENTED_SERVER` | Node in inventory but never reported | First heartbeat |

## Prometheus metrics

Scraped at `:9090/metrics`.

| Metric | Description |
|--------|-------------|
| `fiia_nodes_alive_total` | Nodes heartbeating in last 10 min |
| `fiia_nodes_total` | Total nodes known to hub |
| `fiia_drift_events_total` | Drift events since hub start |
| `fiia_nodes_paused` | Nodes missing one heartbeat window |
| `fiia_nodes_unreachable` | Nodes missing two+ windows |
| `fiia_nodes_uninstrumented` | In inventory, never reported |
| `fiia_node_cpu_util_pct{node_id}` | Per-node CPU % |
| `fiia_node_mem_util_pct{node_id}` | Per-node memory % |
| `fiia_node_disk_util_pct{node_id}` | Per-node disk % |
| `fiia_node_net_util_bps{node_id}` | Per-node network bytes/sec |

## Configuration

### Agent — `/etc/fiia/agent.toml` (mode 0400)

```toml
[agent]
node_id               = "hostname"
hub_addr              = "hub.example.com:9443"
hmac_secret_hex       = "<32-byte hex>"
ca_cert_path          = "/etc/fiia/root_ca.pem"
heartbeat_interval_sec = 300
manifest_path         = "/etc/fiia/manifest.json"   # preferred
# ansible_playbook_path = "/etc/fiia/baseline.yml"  # legacy fallback
drift_log_path        = "/var/log/fiia/drift.log"
audit_interval_sec    = 1200
audit_jitter_max_sec  = 120
audit_timeout_sec     = 600
queue_dir             = "/var/lib/fiia/queue"
```

`manifest_path` takes precedence over `ansible_playbook_path`. Generate the manifest with `fiia.fleet.manifest` as the last task in your provisioning play.

### Hub — `/etc/fiia/hub.toml`

```toml
[hub]
listen_addr   = ":9443"
cert_path     = "/etc/fiia/hub_cert.pem"
key_path      = "/etc/fiia/hub_key.pem"

db_driver     = "sqlite"                    # "sqlite" | "postgres"
db_path       = "/var/lib/fiia/hub.db"
# db_dsn      = "postgres://fiia:pass@localhost:5432/fiia?sslmode=require"

metrics_addr  = "127.0.0.1:9090"
api_addr      = "127.0.0.1:9091"

# inventory_csv_path     = "/etc/fiia/inventory.csv"
# reconcile_interval_sec = 3600
```

## Production deployment

See [step-ca-setup.md](step-ca-setup.md) for TLS CA setup.

```sh
ansible-playbook \
  -i inventory/production.ini \
  deploy/ansible/bootstrap.yml \
  -e @secrets/fleet-vars.yml
```

## Security invariants

- Hub validates HMAC **before** decoding MessagePack — no exceptions
- `InsecureSkipVerify` is forbidden in all TLS configs
- Agent has no inbound connections and no write path to host config
- `agent.toml` is mode 0400, owned by the fiia system user
