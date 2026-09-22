package health

import (
	"sync"
	"time"
)

type PathState int

const (
	StateConnecting PathState = iota
	StateActive
	StateDegraded
	StateDead
)

func (s PathState) String() string {
	switch s {
	case StateConnecting:
		return "CONNECTING"
	case StateActive:
		return "ACTIVE"
	case StateDegraded:
		return "DEGRADED"
	case StateDead:
		return "DEAD"
	default:
		return "UNKNOWN"
	}
}

// PathTracker tracks health, RTT, loss, and goodput for a single path
type PathTracker struct {
	mu sync.RWMutex

	PathID   uint8
	Iface    string
	State    PathState
	Category string // "unlimited", "metered", "backup"

	// RTT tracking (RFC 6298 / TCP EWMA)
	srtt   time.Duration
	rttvar time.Duration
	minRTT time.Duration

	// Loss tracking
	lossEWMA              float64 // 0.0 to 1.0
	consecutiveLosses     int
	consecutiveProbeAcks  int
	lastSeen              time.Time
	lastProbeSent         time.Time
	pendingProbeID        uint64
	pendingProbeTxNs      int64

	// Goodput tracking
	bytesDelivered     uint64
	lastDeliverySample time.Time
	goodputBps         float64 // Bits per second

	// EWMA parameters
	alpha float64 // RTT weight (default 0.125)
	beta  float64 // RTTVAR weight (default 0.25)
	gamma float64 // Loss weight (default 0.10)
}

// NewPathTracker creates a tracker for a path
func NewPathTracker(pathID uint8, iface string, category string) *PathTracker {
	if category == "" {
		category = "unlimited"
	}
	return &PathTracker{
		PathID:        pathID,
		Iface:         iface,
		Category:      category,
		State:         StateConnecting,
		alpha:         0.125,
		beta:          0.25,
		gamma:         0.10,
		minRTT:        time.Hour,
		lastSeen:      time.Now(),
		lastDeliverySample: time.Now(),
	}
}

// OnProbeSent records probe transmission
func (p *PathTracker) OnProbeSent(probeID uint64, txTimestampNs int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pendingProbeID = probeID
	p.pendingProbeTxNs = txTimestampNs
	p.lastProbeSent = time.Now()
}

// OnProbeAck processes an echoed probe pong.
// CRITICAL BONDIFY BUG FIX: Feeds instLoss=0 into LossEWMA even when no DATA traffic is flowing!
func (p *PathTracker) OnProbeAck(probeID uint64, rxNs int64) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.lastSeen = now
	p.consecutiveLosses = 0
	p.consecutiveProbeAcks++

	var sampleRTT time.Duration
	if probeID == p.pendingProbeID && p.pendingProbeTxNs > 0 {
		nowNs := now.UnixNano()
		rttNs := nowNs - p.pendingProbeTxNs
		if rttNs > 0 {
			sampleRTT = time.Duration(rttNs)
		}
	}
	if sampleRTT <= 0 {
		sampleRTT = 10 * time.Millisecond
	}

	// 1. RTT EWMA
	if p.srtt == 0 {
		p.srtt = sampleRTT
		p.rttvar = sampleRTT / 2
		p.minRTT = sampleRTT
	} else {
		diff := p.srtt - sampleRTT
		if diff < 0 {
			diff = -diff
		}
		p.rttvar = time.Duration((1.0-p.beta)*float64(p.rttvar) + p.beta*float64(diff))
		p.srtt = time.Duration((1.0-p.alpha)*float64(p.srtt) + p.alpha*float64(sampleRTT))
		if sampleRTT < p.minRTT {
			p.minRTT = sampleRTT
		}
	}

	// 2. CRITICAL BONDIFY FIX: Feed instLoss = 0.0 into LossEWMA on probe ACK!
	// This ensures an idle path with no DATA traffic recovers from DEGRADED back to ACTIVE.
	p.lossEWMA = (1.0-p.gamma)*p.lossEWMA + p.gamma*0.0

	// 3. Update Path State
	p.updateStateLocked()

	return p.srtt
}

// OnProbeTimeout records a timed-out probe
func (p *PathTracker) OnProbeTimeout() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.consecutiveLosses++
	p.consecutiveProbeAcks = 0

	// Feed instLoss = 1.0 on failed probe
	p.lossEWMA = (1.0-p.gamma)*p.lossEWMA + p.gamma*1.0
	p.updateStateLocked()
}

// OnDataLoss records packet loss from sequence number gaps
func (p *PathTracker) OnDataLoss(lostCount, receivedCount int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := lostCount + receivedCount
	if total <= 0 {
		return
	}
	sampleLoss := float64(lostCount) / float64(total)
	p.lossEWMA = (1.0-p.gamma)*p.lossEWMA + p.gamma*sampleLoss
	p.updateStateLocked()
}

// OnDataDelivered records delivered byte counts for goodput calculation
func (p *PathTracker) OnDataDelivered(bytesCount int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.bytesDelivered += uint64(bytesCount)
	now := time.Now()
	elapsed := now.Sub(p.lastDeliverySample).Seconds()
	if elapsed >= 0.5 {
		// Calculate bits per second
		bits := float64(p.bytesDelivered * 8)
		rate := bits / elapsed
		if p.goodputBps == 0 {
			p.goodputBps = rate
		} else {
			p.goodputBps = 0.7*p.goodputBps + 0.3*rate
		}
		p.bytesDelivered = 0
		p.lastDeliverySample = now
	}
}

func (p *PathTracker) updateStateLocked() {
	if p.consecutiveLosses >= 5 || p.lossEWMA >= 0.50 {
		p.State = StateDead
	} else if p.consecutiveLosses >= 2 || p.lossEWMA >= 0.15 {
		p.State = StateDegraded
	} else {
		p.State = StateActive
	}
}

// Snapshot returns a copy of current metrics
func (p *PathTracker) Snapshot() (state PathState, rtt time.Duration, loss float64, goodput float64, minRTT time.Duration) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.State, p.srtt, p.lossEWMA, p.goodputBps, p.minRTT
}

// SetLossForTesting allows tests to simulate instant link loss
func (p *PathTracker) SetLossForTesting(loss float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lossEWMA = loss
	p.updateStateLocked()
}
