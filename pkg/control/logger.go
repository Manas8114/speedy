package control

import (
	"sync"
	"time"
)

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// RingLogger provides a bounded, rotating in-memory log buffer (prevents unbounded memory growth)
type RingLogger struct {
	mu       sync.RWMutex
	capacity int
	entries  []LogEntry
	index    int
	total    uint64
}

func NewRingLogger(capacity int) *RingLogger {
	if capacity <= 0 {
		capacity = 500
	}
	return &RingLogger{
		capacity: capacity,
		entries:  make([]LogEntry, 0, capacity),
	}
}

func (r *RingLogger) Log(level, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	}

	r.total++
	if len(r.entries) < r.capacity {
		r.entries = append(r.entries, entry)
	} else {
		r.entries[r.index] = entry
		r.index = (r.index + 1) % r.capacity
	}
}

// Entries returns all log entries in chronological order
func (r *RingLogger) Entries() []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.entries) < r.capacity {
		res := make([]LogEntry, len(r.entries))
		copy(res, r.entries)
		return res
	}

	res := make([]LogEntry, r.capacity)
	for i := 0; i < r.capacity; i++ {
		idx := (r.index + i) % r.capacity
		res[i] = r.entries[idx]
	}
	return res
}
