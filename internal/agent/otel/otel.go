package otel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gleicon/fiia/internal/agent/audit"
	"github.com/gleicon/fiia/internal/assert"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
)

const (
	metricExportInterval = 30 * time.Second
	maxDeviationLabelLen = 800
)

// Handle emits drift verdicts and liveness into the operator's OTel pipeline.
// All alerting happens downstream (collector, Grafana, Alertmanager):
//   - absence of fiia.alive → node silent (dead-man alert in the backend)
//   - fiia.drift.status == 1 → drift detected
//   - fiia.drift.status == 2 → check error (manifest missing/unreadable)
type Handle struct {
	nodeID  string
	alive   metric.Int64Gauge
	status  metric.Int64Gauge
	details metric.Int64Gauge
	checks  metric.Int64Counter
	logger  log.Logger
}

// endpointHost strips scheme and path from a base endpoint URL
// ("http://otelcol:4318" → "otelcol:4318") for WithEndpoint.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	return u.Host
}

// newMetricExporter builds the metric exporter: OTLP/HTTP for a real endpoint,
// stdout otherwise. Only the exporter varies between the two modes; the
// provider construction is shared (see Setup).
func newMetricExporter(ctx context.Context, endpoint string) (sdkmetric.Exporter, error) {
	if endpoint == "" {
		exp, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("otel stdout metric exporter: %w", err)
		}
		return exp, nil
	}
	// NOTE: otlploghttp/otlpmetrichttp WithEndpointURL would replace the
	// default "/v1/<signal>" path with the URL's (empty) path and 404
	// against the collector. Pass host and path separately instead.
	exp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpointHost(endpoint)),
		otlpmetrichttp.WithURLPath("/v1/metrics"))
	if err != nil {
		return nil, fmt.Errorf("otel OTLP metric exporter: %w", err)
	}
	return exp, nil
}

// newLogExporter builds the log exporter: OTLP/HTTP for a real endpoint,
// stdout otherwise.
func newLogExporter(ctx context.Context, endpoint string) (sdklog.Exporter, error) {
	if endpoint == "" {
		exp, err := stdoutlog.New(stdoutlog.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("otel stdout log exporter: %w", err)
		}
		return exp, nil
	}
	exp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(endpointHost(endpoint)),
		otlploghttp.WithURLPath("/v1/logs"))
	if err != nil {
		return nil, fmt.Errorf("otel OTLP log exporter: %w", err)
	}
	return exp, nil
}

// driftStatus maps a check status to the fiia.drift.status gauge value.
func driftStatus(status string) int64 {
	switch status {
	case audit.StatusOK:
		return 0
	case audit.StatusDriftDetected:
		return 1
	default:
		return 2
	}
}

// Setup builds meter + logger providers for nodeID.
// An empty endpoint selects human-readable stdout export (no collector needed);
// otherwise OTLP/HTTP is used (e.g. http://collector:4318, or the
// OTEL_EXPORTER_OTLP_ENDPOINT env convention passed through by the caller).
// The returned shutdown flushes pending telemetry and must be called on exit.
func Setup(ctx context.Context, endpoint, nodeID string) (*Handle, func(context.Context) error, error) {
	assert.True(nodeID != "", "nodeID must not be empty")

	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			attribute.String("service.name", "fiia-agent"),
			attribute.String("host.name", nodeID),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("otel resource: %w", err)
	}

	metricExp, err := newMetricExporter(ctx, endpoint)
	if err != nil {
		return nil, nil, err
	}
	logExp, err := newLogExporter(ctx, endpoint)
	if err != nil {
		return nil, nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(metricExportInterval))),
	)
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)

	meter := meterProvider.Meter("fiia-agent")
	alive, err := meter.Int64Gauge("fiia.alive",
		metric.WithDescription("1 when the agent heartbeat was emitted; alert on absence"))
	if err != nil {
		return nil, nil, fmt.Errorf("otel fiia.alive gauge: %w", err)
	}
	status, err := meter.Int64Gauge("fiia.drift.status",
		metric.WithDescription("0 clean, 1 drift detected, 2 check error"))
	if err != nil {
		return nil, nil, fmt.Errorf("otel fiia.drift.status gauge: %w", err)
	}
	details, err := meter.Int64Gauge("fiia.drift.details",
		metric.WithDescription("deviation count while drifting; carries the deviation list as the 'deviations' label"))
	if err != nil {
		return nil, nil, fmt.Errorf("otel fiia.drift.details gauge: %w", err)
	}
	checks, err := meter.Int64Counter("fiia.checks.total",
		metric.WithDescription("manifest checks completed"))
	if err != nil {
		return nil, nil, fmt.Errorf("otel fiia.checks.total counter: %w", err)
	}

	shutdown := func(ctx context.Context) error {
		return errors.Join(
			meterProvider.Shutdown(ctx),
			loggerProvider.Shutdown(ctx),
		)
	}

	return &Handle{
		nodeID:  nodeID,
		alive:   alive,
		status:  status,
		details: details,
		checks:  checks,
		logger:  loggerProvider.Logger("fiia-agent"),
	}, shutdown, nil
}

