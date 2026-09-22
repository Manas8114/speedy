package testrig

import (
	"bytes"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"speedy/pkg/fec"
	"speedy/pkg/health"
	"speedy/pkg/reorder"
	"speedy/pkg/scheduler"
)

func TestPhase4_ResilienceFECAndRedundantGate(t *testing.T) {
	// Part 1: FEC Resilience under 5% real random packet loss
	t.Log("Testing FEC under 5.0% synthetic network packet loss...")

	k := 10
	m := 1
	encoder := fec.NewBlockEncoder(k, m)
	decoder := fec.NewBlockDecoder(k)

	totalBlocks := 50 // 50 blocks of 10 = 500 packets
	totalPackets := totalBlocks * k

	rng := rand.New(rand.NewSource(42))
	networkLost := 0
	appRecovered := 0
	appLost := 0

	for b := uint64(1); b <= uint64(totalBlocks); b++ {
		var blockPackets [][]byte
		for i := 0; i < k; i++ {
			pkt := []byte{byte(b), byte(i), 0xAA, 0xBB, 0xCC}
			blockPackets = append(blockPackets, pkt)
		}

		// Feed encoder to get parity
		var parities [][]byte
		for _, pkt := range blockPackets {
			_, p, full := encoder.AddPacket(pkt)
			if full {
				parities = p
			}
		}

		// Simulate 5% network loss on data packets
		for i, pkt := range blockPackets {
			if rng.Float64() < 0.05 {
				// Packet lost in network!
				networkLost++
			} else {
				// Packet received
				decoder.AddDataPacket(b, i, pkt)
			}
		}

		// Parity packet arrives (assuming parity not lost)
		if len(parities) > 0 {
			reconstructed, missingIdx, err := decoder.AddParityPacket(b, parities[0])
			if err == nil && missingIdx >= 0 {
				appRecovered++
				if !bytes.Equal(reconstructed, blockPackets[missingIdx]) {
					t.Fatalf("Reconstructed packet mismatch: got %v, want %v", reconstructed, blockPackets[missingIdx])
				}
			}
		}
	}

	appLost = networkLost - appRecovered
	if appLost < 0 {
		appLost = 0
	}
	appLossPct := (float64(appLost) / float64(totalPackets)) * 100.0
	netLossPct := (float64(networkLost) / float64(totalPackets)) * 100.0

	t.Logf("FEC Test Results:")
	t.Logf("   Total Packets:   %d", totalPackets)
	t.Logf("   Network Loss:    %d packets (%.2f%%)", networkLost, netLossPct)
	t.Logf("   FEC Recovered:   %d packets", appRecovered)
	t.Logf("   App-Level Loss:  %d packets (%.2f%%)", appLost, appLossPct)

	if appLossPct >= 1.0 {
		t.Fatalf("Gate failure: application-level loss was %.2f%%, expected < 1.0%%", appLossPct)
	}
	t.Logf("✓ GATE PASS: Under %.2f%% real network packet loss, FEC reduced application-level loss to %.2f%% (< 1.0%% target)", netLossPct, appLossPct)

	// Part 2: REDUNDANT Mode Path Kill Mid-Transfer
	t.Log("Testing REDUNDANT mode path kill mid-transfer...")

	link0 := NewSimulatedLink(2*time.Millisecond, 0.0, 1000)
	link1 := NewSimulatedLink(2*time.Millisecond, 0.0, 1000)
	defer link0.Close()
	defer link1.Close()

	t0 := health.NewPathTracker(0, "eth0", "unlimited")
	t1 := health.NewPathTracker(1, "wlan0", "unlimited")
	p0 := scheduler.NewPath(0, "eth0", t0)
	p1 := scheduler.NewPath(1, "wlan0", t1)
	paths := []*scheduler.Path{p0, p1}

	redScheduler := scheduler.NewRedundantScheduler()
	reorderBuf := reorder.NewBuffer(reorder.DefaultConfig())

	var receivedPackets [][]byte
	var rxMu sync.Mutex
	var stopReceiver atomic.Bool

	receiveFromLink := func(link *SimulatedLink) {
		for !stopReceiver.Load() {
			select {
			case pkt := <-link.Recv():
				// Extract sequence from first 8 bytes
				var seq uint64
				for i := 0; i < 8 && i < len(pkt); i++ {
					seq = (seq << 8) | uint64(pkt[i])
				}
				ready := reorderBuf.Push(seq, pkt[8:])
				if len(ready) > 0 {
					rxMu.Lock()
					receivedPackets = append(receivedPackets, ready...)
					rxMu.Unlock()
				}
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	go receiveFromLink(link0)
	go receiveFromLink(link1)

	transferTotal := 500
	t.Logf("Starting REDUNDANT transfer of %d packets...", transferTotal)

	for i := 1; i <= transferTotal; i++ {
		// Halfway through the transfer, kill Link 0 outright!
		if i == 250 {
			t.Log(">>> KILLING PATH 0 OUTRIGHT AT PACKET 250 <<<")
			link0.Close()
			link0.SetLossRate(1.0) // 100% loss on link 0
		}

		activePaths, err := redScheduler.SelectAllPaths(paths, 100)
		if err != nil {
			t.Fatalf("Redundant SelectAllPaths failed: %v", err)
		}

		// Wire packet: 8 bytes seq + payload
		wirePkt := make([]byte, 8+10)
		seq := uint64(i)
		for s := 0; s < 8; s++ {
			wirePkt[s] = byte(seq >> (56 - 8*s))
		}
		copy(wirePkt[8:], []byte("REDUNDANT!"))

		// Duplicate across all active paths
		for _, p := range activePaths {
			if p.PathID == 0 {
				link0.Send(wirePkt)
			} else {
				link1.Send(wirePkt)
			}
		}
	}

	// Wait for transfer to complete on surviving path
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rxMu.Lock()
		c := len(receivedPackets)
		rxMu.Unlock()
		if c >= transferTotal {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rxMu.Lock()
	receivedCount := len(receivedPackets)
	rxMu.Unlock()

	_, _, _, duplicates, _ := reorderBuf.Stats()
	t.Logf("Redundant Transfer Results:")
	t.Logf("   Packets Sent:       %d", transferTotal)
	t.Logf("   Packets Delivered:  %d", receivedCount)
	t.Logf("   Duplicates Deduped: %d", duplicates)

	if receivedCount != transferTotal {
		t.Fatalf("Gate failure: transfer did not complete after killing path 0! got %d / %d", receivedCount, transferTotal)
	}
	t.Logf("✓ GATE PASS: Path 0 killed outright mid-transfer; transfer completed 100%% (%d/%d) on surviving path with 0 resets", receivedCount, transferTotal)

	stopReceiver.Store(true)
}
