# Fiia — Architecture

One binary (`fiia-agent`) plus the `fiia.fleet` ansible collection. No
server side: verdicts and liveness go to your OpenTelemetry pipeline,
where all alerting happens. The agent binary is the single manifest
generator; Ansible invokes `-write-manifest` as the last task.

```
provision (ansible, plain SSH)              operate (OTel backend)
──────────────────────────────              ─────────────────────
fiia-agent -write-manifest (last task)
  → /etc/fiia/manifest.json          fiia-agent (systemd unit)
     │                                 ├─ heartbeat loop → fiia.alive
     │                                 ├─ audit loop → fiia.drift.status
     │                                 ├─   + fiia.drift.details (deviations)
     │                                 ├─   + remediate (opt-in)
     └─ promise ───────────────────→  └─ check log record (fiia.deviations)
                                              │  ┌ stdout (default)
                                              ├──┤
                                              │  └ OTLP/HTTP (endpoint set)
                                              ▼
                                     collector → Grafana / Alertmanager
                                       ├─ absent fiia.alive → node silent
                                       └─ fiia.drift.status == 1 → drift
```

## Components

| Package | Responsibility |
|---------|----------------|
| `cmd/agent` | Flags, OTel setup, SIGTERM, systemd notify |
| `internal/agent/config` | `agent.toml` (only `manifest_path` required) |
| `internal/agent/audit` | Manifest read / check / generate / scan / remediate; no subprocess |
| `internal/agent/otel` | OTel providers; gauges, counter, per-check log records |
| `internal/agent/sdnotify` | Inline sd_notify, no CGO |
| `internal/assert` | Shared precondition helper |
| `fiia.fleet` collection | `agent` role (provision-time) — invokes the agent binary |

## Rules

- `cmd/agent` imports `internal/agent/*` only
- `audit` never touches OTel; only `otel` imports the SDK
- Manifest modes: `declared` (listed items) / `snapshot` (+ full pkg/svc lists)
- Verdicts: `OK` (0) / `DRIFT_DETECTED` (1) / `MANIFEST_NOT_FOUND` or
  `CHECK_ERROR` (2); stale manifest (>90d) warns without changing the verdict
- No new external dependencies without written justification (decisions live
  in git history)

## Limits (systemd unit)

CPU 10%, memory 256M, IO 512K, `Nice=19`, watchdog 120s.
