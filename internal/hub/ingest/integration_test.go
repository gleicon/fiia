package ingest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/gleicon/fiia/internal/hub/ingest"
	"github.com/gleicon/fiia/internal/hub/inventory"
	"github.com/gleicon/fiia/internal/hub/metrics"
	"github.com/gleicon/fiia/internal/hub/registry"
	"github.com/gleicon/fiia/internal/hub/store"
	"github.com/gleicon/fiia/internal/wire"
)

// serveAndCleanup starts l.ServeListener(ln) in a goroutine and registers a
// t.Cleanup that closes ln and waits for ServeListener (and its writer goroutine)
// to fully exit. Register store cleanup BEFORE calling this so LIFO ordering
// ensures the listener stops before the store closes.
func serveAndCleanup(t *testing.T, l *ingest.Listener, ln net.Listener) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		l.ServeListener(ln) //nolint:errcheck
		close(done)
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
	})
}

// genTestTLS returns a server TLS config and a CA pool for the client.
// Uses a self-signed ECDSA cert valid for 127.0.0.1.
func genTestTLS(t *testing.T) (server_cfg *tls.Config, ca_pool *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fiia-test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	cert_der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	cert_pem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert_der})
	key_der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	key_pem := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key_der})

	tls_cert, err := tls.X509KeyPair(cert_pem, key_pem)
	if err != nil {
		t.Fatalf("x509 key pair: %v", err)
	}

	server_cfg = &tls.Config{
		Certificates: []tls.Certificate{tls_cert},
		MinVersion:   tls.VersionTLS13,
	}

	leaf, err := x509.ParseCertificate(cert_der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	ca_pool = x509.NewCertPool()
	ca_pool.AddCert(leaf)
	return server_cfg, ca_pool
}

// TestIntegrationHeartbeat is the Increment-1 smoke test:
// agent sends heartbeat → hub validates HMAC → registry updated → /metrics shows fiia_nodes_alive_total 1.
func TestIntegrationHeartbeat(t *testing.T) {
	server_tls, ca_pool := genTestTLS(t)

	// Store with temp database.
	db_path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	node_id := "test-node-1"
	secret := []byte("test-hmac-secret-32bytes-exactly")
	if err := s.SetNodeSecret(node_id, secret); err != nil {
		t.Fatalf("set node secret: %v", err)
	}

	reg := registry.New(s)

	// Start ingest listener on a random port.
	ingest_ln, err := tls.Listen("tcp", "127.0.0.1:0", server_tls)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	ingest_l := ingest.New(server_tls, reg, s, nil, nil, 100.0, 10)
	serveAndCleanup(t, ingest_l, ingest_ln)
	ingest_addr := ingest_ln.Addr().String()

	// Start metrics server on a random port with an isolated Prometheus registry.
	prom := prometheus.NewRegistry()
	metrics_srv := metrics.New(reg, s, nil, prom, prom)
	metrics_ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("metrics listen: %v", err)
	}
	t.Cleanup(func() { metrics_ln.Close() })
	go metrics_srv.ServeListener(metrics_ln) //nolint:errcheck
	metrics_url := "http://" + metrics_ln.Addr().String() + "/metrics"

	// Build a valid heartbeat frame.
	payload, err := wire.EncodeHeartbeat(wire.HeartbeatPayload{
		NodeID:        node_id,
		TimestampUnix: time.Now().Unix(),
		Status:        "OK",
	})
	if err != nil {
		t.Fatalf("encode heartbeat: %v", err)
	}
	frame := wire.BuildFrame(secret, payload)

	// Send frame over TLS.
	client_tls := &tls.Config{
		RootCAs:    ca_pool,
		MinVersion: tls.VersionTLS13,
	}
	conn, err := tls.Dial("tcp", ingest_addr, client_tls)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		conn.Close()
		t.Fatalf("write frame: %v", err)
	}
	conn.Close()

	// Poll until registry reflects the heartbeat (up to 2 s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reg.AliveCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reg.AliveCount() != 1 {
		t.Fatalf("registry AliveCount: got %d, want 1", reg.AliveCount())
	}

	// Verify /metrics endpoint returns fiia_nodes_alive_total 1.
	resp, err := http.Get(metrics_url) //nolint:noctx
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}

	if !strings.Contains(string(body), "fiia_nodes_alive_total 1") {
		t.Fatalf("/metrics missing fiia_nodes_alive_total 1:\n%s", body)
	}
}

