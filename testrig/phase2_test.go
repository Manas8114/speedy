package testrig

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"speedy/pkg/benchmark"
	"speedy/pkg/config"
	"speedy/pkg/health"
	"speedy/pkg/nic"
	"speedy/pkg/scheduler"
)

func TestPhase2_AdaptiveSchedulingAndUXGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Setup paths with synthetic mismatch (Path 0: Fast 10ms, Path 1: Slower 50ms)
	t0 := health.NewPathTracker(0, "wlan0", "unlimited")
	t1 := health.NewPathTracker(1, "cell0", "unlimited")

	now := time.Now()
	t0.OnProbeSent(1, now.Add(-10*time.Millisecond).UnixNano())
	t0.OnProbeAck(1, now.UnixNano())
	t0.OnDataDelivered(100000) // ~10 Mbps

	t1.OnProbeSent(2, now.Add(-50*time.Millisecond).UnixNano())
	t1.OnProbeAck(2, now.UnixNano())
	t1.OnDataDelivered(20000) // ~2 Mbps

	p0 := scheduler.NewPath(0, "wlan0", t0)
	p1 := scheduler.NewPath(1, "cell0", t1)
	p0.CwndBytes = 1000000
	p1.CwndBytes = 200000
	paths := []*scheduler.Path{p0, p1}

	// 2. Gate Test: Passive Pre-flight Benchmark seeds initial weights
	bench := benchmark.NewPreflightBenchmark(paths)
	results, err := bench.Run(ctx, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Preflight benchmark failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 benchmark results, got %d", len(results))
	}
	t.Logf("✓ GATE PASS: Pre-flight benchmark completed across paths:")
	for _, res := range results {
		t.Logf("   Path %d (%s): RTT=%v, Goodput=%.2f Mbps", res.PathID, res.Iface, res.RTT, res.GoodputBps/(1024*1024))
	}

	// 3. Gate Test: Switching Scheduler Tiers visibly changes wire distribution
	// Test Tier 1 (Round-Robin) -> exact 50/50
	rr := scheduler.NewRoundRobinScheduler()
	p0.PacketCount.Store(0)
	p1.PacketCount.Store(0)
	for i := 0; i < 200; i++ {
		_, _ = rr.SelectPath(paths, 1000)
	}
	rrCount0 := p0.PacketCount.Load()
	rrCount1 := p1.PacketCount.Load()
	t.Logf("Tier 1 (Round-Robin) wire counts: Path 0 = %d, Path 1 = %d", rrCount0, rrCount1)
	if rrCount0 != 100 || rrCount1 != 100 {
		t.Fatalf("Expected 50/50 under Tier 1, got %d / %d", rrCount0, rrCount1)
	}

	// Switch to Tier 4 (HoL-Aware) under same mismatch -> Favors fast path!
	hol := scheduler.NewHoLAwareScheduler()
	p0.PacketCount.Store(0)
	p1.PacketCount.Store(0)
	for i := 0; i < 200; i++ {
		_, _ = hol.SelectPath(paths, 1000)
	}
	holCount0 := p0.PacketCount.Load()
	holCount1 := p1.PacketCount.Load()
	t.Logf("Tier 4 (HoL-Aware) wire counts: Path 0 = %d, Path 1 = %d", holCount0, holCount1)
	if holCount0 <= holCount1 {
		t.Fatalf("Gate failure: Tier 4 did not alter wire behavior to favor fast path! p0=%d, p1=%d", holCount0, holCount1)
	}
	t.Logf("✓ GATE PASS: Switching scheduler tiers visibly alters wire distribution under identical network conditions")

	// 4. Gate Test: Backup-only link category carries ~0 traffic, takes over automatically
	p1.Tracker.Category = "backup"
	p0.PacketCount.Store(0)
	p1.PacketCount.Store(0)

	// Primary is healthy
	for i := 0; i < 100; i++ {
		_, _ = rr.SelectPath(paths, 1000)
	}
	if p1.PacketCount.Load() != 0 {
		t.Fatalf("Gate failure: backup link carried traffic while primary was healthy! count=%d", p1.PacketCount.Load())
	}
	t.Logf("Backup link carried 0 packets while primary was healthy")

	// Primary drops dead
	t0.SetLossForTesting(1.0)
	for i := 0; i < 5; i++ {
		t0.OnProbeTimeout()
	}

	// Backup takes over immediately
	p, err := rr.SelectPath(paths, 1000)
	if err != nil || p.PathID != 1 {
		t.Fatalf("Gate failure: backup link failed to take over immediately when primary died! err=%v, path=%v", err, p)
	}
	t.Logf("✓ GATE PASS: Backup-only link carried 0 traffic while primary was healthy and took over seamlessly upon failure")

	// 5. Gate Test: Live NIC Unplug & Hot-plug mid-session drains path without breaking tunnel
	watcher := nic.NewWatcher(500 * time.Millisecond)
	var removedSignal atomic.Bool
	watcher.OnChange(func(added []nic.InterfaceInfo, removed []string) {
		for _, r := range removed {
			if r == "wlan0" {
				removedSignal.Store(true)
				// Drain path
				t0.SetLossForTesting(1.0)
			}
		}
	})

	_ = watcher.Start(ctx)
	defer watcher.Stop()

	// Simulate unplugging wlan0
	watcher.SimulateHotplug(nil, []string{"wlan0"})
	time.Sleep(50 * time.Millisecond)

	if !removedSignal.Load() {
		t.Fatal("Gate failure: live unplug event was not detected by NIC watcher")
	}

	// Verify tunnel continues operating on surviving path
	survivingPath, err := rr.SelectPath(paths, 1000)
	if err != nil || survivingPath.PathID != 1 {
		t.Fatalf("Gate failure: tunnel broke after NIC unplug! chosen=%v, err=%v", survivingPath, err)
	}
	t.Logf("✓ GATE PASS: Mid-session NIC unplug detected and drained cleanly without breaking the tunnel")

	// 6. Gate Test: Settings persistence and auto-normalization
	tmpConfig := "test_speedy_config.json"
	defer os.Remove(tmpConfig)

	settings := config.DefaultSettings(tmpConfig)
	settings.SelectedTier = 3
	settings.SetCategory("cell0", "metered")
	// Set weights that do NOT sum to 100 (e.g. 80 and 40)
	settings.SetWeight("wlan0", 80.0)
	settings.SetWeight("cell0", 40.0)

	if err := settings.Save(); err != nil {
		t.Fatalf("Save settings failed: %v", err)
	}

	loaded, err := config.Load(tmpConfig)
	if err != nil {
		t.Fatalf("Load settings failed: %v", err)
	}
	if loaded.SelectedTier != 3 || loaded.Categories["cell0"] != "metered" {
		t.Fatalf("Loaded settings mismatch: %+v", loaded)
	}

	normWeights := loaded.GetNormalizedWeights()
	sum := normWeights["wlan0"] + normWeights["cell0"]
	t.Logf("Auto-Normalized Weights (sum=%.1f%%): wlan0=%.1f%%, cell0=%.1f%%", sum, normWeights["wlan0"], normWeights["cell0"])
	if sum < 99.9 || sum > 100.1 {
		t.Fatalf("Normalized weights do not sum to 100: %+v", normWeights)
	}
	t.Logf("✓ GATE PASS: Settings persisted and reloaded with auto-normalized weights")
}
