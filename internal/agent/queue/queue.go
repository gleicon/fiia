package queue

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	maxEntries = 64
	slotCount  = 64
)

type queueState struct {
	Head  uint8 `msgpack:"head"`
	Tail  uint8 `msgpack:"tail"`
	Count uint8 `msgpack:"count"`
}

// Queue is a disk-backed ring buffer of pre-encoded wire frames.
// Bounded at 64 entries; overflow overwrites the oldest entry.
// Not safe for concurrent use — callers must serialize access.
type Queue struct {
	dir string
	st  queueState
}

func assert(condition bool, message string) {
	if !condition {
		panic("agent/queue: assertion failed: " + message)
	}
}

// Open opens or creates a queue at dir.
// If no state file exists, a fresh empty queue is returned.
func Open(dir string) (*Queue, error) {
	assert(dir != "", "dir must not be empty")

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create queue dir %q: %w", dir, err)
	}

	q := &Queue{dir: dir}
	if err := q.loadState(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load queue state: %w", err)
	}

	assert(q.st.Count <= maxEntries, "loaded count must not exceed max entries")
	return q, nil
}

// Write appends frame to the queue.
// If the queue is full (64 entries), the oldest entry is silently overwritten.
func (q *Queue) Write(frame []byte) error {
	assert(len(frame) > 0, "frame must not be empty")
	assert(q.dir != "", "queue dir must not be empty")

	if q.st.Count == maxEntries {
		q.st.Head = (q.st.Head + 1) % slotCount
		q.st.Count--
	}

	slot_path := q.slotPath(q.st.Tail)
	if err := os.WriteFile(slot_path, frame, 0600); err != nil {
		return fmt.Errorf("write slot %d: %w", q.st.Tail, err)
	}

	q.st.Tail = (q.st.Tail + 1) % slotCount
	q.st.Count++

	if err := q.saveState(); err != nil {
		return fmt.Errorf("save state after write: %w", err)
	}

	assert(q.st.Count <= maxEntries, "count must not exceed max after write")
	return nil
}

// Peek returns the oldest frame without removing it.
// Returns (nil, false, nil) when the queue is empty.
func (q *Queue) Peek() ([]byte, bool, error) {
	assert(q.dir != "", "queue dir must not be empty")

	if q.st.Count == 0 {
		return nil, false, nil
	}

	slot_path := q.slotPath(q.st.Head)
	data, err := os.ReadFile(slot_path)
	if err != nil {
		return nil, false, fmt.Errorf("read slot %d: %w", q.st.Head, err)
	}

	assert(len(data) > 0, "slot file must not be empty")
	return data, true, nil
}

// Advance removes the oldest entry. Call after the hub sends an ACK.
func (q *Queue) Advance() error {
	assert(q.dir != "", "queue dir must not be empty")
	assert(q.st.Count > 0, "cannot advance empty queue")

	slot_path := q.slotPath(q.st.Head)
	if err := os.Remove(slot_path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove slot %d: %w", q.st.Head, err)
	}

	q.st.Head = (q.st.Head + 1) % slotCount
	q.st.Count--

	if err := q.saveState(); err != nil {
		return fmt.Errorf("save state after advance: %w", err)
	}

	assert(q.st.Count < maxEntries, "count must be less than max after advance")
	return nil
}

// Len returns the number of unacknowledged entries in the queue.
func (q *Queue) Len() int {
	return int(q.st.Count)
}

func (q *Queue) slotPath(slot uint8) string {
	return filepath.Join(q.dir, fmt.Sprintf("%02d.frame", slot))
}

func (q *Queue) statePath() string {
	return filepath.Join(q.dir, "state")
}

func (q *Queue) loadState() error {
	data, err := os.ReadFile(q.statePath())
	if err != nil {
		return err
	}
	assert(len(data) > 0, "state file must not be empty")
	if err := msgpack.Unmarshal(data, &q.st); err != nil {
		return fmt.Errorf("unmarshal state: %w", err)
	}
	return nil
}

func (q *Queue) saveState() error {
	assert(q.dir != "", "queue dir must not be empty")

	data, err := msgpack.Marshal(q.st)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	assert(len(data) > 0, "marshaled state must not be empty")

	tmp_path := q.statePath() + ".tmp"
	if err := os.WriteFile(tmp_path, data, 0600); err != nil {
		return fmt.Errorf("write tmp state: %w", err)
	}

	if err := os.Rename(tmp_path, q.statePath()); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}
