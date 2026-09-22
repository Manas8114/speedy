package scheduler

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"speedy/pkg/health"
)

var (
	ErrNoActivePaths = errors.New("no active paths available for scheduling")
)

// Path represents a bonded physical uplink path for scheduling
type Path struct {
	PathID       uint8
	Iface        string
	IfaceIndex   int
	Tracker      *health.PathTracker
	ManualWeight float64 // Optional user-assigned percentage (0 to 100)
	InFlight     atomic.Int64
	CwndBytes    int64
	PacketCount  atomic.Uint64
	ByteCount    atomic.Uint64
}

func NewPath(pathID uint8, iface string, tracker *health.PathTracker) *Path {
	return &Path{
		PathID:    pathID,
		Iface:     iface,
		Tracker:   tracker,
		CwndBytes: 65536, // Default 64KB initial cwnd
	}
}

func NewPathWithIndex(pathID uint8, iface string, ifaceIndex int, tracker *health.PathTracker) *Path {
	return &Path{
		PathID:     pathID,
		Iface:      iface,
		IfaceIndex: ifaceIndex,
		Tracker:    tracker,
		CwndBytes:  65536,
	}
}

// Scheduler selects the path for an outgoing packet
type Scheduler interface {
	Name() string
	Tier() int
	SelectPath(paths []*Path, pktLen int) (*Path, error)
	SelectAllPaths(paths []*Path, pktLen int) ([]*Path, error)
}

// FilterActivePaths filters paths, taking into account Backup-only category rules
func FilterActivePaths(paths []*Path) []*Path {
	var primaryActive []*Path
	var backupActive []*Path

	for _, p := range paths {
		state, _, _, _, _ := p.Tracker.Snapshot()
		if state == health.StateDead {
			continue
		}
		if p.Tracker.Category == "backup" {
			backupActive = append(backupActive, p)
		} else {
			primaryActive = append(primaryActive, p)
		}
	}

	// Rule: Backup-only links carry 0 traffic while any primary link is healthy
	if len(primaryActive) > 0 {
		return primaryActive
	}
	return backupActive
}

// Tier 1: Round-Robin Scheduler
type RoundRobinScheduler struct {
	idx atomic.Uint64
}

func NewRoundRobinScheduler() *RoundRobinScheduler {
	return &RoundRobinScheduler{}
}

func (rr *RoundRobinScheduler) Name() string { return "Tier 1: Round-Robin" }
func (rr *RoundRobinScheduler) Tier() int    { return 1 }

func (rr *RoundRobinScheduler) SelectPath(paths []*Path, pktLen int) (*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	val := rr.idx.Add(1)
	chosen := active[(val-1)%uint64(len(active))]
	chosen.PacketCount.Add(1)
	chosen.ByteCount.Add(uint64(pktLen))
	return chosen, nil
}

func (rr *RoundRobinScheduler) SelectAllPaths(paths []*Path, pktLen int) ([]*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	return active, nil
}

// Tier 2: Goodput-Weighted Scheduler (with auto-normalized manual sliders)
type WeightedScheduler struct {
	mu            sync.Mutex
	deficitCredit map[uint8]float64
	currentIndex  int
}

func NewWeightedScheduler() *WeightedScheduler {
	return &WeightedScheduler{
		deficitCredit: make(map[uint8]float64),
	}
}

func (ws *WeightedScheduler) Name() string { return "Tier 2: Goodput-Weighted" }
func (ws *WeightedScheduler) Tier() int    { return 2 }

func (ws *WeightedScheduler) SelectPath(paths []*Path, pktLen int) (*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	if len(active) == 1 {
		active[0].PacketCount.Add(1)
		active[0].ByteCount.Add(uint64(pktLen))
		return active[0], nil
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()

	// Compute weights: if manual weight is set, normalize manual weights; else use observed goodput * (1 - loss)
	hasManual := false
	manualSum := 0.0
	for _, p := range active {
		if p.ManualWeight > 0 {
			hasManual = true
			manualSum += p.ManualWeight
		}
	}

	weights := make(map[uint8]float64)
	if hasManual && manualSum > 0 {
		for _, p := range active {
			// Auto-normalized to 1.0 without error blocks!
			weights[p.PathID] = math.Max(0.01, p.ManualWeight/manualSum)
		}
	} else {
		// Auto goodput-weighted
		totalScore := 0.0
		for _, p := range active {
			_, _, loss, goodput, _ := p.Tracker.Snapshot()
			if goodput <= 0 {
				goodput = 1000000.0 // Default 1 Mbps baseline
			}
			score := goodput * (1.0 - loss)
			if score <= 0 {
				score = 1000.0
			}
			weights[p.PathID] = score
			totalScore += score
		}
		for _, p := range active {
			weights[p.PathID] = weights[p.PathID] / totalScore
		}
	}

	// Deficit Round Robin step with rotating index
	for {
		for i := 0; i < len(active); i++ {
			idx := (ws.currentIndex + i) % len(active)
			p := active[idx]
			w := weights[p.PathID]
			ws.deficitCredit[p.PathID] += w * 1500.0 // Quantum proportional to weight
			if ws.deficitCredit[p.PathID] >= float64(pktLen) {
				ws.deficitCredit[p.PathID] -= float64(pktLen)
				ws.currentIndex = (idx + 1) % len(active)
				p.PacketCount.Add(1)
				p.ByteCount.Add(uint64(pktLen))
				return p, nil
			}
		}
	}
}

func (ws *WeightedScheduler) SelectAllPaths(paths []*Path, pktLen int) ([]*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	return active, nil
}

// Tier 3: Min-RTT + cwnd-Aware Scheduler
type MinRTTScheduler struct{}

func NewMinRTTScheduler() *MinRTTScheduler {
	return &MinRTTScheduler{}
}

func (m *MinRTTScheduler) Name() string { return "Tier 3: Min-RTT + cwnd" }
func (m *MinRTTScheduler) Tier() int    { return 3 }

func (m *MinRTTScheduler) SelectPath(paths []*Path, pktLen int) (*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}

	var bestPath *Path
	var minRTT time.Duration = time.Hour

	// First pass: find lowest RTT path with available cwnd
	for _, p := range active {
		_, srtt, _, _, _ := p.Tracker.Snapshot()
		inFlight := p.InFlight.Load()
		if inFlight+int64(pktLen) <= p.CwndBytes {
			if srtt < minRTT {
				minRTT = srtt
				bestPath = p
			}
		}
	}

	// Fallback if all windows are full: pick lowest RTT path anyway
	if bestPath == nil {
		minRTT = time.Hour
		for _, p := range active {
			_, srtt, _, _, _ := p.Tracker.Snapshot()
			if srtt < minRTT {
				minRTT = srtt
				bestPath = p
			}
		}
	}

	if bestPath == nil {
		bestPath = active[0]
	}

	bestPath.PacketCount.Add(1)
	bestPath.ByteCount.Add(uint64(pktLen))
	bestPath.InFlight.Add(int64(pktLen))
	return bestPath, nil
}

