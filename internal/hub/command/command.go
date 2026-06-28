package command

import "sync"

// Entry is a hub→agent command with its parameters.
type Entry struct {
	Command      string
	PlaybookPath string // for "config_update"
	IntervalSec  int    // for "config_update"
}

// Queue is a thread-safe, in-memory per-node command queue.
// Commands are transient — not persisted across hub restarts.
type Queue struct {
	mu      sync.Mutex
	pending map[string][]Entry // node_id → FIFO list
}

func assert(condition bool, message string) {
	if !condition {
		panic("hub/command: assertion failed: " + message)
	}
}

// New creates an empty Queue.
func New() *Queue {
	return &Queue{pending: make(map[string][]Entry)}
}

// Enqueue adds e to the back of node_id's command list.
func (q *Queue) Enqueue(node_id string, e Entry) {
	assert(node_id != "", "node_id must not be empty")
	assert(e.Command != "", "entry command must not be empty")

	q.mu.Lock()
	defer q.mu.Unlock()

	q.pending[node_id] = append(q.pending[node_id], e)
}

// Pop removes and returns the oldest pending entry for node_id.
// Returns (zero Entry, false) when the queue for that node is empty.
func (q *Queue) Pop(node_id string) (Entry, bool) {
	assert(node_id != "", "node_id must not be empty")

	q.mu.Lock()
	defer q.mu.Unlock()

	entries := q.pending[node_id]
	if len(entries) == 0 {
		return Entry{}, false
	}

	e := entries[0]
	q.pending[node_id] = entries[1:]
	assert(e.Command != "", "popped entry command must not be empty")
	return e, true
}
