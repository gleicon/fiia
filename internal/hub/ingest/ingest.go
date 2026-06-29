package ingest

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/gleicon/fiia/internal/hub/command"
	"github.com/gleicon/fiia/internal/hub/registry"
	"github.com/gleicon/fiia/internal/hub/store"
	"github.com/gleicon/fiia/internal/wire"
)

const (
	// read_timeout_sec: maximum time to read a single frame from a client.
	read_timeout_sec = 30
	// ack_write_timeout_sec: time allowed to write an ACK frame back to the agent.
	ack_write_timeout_sec = 5
	// frame_size_max_bytes: reject frames larger than 1 MB to prevent memory exhaustion.
	frame_size_max_bytes = 1 << 20 // 1 MB

	// write_ch_cap: depth of the async write channel.
	write_ch_cap = 1024
	// write_batch_size: flush the write queue when this many ops are pending.
	write_batch_size = 512
	// write_flush_interval: maximum time between batch flushes.
	write_flush_interval = 100 * time.Millisecond
)

// dbWriteKind identifies the type of a pending async write op.
type dbWriteKind uint8

const (
	opUpdateHeartbeat dbWriteKind = iota + 1
	opClearAlert
)

// dbWriteOp is a single pending write, queued by the ingest hot path.
// Only high-frequency, idempotent writes are async; drift and security events
// are written synchronously before the connection is released.
type dbWriteOp struct {
	kind       dbWriteKind
	node_id    string
	timestamp  int64  // opUpdateHeartbeat
	alert_type string // opClearAlert
}

// alertKey deduplicates ClearAlert ops within a flush window.
type alertKey struct {
	node_id    string
	alert_type string
}

// Listener accepts TLS connections from agents, validates HMAC signatures,
// and routes payloads to the registry or store.
type Listener struct {
	tls_cfg       *tls.Config
	reg           *registry.Registry
	store         store.Store
	drift_counter *atomic.Int64
	cmdq          *command.Queue
	rate_rps      float64
	rate_burst    int
	limiters      sync.Map // node_id → *rate.Limiter
	write_ch      chan dbWriteOp
	dummy_secret  [32]byte // random; replaces real secret for unknown nodes to prevent timing oracle
	webhookURL    string   // optional; if set, alert set/clear fires an async HTTP POST
}

func assert(condition bool, message string) {
	if !condition {
		panic("hub/ingest: assertion failed: " + message)
	}
}

// New creates a Listener. drift_counter and cmdq may be nil.
// rate_rps and rate_burst configure per-node token-bucket rate limiting.
// The async write goroutine is started by ServeListener and flushed on return;
// callers must not share a Listener across multiple ServeListener calls.
func New(tls_cfg *tls.Config, reg *registry.Registry, s store.Store, drift_counter *atomic.Int64, cmdq *command.Queue, rate_rps float64, rate_burst int) *Listener {
	assert(tls_cfg != nil, "tls_cfg must not be nil")
	assert(reg != nil, "registry must not be nil")
	assert(s != nil, "store must not be nil")
	assert(rate_rps > 0, "rate_rps must be positive")
	assert(rate_burst > 0, "rate_burst must be positive")

	var dummy [32]byte
	if _, err := rand.Read(dummy[:]); err != nil {
		panic("hub/ingest: generate dummy secret: " + err.Error())
	}

	return &Listener{
		tls_cfg:       tls_cfg,
		reg:           reg,
		store:         s,
		drift_counter: drift_counter,
		cmdq:          cmdq,
		rate_rps:      rate_rps,
		rate_burst:    rate_burst,
		write_ch:      make(chan dbWriteOp, write_ch_cap),
		dummy_secret:  dummy,
	}
}

// WithWebhook enables async HTTP POST on every alert set/clear.
// The payload is JSON: {"node_id","alert_type","action","timestamp"}.
// Pass "" to disable (default).
func (l *Listener) WithWebhook(url string) *Listener {
	l.webhookURL = url
	return l
}

// fireWebhook posts an alert event to the configured webhook URL in a goroutine.
// action is "set" or "clear". No-op when webhookURL is empty.
func (l *Listener) fireWebhook(node_id, alert_type, action string, ts int64) {
	if l.webhookURL == "" {
		return
	}
	url := l.webhookURL
	go func() {
		body, _ := json.Marshal(map[string]any{
			"node_id":    node_id,
			"alert_type": alert_type,
			"action":     action,
			"timestamp":  ts,
		})
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("ingest: webhook POST: %v", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("ingest: webhook returned %d for node=%s alert=%s", resp.StatusCode, node_id, alert_type)
		}
	}()
}

