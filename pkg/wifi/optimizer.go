package wifi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"speedy/pkg/health"
	"speedy/pkg/scheduler"
)

// Optimizer monitors reachable Wi-Fi candidates, benchmarks them, and optimizes path selection
type Optimizer struct {
	mu            sync.RWMutex
	cfg           Config
	candidates    []WiFiCandidate
	activeBSSID   string
	lastBenchmark time.Time
	onDrift       func(oldCandidate, newCandidate WiFiCandidate)
}

func NewOptimizer(cfg Config) *Optimizer {
	return &Optimizer{
		cfg: cfg,
	}
}

func (o *Optimizer) SetMode(mode OptimizerMode, pinBSSID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cfg.Mode = mode
	if pinBSSID != "" {
		o.cfg.PinnedBSSID = pinBSSID
	}
}

func (o *Optimizer) Exclude(ssidOrBand string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.Contains(strings.ToLower(ssidOrBand), "ghz") {
		o.cfg.ExcludedBands = append(o.cfg.ExcludedBands, ssidOrBand)
	} else {
		o.cfg.ExcludedSSIDs = append(o.cfg.ExcludedSSIDs, ssidOrBand)
	}
}

// RefreshAndEstimate scans OS visible networks, derives goodput estimates, and ranks candidates
func (o *Optimizer) RefreshAndEstimate(ctx context.Context) ([]WiFiCandidate, *WiFiCandidate, error) {
	visible, err := ScanVisibleNetworks(ctx)
	if err != nil && len(visible) == 0 {
		return nil, nil, fmt.Errorf("Wi-Fi network scan failed: %w", err)
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	// Filter excluded SSIDs and Bands
	var filtered []WiFiCandidate
	for _, cand := range visible {
		if o.isExcludedLocked(cand) {
			continue
		}
		filtered = append(filtered, cand)
	}

	if len(filtered) == 0 {
		filtered = visible // Fallback if everything was excluded
	}

	// Estimate reachable candidates
	for i := range filtered {
		cand := &filtered[i]
		cand.EstimatedThroughput, cand.EstimatedRTT = o.estimateCandidate(cand)
		cand.CompositeScore = o.calculateScore(cand)
	}

	// Sort by CompositeScore descending
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CompositeScore > filtered[j].CompositeScore
	})

	o.candidates = filtered
	o.lastBenchmark = time.Now()

	// Select candidate based on Mode
	var selected *WiFiCandidate
	switch o.cfg.Mode {
	case ModeManualPin:
		for i := range filtered {
			if strings.EqualFold(filtered[i].BSSID, o.cfg.PinnedBSSID) ||
				strings.EqualFold(filtered[i].SSID, o.cfg.PinnedBSSID) {
				selected = &filtered[i]
				break
			}
		}
		if selected == nil && len(filtered) > 0 {
			selected = &filtered[0]
		}
	case ModeAuto:
		if len(filtered) > 0 {
			selected = &filtered[0] // Top ranked candidate
		}
	default:
		if len(filtered) > 0 {
			selected = &filtered[0]
		}
	}

	if selected != nil {
		o.activeBSSID = selected.BSSID
	}

	return filtered, selected, nil
}

// RefreshAndBenchmark is a backward-compatible alias for RefreshAndEstimate.
func (o *Optimizer) RefreshAndBenchmark(ctx context.Context) ([]WiFiCandidate, *WiFiCandidate, error) {
	return o.RefreshAndEstimate(ctx)
}

func (o *Optimizer) isExcludedLocked(c WiFiCandidate) bool {
	for _, excSSID := range o.cfg.ExcludedSSIDs {
		if strings.EqualFold(c.SSID, excSSID) {
			return true
		}
	}
	for _, excBand := range o.cfg.ExcludedBands {
		if strings.EqualFold(c.Band, excBand) {
			return true
		}
	}
	return false
}