// TestIntegrationDriftPayload verifies that a DriftPayload is stored in the hub's drift_events table.
func TestIntegrationDriftPayload(t *testing.T) {
	server_tls, ca_pool := genTestTLS(t)

	db_path := filepath.Join(t.TempDir(), "drift_test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	node_id := "drift-node-1"
	secret := []byte("drift-hmac-secret-32bytes-exactly")
	if err := s.SetNodeSecret(node_id, secret); err != nil {
		t.Fatalf("set node secret: %v", err)
	}

	reg := registry.New(s)
	var drift_counter atomic.Int64
	ingest_l := ingest.New(server_tls, reg, s, &drift_counter, nil, 100.0, 10)

	ingest_ln, err := tls.Listen("tcp", "127.0.0.1:0", server_tls)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	serveAndCleanup(t, ingest_l, ingest_ln)

	// Build and send a drift payload.
	drift_p := wire.DriftPayload{
		NodeID:        node_id,
		TimestampUnix: time.Now().Unix(),
		Status:        "DRIFT_DETECTED",
		TasksChanged:  []string{"configure nginx", "update sshd config"},
	}
	payload, err := wire.EncodeDrift(drift_p)
	if err != nil {
		t.Fatalf("encode drift: %v", err)
	}
	frame := wire.BuildFrame(secret, payload)

	client_tls := &tls.Config{RootCAs: ca_pool, MinVersion: tls.VersionTLS13}
	conn, err := tls.Dial("tcp", ingest_ln.Addr().String(), client_tls)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		conn.Close()
		t.Fatalf("write frame: %v", err)
	}
	conn.Close()

	// Poll until drift counter is incremented.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if drift_counter.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if drift_counter.Load() != 1 {
		t.Fatalf("drift_counter: got %d, want 1", drift_counter.Load())
	}

	// Verify drift event stored in database.
	events, err := s.GetDriftEvents(node_id, 10)
	if err != nil {
		t.Fatalf("get drift events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("drift events count: got %d, want 1", len(events))
	}
	if events[0].Status != "DRIFT_DETECTED" {
		t.Errorf("drift status: got %q, want DRIFT_DETECTED", events[0].Status)
	}
	if len(events[0].TasksChanged) != 2 {
		t.Errorf("tasks_changed count: got %d, want 2", len(events[0].TasksChanged))
	}
}

// TestIntegrationRateLimit verifies that a node exceeding rate_limit_rps gets its
// connection dropped with no ACK, while the first frame within the limit is ACK'd.
func TestIntegrationRateLimit(t *testing.T) {
	server_tls, ca_pool := genTestTLS(t)

	db_path := filepath.Join(t.TempDir(), "rate_test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	node_id := "rate-node-1"
	secret := []byte("rate-hmac-secret-32bytes-exactly")
	if err := s.SetNodeSecret(node_id, secret); err != nil {
		t.Fatalf("set node secret: %v", err)
	}

	reg := registry.New(s)
	// 0.1 RPS, burst 1: first frame consumes the only token; second is immediately dropped.
	ingest_l := ingest.New(server_tls, reg, s, nil, nil, 0.1, 1)

	ingest_ln, err := tls.Listen("tcp", "127.0.0.1:0", server_tls)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	serveAndCleanup(t, ingest_l, ingest_ln)

	payload, err := wire.EncodeHeartbeat(wire.HeartbeatPayload{
		NodeID:        node_id,
		TimestampUnix: 1700000000,
		Status:        "OK",
	})
	if err != nil {
		t.Fatalf("encode heartbeat: %v", err)
	}
	frame := wire.BuildFrame(secret, payload)
	client_tls := &tls.Config{RootCAs: ca_pool, MinVersion: tls.VersionTLS13}

	// First frame: within rate limit → hub sends ACK (5 bytes: 4-byte len=1 + PayloadTypeAck).
	conn1, err := tls.Dial("tcp", ingest_ln.Addr().String(), client_tls)
	if err != nil {
		t.Fatalf("dial hub (frame 1): %v", err)
	}
	if _, err := conn1.Write(frame); err != nil {
		conn1.Close()
		t.Fatalf("write frame 1: %v", err)
	}
	var ack [5]byte
	conn1.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	if _, err := io.ReadFull(conn1, ack[:]); err != nil {
		conn1.Close()
		t.Fatalf("read ACK for frame 1: %v", err)
	}
	conn1.Close()
	if ack[4] != wire.PayloadTypeAck {
		t.Errorf("frame 1: expected PayloadTypeAck (0x%02x), got 0x%02x", wire.PayloadTypeAck, ack[4])
	}

	// Second frame immediately: rate limit exceeded → hub closes connection with no response.
	conn2, err := tls.Dial("tcp", ingest_ln.Addr().String(), client_tls)
	if err != nil {
		t.Fatalf("dial hub (frame 2): %v", err)
	}
	if _, err := conn2.Write(frame); err != nil {
		conn2.Close()
		t.Fatalf("write frame 2: %v", err)
	}
	conn2.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) //nolint:errcheck
	_, read_err := io.ReadFull(conn2, ack[:])
	conn2.Close()
	if read_err == nil {
		t.Fatal("frame 2: expected no response (rate limited), but read succeeded")
	}
}

