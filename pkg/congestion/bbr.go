package congestion

import (
	"sync"
	"time"
)

// BBRConfig configures the BBR-style congestion controller
type BBRConfig struct {
	MinCwndBytes int64         // Floor (e.g. 4 packets = 6000 bytes)
	MaxCwndBytes int64         // Ceiling (e.g. 4 MB = 4194304 bytes)
	Gain         float64       // Gain factor (e.g. 2.0 in probe bw)
	WindowSize   time.Duration // Rolling window for max bandwidth filter
}

func DefaultBBRConfig() BBRConfig {
	return BBRConfig{
		MinCwndBytes: 6000,
		MaxCwndBytes: 4 * 1024 * 1024,
		Gain:         2.0,
		WindowSize:   2 * time.Second,
	}
}

type rateSample struct {
	rateBps   float64
	timestamp time.Time
}

// BBRController implements per-path BBR-style congestion control
type BBRController struct {
	mu          sync.Mutex
	cfg         BBRConfig
	btlBwBps    float64
	rtProp      time.Duration
	cwndBytes   int64
	samples     []rateSample
	lastMinRTT  time.Time
	minRTT      time.Duration
	inFlight    int64
}

func NewBBRController(cfg BBRConfig) *BBRController {
	if cfg.MinCwndBytes <= 0 {
		cfg.MinCwndBytes = 6000
	}
	if cfg.MaxCwndBytes <= 0 {
		cfg.MaxCwndBytes = 4194304
	}
	if cfg.Gain <= 0 {
		cfg.Gain = 2.0
	}
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 2 * time.Second
	}
	return &BBRController{
		cfg:       cfg,
		cwndBytes: cfg.MinCwndBytes,
		minRTT:    time.Hour,
	}
}

// Update processes an ack / delivery sample with delivered bytes and round trip time
func (b *BBRController) Update(deliveredBytes int, sampleRTT time.Duration) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()

	// 1. Update RTprop (min RTT filter)
	if sampleRTT > 0 {
		if sampleRTT < b.minRTT || now.Sub(b.lastMinRTT) > 10*time.Second {
			b.minRTT = sampleRTT
			b.lastMinRTT = now
		}
	}
	if b.minRTT == time.Hour || b.minRTT <= 0 {
		b.minRTT = 10 * time.Millisecond
	}

	// 2. Compute sample delivery rate in bytes/sec
	var sampleRate float64
	if sampleRTT > 0 && deliveredBytes > 0 {
		sampleRate = float64(deliveredBytes) / sampleRTT.Seconds()
	}

	// 3. Add to windowed-max filter
	b.samples = append(b.samples, rateSample{rateBps: sampleRate, timestamp: now})

	// Purge samples older than window
	cutoff := now.Add(-b.cfg.WindowSize)
	validIdx := 0
	var maxRate float64
	for i, s := range b.samples {
		if s.timestamp.After(cutoff) {
			validIdx = i
			break
		}
	}
	b.samples = b.samples[validIdx:]
	for _, s := range b.samples {
		if s.rateBps > maxRate {
			maxRate = s.rateBps
		}
	}
	b.btlBwBps = maxRate

	// 4. Calculate BBR Target cwnd = BtlBw * RTprop * gain
	targetCwnd := int64(b.btlBwBps * b.minRTT.Seconds() * b.cfg.Gain)

	// 5. Invariants: Floor at minCwnd, Cap at maxCwnd
	if targetCwnd < b.cfg.MinCwndBytes {
		targetCwnd = b.cfg.MinCwndBytes
	}
	if targetCwnd > b.cfg.MaxCwndBytes {
		targetCwnd = b.cfg.MaxCwndBytes
	}

	b.cwndBytes = targetCwnd
	return b.cwndBytes
}

// DrainSustainedZero simulates elapsed time without deliveries, draining cwnd to floor
func (b *BBRController) DrainSustainedZero(elapsed time.Duration) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-b.cfg.WindowSize)

	// Remove all expired samples
	var fresh []rateSample
	var maxRate float64
	for _, s := range b.samples {
		if s.timestamp.After(cutoff) {
			fresh = append(fresh, s)
			if s.rateBps > maxRate {
				maxRate = s.rateBps
			}
		}
	}
	b.samples = fresh
	b.btlBwBps = maxRate

	targetCwnd := int64(b.btlBwBps * b.minRTT.Seconds() * b.cfg.Gain)
	if targetCwnd < b.cfg.MinCwndBytes {
		targetCwnd = b.cfg.MinCwndBytes
	}
	if targetCwnd > b.cfg.MaxCwndBytes {
		targetCwnd = b.cfg.MaxCwndBytes
	}

	b.cwndBytes = targetCwnd
	return b.cwndBytes
}

// Cwnd returns current congestion window in bytes
func (b *BBRController) Cwnd() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cwndBytes
}

// BtlBw returns current bottleneck bandwidth estimate in bytes/sec
func (b *BBRController) BtlBw() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.btlBwBps
}

// MinRTT returns minimum RTT estimate
func (b *BBRController) MinRTT() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.minRTT
}