func (o *Optimizer) calculateScore(c *WiFiCandidate) float64 {
	// Standard Score (0.0 to 1.0)
	standardScore := 0.5
	switch c.Standard {
	case WiFi7:
		standardScore = 1.0
	case WiFi6E:
		standardScore = 0.9
	case WiFi6:
		standardScore = 0.8
	case WiFi5:
		standardScore = 0.6
	case WiFi4:
		standardScore = 0.3
	}

	// Band Score: 6 GHz = 1.0, 5 GHz = 0.85, 2.4 GHz = 0.4
	bandScore := 0.5
	if strings.Contains(strings.ToLower(c.Band), "6") {
		bandScore = 1.0
	} else if strings.Contains(strings.ToLower(c.Band), "5") {
		bandScore = 0.85
	} else if strings.Contains(strings.ToLower(c.Band), "2.4") {
		bandScore = 0.4
	}

	// Signal Score: signal% / 100
	signalScore := float64(c.SignalPercent) / 100.0
	if signalScore > 1.0 {
		signalScore = 1.0
	}

	// Goodput Score: scaled to 1000 Mbps
	goodputScore := c.EstimatedThroughput / 1000.0
	if goodputScore > 1.0 {
		goodputScore = 1.0
	}

	// Composite weighting: 35% standard, 25% band, 15% signal, 25% estimated goodput
	return (standardScore * 0.35) + (bandScore * 0.25) + (signalScore * 0.15) + (goodputScore * 0.25)
}

// estimateCandidate derives goodput from driver-reported link rate. This is NOT a network measurement.
func (o *Optimizer) estimateCandidate(c *WiFiCandidate) (float64, time.Duration) {
	// Calculate baseline from driver reported link rate + signal
	baseRate := c.TxRateMbps
	if baseRate <= 0 {
		baseRate = float64(c.SignalPercent) * 6.0
	}
	// Real-world goodput is typically 65-75% of physical layer link rate
	goodput := baseRate * 0.72

	rtt := 15 * time.Millisecond
	if strings.Contains(c.Band, "6") {
		rtt = 4 * time.Millisecond
	} else if strings.Contains(c.Band, "5") {
		rtt = 10 * time.Millisecond
	} else if strings.Contains(c.Band, "2.4") {
		rtt = 28 * time.Millisecond
	}

	return goodput, rtt
}

// CheckDrift detects if active Wi-Fi 6E/7 link degraded due to distance/interference
func (o *Optimizer) CheckDrift(currentCandidate *WiFiCandidate, currentGoodput float64) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if currentCandidate == nil {
		return false
	}
	base := currentCandidate.EstimatedThroughput
	if base <= 0 {
		base = currentCandidate.TxRateMbps * 0.72
	}
	if base <= 0 {
		return false
	}

	// If throughput dropped by more than 25% from baseline rate, drift detected!
	dropPct := (base - currentGoodput) / base
	return dropPct > 0.25
}

// Candidates returns current ranked candidates
func (o *Optimizer) Candidates() []WiFiCandidate {
	o.mu.RLock()
	defer o.mu.RUnlock()
	res := make([]WiFiCandidate, len(o.candidates))
	copy(res, o.candidates)
	return res
}

// CreateOptimizerPath wraps the optimal candidate into a scheduler.Path
func (o *Optimizer) CreateOptimizerPath(pathID uint8, c *WiFiCandidate) *scheduler.Path {
	tput := c.EstimatedThroughput
	if tput <= 0 {
		tput = c.TxRateMbps * 0.72
	}
	if tput <= 0 {
		tput = 100.0 // 100 Mbps default
	}
	rtt := c.EstimatedRTT
	if rtt <= 0 {
		rtt = 10 * time.Millisecond
	}

	tracker := health.NewPathTracker(pathID, fmt.Sprintf("wifi_%s", c.SSID), "unlimited")
	now := time.Now()
	tracker.OnProbeSent(1, now.Add(-rtt).UnixNano())
	tracker.OnProbeAck(1, now.UnixNano())
	tracker.OnDataDelivered(int(tput * 1024 * 1024 / 8))

	p := scheduler.NewPath(pathID, c.SSID, tracker)
	p.CwndBytes = int64(tput * 1024 * 1024 / 8 * 0.05) // 50ms BDP
	if p.CwndBytes < 65536 {
		p.CwndBytes = 65536
	}
	return p
}
