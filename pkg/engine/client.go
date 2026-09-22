package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"speedy/pkg/crypto"
	"speedy/pkg/protocol"
	"speedy/pkg/routing"
	"speedy/pkg/tunnel"
)

// ClientConfig holds configuration for the Speedy client engine
type ClientConfig struct {
	RelayAddr       string
	RelayPubKeyHex  string
	TunName         string
	TunDevice       tunnel.Device // If provided, uses this device; else creates one
	InstallDefRoute bool
	RouteManager    *routing.Manager
	StaticKey       crypto.KeyPair
}

// ClientEngine manages the client tunnel connection
type ClientEngine struct {
	cfg          ClientConfig
	tun          tunnel.Device
	udpConn      *net.UDPConn
	relayAddr    *net.UDPAddr
	session      *crypto.HandshakeSession
	sessionID    uint32
	assignedIP   string
	globalSeq    atomic.Uint64
	pathSeq      atomic.Uint64
	bytesSent    atomic.Uint64
	bytesRecv    atomic.Uint64
	packetsSent  atomic.Uint64
	packetsRecv  atomic.Uint64
	running      atomic.Bool
	stopCh       chan struct{}
	wg           sync.WaitGroup
	replayFilter *crypto.ReplayFilter
}

func NewClientEngine(cfg ClientConfig) (*ClientEngine, error) {
	if cfg.TunName == "" {
		cfg.TunName = "speedy-client0"
	}
	return &ClientEngine{
		cfg:          cfg,
		stopCh:       make(chan struct{}),
		replayFilter: crypto.NewReplayFilter(),
	}, nil
}

