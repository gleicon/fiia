package transport

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/agent/queue"
	"github.com/gleicon/fiia/internal/wire"
)

const ack_read_timeout = 2 * time.Second

// Transport manages TLS connections from agent to hub.
// Each send opens a new TLS connection and closes it on completion.
type Transport struct {
	cfg             *agentcfg.AgentConfig
	tls_cfg         *tls.Config
	q               *queue.Queue
	connect_timeout time.Duration
}

func assert(condition bool, message string) {
	if !condition {
		panic("agent/transport: assertion failed: " + message)
	}
}

// New loads the root CA from disk and builds the TLS client config.
// Opens the disk queue at cfg.QueueDir (non-empty, defaults set by config.Load).
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

	var q *queue.Queue
	if cfg.QueueDir != "" {
		q, err = queue.Open(cfg.QueueDir)
		if err != nil {
			return nil, fmt.Errorf("open queue at %q: %w", cfg.QueueDir, err)
		}
	}

	assert(tls_cfg.MinVersion == tls.VersionTLS13, "TLS 1.3 must be enforced")
	return &Transport{
		cfg:             cfg,
		tls_cfg:         tls_cfg,
		q:               q,
		connect_timeout: connect_timeout_duration,
	}, nil
}

// SendHeartbeat signs and transmits a heartbeat payload via the queue.
// Writes frame to queue first, then drains all queued entries.
// Returns (false, nil) on failure; (true, cmd) when hub delivered a command.
func (t *Transport) SendHeartbeat(p wire.HeartbeatPayload) (bool, *wire.CommandPayload) {
	assert(t.tls_cfg != nil, "tls_cfg must not be nil")
	assert(p.NodeID != "", "heartbeat node_id must not be empty")

	payload_bytes, err := wire.EncodeHeartbeat(p)
	if err != nil {
		log.Printf("transport: encode heartbeat: %v", err)
		return false, nil
	}
	frame := wire.BuildFrame(t.cfg.HMACSecret, payload_bytes)

	if t.q != nil {
		if err := t.q.Write(frame); err != nil {
			log.Printf("transport: queue heartbeat: %v — falling back to direct send", err)
		} else {
			ok, cmd := t.drainQueue()
			return ok, cmd
		}
	}

	if err := t.sendFrame(frame); err != nil {
		log.Printf("transport: send heartbeat: %v", err)
		return false, nil
	}
	return true, nil
}

// SendAuditResult signs and queues a drift payload for delivery on the next heartbeat drain.
// Falls back to immediate direct send if the queue is unavailable.
func (t *Transport) SendAuditResult(p wire.DriftPayload) {
	assert(t.tls_cfg != nil, "tls_cfg must not be nil")
	assert(p.NodeID != "", "drift node_id must not be empty")

	payload_bytes, err := wire.EncodeDrift(p)
	if err != nil {
		log.Printf("transport: encode drift: %v", err)
		return
	}
	frame := wire.BuildFrame(t.cfg.HMACSecret, payload_bytes)

	if t.q != nil {
		if queue_err := t.q.Write(frame); queue_err == nil {
			return // queued; heartbeat drain will deliver it
		}
		log.Printf("transport: queue audit: %v", err)
	}

	if err := t.sendFrame(frame); err != nil {
		log.Printf("transport: send audit result: %v", err)
	}
}

