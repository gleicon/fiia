# Fiia — Fleet Configuration Audit Agent

Fiia is a purpose-built, two-binary system for continuous configuration drift detection and liveness telemetry on bare-metal and hypervisor fleets. It is designed to run passively at scale (5,000+ nodes) without interfering with workloads.

## The problem

Large fleets drift. A node gets patched manually, a sysctl gets tweaked in-band, a template gets rolled back by a human. Config management systems push state, but they don't continuously verify it. The question "is this node running what it should be running?" has no reliable answer at any given moment. Monitoring systems tell you the node is alive; they don't tell you it's correct.

## How Fiia solves it

Two binaries, one job each:

- **fiia-agent** — runs as a locked-down systemd unit on every node. Sends a 5-minute heartbeat with live USE-method metrics, and runs `ansible-playbook --check --diff` on a locally cached declarative baseline every 20 minutes. Reports drift status to the hub. Never modifies anything.

- **fiia-hub** — receives, authenticates, and stores all reports. Validates per-node HMAC-SHA256 before decoding any payload. Maintains a heartbeat registry, detects nodes that stopped reporting, flags nodes present in inventory but never seen, and exposes Prometheus metrics and a REST API.

Fiia is **read-only and passive**. Zero auto-remediation.

## What Fiia is not

- Not a configuration management system (Ansible, Puppet, Chef, Salt). Those push state; Fiia only audits it.
- Not a general monitoring agent (Prometheus node_exporter, Datadog, Telegraf). Those collect metrics; Fiia's USE telemetry is diagnostic context for drift events, not a replacement for your monitoring stack.
- Not a SIEM or intrusion detection system. Fiia detects divergence from a known-good baseline; it does not inspect runtime behavior, syscall patterns, or file integrity beyond what Ansible check-mode can observe.
- Not a distributed tracing or log aggregation tool. Fiia produces structured JSON alerts and Prometheus metrics; ship those to your existing observability stack.
- Not a host-inventory system. Fiia consumes inventory (CSV or NetBox); it does not replace it.

## Why not existing tools?

| Tool | Why not |
|------|---------|
| Prometheus Alertmanager | No concept of config baseline or drift state; alerts on metrics, not configuration correctness |
| Puppet/Chef continuous runs | Active remediation — modifies the node; Fiia is read-only |
| Ansible `--check` cron job | No central reporting, no liveness tracking, no fleet visibility, no alert persistence |
| Osquery | Binary and query model complexity; no Ansible-native baseline integration |
| Tripwire / AIDE | File integrity monitoring only; no service/sysctl/package state; no Prometheus integration |

Fiia's differentiator: it uses the same playbooks you already have for provisioning — no translation layer, no separate DSL, no new agent language to maintain.

## Quick start

See **[docs/development.md](docs/development.md)** for prerequisites, local dev loop, and Makefile reference.

## Docs

| Document | Contents |
|----------|----------|
| [docs/architecture.md](docs/architecture.md) | Component design, data flows, dependency rules, upgrade paths |
| [docs/protocol.md](docs/protocol.md) | Wire format, payload types, authentication, schema versioning |
| [docs/development.md](docs/development.md) | Build, test, local dev, configuration reference, production deployment |
| [docs/step-ca-setup.md](docs/step-ca-setup.md) | Production TLS CA setup with step-ca |
| [SPEC.md](SPEC.md) | Functional and non-functional requirements, acceptance criteria |
| [DECISIONS.md](DECISIONS.md) | Design decisions with rationale |