// getLimiter returns the rate.Limiter for node_id, creating one on first access.
func (l *Listener) getLimiter(node_id string) *rate.Limiter {
	assert(node_id != "", "node_id must not be empty")

	v, _ := l.limiters.LoadOrStore(node_id,
		rate.NewLimiter(rate.Limit(l.rate_rps), l.rate_burst))
	return v.(*rate.Limiter)
}

// enqueueWrite pushes op onto the async write channel.
// Non-blocking: drops with a log line if the channel is full.
func (l *Listener) enqueueWrite(op dbWriteOp) {
	assert(op.node_id != "", "op.node_id must not be empty")

	select {
	case l.write_ch <- op:
	default:
		log.Printf("ingest: write queue full (cap=%d), dropping op %d for node %q", write_ch_cap, op.kind, op.node_id)
	}
}

// flushBatch writes a batch of ops to the store, deduplicating within the batch.
// For UpdateHeartbeat: only the latest timestamp per node is written.
// For ClearAlert: each (node_id, alert_type) pair is written once.
func (l *Listener) flushBatch(ops []dbWriteOp) {
	assert(len(ops) > 0, "ops must not be empty")

	heartbeats := make(map[string]int64)  // node_id → latest timestamp
	clears := make(map[alertKey]struct{}) // unique alert clears

	for _, op := range ops {
		switch op.kind {
		case opUpdateHeartbeat:
			if op.timestamp > heartbeats[op.node_id] {
				heartbeats[op.node_id] = op.timestamp
			}
		case opClearAlert:
			clears[alertKey{op.node_id, op.alert_type}] = struct{}{}
		}
	}

	for node_id, ts := range heartbeats {
		if err := l.store.UpdateHeartbeat(node_id, ts); err != nil {
			log.Printf("ingest: batch update heartbeat for %q: %v", node_id, err)
		}
	}
	for k := range clears {
		if err := l.store.ClearAlert(k.node_id, k.alert_type); err != nil {
			log.Printf("ingest: batch clear alert %q for %q: %v", k.alert_type, k.node_id, err)
		}
	}
}

// runWriter drains write_ch in batches on a timer. Flushes remaining ops when
// done_ch is closed. Intended to run in a dedicated goroutine.
func (l *Listener) runWriter(done_ch <-chan struct{}) {
	assert(done_ch != nil, "done_ch must not be nil")

	ticker := time.NewTicker(write_flush_interval)
	defer ticker.Stop()

	pending := make([]dbWriteOp, 0, write_batch_size)

	flush := func() {
		if len(pending) == 0 {
			return
		}
		l.flushBatch(pending)
		pending = pending[:0]
	}

	for {
		select {
		case op := <-l.write_ch:
			pending = append(pending, op)
			if len(pending) >= write_batch_size {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-done_ch:
		drain:
			for {
				select {
				case op := <-l.write_ch:
					pending = append(pending, op)
				default:
					break drain
				}
			}
			flush()
			return
		}
	}
}

// Serve creates a TLS listener on addr and calls ServeListener.
func (l *Listener) Serve(addr string) error {
	assert(addr != "", "addr must not be empty")

	ln, err := tls.Listen("tcp", addr, l.tls_cfg)
	if err != nil {
		return fmt.Errorf("tls listen on %q: %w", addr, err)
	}
	log.Printf("ingest: listening on %s", addr)
	return l.ServeListener(ln)
}

// ServeListener accepts connections from ln and handles each in a new goroutine.
// Starts the async write goroutine; blocks until it has fully flushed before returning.
func (l *Listener) ServeListener(ln net.Listener) error {
	assert(ln != nil, "ln must not be nil")

	done_ch := make(chan struct{})
	var writer_done sync.WaitGroup
	writer_done.Add(1)
	go func() {
		defer writer_done.Done()
		l.runWriter(done_ch)
	}()
	// Defer order (LIFO): ln.Close → close(done_ch) → writer_done.Wait.
	// Writer is guaranteed to have flushed before ServeListener returns.
	defer writer_done.Wait()
	defer func() { close(done_ch) }()
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go l.handleConn(conn)
	}
}

// handleConn reads a single frame from conn, validates it, routes it, and sends
// a response (command or ACK) back to the agent for heartbeat payloads.
func (l *Listener) handleConn(conn net.Conn) {
	assert(conn != nil, "conn must not be nil")
	defer conn.Close()

	deadline := time.Now().Add(read_timeout_sec * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		log.Printf("ingest: set read deadline: %v", err)
		return
	}

	body_bytes, err := readFrame(conn)
	if err != nil {
		log.Printf("ingest: read frame from %s: %v", conn.RemoteAddr(), err)
		return
	}

	node_id, respond := l.processFrame(body_bytes, conn.RemoteAddr().String())
	if respond {
		l.sendHeartbeatResponse(conn, node_id)
	}
}

