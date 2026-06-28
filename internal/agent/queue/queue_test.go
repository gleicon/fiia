package queue_test

import (
	"testing"

	"github.com/gleicon/fiia/internal/agent/queue"
)

func TestQueueWriteAndPeek(t *testing.T) {
	q, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	frame := []byte("test-frame-data")
	if err := q.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	if q.Len() != 1 {
		t.Fatalf("want len=1, got %d", q.Len())
	}

	data, ok, err := q.Peek()
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !ok {
		t.Fatal("peek: expected entry, got empty")
	}
	if string(data) != string(frame) {
		t.Fatalf("peek: want %q, got %q", frame, data)
	}
}

func TestQueueAdvanceOrderFIFO(t *testing.T) {
	q, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := q.Write([]byte("a")); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := q.Write([]byte("b")); err != nil {
		t.Fatalf("write b: %v", err)
	}

	if err := q.Advance(); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if q.Len() != 1 {
		t.Fatalf("want len=1 after advance, got %d", q.Len())
	}

	data, ok, err := q.Peek()
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !ok || string(data) != "b" {
		t.Fatalf("want oldest=b, got %q ok=%v", data, ok)
	}
}

func TestQueuePersistenceAcrossOpen(t *testing.T) {
	dir := t.TempDir()

	q1, err := queue.Open(dir)
	if err != nil {
		t.Fatalf("open q1: %v", err)
	}
	frame := []byte("persist-me")
	if err := q1.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	q2, err := queue.Open(dir)
	if err != nil {
		t.Fatalf("reopen q2: %v", err)
	}
	if q2.Len() != 1 {
		t.Fatalf("want len=1 after reopen, got %d", q2.Len())
	}

	data, ok, err := q2.Peek()
	if err != nil {
		t.Fatalf("peek after reopen: %v", err)
	}
	if !ok || string(data) != string(frame) {
		t.Fatalf("want %q, got %q ok=%v", frame, data, ok)
	}
}

func TestQueueOverwriteOldestWhenFull(t *testing.T) {
	q, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	for i := 0; i < 64; i++ {
		if err := q.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if q.Len() != 64 {
		t.Fatalf("want len=64, got %d", q.Len())
	}

	// One more write must evict slot 0, making slot 1 the new head.
	if err := q.Write([]byte{64}); err != nil {
		t.Fatalf("write 64: %v", err)
	}
	if q.Len() != 64 {
		t.Fatalf("want len=64 after overflow write, got %d", q.Len())
	}

	data, ok, err := q.Peek()
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !ok || data[0] != 1 {
		t.Fatalf("want oldest byte=1, got %v ok=%v", data, ok)
	}
}

func TestQueueEmptyPeekReturnsFalse(t *testing.T) {
	q, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	_, ok, err := q.Peek()
	if err != nil {
		t.Fatalf("peek empty: %v", err)
	}
	if ok {
		t.Fatal("peek empty: want false, got true")
	}
}

func TestQueueDrainToEmpty(t *testing.T) {
	q, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	frames := []string{"x", "y", "z"}
	for _, f := range frames {
		if err := q.Write([]byte(f)); err != nil {
			t.Fatalf("write %q: %v", f, err)
		}
	}

	for i, want := range frames {
		data, ok, err := q.Peek()
		if err != nil {
			t.Fatalf("peek %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("peek %d: expected entry", i)
		}
		if string(data) != want {
			t.Fatalf("peek %d: want %q, got %q", i, want, data)
		}
		if err := q.Advance(); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}

	if q.Len() != 0 {
		t.Fatalf("want len=0 after full drain, got %d", q.Len())
	}
}
