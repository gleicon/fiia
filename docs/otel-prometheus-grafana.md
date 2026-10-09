# Fiia OTel, Prometheus, Grafana

The live-observability test shipped with the repo (`make e2e-live`) and the
configuration you can copy to build your own monitoring environment for real
Fiia agents.

## How it fits

The agent exports drift verdicts and liveness over OTLP/HTTP. The collector
turns them into Prometheus metrics. Prometheus stores them and evaluates alert
rules. Grafana renders a dashboard and fires its own alerts. Everything runs in
Docker (Compose) with nothing installed on your host.

```
fiia-agent -> otel-collector (prometheus exporter :9464) -> Prometheus (:9090)
     |                              |                            |
     |                              + git debug + file (telemetry.json)
     |                                                          + alert rules
     + heartbeats + drift metrics                                 -> Grafana (:3000) dashboard + alerts
                                                                            -> webhook (run-webhook.sh)
```

## Run it

```sh
make e2e-live          # start, drive clean -> drift -> restored, leave the stack running
make e2e-live-down     # stop and remove the stack
bash e2e/observe/run-webhook.sh   # optional: print alert notifications as they fire
```

| Service | URL | Credentials |
|---------|-----|-------------|
| Grafana | http://localhost:3000 | `admin` / `admin` |
| Prometheus | http://localhost:9090 | |
| Collector | http://localhost:9464/metrics | |
| OTLP receiver | http://localhost:4318 | |

## What you see

Dashboard *Fiia, drift detection*: a drift status stat, an alive stat,
timeseries for drift/checks/alive, and two tables, *Firing alerts* and *Drifted
nodes* (each drifted node plus its deviations).

Alerts in Prometheus (http://localhost:9090/alerts) and Grafana Alerting
(http://localhost:3000/alerting):

- `FiiaDriftDetected`: `fiia_drift_status == 1` for 15s
- `FiiaCheckError`: `fiia_drift_status == 2` for 15s
- `FiiaNodeSilent`: `absent(fiia_alive)` for 30s

When an alert fires, Grafana POSTs the alert payload to the webhook in
`contactpoints.yml`. Run the local listener to print it.

## Configuration

### `e2e/otelcol.yaml`

OTLP/HTTP receiver on `:4318`, a `prometheus` exporter on `:9464` (what
Prometheus scrapes; `add_metric_suffixes: false` keeps names like
`fiia_drift_status`), and `debug` + `file` exporters for
`e2e/out/telemetry.json`. Pipelines: `metrics` to `[debug, file, prometheus]`,
`logs` to `[debug, file]`.

### `e2e/observe/prometheus.yml` + `rules.yml`

Scrapes `otelcol:9464` every 5s, evaluates rules every 10s. The alert rules are
in `rules.yml`.

### `e2e/observe/grafana/provisioning/`

Loaded by Grafana at startup; restart Grafana to re-read.

- `datasources/datasources.yml`: a Prometheus datasource with `uid: prometheus`
  (rules and dashboard reference it by UID).
- `dashboards/dashboards.yml` + `fiia.json`: a folder provider that auto-loads
  the dashboard.
- `alerting/alertrules.yml`: Grafana Unified Alerting rules mirroring the
  Prometheus rules. Each rule is query, reduce (last), threshold (gt 0). All
  three use `noDataState: OK`, because an empty query result is normal (the
  drift query returns nothing when clean, `absent(fiia_alive)` returns nothing
  while the agent is alive). The group `interval` must divide evenly by the
  alert scheduler tick (10s).
- `alerting/contactpoints.yml`: a `webhook` contact point, by default
  `http://host.docker.internal:9999/fiia-alert`. Change the URL to
  Slack/Teams/email in production.
- `alerting/policies.yml`: routes every alert to that contact point.

### `e2e/observe/run-webhook.sh`

A tiny HTTP receiver that prints Grafana alert notifications and appends them
to `/tmp/fiia-webhook.log`.

## Where the deviations live

`fiia.drift.status` is a number (0/1/2). The deviations arrive two ways:

- the `fiia.drift.details` metric, a gauge emitted while drifting with the
  deviation list as the `deviations` label (queryable, shown in the *Drifted
  nodes* panel);
- the log record per check (`fiia.status`, `fiia.deviations[]`,
  `fiia.manifest_generated_at`) through the logs pipeline to the `debug` and
  `file` exporters.

## Build your own environment

1. Ship the collector with `otelcol.yaml`. Point your agents at it with
   `OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector>:4318`, or `otlp_endpoint` in
   `agent.toml`.
2. Ship Prometheus with `prometheus.yml`. Change the scrape target from
   `otelcol:9464` to your collector's host.
3. Ship Grafana with the `provisioning/` tree. Edits for a real environment:
   - `datasources.yml`: `url: http://prometheus:9090` to your Prometheus.
   - `contactpoints.yml`: replace the webhook URL with your notification
     target. Add SMTP to `grafana.ini` for email.
   - `dashboards/fiia.json`: labels use `host_name` (the agent's `host.name`).
4. Secure it: enable Grafana auth and change the admin password, put Prometheus
   behind auth or TLS, and use OTLP headers/TLS on the agent if the collector
   requires them.

## Notes

- Ports in use while the stack runs: `4318` (OTLP), `9464` (collector metrics),
  `9090` (Prometheus), `3000` (Grafana), `9999` (optional webhook). `make e2e`
  and `make e2e-live` conflict on 4318/9464; stop one first.
- Prometheus and Grafana alert annotations render `{{ $labels.host_name }}` to
  the host value. The raw template is the source; the Alerts pages and
  notifications show the rendered text.