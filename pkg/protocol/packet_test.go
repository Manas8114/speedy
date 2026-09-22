package protocol

import (
	"testing"
)

func TestHeaderEncodeDecode(t *testing.T) {
	orig := Header{
		Magic:      Magic,
		Type:       TypeData,
		PathID:     2,
		SessionID:  0x12345678,
		GlobalSeq:  9876543210,
		PathSeq:    123456,
		Flags:      FlagRedundant | FlagFEC,
		PayloadLen: 512,
	}

	buf := make([]byte, HeaderSize+512)
	for i := 0; i < 512; i++ {
		buf[HeaderSize+i] = byte(i % 256)
	}

	err := orig.EncodeHeader(buf)
	if err != nil {
		t.Fatalf("EncodeHeader failed: %v", err)
	}

	decoded, err := DecodeHeader(buf)
	if err != nil {
		t.Fatalf("DecodeHeader failed: %v", err)
	}

	if decoded != orig {
		t.Fatalf("Decoded header mismatch:\ngot:  %+v\nwant: %+v", decoded, orig)
	}
}

func TestProbePayload(t *testing.T) {
	orig := ProbePayload{
		ProbeID:       42,
		TxTimestampNs: 1727000000123456789,
		RxTimestampNs: 1727000000133456789,
	}

	encoded := orig.Encode()
	decoded, err := DecodeProbePayload(encoded)
	if err != nil {
		t.Fatalf("DecodeProbePayload failed: %v", err)
	}

	if decoded != orig {
		t.Fatalf("Decoded probe payload mismatch: got %+v, want %+v", decoded, orig)
	}
}

func TestCorruptedHeader(t *testing.T) {
	buf := make([]byte, HeaderSize)
	// Invalid magic
	buf[0] = 0xFF
	buf[1] = 0xFF

	_, err := DecodeHeader(buf)
	if err == nil {
		t.Fatal("expected error on invalid magic, got nil")
	}

	// Truncated buffer
	_, err = DecodeHeader(buf[:10])
	if err == nil {
		t.Fatal("expected error on truncated header, got nil")
	}
}
