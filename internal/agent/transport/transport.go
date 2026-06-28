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
// Returns false on failure — caller uses this to drive backoff.
func (t *Transport) SendHeartbeat(p wire.HeartbeatPayload) bool {
	assert(t.tls_cfg != nil, "tls_cfg must not be nil")
	assert(p.NodeID != "", "heartbeat node_id must not be empty")

	payload_bytes, err := wire.EncodeHeartbeat(p)
	if err != nil {
		log.Printf("transport: encode heartbeat: %v", err)
		return false
	}
	frame := wire.BuildFrame(t.cfg.HMACSecret, payload_bytes)

	if t.q != nil {
		if err := t.q.Write(frame); err != nil {
			log.Printf("transport: queue heartbeat: %v — falling back to direct send", err)
		} else {
			return t.drainQueue()
		}
	}

	if err := t.sendFrame(frame); err != nil {
		log.Printf("transport: send heartbeat: %v", err)
		return false
	}
	return true
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
// each successful send. Stops and returns false on the first failure.
func (t *Transport) drainQueue() bool {
	assert(t.q != nil, "drainQueue called without queue")

	for t.q.Len() > 0 {
		frame, ok, err := t.q.Peek()
		if err != nil || !ok {
			log.Printf("transport: peek queue: %v", err)
			return false
		}
		assert(len(frame) > 0, "peeked frame must not be empty")

		ptype, err := peekFrameType(frame)
		if err != nil {
			log.Printf("transport: corrupt queued frame, skipping: %v", err)
			if aerr := t.q.Advance(); aerr != nil {
				log.Printf("transport: advance past corrupt frame: %v", aerr)
				return false
			}
			continue
		}

		if ptype == wire.PayloadTypeHeartbeat {
			acked, serr := t.sendFrameExpectAck(frame)
			if serr != nil {
				log.Printf("transport: send queued heartbeat: %v", serr)
				return false
			}
			if !acked {
				log.Printf("transport: heartbeat sent but no ACK received — retrying next cycle")
				return false
			}
		} else {
			if serr := t.sendFrame(frame); serr != nil {
				log.Printf("transport: send queued frame (type=%d): %v", ptype, serr)
				return false
			}
		}

		if aerr := t.q.Advance(); aerr != nil {
			log.Printf("transport: advance queue: %v", aerr)
			return false
		}
	}
	return true
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

// sendFrameExpectAck sends frame and reads one optional ACK response.
// Returns (true, nil) on ACK received, (false, nil) on timeout/no-ACK, (false, err) on send error.
func (t *Transport) sendFrameExpectAck(frame []byte) (bool, error) {
	conn, err := t.openConn()
	if err != nil {
		return false, err
	}
	defer conn.Close()

	if err := t.writeFrame(conn, frame); err != nil {
		return false, err
	}

	acked := readAck(conn)
	assert(conn != nil, "conn must not be nil after ACK read")
	return acked, nil
}

// readAck reads a 5-byte ACK frame from conn with a short timeout.
// Returns true only if the frame is a valid PayloadTypeAck.
func readAck(conn net.Conn) bool {
	assert(conn != nil, "conn must not be nil")

	if err := conn.SetReadDeadline(time.Now().Add(ack_read_timeout)); err != nil {
		return false
	}
	var buf [wire.FrameHeaderSize + 1]byte
	if _, err := io.ReadFull(conn, buf[:]); err != nil {
		return false
	}
	body_len := binary.BigEndian.Uint32(buf[:wire.FrameHeaderSize])
	return body_len == 1 && buf[wire.FrameHeaderSize] == wire.PayloadTypeAck
}

// peekFrameType extracts the payload type from a complete wire frame.
// frame must include the 4-byte length header and trailing HMAC.
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
