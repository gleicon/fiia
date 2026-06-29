package ingest

import (
	"sync"
	"testing"

	"github.com/gleicon/fiia/internal/hub/store"
)

// fakeStore records UpdateHeartbeat and ClearAlert calls for deduplication assertions.
// All other store.Store methods are stubs that return zero values.
type fakeStore struct {
	mu         sync.Mutex
	heartbeats map[string]int64
	clears     []alertKey
}

func newFakeStore() *fakeStore {
	return &fakeStore{heartbeats: make(map[string]int64)}
}

func (f *fakeStore) UpdateHeartbeat(node_id string, ts int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats[node_id] = ts
	return nil
}
func (f *fakeStore) ClearAlert(node_id, alert_type string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears = append(f.clears, alertKey{node_id, alert_type})
	return nil
}
func (f *fakeStore) GetNodeSecret(_ string) ([]byte, error)                     { return nil, nil }
func (f *fakeStore) SetNodeSecret(_ string, _ []byte) error                     { return nil }
func (f *fakeStore) AppendDrift(_ string, _ int64, _ string, _ []string) error  { return nil }
func (f *fakeStore) GetDriftEvents(_ string, _ int) ([]store.DriftEvent, error) { return nil, nil }
func (f *fakeStore) SetAlert(_ string, _ string, _ int64) error                 { return nil }
func (f *fakeStore) GetAlerts() ([]store.Alert, error)                          { return nil, nil }
func (f *fakeStore) GetNodes() ([]store.Node, error)                            { return nil, nil }
func (f *fakeStore) GetNode(_ string) (store.Node, error)                       { return store.Node{}, nil }
func (f *fakeStore) CountNodesWithStatus(_ string) (int64, error)               { return 0, nil }
func (f *fakeStore) Close() error                                               { return nil }

func listenerWithFakeStore(s *fakeStore) *Listener {
	return &Listener{store: s, write_ch: make(chan dbWriteOp, 128)}
}

func TestFlushBatchDeduplicatesHeartbeat(t *testing.T) {
	fs := newFakeStore()
	l := listenerWithFakeStore(fs)

	// Three heartbeats for the same node; only the latest timestamp should be written.
	ops := []dbWriteOp{
		{kind: opUpdateHeartbeat, node_id: "n1", timestamp: 100},
		{kind: opUpdateHeartbeat, node_id: "n1", timestamp: 300},
		{kind: opUpdateHeartbeat, node_id: "n1", timestamp: 200},
	}
	l.flushBatch(ops)

	if len(fs.heartbeats) != 1 {
		t.Fatalf("heartbeat write count: got %d, want 1", len(fs.heartbeats))
	}
	if fs.heartbeats["n1"] != 300 {
		t.Errorf("heartbeat timestamp: got %d, want 300", fs.heartbeats["n1"])
	}
}

func TestFlushBatchDeduplicatesClearAlert(t *testing.T) {
	fs := newFakeStore()
	l := listenerWithFakeStore(fs)

	// Three clears for the same (node, alert_type) pair; only one call should be issued.
	ops := []dbWriteOp{
		{kind: opClearAlert, node_id: "n1", alert_type: "AGENT_PAUSED"},
		{kind: opClearAlert, node_id: "n1", alert_type: "AGENT_PAUSED"},
		{kind: opClearAlert, node_id: "n1", alert_type: "AGENT_PAUSED"},
	}
	l.flushBatch(ops)

	if len(fs.clears) != 1 {
		t.Fatalf("clear_alert call count: got %d, want 1", len(fs.clears))
	}
}

func TestFlushBatchDistinctAlertTypesEachWritten(t *testing.T) {
	fs := newFakeStore()
	l := listenerWithFakeStore(fs)

	ops := []dbWriteOp{
		{kind: opClearAlert, node_id: "n1", alert_type: "AGENT_PAUSED"},
		{kind: opClearAlert, node_id: "n1", alert_type: "AGENT_UNREACHABLE"},
	}
	l.flushBatch(ops)

	if len(fs.clears) != 2 {
		t.Fatalf("clear_alert call count: got %d, want 2", len(fs.clears))
	}
}

func TestFlushBatchMultipleNodesIndependent(t *testing.T) {
	fs := newFakeStore()
	l := listenerWithFakeStore(fs)

	ops := []dbWriteOp{
		{kind: opUpdateHeartbeat, node_id: "n1", timestamp: 10},
		{kind: opUpdateHeartbeat, node_id: "n2", timestamp: 20},
		{kind: opUpdateHeartbeat, node_id: "n1", timestamp: 50},
	}
	l.flushBatch(ops)

	if len(fs.heartbeats) != 2 {
		t.Fatalf("heartbeat write count: got %d, want 2", len(fs.heartbeats))
	}
	if fs.heartbeats["n1"] != 50 {
		t.Errorf("n1 timestamp: got %d, want 50", fs.heartbeats["n1"])
	}
	if fs.heartbeats["n2"] != 20 {
		t.Errorf("n2 timestamp: got %d, want 20", fs.heartbeats["n2"])
	}
}