// Start connects to the relay, performs Noise_IK handshake, sets up routes, and starts tunnel loops
func (c *ClientEngine) Start(ctx context.Context) error {
	rAddr, err := net.ResolveUDPAddr("udp", c.cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("resolve relay addr %s failed: %w", c.cfg.RelayAddr, err)
	}
	c.relayAddr = rAddr

	// Parse Relay Public Key
	relayPub, err := crypto.KeyFromHex(c.cfg.RelayPubKeyHex)
	if err != nil {
		return fmt.Errorf("invalid relay public key: %w", err)
	}

	// Dial UDP
	conn, err := net.DialUDP("udp", nil, rAddr)
	if err != nil {
		return fmt.Errorf("dial UDP to %s failed: %w", rAddr, err)
	}
	c.udpConn = conn

	// If no static key, generate one
	if c.cfg.StaticKey.Public == (crypto.Key{}) {
		k, err := crypto.GenerateKeyPair()
		if err != nil {
			return err
		}
		c.cfg.StaticKey = k
	}

	// Perform Noise_IK Handshake
	initiator, err := crypto.NewInitiator(c.cfg.StaticKey, relayPub)
	if err != nil {
		return fmt.Errorf("initiate noise failed: %w", err)
	}

	clientPayload := []byte("SPEEDY_V2_CLIENT_INIT")
	initBytes, err := initiator.CreateInitMessage(clientPayload)
	if err != nil {
		return fmt.Errorf("create handshake init failed: %w", err)
	}

	// Send Handshake Init packet
	initHdr := protocol.Header{
		Magic:      protocol.Magic,
		Type:       protocol.TypeHandshakeInit,
		PathID:     0,
		PayloadLen: uint16(len(initBytes)),
	}
	pktBuf := make([]byte, protocol.HeaderSize+len(initBytes))
	_ = initHdr.EncodeHeader(pktBuf)
	copy(pktBuf[protocol.HeaderSize:], initBytes)

	if _, err := conn.Write(pktBuf); err != nil {
		return fmt.Errorf("send handshake init failed: %w", err)
	}

	// Read Handshake Resp with timeout
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	respBuf := make([]byte, 1024)
	n, err := conn.Read(respBuf)
	if err != nil {
		return fmt.Errorf("read handshake response timed out: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	respHdr, err := protocol.DecodeHeader(respBuf[:n])
	if err != nil {
		return fmt.Errorf("decode handshake resp header failed: %w", err)
	}
	if respHdr.Type != protocol.TypeHandshakeResp {
		return fmt.Errorf("expected HandshakeResp (0x02), got 0x%02x", respHdr.Type)
	}

	respPayload := respBuf[protocol.HeaderSize : protocol.HeaderSize+int(respHdr.PayloadLen)]
	session, decPayload, err := initiator.ProcessRespMessage(respPayload)
	if err != nil {
		return fmt.Errorf("process handshake response failed: %w", err)
	}

	c.session = session
	c.sessionID = respHdr.SessionID
	c.assignedIP = string(decPayload) // Format: "10.254.1.2/24"

	// Setup TUN Device
	if c.cfg.TunDevice != nil {
		c.tun = c.cfg.TunDevice
	} else {
		dev, err := tunnel.CreateTUN(c.cfg.TunName, protocol.DefaultMTU)
		if err != nil {
			return fmt.Errorf("create TUN %s failed: %w", c.cfg.TunName, err)
		}
		c.tun = dev
	}

	// Install routing-loop guard and default route if requested
	if c.cfg.InstallDefRoute && c.cfg.RouteManager != nil {
		relayHost := rAddr.IP.String()
		if err := c.cfg.RouteManager.PinRelayRoute(relayHost, "", ""); err != nil {
			return fmt.Errorf("routing loop guard failed: %w", err)
		}
		tunIPStr := c.assignedIP
		if ip, _, err := net.ParseCIDR(c.assignedIP); err == nil {
			tunIPStr = ip.String()
		}
		if err := c.cfg.RouteManager.InstallDefaultRoute(c.tun.Name(), tunIPStr); err != nil {
			return fmt.Errorf("install default route failed: %w", err)
		}
	}

	c.running.Store(true)

	// Launch worker loops
	c.wg.Add(2)
	go c.tunToNetLoop()
	go c.netToTunLoop()

	return nil
}

func (c *ClientEngine) Stop() error {
	if !c.running.Swap(false) {
		return nil
	}
	close(c.stopCh)
	if c.udpConn != nil {
		c.udpConn.Close()
	}
	if c.tun != nil {
		c.tun.Close()
	}
	if c.cfg.InstallDefRoute && c.cfg.RouteManager != nil {
		_ = c.cfg.RouteManager.RestoreRoutes()
	}
	c.wg.Wait()
	return nil
}

func (c *ClientEngine) tunToNetLoop() {
	defer c.wg.Done()
	readBuf := make([]byte, protocol.DefaultMTU)
	txBuf := make([]byte, protocol.MaxPacketSize)

	for c.running.Load() {
		n, err := c.tun.Read(readBuf)
		if err != nil {
			if errors.Is(err, tunnel.ErrDeviceClosed) || !c.running.Load() {
				return
			}
			continue
		}
		if n == 0 {
			continue
		}

		gSeq := c.globalSeq.Add(1)
		pSeq := c.pathSeq.Add(1)

		// Encrypt payload
		ad := make([]byte, protocol.HeaderSize)
		hdr := protocol.Header{
			Magic:      protocol.Magic,
			Type:       protocol.TypeData,
			PathID:     0,
			SessionID:  c.sessionID,
			GlobalSeq:  gSeq,
			PathSeq:    pSeq,
			Flags:      0,
			PayloadLen: uint16(n + protocol.Poly1305TagSize),
		}
		_ = hdr.EncodeHeader(ad)

		ciphertext := c.session.TxState.Encrypt(txBuf[protocol.HeaderSize:protocol.HeaderSize], 0, pSeq, readBuf[:n], ad)
		copy(txBuf[:protocol.HeaderSize], ad)

		packetLen := protocol.HeaderSize + len(ciphertext)
		wn, err := c.udpConn.Write(txBuf[:packetLen])
		if err == nil {
			c.bytesSent.Add(uint64(wn))
			c.packetsSent.Add(1)
		}
	}
}

func (c *ClientEngine) netToTunLoop() {
	defer c.wg.Done()
	rxBuf := make([]byte, protocol.MaxPacketSize)
	plainBuf := make([]byte, protocol.DefaultMTU)

	for c.running.Load() {
		n, err := c.udpConn.Read(rxBuf)
		if err != nil {
			if !c.running.Load() {
				return
			}
			continue
		}
		if n < protocol.HeaderSize {
			continue
		}

		hdr, err := protocol.DecodeHeader(rxBuf[:n])
		if err != nil {
			continue
		}

		if hdr.Type != protocol.TypeData {
			continue
		}

		// Replay check
		if !c.replayFilter.CheckAndSet(hdr.PathSeq) {
			continue
		}

		ad := rxBuf[:protocol.HeaderSize]
		ciphertext := rxBuf[protocol.HeaderSize:n]

		plaintext, err := c.session.RxState.Decrypt(plainBuf[:0], hdr.PathID, hdr.PathSeq, ciphertext, ad)
		if err != nil {
			continue
		}

		wn, err := c.tun.Write(plaintext)
		if err == nil {
			c.bytesRecv.Add(uint64(wn))
			c.packetsRecv.Add(1)
		}
	}
}

// Stats returns client counters
func (c *ClientEngine) Stats() (sentBytes, recvBytes, sentPkts, recvPkts uint64) {
	return c.bytesSent.Load(), c.bytesRecv.Load(), c.packetsSent.Load(), c.packetsRecv.Load()
}

func (c *ClientEngine) EncryptPayload(plaintext, ad []byte) []byte {
	if c.session == nil || c.session.TxState == nil {
		return nil
	}
	pSeq := c.pathSeq.Add(1)
	return c.session.TxState.Encrypt(nil, 0, pSeq, plaintext, ad)
}

func (c *ClientEngine) AssignedIP() string {
	return c.assignedIP
}

func (c *ClientEngine) SessionID() uint32 {
	return c.sessionID
}
