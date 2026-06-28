package heartbeat

import (
	"context"
	"log"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/agent/sdnotify"
	"github.com/gleicon/fiia/internal/agent/telemetry"
	"github.com/gleicon/fiia/internal/agent/transport"
	"github.com/gleicon/fiia/internal/wire"
)

const (
	// watchdog_interval_sec: sd_notify WATCHDOG=1 sent at this interval.
	// Must be less than half of WatchdogSec in the systemd unit (WatchdogSec=120).
	watchdog_interval_sec = 25

	// backoff_base_sec: first retry interval when hub is unreachable.
	backoff_base_sec = 5
	// backoff_steps_max: number of doublings before capping at normal interval.
	// 5s → 10s → 20s → 40s → normal_interval
	backoff_steps_max = 4
)

func assert(condition bool, message string) {
	if !condition {
		panic("agent/heartbeat: assertion failed: " + message)
	}
}

// nextInterval returns the next heartbeat delay based on consecutive failure count.
// On failure the interval doubles from backoff_base_sec up to normal_interval.
// On success it returns normal_interval immediately.
func nextInterval(normal time.Duration, consecutive_fails int) time.Duration {
	assert(normal > 0, "normal interval must be positive")
	assert(consecutive_fails >= 0, "consecutive_fails must not be negative")

	if consecutive_fails == 0 {
		return normal
	}
	steps := consecutive_fails - 1
	if steps > backoff_steps_max {
		steps = backoff_steps_max
	}
	delay := time.Duration(backoff_base_sec<<steps) * time.Second
	if delay > normal {
		delay = normal
	}
	return delay
}

// Run starts the heartbeat loop. Blocks until ctx is cancelled.
// Uses adaptive intervals: normal on success, exponential backoff on hub failure.
// Watchdog ticks are independent of hub reachability.
func Run(ctx context.Context, cfg *agentcfg.AgentConfig, tr *transport.Transport) {
	assert(ctx != nil, "ctx must not be nil")
	assert(cfg != nil, "cfg must not be nil")
	assert(tr != nil, "transport must not be nil")
	assert(cfg.HeartbeatIntervalSec > 0, "heartbeat_interval_sec must be positive")

	normal_interval := time.Duration(cfg.HeartbeatIntervalSec) * time.Second

	watchdog_ticker := time.NewTicker(watchdog_interval_sec * time.Second)
	defer watchdog_ticker.Stop()

	// Fire the first heartbeat immediately, then use adaptive timer.
	heartbeat_timer := time.NewTimer(0)
	defer heartbeat_timer.Stop()

	collector := telemetry.NewCollector()
	consecutive_fails := 0
	log.Printf("heartbeat: started (interval=%s)", normal_interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("heartbeat: stopping")
			return
		case <-watchdog_ticker.C:
			sdnotify.Notify("WATCHDOG=1")
		case <-heartbeat_timer.C:
			ok := sendHeartbeat(cfg, tr, collector)
			if ok {
				if consecutive_fails > 0 {
					log.Printf("heartbeat: hub reachable again after %d failed attempt(s)", consecutive_fails)
				}
				consecutive_fails = 0
			} else {
				consecutive_fails++
			}
			next := nextInterval(normal_interval, consecutive_fails)
			if consecutive_fails > 0 {
				log.Printf("heartbeat: hub unreachable (attempt %d), retrying in %s", consecutive_fails, next)
			}
			heartbeat_timer.Reset(next)
		}
	}
}

// sendHeartbeat builds and transmits a single heartbeat payload with current USE metrics.
// Returns true on success, false on any send failure.
func sendHeartbeat(cfg *agentcfg.AgentConfig, tr *transport.Transport, collector *telemetry.Collector) bool {
	assert(cfg != nil, "cfg must not be nil")
	assert(tr != nil, "transport must not be nil")
	assert(cfg.NodeID != "", "node_id must not be empty")

	p := wire.HeartbeatPayload{
		NodeID:        cfg.NodeID,
		TimestampUnix: time.Now().Unix(),
		Status:        "OK",
		Metrics:       collector.Collect(),
	}
	assert(p.TimestampUnix > 0, "heartbeat timestamp must be positive")

	return tr.SendHeartbeat(p)
}

