package reorder

import (
	"sync"
	"time"
)

// Config options for the reorder buffer
type Config struct {
	MaxPackets     int           // Max packets in buffer before force-advancing
	ReorderTimeout time.Duration // Max time to wait for a missing sequence number
}

// DefaultConfig provides sensible defaults
func DefaultConfig() Config {
	return Config{
		MaxPackets:     512,
		ReorderTimeout: 30 * time.Millisecond,
	}
}

type bufferedPacket struct {
	seq       uint64
	data      []byte
	arrivedAt time.Time
}

// Buffer reorders out-of-order packets and dedupes duplicates
type Buffer struct {
	mu              sync.Mutex
	cfg             Config
	nextExpectedSeq uint64
	buffered        map[uint64]bufferedPacket
	minBufferedSeq  uint64
	lateDrops       uint64
	duplicates      uint64
	timeouts        uint64
	released        uint64
}

// NewBuffer creates a new reorder buffer
func NewBuffer(cfg Config) *Buffer {
	if cfg.MaxPackets <= 0 {
		cfg.MaxPackets = 512
	}
	if cfg.ReorderTimeout <= 0 {
		cfg.ReorderTimeout = 30 * time.Millisecond
	}
	return &Buffer{
		cfg:             cfg,
		nextExpectedSeq: 1,
		buffered:        make(map[uint64]bufferedPacket),
	}
}

// Push inserts an arriving packet with its global sequence number.
// It returns a slice of packets that are now ready to be released in strict sequential order.
// If the packet is a duplicate or too old, it is dropped and nil is returned.
func (b *Buffer) Push(seq uint64, data []byte) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()

	// Initial packet sets starting sequence if not set
	if b.nextExpectedSeq == 0 {
		b.nextExpectedSeq = seq
	}

	// 1. Check duplicate / too old
	if seq < b.nextExpectedSeq {
		b.lateDrops++
		b.duplicates++
		return nil
	}

	// 2. Check duplicate in buffer
	if _, exists := b.buffered[seq]; exists {
		b.duplicates++
		return nil
	}

	// 3. Exactly the next expected sequence: fast path
	if seq == b.nextExpectedSeq {
		var ready [][]byte
		ready = append(ready, data)
		b.released++
		b.nextExpectedSeq++

		// Flush any contiguous following packets from buffer
		for {
			nextPkt, ok := b.buffered[b.nextExpectedSeq]
			if !ok {
				break
			}
			delete(b.buffered, b.nextExpectedSeq)
			ready = append(ready, nextPkt.data)
			b.released++
			b.nextExpectedSeq++
		}
		return ready
	}

	// 4. Out of order: save in buffer
	b.buffered[seq] = bufferedPacket{
		seq:       seq,
		data:      data,
		arrivedAt: now,
	}

	// 5. Check if buffer exceeds max capacity
	if len(b.buffered) >= b.cfg.MaxPackets {
		return b.flushEarliestLocked()
	}

	return nil
}

// FlushTimeout checks if the missing expected packet has timed out.
// If timed out, it advances the sequence past the gap and releases contiguous buffered packets.
func (b *Buffer) FlushTimeout() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffered) == 0 {
		return nil
	}

	// Find the lowest sequence number currently in buffer
	var lowestSeq uint64 = ^uint64(0)
	var oldestArrival time.Time
	for s, pkt := range b.buffered {
		if s < lowestSeq {
			lowestSeq = s
			oldestArrival = pkt.arrivedAt
		}
	}

	if lowestSeq == ^uint64(0) {
		return nil
	}

	// If the oldest buffered packet has waited longer than ReorderTimeout, advance!
	if time.Since(oldestArrival) >= b.cfg.ReorderTimeout {
		b.timeouts++
		b.nextExpectedSeq = lowestSeq

		var ready [][]byte
		for {
			nextPkt, ok := b.buffered[b.nextExpectedSeq]
			if !ok {
				break
			}
			delete(b.buffered, b.nextExpectedSeq)
			ready = append(ready, nextPkt.data)
			b.released++
			b.nextExpectedSeq++
		}
		return ready
	}

	return nil
}

func (b *Buffer) flushEarliestLocked() [][]byte {
	var lowestSeq uint64 = ^uint64(0)
	for s := range b.buffered {
		if s < lowestSeq {
			lowestSeq = s
		}
	}
	if lowestSeq == ^uint64(0) {
		return nil
	}

	b.timeouts++
	b.nextExpectedSeq = lowestSeq

	var ready [][]byte
	for {
		nextPkt, ok := b.buffered[b.nextExpectedSeq]
		if !ok {
			break
		}
		delete(b.buffered, b.nextExpectedSeq)
		ready = append(ready, nextPkt.data)
		b.released++
		b.nextExpectedSeq++
	}
	return ready
}

// Stats returns buffer metrics
func (b *Buffer) Stats() (occupancy int, released, lateDrops, duplicates, timeouts uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buffered), b.released, b.lateDrops, b.duplicates, b.timeouts
}