// sendHeartbeatResponse sends a command frame if one is pending for node_id,
// otherwise sends a PayloadTypeAck. Errors are logged and ignored — the payload
// was already stored before this call.
func (l *Listener) sendHeartbeatResponse(conn net.Conn, node_id string) {
	assert(conn != nil, "conn must not be nil")
	assert(node_id != "", "node_id must not be empty")

	if err := conn.SetWriteDeadline(time.Now().Add(ack_write_timeout_sec * time.Second)); err != nil {
		log.Printf("ingest: set response write deadline: %v", err)
		return
	}

	if l.cmdq != nil {
		if entry, ok := l.cmdq.Pop(node_id); ok {
			frame, err := wire.BuildCommandFrame(wire.CommandPayload{
				Command:      entry.Command,
				PlaybookPath: entry.PlaybookPath,
				IntervalSec:  entry.IntervalSec,
			})
			if err != nil {
				log.Printf("ingest: build command frame for %q: %v", node_id, err)
			} else {
				if _, err := conn.Write(frame); err != nil {
					log.Printf("ingest: write command %q to %q: %v", entry.Command, node_id, err)
				} else {
					log.Printf("ingest: delivered command %q to node %s", entry.Command, node_id)
				}
				return
			}
		}
	}

	if _, err := conn.Write(wire.BuildAckFrame()); err != nil {
		log.Printf("ingest: write ACK to %q: %v", node_id, err)
	}
}

// readFrame reads the 4-byte length prefix then the body.
// Returns only the body bytes (after the header).
func readFrame(conn net.Conn) ([]byte, error) {
	assert(conn != nil, "conn must not be nil")

	var header [wire.FrameHeaderSize]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	body_len_bytes := binary.BigEndian.Uint32(header[:])
	if body_len_bytes == 0 {
		return nil, fmt.Errorf("frame body length is zero")
	}
	if uint64(body_len_bytes) > frame_size_max_bytes {
		return nil, fmt.Errorf("frame body length %d exceeds maximum %d", body_len_bytes, frame_size_max_bytes)
	}
	if body_len_bytes <= wire.HMACSize {
		return nil, fmt.Errorf("frame body length %d too short (minimum %d)", body_len_bytes, wire.HMACSize+1)
	}

	body := make([]byte, body_len_bytes)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, fmt.Errorf("read body (%d bytes): %w", body_len_bytes, err)
	}
	return body, nil
}

// processFrame validates HMAC and routes the payload. All security checks occur here.
// Order is fixed: split → peek node_id → get secret → verify → rate check → peek type → route.
// Returns (node_id, true) when a heartbeat response (command or ACK) should be sent.
func (l *Listener) processFrame(body_bytes []byte, remote_addr string) (string, bool) {
	assert(len(body_bytes) > wire.HMACSize, "body_bytes must be longer than HMACSize")

	payload_bytes, sig_bytes, err := wire.SplitFrame(body_bytes)
	if err != nil {
		log.Printf("ingest: split frame from %s: %v", remote_addr, err)
		return "", false
	}

	node_id, err := wire.PeekNodeID(payload_bytes)
	if err != nil {
		log.Printf("ingest: peek node_id from %s: %v", remote_addr, err)
		return "", false
	}

	secret_bytes, secret_err := l.store.GetNodeSecret(node_id)
	known := secret_err == nil
	if !known {
		// Use dummy secret so Verify always runs, preventing timing oracle on node existence.
		secret_bytes = l.dummy_secret[:]
	}

	if !wire.Verify(secret_bytes, payload_bytes, sig_bytes) {
		if known {
			log.Printf("SECURITY: ingest: HMAC mismatch for node %q from %s — dropping payload", node_id, remote_addr)
			// Security alert is written synchronously — never batched.
			now := time.Now().Unix()
			if err := l.store.SetAlert(node_id, "HMAC_MISMATCH", now); err != nil {
				log.Printf("ingest: set HMAC_MISMATCH alert for %q: %v", node_id, err)
			} else {
				l.fireWebhook(node_id, "HMAC_MISMATCH", "set", now)
			}
		} else {
			log.Printf("ingest: unknown node %q from %s", node_id, remote_addr)
		}
		return "", false
	}

	if !l.getLimiter(node_id).Allow() {
		log.Printf("ingest: rate limit exceeded for node %q from %s — dropping payload", node_id, remote_addr)
		return "", false
	}

	payload_type, err := wire.PeekPayloadType(payload_bytes)
	if err != nil {
		log.Printf("ingest: peek payload_type for node %q: %v", node_id, err)
		return "", false
	}

	switch payload_type {
	case wire.PayloadTypeHeartbeat:
		l.routeHeartbeat(payload_bytes, node_id)
		return node_id, true
	case wire.PayloadTypeDrift:
		l.routeDrift(payload_bytes, node_id)
		return "", false
	default:
		log.Printf("ingest: unknown payload_type %d for node %q", payload_type, node_id)
		return "", false
	}
}