// TestIntegrationHMACMismatch verifies that a frame with a valid node_id but wrong HMAC
// is dropped, the connection gets no ACK, and an HMAC_MISMATCH alert is set in the store.
func TestIntegrationHMACMismatch(t *testing.T) {
	server_tls, ca_pool := genTestTLS(t)

	db_path := filepath.Join(t.TempDir(), "hmac_test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	node_id := "hmac-node-1"
	real_secret := []byte("hmac-real-secret-32bytes-exactly")
	wrong_secret := []byte("hmac-wrng-secret-32bytes-exactly")
	if err := s.SetNodeSecret(node_id, real_secret); err != nil {
		t.Fatalf("set node secret: %v", err)
	}

	reg := registry.New(s)
	ingest_l := ingest.New(server_tls, reg, s, nil, nil, 100.0, 10)

	ingest_ln, err := tls.Listen("tcp", "127.0.0.1:0", server_tls)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	serveAndCleanup(t, ingest_l, ingest_ln)

	payload, err := wire.EncodeHeartbeat(wire.HeartbeatPayload{
		NodeID:        node_id,
		TimestampUnix: time.Now().Unix(),
		Status:        "OK",
	})
	if err != nil {
		t.Fatalf("encode heartbeat: %v", err)
	}
	// Sign with wrong_secret — hub knows real_secret, so Verify fails.
	frame := wire.BuildFrame(wrong_secret, payload)

	client_tls := &tls.Config{RootCAs: ca_pool, MinVersion: tls.VersionTLS13}
	conn, err := tls.Dial("tcp", ingest_ln.Addr().String(), client_tls)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		conn.Close()
		t.Fatalf("write frame: %v", err)
	}
	var ack [5]byte
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) //nolint:errcheck
	_, read_err := io.ReadFull(conn, ack[:])
	conn.Close()
	if read_err == nil {
		t.Fatal("expected no ACK for HMAC mismatch, but read succeeded")
	}

	// Alert must be set in the store.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alerts, _ := s.GetAlerts()
		for _, a := range alerts {
			if a.NodeID == node_id && a.AlertType == "HMAC_MISMATCH" {
				return // pass
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("HMAC_MISMATCH alert not set after mismatch frame")
}

// TestIntegrationUnknownNode verifies that a frame from an unregistered node_id
// is silently dropped (no ACK) and no alert is written to the store.
func TestIntegrationUnknownNode(t *testing.T) {
	server_tls, ca_pool := genTestTLS(t)

	db_path := filepath.Join(t.TempDir(), "unknown_test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	reg := registry.New(s)
	ingest_l := ingest.New(server_tls, reg, s, nil, nil, 100.0, 10)

	ingest_ln, err := tls.Listen("tcp", "127.0.0.1:0", server_tls)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	serveAndCleanup(t, ingest_l, ingest_ln)

	// node_id not registered in store; any secret will fail Verify.
	payload, err := wire.EncodeHeartbeat(wire.HeartbeatPayload{
		NodeID:        "ghost-node-never-registered",
		TimestampUnix: time.Now().Unix(),
		Status:        "OK",
	})
	if err != nil {
		t.Fatalf("encode heartbeat: %v", err)
	}
	frame := wire.BuildFrame([]byte("some-secret-32-bytes-exactly!!!!"), payload)

	client_tls := &tls.Config{RootCAs: ca_pool, MinVersion: tls.VersionTLS13}
	conn, err := tls.Dial("tcp", ingest_ln.Addr().String(), client_tls)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		conn.Close()
		t.Fatalf("write frame: %v", err)
	}
	var ack [5]byte
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) //nolint:errcheck
	_, read_err := io.ReadFull(conn, ack[:])
	conn.Close()
	if read_err == nil {
		t.Fatal("expected no ACK for unknown node, but read succeeded")
	}

	// No alert must be written for unknown nodes.
	time.Sleep(100 * time.Millisecond)
	alerts, err := s.GetAlerts()
	if err != nil {
		t.Fatalf("get alerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("unknown node: want no alerts, got %v", alerts)
	}
}

// TestIntegrationInventoryReconciler verifies that nodes in the inventory CSV but
// absent from the registry are flagged UNINSTRUMENTED_SERVER after reconciliation.
func TestIntegrationInventoryReconciler(t *testing.T) {
	db_path := filepath.Join(t.TempDir(), "inv_test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	reg := registry.New(s)

	// Write a CSV inventory with one node that has never reported.
	csv_path := filepath.Join(t.TempDir(), "inventory.csv")
	if err := os.WriteFile(csv_path, []byte("absent-node-1\n"), 0644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	reader := inventory.NewCSVReader(csv_path)
	stop_ch := make(chan struct{})
	defer close(stop_ch)

	// Run reconciler with a very short interval; it also runs once immediately.
	go inventory.RunReconciler(reader, reg, s, 3600, stop_ch)

	// Poll for the alert to appear (reconciler runs immediately on start).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alerts, _ := s.GetAlerts()
		for _, a := range alerts {
			if a.NodeID == "absent-node-1" && a.AlertType == "UNINSTRUMENTED_SERVER" {
				return // pass
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("UNINSTRUMENTED_SERVER alert not set for absent-node-1 after reconciler run")
}
