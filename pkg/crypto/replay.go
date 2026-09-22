package crypto

import (
	"sync"
)

const WindowSize = 64

// ReplayFilter protects against replay attacks on a per-path basis using a sliding bitmap window
type ReplayFilter struct {
	mu     sync.Mutex
	maxSeq uint64
	bitmap uint64
}

// NewReplayFilter creates a new replay filter
func NewReplayFilter() *ReplayFilter {
	return &ReplayFilter{}
}

// CheckAndSet returns true if the sequence number is acceptable and marks it as seen.
// Returns false if the sequence number was already seen or is older than the window.
func (rf *ReplayFilter) CheckAndSet(seq uint64) bool {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Initial packet
	if rf.maxSeq == 0 && rf.bitmap == 0 {
		rf.maxSeq = seq
		rf.bitmap = 1
		return true
	}

	if seq > rf.maxSeq {
		diff := seq - rf.maxSeq
		if diff >= WindowSize {
			rf.bitmap = 1
		} else {
			rf.bitmap = (rf.bitmap << diff) | 1
		}
		rf.maxSeq = seq
		return true
	}

	diff := rf.maxSeq - seq
	if diff >= WindowSize {
		// Too old, outside replay window
		return false
	}

	bit := uint64(1) << diff
	if (rf.bitmap & bit) != 0 {
		// Already seen (duplicate / replay)
		return false
	}

	// Mark as seen
	rf.bitmap |= bit
	return true
}

// Reset clears the filter state
func (rf *ReplayFilter) Reset() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	rf.maxSeq = 0
	rf.bitmap = 0
}
