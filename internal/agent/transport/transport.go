package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/wire"
)

// pendingFrame holds a single encoded frame awaiting delivery on hub reconnect.
// Only audit results are queued; heartbeats are dropped on failure.
type pendingFrame struct {
	data []byte
}

// Transport manages TLS connections from agent to hub.
// Each send opens a new TLS connection and closes it on completion.
type Transport struct {
	cfg          *agentcfg.AgentConfig
	tls_cfg      *tls.Config
	// pending holds at most one audit result to retry on the next heartbeat.
	// Overwrites any previous pending frame — no accumulation.
	pending      atomic.Pointer[pendingFrame]
	connect_timeout time.Duration
}

func assert(condition bool, message string) {
	if !condition {
		panic("agent/transport: assertion failed: " + message)
	}
}

// New loads the root CA from disk and builds the TLS client config.
func New(cfg *agentcfg.AgentConfig) (*Transport, error) {
	assert(cfg != nil, "cfg must not be nil")
	assert(cfg.CACertPath != "", "ca_cert_path must not be empty")
	assert(len(cfg.HMACSecret) > 0, "hmac_secret must not be empty")

	ca_pem, err := os.ReadFile(cfg.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert %q: %w", cfg.CACertPath, err)
	}
	assert(len(ca_pem) > 0, "CA cert PEM must not be empty")

	cert_pool := x509.NewCertPool()
	if !cert_pool.AppendCertsFromPEM(ca_pem) {
		return nil, fmt.Errorf("parse CA cert from %q: no valid PEM blocks found", cfg.CACertPath)
	}

	tls_cfg := &tls.Config{
		RootCAs:    cert_pool,
		MinVersion: tls.VersionTLS13,
	}

	connect_timeout_duration := time.Duration(cfg.ConnectTimeoutSec) * time.Second
	if connect_timeout_duration <= 0 {
		connect_timeout_duration = 10 * time.Second
	}

	return &Transport{
		cfg:             cfg,
		tls_cfg:         tls_cfg,
		connect_timeout: connect_timeout_duration,
	}, nil
}

// SendHeartbeat signs and transmits a heartbeat payload.
// Before sending the heartbeat, attempts to flush any pending audit result.
// Returns false on failure — caller uses this to drive backoff.
func (t *Transport) SendHeartbeat(p wire.HeartbeatPayload) bool {
	assert(t.tls_cfg != nil, "tls_cfg must not be nil")
	assert(p.NodeID != "", "heartbeat node_id must not be empty")

	t.flushPending()

	payload_bytes, err := wire.EncodeHeartbeat(p)
	if err != nil {
		log.Printf("transport: encode heartbeat: %v", err)
		return false
	}

	frame := wire.BuildFrame(t.cfg.HMACSecret, payload_bytes)
	if err := t.sendFrame(frame); err != nil {
		log.Printf("transport: send heartbeat: %v", err)
		return false
	}
	return true
}

// SendAuditResult signs and transmits a drift payload.
// On failure, stores the frame in the pending slot for retry on next heartbeat.
func (t *Transport) SendAuditResult(p wire.DriftPayload) {
	assert(t.tls_cfg != nil, "tls_cfg must not be nil")
	assert(p.NodeID != "", "drift node_id must not be empty")

	payload_bytes, err := wire.EncodeDrift(p)
	if err != nil {
		log.Printf("transport: encode drift: %v", err)
		return
	}

	frame := wire.BuildFrame(t.cfg.HMACSecret, payload_bytes)
	if err := t.sendFrame(frame); err != nil {
		log.Printf("transport: send audit result: %v (storing as pending)", err)
		t.pending.Store(&pendingFrame{data: frame})
	}
}

// flushPending attempts to deliver a pending audit result if one exists.
// Clears the slot on success; leaves it for the next attempt on failure.
func (t *Transport) flushPending() {
	pf := t.pending.Load()
	if pf == nil {
		return
	}
	assert(len(pf.data) > 0, "pending frame data must not be empty")

	if err := t.sendFrame(pf.data); err != nil {
		log.Printf("transport: flush pending: %v", err)
		return
	}
	t.pending.Store(nil)
}

func (t *Transport) sendFrame(frame []byte) error {
	assert(len(frame) > wire.FrameHeaderSize+wire.HMACSize, "frame must be longer than header + HMAC")
	assert(t.cfg.HubAddr != "", "hub_addr must not be empty")

	dialer := &net.Dialer{Timeout: t.connect_timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", t.cfg.HubAddr, t.tls_cfg)
	if err != nil {
		return fmt.Errorf("dial %q: %w", t.cfg.HubAddr, err)
	}
	defer conn.Close()

	write_deadline := time.Now().Add(t.connect_timeout)
	if err := conn.SetWriteDeadline(write_deadline); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}

	n_written, err := conn.Write(frame)
	if err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	if n_written != len(frame) {
		return fmt.Errorf("short write: wrote %d of %d bytes", n_written, len(frame))
	}
	return nil
}