// routeDrift decodes a drift payload and persists it to the store synchronously.
// Drift events are infrequent event records — written immediately, not batched.
func (l *Listener) routeDrift(payload_bytes []byte, node_id string) {
	assert(len(payload_bytes) > 0, "payload_bytes must not be empty")
	assert(node_id != "", "node_id must not be empty")

	p, err := wire.DecodeDrift(payload_bytes)
	if err != nil {
		log.Printf("ingest: decode drift for node %q: %v", node_id, err)
		return
	}
	assert(p.NodeID == node_id, "decoded node_id must match peeked node_id")

	if err := l.store.AppendDrift(p.NodeID, p.TimestampUnix, p.Status, p.TasksChanged); err != nil {
		log.Printf("ingest: append drift for node %q: %v", node_id, err)
		return
	}
	if l.drift_counter != nil {
		l.drift_counter.Add(1)
	}
	log.Printf("ingest: drift node=%s status=%s changed=%v", p.NodeID, p.Status, p.TasksChanged)

	switch p.Status {
	case "DRIFT_DETECTED":
		if err := l.store.SetAlert(p.NodeID, "DRIFT_DETECTED", p.TimestampUnix); err != nil {
			log.Printf("ingest: set DRIFT_DETECTED alert for %q: %v", p.NodeID, err)
		} else {
			l.fireWebhook(p.NodeID, "DRIFT_DETECTED", "set", p.TimestampUnix)
		}
	case "OK":
		if err := l.store.ClearAlert(p.NodeID, "DRIFT_DETECTED"); err != nil {
			log.Printf("ingest: clear DRIFT_DETECTED alert for %q: %v", p.NodeID, err)
		} else {
			l.fireWebhook(p.NodeID, "DRIFT_DETECTED", "clear", p.TimestampUnix)
		}
	}

	// Raise MANIFEST_STALE when the manifest has not been regenerated within 90 days.
	// Hub owns time-based alerting; agent sends ManifestGeneratedAt on every drift report.
	if p.ManifestGeneratedAt > 0 {
		const manifest_stale_days = 90
		age_days := (time.Now().Unix() - p.ManifestGeneratedAt) / 86400
		if age_days > manifest_stale_days {
			if err := l.store.SetAlert(p.NodeID, "MANIFEST_STALE", p.TimestampUnix); err != nil {
				log.Printf("ingest: set MANIFEST_STALE for %q: %v", p.NodeID, err)
			} else {
				l.fireWebhook(p.NodeID, "MANIFEST_STALE", "set", p.TimestampUnix)
			}
			log.Printf("ingest: manifest stale node=%s age_days=%d", p.NodeID, age_days)
		} else {
			if err := l.store.ClearAlert(p.NodeID, "MANIFEST_STALE"); err != nil {
				log.Printf("ingest: clear MANIFEST_STALE for %q: %v", p.NodeID, err)
			} else {
				l.fireWebhook(p.NodeID, "MANIFEST_STALE", "clear", p.TimestampUnix)
			}
		}
	}
}

// routeHeartbeat decodes a heartbeat, updates the in-memory registry synchronously,
// then enqueues the DB writes (UpdateHeartbeat + alert clears) for async batch flush.
func (l *Listener) routeHeartbeat(payload_bytes []byte, node_id string) {
	assert(len(payload_bytes) > 0, "payload_bytes must not be empty")
	assert(node_id != "", "node_id must not be empty")

	p, err := wire.DecodeHeartbeat(payload_bytes)
	if err != nil {
		log.Printf("ingest: decode heartbeat for node %q: %v", node_id, err)
		return
	}
	assert(p.NodeID == node_id, "decoded node_id must match peeked node_id")

	// In-memory update is synchronous: metrics and liveness visible immediately.
	// DB write is async: batched by runWriter to keep this goroutine off the DB hot path.
	l.reg.Update(p.NodeID, p.TimestampUnix, p.Metrics)
	l.enqueueWrite(dbWriteOp{kind: opUpdateHeartbeat, node_id: p.NodeID, timestamp: p.TimestampUnix})
	for _, alert_type := range []string{"AGENT_UNREACHABLE", "AGENT_PAUSED"} {
		l.enqueueWrite(dbWriteOp{kind: opClearAlert, node_id: p.NodeID, alert_type: alert_type})
	}

	log.Printf("ingest: heartbeat node=%s cpu=%.1f%% mem=%.1f%% disk=%.1f%%",
		p.NodeID, p.Metrics.CPUUtilPct, p.Metrics.MemUtilPct, p.Metrics.DiskUtilPct)
}