// EmitAlive records one heartbeat. Backends alert on a missing fiia.alive series.
func (h *Handle) EmitAlive(ctx context.Context) {
	h.alive.Record(ctx, 1, metric.WithAttributes(attribute.String("host.name", h.nodeID)))
}

// EmitCheck records one manifest verification: gauges, counter, and a log
// record carrying the deviations for inspection.
func (h *Handle) EmitCheck(ctx context.Context, result audit.CheckResult) {
	// The status gauge value already encodes the verdict (0/1/2); do NOT add a
	// per-status label or Prometheus gets one series per status value. The
	// label set must stay constant across emits so fiia_drift_status is a
	// single series.
	statusAttrs := []attribute.KeyValue{attribute.String("host.name", h.nodeID)}
	h.status.Record(ctx, driftStatus(result.Status), metric.WithAttributes(statusAttrs...))
	h.checks.Add(ctx, 1, metric.WithAttributes(attribute.String("host.name", h.nodeID)))

	// Fleet-visible detail: emit a gauge carrying the deviations as labels ONLY
	// while there is something to report (clean nodes emit nothing). This lets a
	// central Prometheus/Grafana show exactly which nodes drifted and on what,
	// without logging into each server. The label is capped to stay within
	// Prometheus's value limits.
	if len(result.Deviations) > 0 {
		h.details.Record(ctx, int64(len(result.Deviations)), metric.WithAttributes(
			attribute.String("host.name", h.nodeID),
			attribute.String("status", result.Status),
			attribute.String("deviations", joinDeviations(result.Deviations)),
		))
	}

	devs := result.Deviations
	if devs == nil {
		devs = []string{}
	}
	rec := log.Record{}
	rec.SetTimestamp(result.Timestamp())
	rec.SetSeverity(severityFor(result.Status))
	rec.SetBody(attribute.StringValue(checkBody(result)))
	rec.AddAttributes(
		attribute.String("host.name", h.nodeID),
		attribute.String("fiia.status", result.Status),
		attribute.StringSlice("fiia.deviations", devs),
		attribute.Int64("fiia.manifest_generated_at", result.ManifestGeneratedAt),
	)
	h.logger.Emit(ctx, rec)
}

func severityFor(status string) log.Severity {
	switch status {
	case audit.StatusOK:
		return log.SeverityInfo
	case audit.StatusDriftDetected:
		return log.SeverityWarn
	default:
		return log.SeverityError
	}
}

func checkBody(result audit.CheckResult) string {
	switch result.Status {
	case audit.StatusOK:
		return "drift check clean"
	case audit.StatusDriftDetected:
		return fmt.Sprintf("drift detected: %d deviation(s)", len(result.Deviations))
	default:
		return "drift check error: " + result.Status
	}
}

// joinDeviations renders the deviation list as a single label value, capped to
// keep the series within Prometheus label size limits.
func joinDeviations(devs []string) string {
	s := strings.Join(devs, ", ")
	if len(s) > maxDeviationLabelLen {
		s = s[:maxDeviationLabelLen] + "…"
	}
	return s
}
