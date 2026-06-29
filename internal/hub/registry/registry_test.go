package registry

import (
	"sync"
	"testing"
	"time"

	"github.com/gleicon/fiia/internal/hub/store"
	"github.com/gleicon/fiia/internal/wire"
)

// fakeStore records SetAlert calls and returns GetAlerts results.
// All other Store methods are no-op stubs.
type fakeStore struct {
	mu     sync.Mutex
	alerts []store.Alert
}

func (f *fakeStore) SetAlert(node_id, alert_type string, created_unix int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, store.Alert{NodeID: node_id, AlertType: alert_type, CreatedUnix: created_unix})
	return nil
}

func (f *fakeStore) GetAlerts() ([]store.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Alert(nil), f.alerts...), nil
}

func (f *fakeStore) UpdateHeartbeat(_ string, _ int64) error                     { return nil }
func (f *fakeStore) GetNodeSecret(_ string) ([]byte, error)                      { return nil, nil }
func (f *fakeStore) SetNodeSecret(_ string, _ []byte) error                      { return nil }
func (f *fakeStore) AppendDrift(_ string, _ int64, _ string, _ []string) error   { return nil }
func (f *fakeStore) GetDriftEvents(_ string, _ int) ([]store.DriftEvent, error)  { return nil, nil }
func (f *fakeStore) ClearAlert(_ string, _ string) error                         { return nil }
func (f *fakeStore) GetNodes() ([]store.Node, error)                             { return nil, nil }
func (f *fakeStore) GetNode(_ string) (store.Node, error)                        { return store.Node{}, nil }
func (f *fakeStore) CountNodesWithStatus(_ string) (int64, error)                { return 0, nil }
func (f *fakeStore) Close() error                                                { return nil }

func alertsOf(fs *fakeStore, node_id string) []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var types []string
	for _, a := range fs.alerts {
		if a.NodeID == node_id {
			types = append(types, a.AlertType)
		}
	}
	return types
}

func TestCheckExpiryAliveNodeWritesNoAlert(t *testing.T) {
	fs := &fakeStore{}
	r := New(fs)

	now := time.Now().Unix()
	r.Update("alive-node", now-100, wire.USEMetrics{})
	r.checkExpiry()

	if got := alertsOf(fs, "alive-node"); len(got) != 0 {
		t.Errorf("alive node: want no alerts, got %v", got)
	}
}

func TestCheckExpiryPausedNodeWritesAgentPaused(t *testing.T) {
	fs := &fakeStore{}
	r := New(fs)

	// age > paused_threshold_sec (600) but ≤ unreachable_threshold_sec (1200)
	now := time.Now().Unix()
	r.Update("paused-node", now-700, wire.USEMetrics{})
	r.checkExpiry()

	types := alertsOf(fs, "paused-node")
	if len(types) != 1 || types[0] != "AGENT_PAUSED" {
		t.Errorf("paused node: want [AGENT_PAUSED], got %v", types)
	}
}

func TestCheckExpiryUnreachableNodeWritesAgentUnreachable(t *testing.T) {
	fs := &fakeStore{}
	r := New(fs)

	// age > unreachable_threshold_sec (1200)
	now := time.Now().Unix()
	r.Update("unreachable-node", now-1300, wire.USEMetrics{})
	r.checkExpiry()

	types := alertsOf(fs, "unreachable-node")
	if len(types) != 1 || types[0] != "AGENT_UNREACHABLE" {
		t.Errorf("unreachable node: want [AGENT_UNREACHABLE], got %v", types)
	}
}

func TestCheckExpiryMixedFleet(t *testing.T) {
	fs := &fakeStore{}
	r := New(fs)

	now := time.Now().Unix()
	r.Update("alive", now-100, wire.USEMetrics{})
	r.Update("paused", now-700, wire.USEMetrics{})
	r.Update("unreachable", now-1300, wire.USEMetrics{})
	r.checkExpiry()

	if got := alertsOf(fs, "alive"); len(got) != 0 {
		t.Errorf("alive: want no alerts, got %v", got)
	}
	if got := alertsOf(fs, "paused"); len(got) != 1 || got[0] != "AGENT_PAUSED" {
		t.Errorf("paused: want [AGENT_PAUSED], got %v", got)
	}
	if got := alertsOf(fs, "unreachable"); len(got) != 1 || got[0] != "AGENT_UNREACHABLE" {
		t.Errorf("unreachable: want [AGENT_UNREACHABLE], got %v", got)
	}
}

func TestCheckExpiryNoPanicOnEmptyRegistry(t *testing.T) {
	fs := &fakeStore{}
	r := New(fs)
	r.checkExpiry() // must not panic
}
