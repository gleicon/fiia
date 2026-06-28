# Fiia — Fleet Configuration Audit Agent

Two-binary fleet telemetry system for bare-metal and hypervisor fleets.

- **fiia-agent** — runs as a systemd daemon on each edge node. Sends periodic heartbeats with USE-method host metrics and runs `ansible-playbook --check --diff` to detect configuration drift.
- **fiia-hub** — central server. Validates per-node HMAC signatures, maintains a heartbeat registry, exposes Prometheus metrics and a REST API.

Read-only and passive — no automated remediation.

---

## Prerequisites

| Tool | Purpose |
|------|---------|
| Go 1.24+ | Build both binaries |
| Colima | Linux VM for local agent testing |
| Ansible | Installed **on the VM** by the bootstrap playbook — not required on the Mac |
| `pip3 install ansible` | Required **on the Mac** only to run the bootstrap playbook itself |

macOS ansible install (if missing or broken):
```sh
pip3 install ansible
# or: brew install ansible
```

---

## Quick start — local dev

### 1. Start the Colima VM

```sh
make dev-vm-start
```

Starts a 2 CPU / 2 GB Ubuntu VM. Run once; survives reboots with `colima start`.

### 2. Start the hub (macOS)

```sh
make dev-hub
```

Generates dev TLS certs (`dev/ca/`), seeds the `colima-dev` node secret in SQLite, then starts the hub on:

| Port | Service |
|------|---------|
| `:9443` | TLS ingest (agents connect here) |
| `:9091` | REST API |
| `:9090` | Prometheus metrics |

Leave this running in a terminal.

### 3. Deploy the agent into the VM

```sh
make dev-deploy
```

Cross-compiles a Linux binary for the VM's arch (queried live from Colima), generates the SSH inventory, then runs the Ansible bootstrap playbook against the VM. Certs are **not** regenerated — run `make dev-certs-force` if you need to rotate them. The playbook:

- Installs `ansible` on the VM via apt (avoids PEP 668 on Ubuntu 22.04+)
- Creates the `fiia` system user (no login shell, no sudo)
- Deploys TLS root CA, agent config (`0400`, `fiia`-owned), systemd unit, logrotate
- Writes `/var/lib/fiia/ansible.cfg` (disables fact gathering; sets tmp paths)
- Deploys `dev/baseline.yml` to `/etc/fiia/baseline.yml`
- Enables and starts `fiia-agent`
- Polls the hub API to confirm the node registered

### 4. Verify

```sh
make dev-run          # systemctl status fiia-agent
make dev-logs         # tail -f /var/log/fiia/agent.log
make dev-drift-log    # tail -f /var/log/fiia/drift.log (ansible output)
make dev-journal      # journalctl -f (systemd unit events)
```

Check the hub:
```sh
curl http://localhost:9091/nodes
curl http://localhost:9091/nodes/colima-dev/status
curl http://localhost:9090/metrics | grep fiia_nodes_alive
```

---

## Drift detection test

The dev baseline (`dev/baseline.yml`) manages two files on the VM:
- `/etc/fiia/sentinel` — a file with known content
- `/etc/motd` — a login banner

### Introduce drift

```sh
make dev-drift
```

Corrupts both managed files on the VM. The agent detects the change on its next audit cycle (~120 seconds in dev config).

### Check for drift events

```sh
make dev-check-drift
```

Queries three hub endpoints:
- `GET /nodes/colima-dev/status` — current node state
- `GET /nodes/colima-dev/drift` — last 50 drift events with `TasksChanged` list
- `GET /alerts` — all active alerts

### Restore baseline state

```sh
make dev-restore
```

Resets both files to their managed content. The next audit cycle reports `Status: OK`.

---

## Makefile reference

```
make build            build both binaries (host OS)
make test             run all tests

make dev-vm-start     start Colima Ubuntu VM
make dev-vm-stop      stop Colima VM
make dev-hub          generate certs + seed node secret + start hub (macOS)
make dev-build        cross-compile agent for VM arch (queried live from Colima)
make dev-certs        generate dev/ca/ TLS certs (no-op if already present)
make dev-certs-force  force-rotate dev/ca/ TLS certs (redeploy hub + all agents after)
make dev-inventory    generate Ansible SSH inventory from colima ssh-config
make dev-deploy       build + inventory + run Ansible bootstrap playbook (certs NOT rotated)
make dev-run          show agent systemctl status in VM
make dev-logs         follow agent log file (/var/log/fiia/agent.log)
make dev-drift-log    follow drift log file (/var/log/fiia/drift.log)
make dev-journal      follow systemd journal for fiia-agent unit
make dev-watch        stream agent.log + drift.log side by side (labeled by source)
make dev-stop         stop agent in VM
make dev-drift        corrupt managed files to trigger drift detection
make dev-restore      restore managed files to baseline state
make dev-check-drift  query hub API for node status, drift events, alerts
```

Arch is detected automatically: `uname -m` → `arm64` (Apple Silicon) or `amd64` (Intel).

---

## Hub REST API

