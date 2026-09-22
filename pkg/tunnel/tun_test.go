package tunnel

import (
	"bytes"
	"testing"
)

func TestMockDevice(t *testing.T) {
	dev := NewMockDevice("mock0", 1420)
	defer dev.Close()

	if dev.Name() != "mock0" {
		t.Fatalf("unexpected name %s", dev.Name())
	}
	if dev.MTU() != 1420 {
		t.Fatalf("unexpected MTU %d", dev.MTU())
	}

	// Test inject from simulated OS and read by Speedy
	origPayload := []byte{0x45, 0x00, 0x00, 0x3c, 0x12, 0x34, 0x40, 0x00, 0x40, 0x06}
	if err := dev.InjectPacket(origPayload); err != nil {
		t.Fatalf("InjectPacket failed: %v", err)
	}

	readBuf := make([]byte, 1500)
	n, err := dev.Read(readBuf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(readBuf[:n], origPayload) {
		t.Fatalf("Read packet mismatch: got %v, want %v", readBuf[:n], origPayload)
	}

	// Test write from Speedy and capture towards simulated OS
	replyPayload := []byte{0x45, 0x00, 0x00, 0x3c, 0x56, 0x78, 0x40, 0x00, 0x40, 0x06}
	wn, err := dev.Write(replyPayload)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if wn != len(replyPayload) {
		t.Fatalf("Write wrote %d bytes, want %d", wn, len(replyPayload))
	}

	captured, err := dev.CapturePacket()
	if err != nil {
		t.Fatalf("CapturePacket failed: %v", err)
	}
	if !bytes.Equal(captured, replyPayload) {
		t.Fatalf("Captured packet mismatch: got %v, want %v", captured, replyPayload)
	}
}
