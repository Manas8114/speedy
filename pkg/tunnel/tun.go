package tunnel

import (
	"errors"
	"io"
	"sync"
)

// Device represents a network TUN interface
type Device interface {
	io.ReadWriteCloser
	Name() string
	MTU() int
}

var (
	ErrDeviceClosed = errors.New("tun device is closed")
	ErrBufferSmall  = errors.New("read buffer too small for packet")
)

// MockDevice is an in-memory thread-safe TUN device for testing and synthetic network rigs
type MockDevice struct {
	name   string
	mtu    int
	rxChan chan []byte // Packets written by kernel/app to be read by Speedy engine
	txChan chan []byte // Packets written by Speedy engine to be read by kernel/app
	closed chan struct{}
	once   sync.Once
}

// NewMockDevice creates a new simulated TUN interface with a buffer of 1024 packets
func NewMockDevice(name string, mtu int) *MockDevice {
	if mtu <= 0 {
		mtu = 1420
	}
	return &MockDevice{
		name:   name,
		mtu:    mtu,
		rxChan: make(chan []byte, 16384),
		txChan: make(chan []byte, 16384),
		closed: make(chan struct{}),
	}
}

func (m *MockDevice) Name() string {
	return m.name
}

func (m *MockDevice) MTU() int {
	return m.mtu
}

// Read reads an IP packet arriving from the simulated host/app into buf
func (m *MockDevice) Read(buf []byte) (int, error) {
	select {
	case <-m.closed:
		return 0, ErrDeviceClosed
	case pkt, ok := <-m.rxChan:
		if !ok {
			return 0, ErrDeviceClosed
		}
		if len(buf) < len(pkt) {
			return 0, ErrBufferSmall
		}
		copy(buf, pkt)
		return len(pkt), nil
	}
}

// Write writes an IP packet from Speedy engine towards the simulated host/app
func (m *MockDevice) Write(buf []byte) (int, error) {
	select {
	case <-m.closed:
		return 0, ErrDeviceClosed
	default:
		pkt := make([]byte, len(buf))
		copy(pkt, buf)
		select {
		case <-m.closed:
			return 0, ErrDeviceClosed
		case m.txChan <- pkt:
			return len(buf), nil
		}
	}
}

// Close closes the mock device
func (m *MockDevice) Close() error {
	m.once.Do(func() {
		close(m.closed)
	})
	return nil
}

// InjectPacket simulates an outgoing packet from the local OS entering the TUN device
func (m *MockDevice) InjectPacket(pkt []byte) error {
	select {
	case <-m.closed:
		return ErrDeviceClosed
	default:
		cp := make([]byte, len(pkt))
		copy(cp, pkt)
		select {
		case <-m.closed:
			return ErrDeviceClosed
		case m.rxChan <- cp:
			return nil
		}
	}
}

// CapturePacket reads a packet that Speedy engine wrote to the TUN device towards the local OS
func (m *MockDevice) CapturePacket() ([]byte, error) {
	select {
	case <-m.closed:
		return nil, ErrDeviceClosed
	case pkt, ok := <-m.txChan:
		if !ok {
			return nil, ErrDeviceClosed
		}
		return pkt, nil
	}
}