All endpoints return JSON.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/nodes` | All nodes and last-seen timestamps |
| `GET` | `/nodes/{id}/status` | Single node status |
| `GET` | `/nodes/{id}/drift` | Last 50 drift events for a node |
| `GET` | `/alerts` | All active alerts (HMAC_MISMATCH, AGENT_PAUSED, AGENT_UNREACHABLE, UNINSTRUMENTED_SERVER) |

The API has no authentication — bind it to localhost in production (`api_addr = "127.0.0.1:9091"` in `hub.toml`).

---

## Status codes

### Drift event status (`GET /nodes/{id}/drift` → `.Status`)

| Status | Meaning |
|--------|---------|
| `OK` | Baseline matches node state; no tasks changed |
| `DRIFT_DETECTED` | One or more tasks would change; see `TasksChanged` (detected from PLAY RECAP `changed>0` or exit 2) |
| `AUDIT_TIMEOUT` | Playbook did not complete within `audit_timeout_sec` |
| `AUDIT_ERROR` | ansible-playbook failed to start (binary missing, permission error) |
| `AUDIT_RESOURCE_EXCEEDED` | ansible-playbook killed by OOM (exit 137 / SIGKILL) |
| `AUDIT_EXIT_N` | ansible-playbook exited with code N — see table below |

**`AUDIT_EXIT_N` ansible exit codes:**

| N | Meaning |
|---|---------|
| 1 | Task or module error |
| 3 | One or more hosts unreachable |
| 4 | Playbook syntax/parser error |
| 5 | Bad or incomplete options (e.g. missing inventory) |
| 99 | Interrupted (SIGINT/SIGTERM during playbook run) |

### Alert types (`GET /alerts` → `.AlertType`)

| Alert | Meaning | Cleared when |
|-------|---------|--------------|
| `DRIFT_DETECTED` | One or more baseline tasks would change on this node | Next audit reports `OK` |
| `HMAC_MISMATCH` | Frame received with invalid HMAC — possible tampering or wrong secret | Manual |
| `AGENT_PAUSED` | Node silent for one heartbeat window (>`paused_threshold_sec`) | Next heartbeat received |
| `AGENT_UNREACHABLE` | Node silent for two+ heartbeat windows (>`unreachable_threshold_sec`) | Next heartbeat received |
| `UNINSTRUMENTED_SERVER` | Node present in inventory CSV but never reported a heartbeat | First heartbeat received |

---

## Prometheus metrics

Scraped at `http://<hub>:9090/metrics`.

| Metric | Description |
|--------|-------------|
| `fiia_nodes_alive_total` | Nodes with a heartbeat in the last 10 minutes |
| `fiia_nodes_total` | Total nodes known to the hub |
| `fiia_drift_events_total` | Drift events received since hub start |
| `fiia_nodes_paused` | Nodes missing one heartbeat window |
| `fiia_nodes_unreachable` | Nodes missing two+ heartbeat windows |
| `fiia_nodes_uninstrumented` | Nodes in inventory CSV but never reported |
| `fiia_node_cpu_util_pct{node_id}` | Per-node CPU utilization % |
| `fiia_node_mem_util_pct{node_id}` | Per-node memory utilization % |
| `fiia_node_disk_util_pct{node_id}` | Per-node disk utilization % |
| `fiia_node_net_util_bps{node_id}` | Per-node network bytes/sec |

---

## Configuration

### Agent — `/etc/fiia/agent.toml` (root:root, 0400)

```toml
[agent]
node_id             = "hostname"
hub_addr            = "hub.example.com:9443"
hmac_secret_hex     = "<32-byte hex>"     # per-node unique, generated by bootstrap
ca_cert_path        = "/etc/fiia/root_ca.pem"
heartbeat_interval_sec = 300
ansible_playbook_path  = "/etc/fiia/baseline.yml"  # omit to disable audit
drift_log_path         = "/var/log/fiia/drift.log"
audit_interval_sec     = 1200
audit_jitter_max_sec   = 120
audit_timeout_sec      = 600
```

### Hub — `/etc/fiia/hub.toml`

```toml
[hub]
listen_addr   = ":9443"          # TLS ingest
cert_path     = "/etc/fiia/hub_cert.pem"
key_path      = "/etc/fiia/hub_key.pem"
db_path       = "/var/lib/fiia/hub.db"
metrics_addr  = "127.0.0.1:9090"
api_addr      = "127.0.0.1:9091"
# inventory_csv_path    = "/etc/fiia/inventory.csv"  # omit to disable reconciler
# reconcile_interval_sec = 3600
```

---

## Production deployment

See `docs/step-ca-setup.md` for CA and certificate setup.

```sh
# deploy to 5% of fleet (serial batching)
ansible-playbook \
  -i inventory/production.ini \
  deploy/ansible/bootstrap.yml \
  -e @secrets/fleet-vars.yml    # contains fiia_hub_hmac_secret_hex per host
```

The bootstrap playbook:
1. Installs `ansible-core` on the target node
2. Creates `fiia` system user (no login shell, no sudo)
3. Deploys root CA, agent config (0400), binary, systemd unit, logrotate
4. Enables and starts the service
5. Polls `GET /nodes/{hostname}/status` to confirm registration before releasing the deployment lock

---

## Architecture

```
[edge node]                          [hub]
fiia-agent
  ├── heartbeat (5 min; backs off 5→10→20→40s on hub failure)  →  TLS:9443 ingest
  │     + USE metrics (/proc)        ├── HMAC verify (before decode)
  ├── audit loop (20 min ± jitter)   ├── registry (in-memory + SQLite)
  │     ansible-playbook --check     ├── expiry goroutine (AGENT_PAUSED/UNREACHABLE)
  │     → DriftPayload         →     └── drift_events table
  └── sd_notify watchdog (25s)
                                   HTTP:9091 REST API
                                   HTTP:9090 Prometheus /metrics
```

Wire protocol: `[4-byte length][msgpack payload][32-byte HMAC-SHA256]` over TLS 1.3. HMAC is verified **before** MessagePack decode.

Packages:
- `internal/wire` — shared encode/decode/sign between agent and hub (no other shared imports)
- `internal/hub/store` — `Store` interface; SQLite now, Postgres upgrade path
- `internal/hub/inventory` — `InventoryReader` interface; CSV now, NetBox upgrade path
