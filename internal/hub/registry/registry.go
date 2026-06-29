package registry

import (
	"log"
	"sync"
	"time"

	"github.com/gleicon/fiia/internal/hub/store"
	"github.com/gleicon/fiia/internal/wire"
)

const (
	// alive_threshold_sec: a node is considered alive if last_seen within this window.
	alive_threshold_sec = 600 // 10 minutes = 2 heartbeat intervals
	// paused_threshold_sec: node flagged AGENT_PAUSED after one missed heartbeat window.
	paused_threshold_sec = 600
	// unreachable_threshold_sec: node flagged AGENT_UNREACHABLE after two missed windows.
	unreachable_threshold_sec = 1200
)

// NodeState holds the current in-memory state of a fleet node.
type NodeState struct {
	NodeID       string
	LastSeenUnix int64
	Status       string
	Metrics      wire.USEMetrics
}

// Registry is the in-memory heartbeat registry, flushed to Store on each update.
// All methods are safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]*NodeState
	store store.Store
}

func assert(condition bool, message string) {
	if !condition {
		panic("hub/registry: assertion failed: " + message)
	}
}

// New creates a Registry backed by the given Store.
func New(s store.Store) *Registry {
	assert(s != nil, "store must not be nil")

	return &Registry{
		nodes: make(map[string]*NodeState),
		store: s,
	}
}

// Update records a heartbeat for node_id at timestamp_unix with USE metrics.
// Updates the in-memory map and flushes to the Store.
func (r *Registry) Update(node_id string, timestamp_unix int64, m wire.USEMetrics) {
	assert(r.nodes != nil, "nodes map must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(timestamp_unix > 0, "timestamp_unix must be positive")

	r.mu.Lock()
	state, exists := r.nodes[node_id]
	if !exists {
		state = &NodeState{NodeID: node_id}
		r.nodes[node_id] = state
	}
	state.LastSeenUnix = timestamp_unix
	state.Status = "OK"
	state.Metrics = m
	r.mu.Unlock()
	// DB write intentionally absent — caller is responsible for persisting via UpdateHeartbeat.
}

// GetAll returns a snapshot of all node states.
func (r *Registry) GetAll() []NodeState {
	assert(r.nodes != nil, "nodes map must not be nil")

	r.mu.RLock()
	defer r.mu.RUnlock()

	snapshot := make([]NodeState, 0, len(r.nodes))
	for _, state := range r.nodes {
		snapshot = append(snapshot, *state)
	}
	return snapshot
}

// AliveCount returns the number of nodes whose last heartbeat is within alive_threshold_sec.
func (r *Registry) AliveCount() int {
	assert(r.nodes != nil, "nodes map must not be nil")

	now_unix := time.Now().Unix()
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, state := range r.nodes {
		if now_unix-state.LastSeenUnix <= alive_threshold_sec {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of known nodes.
func (r *Registry) TotalCount() int {
	assert(r.nodes != nil, "nodes map must not be nil")

	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// RunExpiry monitors node heartbeat ages and writes alerts for stale nodes.
// Blocks until stop_ch is closed. Intended to run in a dedicated goroutine.
func (r *Registry) RunExpiry(stop_ch <-chan struct{}) {
	assert(r.nodes != nil, "nodes map must not be nil")
	assert(stop_ch != nil, "stop_ch must not be nil")

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop_ch:
			return
		case <-ticker.C:
			r.checkExpiry()
		}
	}
}

func (r *Registry) checkExpiry() {
	assert(r.nodes != nil, "nodes map must not be nil")

	now_unix := time.Now().Unix()
	r.mu.RLock()
	nodes := make([]NodeState, 0, len(r.nodes))
	for _, state := range r.nodes {
		nodes = append(nodes, *state)
	}
	r.mu.RUnlock()

	var alive, paused, unreachable int
	for _, n := range nodes {
		age_sec := now_unix - n.LastSeenUnix
		var alert_type string
		switch {
		case age_sec > unreachable_threshold_sec:
			alert_type = "AGENT_UNREACHABLE"
			unreachable++
		case age_sec > paused_threshold_sec:
			alert_type = "AGENT_PAUSED"
			paused++
		default:
			alive++
			continue
		}
		if err := r.store.SetAlert(n.NodeID, alert_type, now_unix); err != nil {
			log.Printf("registry: set %s alert for %q: %v", alert_type, n.NodeID, err)
		}
	}

	drift := 0
	if alerts, err := r.store.GetAlerts(); err == nil {
		for _, a := range alerts {
			if a.AlertType == "DRIFT_DETECTED" {
				drift++
			}
		}
	}

	log.Printf("registry: fleet total=%d alive=%d paused=%d unreachable=%d drift=%d",
		len(nodes), alive, paused, unreachable, drift)
}