// drainQueue sends all queued frames in FIFO order, advancing the queue on
// each successful send. Returns the first command received from any heartbeat ACK.
func (t *Transport) drainQueue() (bool, *wire.CommandPayload) {
	assert(t.q != nil, "drainQueue called without queue")

	var received_cmd *wire.CommandPayload

	for t.q.Len() > 0 {
		frame, ok, err := t.q.Peek()
		if err != nil || !ok {
			log.Printf("transport: peek queue: %v", err)
			return false, nil
		}
		assert(len(frame) > 0, "peeked frame must not be empty")

		ptype, err := peekFrameType(frame)
		if err != nil {
			log.Printf("transport: corrupt queued frame, skipping: %v", err)
			if aerr := t.q.Advance(); aerr != nil {
				log.Printf("transport: advance past corrupt frame: %v", aerr)
				return false, nil
			}
			continue
		}

		if ptype == wire.PayloadTypeHeartbeat {
			acked, cmd, serr := t.sendFrameExpectAck(frame)
			if serr != nil {
				log.Printf("transport: send queued heartbeat: %v", serr)
				return false, nil
			}
			if !acked {
				log.Printf("transport: heartbeat sent but no ACK — retrying next cycle")
				return false, nil
			}
			if cmd != nil && received_cmd == nil {
				received_cmd = cmd
			}
		} else {
			if serr := t.sendFrame(frame); serr != nil {
				log.Printf("transport: send queued frame (type=%d): %v", ptype, serr)
				return false, nil
			}
		}

		if aerr := t.q.Advance(); aerr != nil {
			log.Printf("transport: advance queue: %v", aerr)
			return false, nil
		}
	}
	return true, received_cmd
}

func (t *Transport) openConn() (net.Conn, error) {
	assert(t.cfg.HubAddr != "", "hub_addr must not be empty")

	dialer := &net.Dialer{Timeout: t.connect_timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", t.cfg.HubAddr, t.tls_cfg)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", t.cfg.HubAddr, err)
	}
	return conn, nil
}

func (t *Transport) writeFrame(conn net.Conn, frame []byte) error {
	assert(len(frame) > wire.FrameHeaderSize+wire.HMACSize, "frame must be longer than header + HMAC")

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

func (t *Transport) sendFrame(frame []byte) error {
	conn, err := t.openConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	return t.writeFrame(conn, frame)
}

// sendFrameExpectAck sends frame and reads one optional response.
// Returns (true, cmd, nil) on success, (false, nil, nil) on timeout/no-ACK, (false, nil, err) on send error.
func (t *Transport) sendFrameExpectAck(frame []byte) (bool, *wire.CommandPayload, error) {
	conn, err := t.openConn()
	if err != nil {
		return false, nil, err
	}
	defer conn.Close()

	if err := t.writeFrame(conn, frame); err != nil {
		return false, nil, err
	}

	acked, cmd := readResponse(conn)
	assert(conn != nil, "conn must not be nil after response read")
	return acked, cmd, nil
}

// readResponse reads a hub→agent response frame.
// Returns (true, nil) for a simple ACK, (true, cmd) for a command, (false, nil) on error/timeout.
func readResponse(conn net.Conn) (bool, *wire.CommandPayload) {
	assert(conn != nil, "conn must not be nil")

	if err := conn.SetReadDeadline(time.Now().Add(ack_read_timeout)); err != nil {
		return false, nil
	}

	var header [wire.FrameHeaderSize]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return false, nil
	}

	body_len := binary.BigEndian.Uint32(header[:])
	if body_len == 0 || body_len > 1<<16 {
		return false, nil
	}

	body := make([]byte, body_len)
	if _, err := io.ReadFull(conn, body); err != nil {
		return false, nil
	}

	// Simple ACK: body is exactly one byte = PayloadTypeAck
	if body_len == 1 && body[0] == wire.PayloadTypeAck {
		return true, nil
	}

	// Attempt to decode as CommandPayload
	var cmd wire.CommandPayload
	if err := msgpack.Unmarshal(body, &cmd); err != nil {
		return true, nil // unrecognized but hub did respond — treat as ACK
	}
	if cmd.PayloadType == wire.PayloadTypeCommand && cmd.Command != "" {
		return true, &cmd
	}
	return true, nil
}

// peekFrameType extracts the payload type from a complete wire frame.
func peekFrameType(frame []byte) (uint8, error) {
	assert(len(frame) > 0, "frame must not be empty")

	if len(frame) <= wire.FrameHeaderSize+wire.HMACSize {
		return 0, fmt.Errorf("frame too short to peek type: %d bytes", len(frame))
	}
	body := frame[wire.FrameHeaderSize:]
	payload, _, err := wire.SplitFrame(body)
	if err != nil {
		return 0, fmt.Errorf("split frame: %w", err)
	}
	return wire.PeekPayloadType(payload)
}
