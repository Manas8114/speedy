package scheduler

import (
	"testing"
	"time"

	"speedy/pkg/health"
)

func TestRoundRobinScheduler(t *testing.T) {
	t0 := health.NewPathTracker(0, "eth0", "unlimited")
	t1 := health.NewPathTracker(1, "wlan0", "unlimited")
	p0 := NewPath(0, "eth0", t0)
	p1 := NewPath(1, "wlan0", t1)
	paths := []*Path{p0, p1}

	rr := NewRoundRobinScheduler()
	for i := 0; i < 100; i++ {
		p, err := rr.SelectPath(paths, 1000)
		if err != nil {
			t.Fatalf("SelectPath failed: %v", err)
		}
		expectedID := uint8(i % 2)
		if p.PathID != expectedID {
			t.Fatalf("expected path %d, got %d on iter %d", expectedID, p.PathID, i)
		}
	}

	if p0.PacketCount.Load() != 50 || p1.PacketCount.Load() != 50 {
		t.Fatalf("expected exact 50/50 split, got %d / %d", p0.PacketCount.Load(), p1.PacketCount.Load())
	}
}

func TestWeightedSchedulerAutoNormalize(t *testing.T) {
	t0 := health.NewPathTracker(0, "eth0", "unlimited")
	t1 := health.NewPathTracker(1, "wlan0", "unlimited")
	p0 := NewPath(0, "eth0", t0)
	p1 := NewPath(1, "wlan0", t1)
	// Weights don't need to sum to 100! (e.g. 70 and 30, or 140 and 60)
	p0.ManualWeight = 140.0
	p1.ManualWeight = 60.0
	paths := []*Path{p0, p1}

	ws := NewWeightedScheduler()
	totalPackets := 1000
	for i := 0; i < totalPackets; i++ {
		_, err := ws.SelectPath(paths, 1000)
		if err != nil {
			t.Fatalf("SelectPath failed: %v", err)
		}
	}

	c0 := p0.PacketCount.Load()
	c1 := p1.PacketCount.Load()

	// Ratio should be approx 70% (700) and 30% (300)
	ratio0 := float64(c0) / float64(totalPackets)
	ratio1 := float64(c1) / float64(totalPackets)

	t.Logf("Weighted Distribution: Path 0: %.2f%% (%d pkts), Path 1: %.2f%% (%d pkts)", ratio0*100, c0, ratio1*100, c1)
	if ratio0 < 0.65 || ratio0 > 0.75 {
		t.Fatalf("expected Path 0 near 70%%, got %.2f%%", ratio0*100)
	}
	if ratio1 < 0.25 || ratio1 > 0.35 {
		t.Fatalf("expected Path 1 near 30%%, got %.2f%%", ratio1*100)
	}
}

func TestBackupOnlyLinkCategory(t *testing.T) {
	t0 := health.NewPathTracker(0, "eth0", "unlimited")
	t1 := health.NewPathTracker(1, "cell0", "backup") // Backup-only link
	p0 := NewPath(0, "eth0", t0)
	p1 := NewPath(1, "cell0", t1)
	paths := []*Path{p0, p1}

	rr := NewRoundRobinScheduler()

	// 1. While primary is healthy, backup carries ZERO traffic
	for i := 0; i < 50; i++ {
		p, err := rr.SelectPath(paths, 1000)
		if err != nil {
			t.Fatalf("SelectPath failed: %v", err)
		}
		if p.PathID != 0 {
			t.Fatalf("expected primary path 0, got backup path %d while primary healthy", p.PathID)
		}
	}

	if p1.PacketCount.Load() != 0 {
		t.Fatalf("expected backup path packet count = 0, got %d", p1.PacketCount.Load())
	}

	// 2. Primary link dies
	t0.SetLossForTesting(1.0)
	for i := 0; i < 5; i++ {
		t0.OnProbeTimeout()
	}

	// 3. Backup path takes over immediately
	p, err := rr.SelectPath(paths, 1000)
	if err != nil {
		t.Fatalf("SelectPath failed after primary died: %v", err)
	}
	if p.PathID != 1 {
		t.Fatalf("expected backup path 1 to take over, got %d", p.PathID)
	}
	t.Logf("Backup-only failover verified: primary drained, backup activated seamlessly.")
}

func TestHoLAwareScheduler(t *testing.T) {
	t0 := health.NewPathTracker(0, "eth0", "unlimited")  // Fast path: 10ms
	t1 := health.NewPathTracker(1, "wlan0", "unlimited") // Slow path: 150ms

	now := time.Now()
	t0.OnProbeSent(1, now.Add(-10*time.Millisecond).UnixNano())
	t0.OnProbeAck(1, now.UnixNano())

	t1.OnProbeSent(2, now.Add(-150*time.Millisecond).UnixNano())
	t1.OnProbeAck(2, now.UnixNano())

	p0 := NewPath(0, "eth0", t0)
	p1 := NewPath(1, "wlan0", t1)
	p0.CwndBytes = 1000000 // Large cwnd on fast path
	paths := []*Path{p0, p1}

	hol := NewHoLAwareScheduler()
	for i := 0; i < 100; i++ {
		p, err := hol.SelectPath(paths, 1000)
		if err != nil {
			t.Fatalf("SelectPath failed: %v", err)
		}
		if p.PathID != 0 {
			t.Fatalf("expected fast path 0 when slack cwnd available, got path %d", p.PathID)
		}
	}
	t.Logf("HoL-aware verified: fast path favored 100%% over 15x slower path when capacity available.")
}
