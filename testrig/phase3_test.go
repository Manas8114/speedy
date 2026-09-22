package testrig

import (
	"testing"
	"time"

	"speedy/pkg/congestion"
	"speedy/pkg/health"
	"speedy/pkg/scheduler"
)

func TestPhase3_CongestionControlAndHoLGate(t *testing.T) {
	// Part 1: BBR Congestion Controller Verification
	cfg := congestion.BBRConfig{
		MinCwndBytes: 6000,
		MaxCwndBytes: 1000000,
		Gain:         2.0,
		WindowSize:   500 * time.Millisecond,
	}
	bbr := congestion.NewBBRController(cfg)

	// Invariant 1: Floors at minimum window
	initialCwnd := bbr.Cwnd()
	if initialCwnd != cfg.MinCwndBytes {
		t.Fatalf("Gate failure: initial cwnd %d does not floor at %d", initialCwnd, cfg.MinCwndBytes)
	}
	t.Logf("✓ GATE PASS: Invariant 1 - Initial cwnd correctly floors at %d bytes", initialCwnd)

	// Invariant 2: Caps at maximum window under massive delivery rate
	// e.g. 100MB delivered in 10ms -> high rate
	bbr.Update(100*1024*1024, 10*time.Millisecond)
	maxCwnd := bbr.Cwnd()
	if maxCwnd != cfg.MaxCwndBytes {
		t.Fatalf("Gate failure: cwnd %d did not cap at max %d", maxCwnd, cfg.MaxCwndBytes)
	}
	t.Logf("✓ GATE PASS: Invariant 2 - cwnd correctly caps at ceiling %d bytes", maxCwnd)

	// Invariant 3: Survives one bad sample without collapsing
	// Set baseline rate: 50KB in 20ms = 2.5 MB/s -> cwnd = 2.5MB * 0.02s * 2 = 100,000 bytes
	bbr.Update(50000, 20*time.Millisecond)
	stableCwnd := bbr.Cwnd()

	// Inject 1 bad sample (e.g. 0 bytes delivered)
	bbr.Update(0, 50*time.Millisecond)
	afterBadSample := bbr.Cwnd()
	if afterBadSample < stableCwnd {
		t.Fatalf("Gate failure: cwnd collapsed on single bad sample! before=%d, after=%d", stableCwnd, afterBadSample)
	}
	t.Logf("✓ GATE PASS: Invariant 3 - Windowed-max filter survived single bad sample without collapsing (cwnd=%d)", afterBadSample)

	// Invariant 4: Drains a sustained-zero-delivery path out of its window
	// Simulate waiting past window size (500ms) with zero deliveries
	time.Sleep(600 * time.Millisecond)
	drainedCwnd := bbr.DrainSustainedZero(600 * time.Millisecond)
	if drainedCwnd != cfg.MinCwndBytes {
		t.Fatalf("Gate failure: sustained zero delivery did not drain cwnd to floor! got %d, want %d", drainedCwnd, cfg.MinCwndBytes)
	}
	t.Logf("✓ GATE PASS: Invariant 4 - Sustained zero delivery successfully drained cwnd to minimum floor (%d bytes)", drainedCwnd)

	// Part 2: Head-of-Line-Blocking Avoidance Gate Test
	// Fast Path 0: 10ms RTT, Slow Path 1: 150ms RTT (15x disparity)
	t0 := health.NewPathTracker(0, "eth0_fast", "unlimited")
	t1 := health.NewPathTracker(1, "cell1_slow", "unlimited")

	now := time.Now()
	t0.OnProbeSent(1, now.Add(-10*time.Millisecond).UnixNano())
	t0.OnProbeAck(1, now.UnixNano())

	t1.OnProbeSent(2, now.Add(-150*time.Millisecond).UnixNano())
	t1.OnProbeAck(2, now.UnixNano())

	p0 := scheduler.NewPath(0, "eth0_fast", t0)
	p1 := scheduler.NewPath(1, "cell1_slow", t1)
	p0.CwndBytes = 500000
	p1.CwndBytes = 500000
	paths := []*scheduler.Path{p0, p1}

	// Naive Round-Robin distributes 50/50 regardless of latency
	rr := scheduler.NewRoundRobinScheduler()
	p0.PacketCount.Store(0)
	p1.PacketCount.Store(0)
	for i := 0; i < 200; i++ {
		_, _ = rr.SelectPath(paths, 1000)
	}
	rrFast := p0.PacketCount.Load()
	rrSlow := p1.PacketCount.Load()
	t.Logf("Round-Robin naive split on 10ms vs 150ms: Fast=%d, Slow=%d (50%% / 50%%)", rrFast, rrSlow)

	// HoL-Aware Scheduler actively favors the fast path to prevent reorder stalls
	hol := scheduler.NewHoLAwareScheduler()
	p0.PacketCount.Store(0)
	p1.PacketCount.Store(0)
	for i := 0; i < 200; i++ {
		chosen, err := hol.SelectPath(paths, 1000)
		if err != nil {
			t.Fatalf("HoL SelectPath failed: %v", err)
		}
		// Fast path delivers and decreases in flight
		chosen.InFlight.Add(-1000)
	}
	holFast := p0.PacketCount.Load()
	holSlow := p1.PacketCount.Load()
	t.Logf("HoL-Aware split on 10ms vs 150ms: Fast=%d, Slow=%d (%.1f%% to fast path)", holFast, holSlow, (float64(holFast)/200.0)*100)

	if holFast <= rrFast || holSlow >= rrSlow {
		t.Fatalf("Gate failure: HoL scheduler did not favor fast path over round-robin! holFast=%d, rrFast=%d", holFast, rrFast)
	}
	t.Logf("✓ GATE PASS: HoL-aware scheduling demonstrably favored fast path (%.1f%%) over naive 50/50 round-robin, preventing reorder buffer stalls", (float64(holFast)/200.0)*100)
}
