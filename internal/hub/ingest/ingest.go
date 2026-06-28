package ingest

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

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
)

// Listener accepts TLS connections from agents, validates HMAC signatures,
// and routes payloads to the registry or store.
type Listener struct {
	tls_cfg       *tls.Config
	reg           *registry.Registry
	store         store.Store
	drift_counter *atomic.Int64   // may be nil
	cmdq          *command.Queue  // may be nil; delivers hub→agent commands on heartbeat ACK
}

func assert(condition bool, message string) {
	if !condition {
		panic("hub/ingest: assertion failed: " + message)
	}
}

// New creates a Listener. drift_counter and cmdq may be nil.
func New(tls_cfg *tls.Config, reg *registry.Registry, s store.Store, drift_counter *atomic.Int64, cmdq *command.Queue) *Listener {
	assert(tls_cfg != nil, "tls_cfg must not be nil")
	assert(reg != nil, "registry must not be nil")
	assert(s != nil, "store must not be nil")

	return &Listener{
		tls_cfg:       tls_cfg,
		reg:           reg,
		store:         s,
		drift_counter: drift_counter,
		cmdq:          cmdq,
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
// Returns when ln.Accept() fails (e.g. listener closed).
func (l *Listener) ServeListener(ln net.Listener) error {
	assert(ln != nil, "ln must not be nil")
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
		if cmd_str, ok := l.cmdq.Pop(node_id); ok {
			frame, err := wire.BuildCommandFrame(wire.CommandPayload{Command: cmd_str})
			if err != nil {
				log.Printf("ingest: build command frame for %q: %v", node_id, err)
			} else {
				if _, err := conn.Write(frame); err != nil {
					log.Printf("ingest: write command %q to %q: %v", cmd_str, node_id, err)
				} else {
					log.Printf("ingest: delivered command %q to node %s", cmd_str, node_id)
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
// Order is fixed: split → peek node_id → get secret → verify → peek type → route.
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

	secret_bytes, err := l.store.GetNodeSecret(node_id)
	if err != nil {
		log.Printf("ingest: unknown node %q from %s: %v", node_id, remote_addr, err)
		return "", false
	}

	if !wire.Verify(secret_bytes, payload_bytes, sig_bytes) {
		log.Printf("SECURITY: ingest: HMAC mismatch for node %q from %s — dropping payload", node_id, remote_addr)
		if err := l.store.SetAlert(node_id, "HMAC_MISMATCH", time.Now().Unix()); err != nil {
			log.Printf("ingest: set HMAC_MISMATCH alert for %q: %v", node_id, err)
		}
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

// routeDrift decodes a drift payload and persists it to the store.
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
		}
	case "OK":
		if err := l.store.ClearAlert(p.NodeID, "DRIFT_DETECTED"); err != nil {
			log.Printf("ingest: clear DRIFT_DETECTED alert for %q: %v", p.NodeID, err)
		}
	}
}

// routeHeartbeat decodes and processes a heartbeat payload.
func (l *Listener) routeHeartbeat(payload_bytes []byte, node_id string) {
	assert(len(payload_bytes) > 0, "payload_bytes must not be empty")
	assert(node_id != "", "node_id must not be empty")

	p, err := wire.DecodeHeartbeat(payload_bytes)
	if err != nil {
		log.Printf("ingest: decode heartbeat for node %q: %v", node_id, err)
		return
	}
	assert(p.NodeID == node_id, "decoded node_id must match peeked node_id")

	l.reg.Update(p.NodeID, p.TimestampUnix, p.Metrics)
	for _, alert_type := range []string{"AGENT_UNREACHABLE", "AGENT_PAUSED"} {
		if err := l.store.ClearAlert(p.NodeID, alert_type); err != nil {
			log.Printf("ingest: clear %s alert node=%s: %v", alert_type, p.NodeID, err)
		}
	}
	log.Printf("ingest: heartbeat node=%s cpu=%.1f%% mem=%.1f%% disk=%.1f%%",
		p.NodeID, p.Metrics.CPUUtilPct, p.Metrics.MemUtilPct, p.Metrics.DiskUtilPct)
}
