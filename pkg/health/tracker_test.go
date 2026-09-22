package health

import (
	"testing"
	"time"
)

func TestPathTrackerRTT(t *testing.T) {
	tracker := NewPathTracker(0, "eth0", "unlimited")

	now := time.Now()
	tracker.OnProbeSent(101, now.Add(-20*time.Millisecond).UnixNano())
	rtt := tracker.OnProbeAck(101, now.UnixNano())

	if rtt <= 0 {
		t.Fatalf("expected positive RTT, got %v", rtt)
	}

	state, srtt, loss, _, minRTT := tracker.Snapshot()
	if state != StateActive {
		t.Fatalf("expected StateActive, got %s", state)
	}
	if srtt <= 0 || minRTT <= 0 {
		t.Fatalf("invalid RTT values: srtt=%v, minRTT=%v", srtt, minRTT)
	}
	if loss != 0.0 {
		t.Fatalf("expected 0.0 loss, got %f", loss)
	}
}

func TestPathTrackerIdlePathRecovery(t *testing.T) {
	tracker := NewPathTracker(1, "wlan0", "unlimited")

	// 1. Simulate path degradation via loss
	tracker.SetLossForTesting(0.40) // 40% loss
	state, _, loss, _, _ := tracker.Snapshot()
	if state != StateDegraded {
		t.Fatalf("expected StateDegraded, got %s (loss=%f)", state, loss)
	}

	// 2. Path becomes IDLE (0 DATA packets flowing!)
	// Only periodic probes succeed. Verify that each OnProbeAck feeds instLoss=0 into LossEWMA!
	now := time.Now()
	for i := uint64(1); i <= 30; i++ {
		tracker.OnProbeSent(i, now.Add(-10*time.Millisecond).UnixNano())
		tracker.OnProbeAck(i, now.UnixNano())
	}

	recoveredState, _, recoveredLoss, _, _ := tracker.Snapshot()
	if recoveredLoss > 0.05 {
		t.Fatalf("idle path loss did not recover, still %f", recoveredLoss)
	}
	if recoveredState != StateActive {
		t.Fatalf("expected idle path to recover to StateActive, got %s (loss=%f)", recoveredState, recoveredLoss)
	}
}

func TestPathTrackerDeadAndDegraded(t *testing.T) {
	tracker := NewPathTracker(2, "cellular0", "metered")

	// 3 probe timeouts -> Degraded
	for i := 0; i < 3; i++ {
		tracker.OnProbeTimeout()
	}
	state, _, _, _, _ := tracker.Snapshot()
	if state != StateDegraded {
		t.Fatalf("expected StateDegraded after 3 timeouts, got %s", state)
	}

	// 2 more timeouts (total 5) -> Dead
	for i := 0; i < 2; i++ {
		tracker.OnProbeTimeout()
	}
	state, _, _, _, _ = tracker.Snapshot()
	if state != StateDead {
		t.Fatalf("expected StateDead after 5 timeouts, got %s", state)
	}
}