func (m *MinRTTScheduler) SelectAllPaths(paths []*Path, pktLen int) ([]*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	return active, nil
}

// Tier 4: Head-of-Line (HoL) Blocking Aware Scheduler
type HoLAwareScheduler struct {
	minRTTDisparityRatio float64 // Default 2.5x
}

func NewHoLAwareScheduler() *HoLAwareScheduler {
	return &HoLAwareScheduler{
		minRTTDisparityRatio: 2.5,
	}
}

func (h *HoLAwareScheduler) Name() string { return "Tier 4: HoL-Blocking-Aware" }
func (h *HoLAwareScheduler) Tier() int    { return 4 }

func (h *HoLAwareScheduler) SelectPath(paths []*Path, pktLen int) (*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	if len(active) == 1 {
		active[0].PacketCount.Add(1)
		active[0].ByteCount.Add(uint64(pktLen))
		return active[0], nil
	}

	// Find the fastest path (lowest RTT)
	var fastPath *Path
	var lowestRTT time.Duration = time.Hour
	for _, p := range active {
		_, srtt, _, _, _ := p.Tracker.Snapshot()
		if srtt > 0 && srtt < lowestRTT {
			lowestRTT = srtt
			fastPath = p
		}
	}
	if fastPath == nil {
		fastPath = active[0]
		lowestRTT = 10 * time.Millisecond
	}

	// Check if fast path has open cwnd capacity
	fastInFlight := fastPath.InFlight.Load()
	fastHasSlack := fastInFlight+int64(pktLen) <= fastPath.CwndBytes

	// If fast path has slack capacity, actively avoid paths whose RTT exceeds threshold
	if fastHasSlack {
		fastPath.PacketCount.Add(1)
		fastPath.ByteCount.Add(uint64(pktLen))
		fastPath.InFlight.Add(int64(pktLen))
		return fastPath, nil
	}

	// If fast path is saturated, allow slower paths to absorb slack traffic provided they aren't severely lagging (> 5x)
	var candidate *Path
	var minSecondaryRTT time.Duration = time.Hour
	for _, p := range active {
		_, srtt, _, _, _ := p.Tracker.Snapshot()
		ratio := float64(srtt) / float64(lowestRTT)
		if ratio < 5.0 && srtt < minSecondaryRTT {
			minSecondaryRTT = srtt
			candidate = p
		}
	}

	if candidate == nil {
		candidate = fastPath
	}

	candidate.PacketCount.Add(1)
	candidate.ByteCount.Add(uint64(pktLen))
	candidate.InFlight.Add(int64(pktLen))
	return candidate, nil
}

func (h *HoLAwareScheduler) SelectAllPaths(paths []*Path, pktLen int) ([]*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	return active, nil
}

// REDUNDANT Mode Scheduler (Duplicates packet across all active paths)
type RedundantScheduler struct{}

func NewRedundantScheduler() *RedundantScheduler {
	return &RedundantScheduler{}
}

func (r *RedundantScheduler) Name() string { return "REDUNDANT (Duplicate & Dedupe)" }
func (r *RedundantScheduler) Tier() int    { return 0 }

func (r *RedundantScheduler) SelectPath(paths []*Path, pktLen int) (*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	active[0].PacketCount.Add(1)
	active[0].ByteCount.Add(uint64(pktLen))
	return active[0], nil
}

func (r *RedundantScheduler) SelectAllPaths(paths []*Path, pktLen int) ([]*Path, error) {
	active := FilterActivePaths(paths)
	if len(active) == 0 {
		return nil, ErrNoActivePaths
	}
	for _, p := range active {
		p.PacketCount.Add(1)
		p.ByteCount.Add(uint64(pktLen))
	}
	return active, nil
}
