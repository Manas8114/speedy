package pmtu

import (
	"context"
	"time"
)

// Prober performs guess-and-verify Path MTU Discovery without relying on ICMP (Glorytun style)
type Prober struct {
	MinMTU int
	MaxMTU int
}

func NewProber(minMTU, maxMTU int) *Prober {
	if minMTU <= 0 {
		minMTU = 1280 // IPv6 minimum MTU
	}
	if maxMTU <= 0 {
		maxMTU = 1500 // Standard Ethernet MTU
	}
	return &Prober{
		MinMTU: minMTU,
		MaxMTU: maxMTU,
	}
}

// DiscoverMTU runs a binary search probe between min and max MTU using a verification probe function
func (p *Prober) DiscoverMTU(ctx context.Context, probeFunc func(size int) bool) int {
	low := p.MinMTU
	high := p.MaxMTU
	best := low

	for low <= high {
		select {
		case <-ctx.Done():
			return best
		default:
		}

		mid := (low + high) / 2
		if probeFunc(mid) {
			best = mid
			low = mid + 1 // Try larger MTU
		} else {
			high = mid - 1 // Too large, step down
		}
		time.Sleep(5 * time.Millisecond)
	}

	return best
}
