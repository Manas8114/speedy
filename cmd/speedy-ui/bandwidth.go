package main

import (
	"sync"
	"time"
)

// BandwidthTracker calculates live per-interface throughput and aggregate rate
type BandwidthTracker struct {
	mu            sync.Mutex
	lastSamples   map[string]uint64
	lastTime      time.Time
	currentRates  map[string]float64 // ifaceName -> Mbps
	aggregateRate float64           // Mbps
}

// NewBandwidthTracker initializes the tracker with an initial baseline sample
func NewBandwidthTracker() *BandwidthTracker {
	bt := &BandwidthTracker{
		lastSamples:  make(map[string]uint64),
		currentRates: make(map[string]float64),
		lastTime:     time.Now(),
	}
	bt.lastSamples = getPlatformInterfaceBytes()
	return bt
}

// Sample computes live delta Mbps per interface
func (bt *BandwidthTracker) Sample() (map[string]float64, float64) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(bt.lastTime).Seconds()
	if elapsed < 0.25 {
		return bt.currentRates, bt.aggregateRate
	}

	samples := getPlatformInterfaceBytes()
	rates := make(map[string]float64)
	var totalRate float64

	for name, currentBytes := range samples {
		if prevBytes, exists := bt.lastSamples[name]; exists && currentBytes >= prevBytes {
			deltaBytes := currentBytes - prevBytes
			rateMbps := (float64(deltaBytes) * 8.0) / (elapsed * 1_000_000.0)
			rates[name] = rateMbps
			totalRate += rateMbps
		} else {
			rates[name] = 0.0
		}
	}

	bt.lastSamples = samples
	bt.lastTime = now
	bt.currentRates = rates
	bt.aggregateRate = totalRate

	return rates, totalRate
}
