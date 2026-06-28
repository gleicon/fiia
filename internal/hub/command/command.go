package command

import "sync"

// Queue is a thread-safe, in-memory per-node command queue.
// Commands are transient — not persisted across hub restarts.
type Queue struct {
	mu      sync.Mutex
	pending map[string][]string // node_id → FIFO list of command strings
}

func assert(condition bool, message string) {
	if !condition {
		panic("hub/command: assertion failed: " + message)
	}
}

// New creates an empty Queue.
func New() *Queue {
	return &Queue{pending: make(map[string][]string)}
}

// Enqueue adds cmd to the back of node_id's command list.
func (q *Queue) Enqueue(node_id, cmd string) {
	assert(node_id != "", "node_id must not be empty")
	assert(cmd != "", "cmd must not be empty")

	q.mu.Lock()
	defer q.mu.Unlock()

	q.pending[node_id] = append(q.pending[node_id], cmd)
}

// Pop removes and returns the oldest pending command for node_id.
// Returns ("", false) when the queue for that node is empty.
func (q *Queue) Pop(node_id string) (string, bool) {
	assert(node_id != "", "node_id must not be empty")

	q.mu.Lock()
	defer q.mu.Unlock()

	cmds := q.pending[node_id]
	if len(cmds) == 0 {
		return "", false
	}

	cmd := cmds[0]
	q.pending[node_id] = cmds[1:]
	assert(cmd != "", "popped command must not be empty")
	return cmd, true
}
