package reorder

import (
	"bytes"
	"testing"
	"time"
)

func TestReorderBufferInOrder(t *testing.T) {
	buf := NewBuffer(DefaultConfig())

	for i := uint64(1); i <= 10; i++ {
		data := []byte{byte(i)}
		ready := buf.Push(i, data)
		if len(ready) != 1 {
			t.Fatalf("expected 1 ready packet for seq %d, got %d", i, len(ready))
		}
		if !bytes.Equal(ready[0], data) {
			t.Fatalf("packet mismatch: got %v, want %v", ready[0], data)
		}
	}

	occ, released, _, _, _ := buf.Stats()
	if occ != 0 || released != 10 {
		t.Fatalf("expected 0 buffered, 10 released; got occ=%d, rel=%d", occ, released)
	}
}

func TestReorderBufferOutOfOrder(t *testing.T) {
	buf := NewBuffer(DefaultConfig())

	// Push seq 1 -> releases immediately
	r1 := buf.Push(1, []byte("pkt1"))
	if len(r1) != 1 {
		t.Fatalf("expected pkt1 to release immediately, got %d", len(r1))
	}

	// Push seq 3 -> buffered (missing seq 2)
	r3 := buf.Push(3, []byte("pkt3"))
	if len(r3) != 0 {
		t.Fatalf("expected pkt3 to be buffered, got %d", len(r3))
	}

	// Push seq 4 -> buffered
	r4 := buf.Push(4, []byte("pkt4"))
	if len(r4) != 0 {
		t.Fatalf("expected pkt4 to be buffered, got %d", len(r4))
	}

	// Push seq 2 -> releases pkt2, pkt3, pkt4 sequentially!
	r2 := buf.Push(2, []byte("pkt2"))
	if len(r2) != 3 {
		t.Fatalf("expected 3 packets to release on arrival of pkt2, got %d", len(r2))
	}

	if string(r2[0]) != "pkt2" || string(r2[1]) != "pkt3" || string(r2[2]) != "pkt4" {
		t.Fatalf("packets released out of order: %s, %s, %s", string(r2[0]), string(r2[1]), string(r2[2]))
	}

	occ, released, _, _, _ := buf.Stats()
	if occ != 0 || released != 4 {
		t.Fatalf("expected 0 buffered, 4 released; got occ=%d, rel=%d", occ, released)
	}
}

func TestReorderBufferDuplicates(t *testing.T) {
	buf := NewBuffer(DefaultConfig())

	_ = buf.Push(1, []byte("pkt1"))
	_ = buf.Push(2, []byte("pkt2"))

	// Re-push seq 1 (duplicate/old)
	dup := buf.Push(1, []byte("pkt1-dup"))
	if len(dup) != 0 {
		t.Fatalf("expected duplicate packet to be dropped, got %d", len(dup))
	}

	_, _, lateDrops, duplicates, _ := buf.Stats()
	if duplicates == 0 || lateDrops == 0 {
		t.Fatalf("expected duplicate counter > 0, got dups=%d, lates=%d", duplicates, lateDrops)
	}
}

func TestReorderBufferTimeoutFlush(t *testing.T) {
	cfg := Config{
		MaxPackets:     512,
		ReorderTimeout: 20 * time.Millisecond,
	}
	buf := NewBuffer(cfg)

	_ = buf.Push(1, []byte("pkt1"))
	// Skip 2, push 3
	_ = buf.Push(3, []byte("pkt3"))

	// Before timeout, nothing ready from FlushTimeout
	if ready := buf.FlushTimeout(); len(ready) != 0 {
		t.Fatalf("expected 0 ready before timeout, got %d", len(ready))
	}

	// Wait past timeout
	time.Sleep(30 * time.Millisecond)

	ready := buf.FlushTimeout()
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready packet after timeout, got %d", len(ready))
	}
	if string(ready[0]) != "pkt3" {
		t.Fatalf("expected pkt3, got %s", string(ready[0]))
	}

	_, _, _, _, timeouts := buf.Stats()
	if timeouts != 1 {
		t.Fatalf("expected timeouts counter = 1, got %d", timeouts)
	}
}
