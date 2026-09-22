package testrig

import (
	"crypto/rand"
	"math/big"
	"sync"
	"sync/atomic"
	"time"
)

// SimulatedLink simulates a physical uplink (e.g. Wi-Fi or 4G) with real network conditions
type SimulatedLink struct {
	mu           sync.Mutex
	Latency      time.Duration
	LossRate     float64 // 0.0 to 1.0 (drop probability)
	PacketsSent  atomic.Uint64
	PacketsRecv  atomic.Uint64
	PacketsDrop  atomic.Uint64
	PacketQueue  chan []byte
	deliveryChan chan []byte
	closed       chan struct{}
}

func NewSimulatedLink(latency time.Duration, lossRate float64, queueSize int) *SimulatedLink {
	if queueSize <= 0 {
		queueSize = 2048
	}
	link := &SimulatedLink{
		Latency:      latency,
		LossRate:     lossRate,
		PacketQueue:  make(chan []byte, queueSize),
		deliveryChan: make(chan []byte, queueSize),
		closed:       make(chan struct{}),
	}
	go link.loop()
	return link
}

func (l *SimulatedLink) SetLossRate(rate float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.LossRate = rate
}

func (l *SimulatedLink) SetLatency(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Latency = d
}

func (l *SimulatedLink) Send(data []byte) {
	l.PacketsSent.Add(1)

	l.mu.Lock()
	loss := l.LossRate
	lat := l.Latency
	l.mu.Unlock()

	// Simulate packet loss
	if loss > 0 {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		if float64(n.Int64())/1000.0 < loss {
			l.PacketsDrop.Add(1)
			return // Packet dropped!
		}
	}

	cp := make([]byte, len(data))
	copy(cp, data)

	go func() {
		if lat > 0 {
			time.Sleep(lat)
		}
		select {
		case <-l.closed:
			return
		case l.deliveryChan <- cp:
			l.PacketsRecv.Add(1)
		}
	}()
}

func (l *SimulatedLink) Recv() <-chan []byte {
	return l.deliveryChan
}

func (l *SimulatedLink) Close() {
	select {
	case <-l.closed:
		return
	default:
		close(l.closed)
	}
}

func (l *SimulatedLink) loop() {
	// Worker loop if needed
}
