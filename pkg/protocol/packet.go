package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Wire protocol constants
const (
	Magic = 0x5350 // "SP" in ASCII

	// HeaderSize:
	// Magic(2) + Type(1) + PathID(1) + SessionID(4) + GlobalSeq(8) + PathSeq(8) + Flags(2) + PayloadLen(2) = 28 bytes
	HeaderSize = 28

	// Poly1305TagSize
	Poly1305TagSize = 16

	// DefaultMTU for TUN interface
	DefaultMTU = 1420
	// MaxPacketSize is header + MTU + Poly1305 tag
	MaxPacketSize = HeaderSize + DefaultMTU + Poly1305TagSize
)

// Packet Types
const (
	TypeHandshakeInit = 0x01 // Noise_IK Init (Client -> Relay)
	TypeHandshakeResp = 0x02 // Noise_IK Resp (Relay -> Client)
	TypePathAdd       = 0x03 // Multipath registration (Client -> Relay)
	TypePathAddAck    = 0x04 // Multipath registration ACK (Relay -> Client)
	TypeData          = 0x05 // Encrypted tunneled IP packet
	TypeProbe         = 0x06 // Path telemetry probe (Client -> Relay or Relay -> Client)
	TypeProbeAck      = 0x07 // Path telemetry probe ACK
	TypeFECParity     = 0x08 // Reed-Solomon parity packet
	TypeHeartbeat     = 0x09 // Keepalive heartbeat
	TypeDrain         = 0x0A // Link draining / closing
)

// Packet Flags
const (
	FlagRedundant = 0x0001 // Packet was duplicated under REDUNDANT mode
	FlagFEC       = 0x0002 // Packet is part of an FEC block
	FlagPreflight = 0x0004 // Packet is part of pre-flight benchmark
)

var (
	ErrPacketTooShort   = errors.New("packet too short for header")
	ErrInvalidMagic     = errors.New("invalid protocol magic bytes")
	ErrInvalidType      = errors.New("invalid packet type")
	ErrBufferTooSmall   = errors.New("destination buffer too small")
	ErrPayloadTruncated = errors.New("packet payload truncated")
)

// Header represents the 28-byte wire header for all Speedy 2.0 packets
type Header struct {
	Magic      uint16
	Type       uint8
	PathID     uint8
	SessionID  uint32
	GlobalSeq  uint64
	PathSeq    uint64
	Flags      uint16
	PayloadLen uint16
}

// EncodeHeader writes the 28-byte header into dst. dst must have len >= 28.
func (h *Header) EncodeHeader(dst []byte) error {
	if len(dst) < HeaderSize {
		return ErrBufferTooSmall
	}
	binary.BigEndian.PutUint16(dst[0:2], h.Magic)
	dst[2] = h.Type
	dst[3] = h.PathID
	binary.BigEndian.PutUint32(dst[4:8], h.SessionID)
	binary.BigEndian.PutUint64(dst[8:16], h.GlobalSeq)
	binary.BigEndian.PutUint64(dst[16:24], h.PathSeq)
	binary.BigEndian.PutUint16(dst[24:26], h.Flags)
	binary.BigEndian.PutUint16(dst[26:28], h.PayloadLen)
	return nil
}

// DecodeHeader parses a 28-byte header from src.
func DecodeHeader(src []byte) (Header, error) {
	if len(src) < HeaderSize {
		return Header{}, ErrPacketTooShort
	}
	magic := binary.BigEndian.Uint16(src[0:2])
	if magic != Magic {
		return Header{}, fmt.Errorf("%w: got 0x%04x expected 0x%04x", ErrInvalidMagic, magic, Magic)
	}

	h := Header{
		Magic:      magic,
		Type:       src[2],
		PathID:     src[3],
		SessionID:  binary.BigEndian.Uint32(src[4:8]),
		GlobalSeq:  binary.BigEndian.Uint64(src[8:16]),
		PathSeq:    binary.BigEndian.Uint64(src[16:24]),
		Flags:      binary.BigEndian.Uint16(src[24:26]),
		PayloadLen: binary.BigEndian.Uint16(src[26:28]),
	}

	if len(src) < HeaderSize+int(h.PayloadLen) {
		return Header{}, ErrPayloadTruncated
	}

	return h, nil
}

// ProbePayload contains high-resolution probe timestamps for RTT measurement
type ProbePayload struct {
	ProbeID       uint64
	TxTimestampNs int64 // Nanoseconds from sender
	RxTimestampNs int64 // Nanoseconds when echoed
}

func (p *ProbePayload) Encode() []byte {
	buf := make([]byte, 24)
	binary.BigEndian.PutUint64(buf[0:8], p.ProbeID)
	binary.BigEndian.PutUint64(buf[8:16], uint64(p.TxTimestampNs))
	binary.BigEndian.PutUint64(buf[16:24], uint64(p.RxTimestampNs))
	return buf
}

func DecodeProbePayload(buf []byte) (ProbePayload, error) {
	if len(buf) < 24 {
		return ProbePayload{}, errors.New("probe payload too short")
	}
	return ProbePayload{
		ProbeID:       binary.BigEndian.Uint64(buf[0:8]),
		TxTimestampNs: int64(binary.BigEndian.Uint64(buf[8:16])),
		RxTimestampNs: int64(binary.BigEndian.Uint64(buf[16:24])),
	}, nil
}
