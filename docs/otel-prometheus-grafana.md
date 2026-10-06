# Fiia — OTel → Prometheus → Grafana live observability

This documents the live-observability test shipped with the repo
(`make e2e-live`) and, more importantly, the **configuration you can copy to
build your own monitoring environment** for real Fiia agents.

## What it is

The agent exports drift verdicts and liveness over OTLP/HTTP. The collector
turns those into Prometheus metrics; Prometheus stores them, evaluates alert
rules, and Grafana renders a dashboard + fires its own alerts. The whole thing
runs in Docker (Compose) with nothing installed on your host.

```
fiia-agent ──OTLP/HTTP──▶ otel-collector ──prometheus exporter (:9464)──▶ Prometheus (:9090)
     │                        │                                                 │
     │                        └─ debug + file (e2e/out/telemetry.json)          ├─ alert rules
     │                                                                          │
     └─ heartbeats + drift status/metrics                                        └─▶ Grafana (:3000) dashboard + Unified Alerting
                                                                                          │
                                                                                          └─▶ webhook notification (e2e/observe/run-webhook.sh)
```

## Run it

```sh
make e2e-live          # start, drive clean → drift → restored, leave the stack running
make e2e-live-down     # stop and remove the stack
bash e2e/observe/run-webhook.sh   # optional: print alert notifications as they fire
```

| Service | URL | Credentials |
|---|---|---|
| Grafana | http://localhost:3000 | `admin` / `admin` |
| Prometheus | http://localhost:9090 | — |
| Collector | http://localhost:9464/metrics | — |
| OTLP receiver | http://localhost:4318 | — |

## What you'll see

- **Grafana dashboard** *"Fiia — drift detection"*: drift status stat (green
  clean / red DRIFT / orange error), alive stat, drift/checks/alive
  timeseries, plus two tables: **"Firing alerts (Prometheus)"** and
  **"Drifted nodes (host → deviations)"** — the latter shows every drifted node
  and its exact deviations, fleet-wide, without logging in.
