//go:build !linux

package telemetry

import "github.com/gleicon/fiia/internal/wire"

// Collector samples USE metrics from the host OS.
// On non-Linux platforms returns zero values.
type Collector struct{}

// NewCollector creates a Collector. Call Collect() to sample metrics.
func NewCollector() *Collector { return &Collector{} }

// Collect returns zero-valued metrics on non-Linux platforms.
func (c *Collector) Collect() wire.USEMetrics { return wire.USEMetrics{} }
