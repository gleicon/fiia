package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gleicon/fiia/internal/hub/api"
	"github.com/gleicon/fiia/internal/hub/command"
	"github.com/gleicon/fiia/internal/hub/store"
)

func newTestServer(t *testing.T, cmdq *command.Queue) (base_url string, s store.Store) {
	t.Helper()

	db_path := filepath.Join(t.TempDir(), "api_test.db")
	s, err := store.NewSQLiteStore(db_path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	srv := api.New(s, cmdq)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.ServeListener(ln) //nolint:errcheck
	return "http://" + ln.Addr().String(), s
}

func TestAPIListNodesEmpty(t *testing.T) {
	base, _ := newTestServer(t, nil)
	resp, err := http.Get(base + "/nodes") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /nodes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// Should be a JSON null or empty array.
	trimmed := strings.TrimSpace(string(body))
	if trimmed != "null" && trimmed != "[]" {
		t.Errorf("unexpected body for empty nodes: %q", trimmed)
	}
}

func TestAPIGetNodeStatusNotFound(t *testing.T) {
	base, _ := newTestServer(t, nil)
	resp, err := http.Get(base + "/nodes/does-not-exist/status") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /nodes/.../status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestAPIGetNodeStatusFound(t *testing.T) {
	base, s := newTestServer(t, nil)
	if err := s.UpdateHeartbeat("known-node", 1700000000); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}

	resp, err := http.Get(base + "/nodes/known-node/status") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /nodes/known-node/status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var node store.Node
	if err := json.NewDecoder(resp.Body).Decode(&node); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if node.ID != "known-node" {
		t.Errorf("node.ID: got %q, want known-node", node.ID)
	}
}

func TestAPIGetNodeStatusIDTooLong(t *testing.T) {
	base, _ := newTestServer(t, nil)
	long_id := strings.Repeat("x", 300)
	resp, err := http.Get(base + "/nodes/" + long_id + "/status") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /nodes/.../status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for too-long node_id, got %d", resp.StatusCode)
	}
}

func TestAPIListAlertsEmpty(t *testing.T) {
	base, _ := newTestServer(t, nil)
	resp, err := http.Get(base + "/alerts") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /alerts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestAPIPostAuditNowNoCmdq(t *testing.T) {
	base, _ := newTestServer(t, nil) // no command queue
	resp, err := http.Post(base+"/nodes/n1/audit_now", "", nil) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /nodes/n1/audit_now: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when cmdq is nil, got %d", resp.StatusCode)
	}
}

func TestAPIPostAuditNowEnqueues(t *testing.T) {
	cmdq := command.New()
	base, _ := newTestServer(t, cmdq)
	resp, err := http.Post(base+"/nodes/n1/audit_now", "", nil) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /nodes/n1/audit_now: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	entry, ok := cmdq.Pop("n1")
	if !ok {
		t.Fatal("command not enqueued")
	}
	if entry.Command != "audit_now" {
		t.Errorf("command: got %q, want audit_now", entry.Command)
	}
}

func TestAPIPostConfigNoCmdq(t *testing.T) {
	base, _ := newTestServer(t, nil)
	body := strings.NewReader(`{"playbook_path":"/etc/site.yml"}`)
	resp, err := http.Post(base+"/nodes/n1/config", "application/json", body) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /nodes/n1/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when cmdq is nil, got %d", resp.StatusCode)
	}
}

func TestAPIPostConfigEnqueues(t *testing.T) {
	cmdq := command.New()
	base, _ := newTestServer(t, cmdq)
	body := strings.NewReader(`{"playbook_path":"/etc/site.yml","interval_sec":600}`)
	resp, err := http.Post(base+"/nodes/n1/config", "application/json", body) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /nodes/n1/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	entry, ok := cmdq.Pop("n1")
	if !ok {
		t.Fatal("command not enqueued")
	}
	if entry.Command != "config_update" {
		t.Errorf("command: got %q, want config_update", entry.Command)
	}
	if entry.PlaybookPath != "/etc/site.yml" {
		t.Errorf("playbook_path: got %q, want /etc/site.yml", entry.PlaybookPath)
	}
	if entry.IntervalSec != 600 {
		t.Errorf("interval_sec: got %d, want 600", entry.IntervalSec)
	}
}

func TestAPIPostConfigBothEmpty(t *testing.T) {
	cmdq := command.New()
	base, _ := newTestServer(t, cmdq)
	body := strings.NewReader(`{}`)
	resp, err := http.Post(base+"/nodes/n1/config", "application/json", body) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /nodes/n1/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 when both fields empty, got %d", resp.StatusCode)
	}
}

func TestAPIPostConfigBodyTooLarge(t *testing.T) {
	cmdq := command.New()
	base, _ := newTestServer(t, cmdq)
	// 4097 bytes > config_body_max (4096)
	oversized := fmt.Sprintf(`{"playbook_path":"%s"}`, strings.Repeat("a", 4090))
	body := strings.NewReader(oversized)
	resp, err := http.Post(base+"/nodes/n1/config", "application/json", body) //nolint:noctx
	if err != nil {
		t.Fatalf("POST /nodes/n1/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for oversized body, got %d", resp.StatusCode)
	}
}

func TestAPIGetNodeDriftEmpty(t *testing.T) {
	base, s := newTestServer(t, nil)
	if err := s.UpdateHeartbeat("drift-node", 1700000000); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	resp, err := http.Get(base + "/nodes/drift-node/drift") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /nodes/drift-node/drift: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}