- **Prometheus alerts** (http://localhost:9090/alerts) and **Grafana Alerting**
  (bell icon, http://localhost:3000/alerting):
  - `FiiaDriftDetected` — `fiia_drift_status == 1` for 15s → *Configuration
    drift detected on <host>*
  - `FiiaCheckError` — `fiia_drift_status == 2` for 15s → permission guidance
  - `FiiaNodeSilent` — `absent(fiia_alive)` for 30s → heartbeat missing
- **Webhook notification**: when an alert fires, Grafana POSTs the full alert
  payload to the configured webhook (see `contactpoints.yml`); run the local
  listener to print it.

## Configuration, file by file

### `e2e/otelcol.yaml` — the collector

- **OTLP/HTTP receiver** on `:4318` (agent export target).
- **`prometheus` exporter** on `:9464` — this is what Prometheus scrapes.
  `add_metric_suffixes: false` keeps metric names predictable:
  `fiia_drift_status`, `fiia_alive`, `fiia_checks_total`, and (while drifting)
  `fiia_drift_details` with the deviation list as a label. The status gauge
  carries only `host.name`, so each metric is a single series (see
  `internal/agent/otel`).
- `debug` + `file` exporters keep the assertions and raw records available
  (`e2e/out/telemetry.json`).

Pipelines: `metrics` → `[debug, file, prometheus]`, `logs` → `[debug, file]`.
The agent's deviations arrive **two ways**: as the `fiia.drift.details` metric
(deviations label, fleet-queryable) and as **log records** — see below.

### `e2e/observe/prometheus.yml` + `rules.yml`

- Scrapes `otelcol:9464` every 5s; evaluates rules every 10s.
- `rules.yml` holds the three alert rules. Note the `expr` uses `== 1` and
  `== 2` (not `> 0`) so a check error doesn't masquerade as a drift alert.

### `e2e/observe/grafana/provisioning/`

Provisioning is loaded by Grafana at startup (restart to re-read).

- **`datasources/datasources.yml`** — a Prometheus datasource with
  `uid: prometheus` (the alert rules and dashboard reference it by UID).
- **`dashboards/dashboards.yml`** + **`dashboards/fiia.json`** — a folder
  provider that auto-loads the dashboard (Firing alerts + Drifted nodes tables).
- **`alerting/alertrules.yml`** — Grafana Unified Alerting rules mirroring the
  Prometheus rules. Gotchas that bit us:
  - group `interval` **must divide evenly by the alert scheduler tick (10s)** —
    `15s` fails provisioning with *"interval should be non-zero and divided
    exactly by scheduler interval: 10"*. Use `10s`/`20s`/`30s`.
  - each rule is query → `reduce` (last) → `threshold` (gt 0), `condition: C`.
  - all three rules use `noDataState: OK`: an empty query result is *normal*
    (`fiia_drift_status == 1` returns nothing when clean; `absent(fiia_alive)`
    returns nothing while the agent is alive). Setting `NoData` here raises
    bogus `DatasourceNoData` alerts.
- **`alerting/contactpoints.yml`** — a `webhook` contact point, by default
  `http://host.docker.internal:9999/fiia-alert` (the local demo listener). For
  real notifications, change the URL to Slack/Teams/email gateway.
- **`alerting/policies.yml`** — route every alert to that contact point
  (`group_by: alertname`, 10s group wait).

### `e2e/observe/run-webhook.sh` + `webhook-listener.py`

A tiny HTTP receiver that prints any Grafana alert notification and appends it
to `/tmp/fiia-webhook.log` — the easiest way to *see* a notification land.

## Where the deviations live

`fiia.drift.status` is just a number (0/1/2). The **actual deviations**
(`file:hash_mismatch:/etc/fiia/sentinel`, `pkg:unauthorized:vim-tiny`,
`svc:inactive:...`, …) arrive **two ways**:
- as the **`fiia.drift.details`** metric — a gauge emitted while drifting with
  the deviation list as the `deviations` label (query `fiia_drift_details{…}`,
  shown in the *Drifted nodes* panel);
- as the **log record** per check (`fiia.status`, `fiia.deviations[]`,
  `fiia.manifest_generated_at`) through the OTLP logs pipeline → collector
  `debug`/`file` exporters.

Alert summaries tell you drift happened; the details metric and log record tell
you exactly what.

## Using this to build your own environment

1. **Ship the collector** with `otelcol.yaml` (adjust the OTLP receiver if
   needed). Point your real agents at it:
   `OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector>:4318` (or
   `otlp_endpoint` in `agent.toml`).
2. **Ship Prometheus** with `prometheus.yml`; change the scrape target from
   `otelcol:9464` to your collector's host. Keep the rules or adapt the
   expressions to your metric names.
3. **Ship Grafana** with the `provisioning/` tree. The only edits you need for
   a real environment:
   - `datasources.yml`: `url: http://prometheus:9090` → your Prometheus.
   - `contactpoints.yml`: replace the webhook URL with your notification
     target (Slack/Teams/webhook/email). Add SMTP to `grafana.ini` if using
     email.
   - `dashboards/fiia.json`: labels already use `host_name` (the agent's
     `host.name`); no change needed unless your agents use a different label.
4. **Secure it** (production): enable Grafana auth + change the admin
   password, put Prometheus behind auth/TLS or a reverse proxy, set
   `OTEL_EXPORTER_OTLP_*_HEADERS`/TLS on the agent if your collector requires
   auth.
5. **Permissions**: a standard install needs no OS changes — the `fiia.fleet`
   role chowns the manifest to the service user (still `0400`), and the agent
   alerts (`svc:unverifiable`, `CHECK_ERROR`/status 2) if it can't read the
   manifest or query systemd, with remediation guidance in the message.

## Alert messages

Prometheus and Grafana alert annotations render `{{ $labels.host_name }}` →
the host value (`e2e-node` in the test). The raw `{{ $labels ... }}` you see on
the *Rules* page is the template source; the *Alerts* page and notifications
show the rendered text.

## Notes & known quirks

- Ports in use while the stack runs: `4318` (OTLP), `9464` (collector metrics),
  `9090` (Prometheus), `3000` (Grafana), `9999` (optional webhook listener).
  `make e2e` and `make e2e-live` conflict on 4318/9464 — stop one first.
- Grafana re-reads provisioning at startup; after editing
  `provisioning/alerting`, `docker restart <grafana-container>`.
- If Grafana fails to start after provisioning edits, the error is in
  `docker logs <grafana>` (e.g. an alert-rule `interval` not divisible by 10s).
- Colima: keep at least 4 CPU / 6 GiB if running the systemd suite nearby
  (`colima start --cpu 4 --memory 6`).