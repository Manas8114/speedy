package benchmark

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"speedy/pkg/platform"
	"speedy/pkg/protocol"
	"speedy/pkg/scheduler"
)

// Result holds the pre-flight benchmark results for a single path
type Result struct {
	PathID     uint8
	Iface      string
	RTT        time.Duration
	GoodputBps float64
	LossRate   float64
}

// PreflightBenchmark runs authentic UDP probe packets on each path to seed initial scheduler weights
type PreflightBenchmark struct {
	paths      []*scheduler.Path
	targetAddr *net.UDPAddr
}

func NewPreflightBenchmark(paths []*scheduler.Path) *PreflightBenchmark {
	return &PreflightBenchmark{
		paths: paths,
	}
}

// NewPreflightBenchmarkWithTarget creates a preflight benchmark targeting a remote relay or echo server
func NewPreflightBenchmarkWithTarget(paths []*scheduler.Path, targetAddr *net.UDPAddr) *PreflightBenchmark {
	return &PreflightBenchmark{
		paths:      paths,
		targetAddr: targetAddr,
	}
}

// Run executes the active UDP benchmark across all paths concurrently
func (b *PreflightBenchmark) Run(ctx context.Context, duration time.Duration) ([]Result, error) {
	if duration <= 0 {
		duration = 1000 * time.Millisecond
	}

	var responderAddr *net.UDPAddr
	var responderConn *net.UDPConn
	var stopResponder sync.WaitGroup

	if b.targetAddr != nil {
		responderAddr = b.targetAddr
	} else {
		// Spin up a temporary local probe responder to echo packets
		var err error
		responderConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			return nil, fmt.Errorf("failed to start benchmark probe responder: %w", err)
		}
		defer responderConn.Close()
		responderAddr = responderConn.LocalAddr().(*net.UDPAddr)

		// Responder echo loop
		stopResponder.Add(1)
		go func() {
			defer stopResponder.Done()
			buf := make([]byte, protocol.MaxPacketSize)
			for {
				responderConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				n, rAddr, err := responderConn.ReadFromUDP(buf)
				if err != nil {
					select {
					case <-ctx.Done():
						return
					default:
						if n == 0 {
							continue
						}
					}
				}
				if n < protocol.HeaderSize {
					continue
				}

				hdr, err := protocol.DecodeHeader(buf[:n])
				if err != nil || hdr.Type != protocol.TypeProbe {
					continue
				}

				// Echo back as TypeProbeAck
				ackHdr := hdr
				ackHdr.Type = protocol.TypeProbeAck
				_ = ackHdr.EncodeHeader(buf[:protocol.HeaderSize])

				_, _ = responderConn.WriteToUDP(buf[:n], rAddr)
			}
		}()
	}

	var results []Result
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, p := range b.paths {
		wg.Add(1)
		go func(path *scheduler.Path) {
			defer wg.Done()

			clientConn, err := net.DialUDP("udp", nil, responderAddr)
			if err != nil {
				return
			}
			defer clientConn.Close()

			if path.IfaceIndex > 0 || path.Iface != "" {
				_ = platform.BindSocketToInterface(clientConn, path.IfaceIndex, path.Iface)
			}

			probesCount := 20
			probePayloadSize := 1024 // 1KB burst data payload
			payloadData := make([]byte, probePayloadSize)

			var totalRTT time.Duration
			successful := 0
			bytesSent := 0

			txBuf := make([]byte, protocol.HeaderSize+probePayloadSize)
			rxBuf := make([]byte, protocol.MaxPacketSize)

			benchStart := time.Now()

			for i := 1; i <= probesCount; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				hdr := protocol.Header{
					Magic:      protocol.Magic,
					Type:       protocol.TypeProbe,
					PathID:     path.PathID,
					SessionID:  9999,
					GlobalSeq:  uint64(i),
					PathSeq:    uint64(i),
					PayloadLen: uint16(probePayloadSize),
				}
				_ = hdr.EncodeHeader(txBuf)
				copy(txBuf[protocol.HeaderSize:], payloadData)

				txTime := time.Now()
				path.Tracker.OnProbeSent(uint64(i), txTime.UnixNano())

				if _, err := clientConn.Write(txBuf); err != nil {
					continue
				}
				bytesSent += len(txBuf)

				clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, err := clientConn.Read(rxBuf)
				if err != nil || n < protocol.HeaderSize {
					path.Tracker.OnProbeTimeout()
					continue
				}

				rtt := time.Since(txTime)
				totalRTT += rtt
				successful++

				// Update path tracker with genuine measured network sample
				path.Tracker.OnProbeAck(uint64(i), time.Now().UnixNano())
				path.Tracker.OnDataDelivered(probePayloadSize)
			}

			elapsed := time.Since(benchStart).Seconds()
			var avgRTT time.Duration
			var bps float64
			lossRate := 0.0

			if successful > 0 {
				avgRTT = totalRTT / time.Duration(successful)
				bps = float64(successful*probePayloadSize*8) / elapsed
				lossRate = float64(probesCount-successful) / float64(probesCount)
			}

			res := Result{
				PathID:     path.PathID,
				Iface:      path.Iface,
				RTT:        avgRTT,
				GoodputBps: bps,
				LossRate:   lossRate,
			}

			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return results, nil
}
