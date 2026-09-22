package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"speedy/pkg/crypto"
	"speedy/pkg/protocol"
	"speedy/pkg/tunnel"
)

// RelayConfig holds configuration for the Speedy relay server
type RelayConfig struct {
	ListenAddr string
	PoolCIDR   string
	TunName    string
	TunDevice  tunnel.Device
	StaticKey  crypto.KeyPair
	NatIface   string
}

type ClientSession struct {
	SessionID    uint32
	ClientAddr   *net.UDPAddr
	ClientPub    crypto.Key
	AssignedIP   string
	Session      *crypto.HandshakeSession
	GlobalSeq    atomic.Uint64
	PathSeq      atomic.Uint64
	ReplayFilter *crypto.ReplayFilter
}

// RelayEngine manages the relay server
type RelayEngine struct {
	cfg         RelayConfig
	tun         tunnel.Device
	udpConn     *net.UDPConn
	sessions    map[uint32]*ClientSession
	addrSession map[string]*ClientSession
	ipSession   map[string]*ClientSession
	sessionsMu  sync.RWMutex
	nextSessID  atomic.Uint32
	nextIPIndex atomic.Uint32
	bytesSent   atomic.Uint64
	bytesRecv   atomic.Uint64
	packetsSent atomic.Uint64
	packetsRecv atomic.Uint64
	running     atomic.Bool
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

func NewRelayEngine(cfg RelayConfig) (*RelayEngine, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":51820"
	}
	if cfg.TunName == "" {
		cfg.TunName = "speedy-relay0"
	}
	if cfg.PoolCIDR == "" {
		cfg.PoolCIDR = "10.254.1.0/24"
	}
	if cfg.StaticKey.Public == (crypto.Key{}) {
		k, err := crypto.GenerateKeyPair()
		if err != nil {
			return nil, err
		}
		cfg.StaticKey = k
	}

	r := &RelayEngine{
		cfg:         cfg,
		sessions:    make(map[uint32]*ClientSession),
		addrSession: make(map[string]*ClientSession),
		ipSession:   make(map[string]*ClientSession),
		stopCh:      make(chan struct{}),
	}
	r.nextSessID.Store(1000)
	r.nextIPIndex.Store(2) // 1 is relay IP: 10.254.1.1
	return r, nil
}

func (r *RelayEngine) PublicKey() crypto.Key {
	return r.cfg.StaticKey.Public
}

// Start begins listening on UDP and starts the TUN device and forwarding loops
func (r *RelayEngine) Start(ctx context.Context) error {
	lAddr, err := net.ResolveUDPAddr("udp", r.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("resolve listen addr %s failed: %w", r.cfg.ListenAddr, err)
	}

	conn, err := net.ListenUDP("udp", lAddr)
	if err != nil {
		return fmt.Errorf("listen UDP on %s failed: %w", lAddr, err)
	}
	r.udpConn = conn

	// Setup TUN Device
	if r.cfg.TunDevice != nil {
		r.tun = r.cfg.TunDevice
	} else {
		dev, err := tunnel.CreateTUN(r.cfg.TunName, protocol.DefaultMTU)
		if err != nil {
			return fmt.Errorf("create TUN %s failed: %w", r.cfg.TunName, err)
		}
		r.tun = dev
	}

	r.running.Store(true)

	// Launch worker loops
	r.wg.Add(2)
	go r.udpReceiverLoop()
	go r.tunReceiverLoop()

	return nil
}

func (r *RelayEngine) Stop() error {
	if !r.running.Swap(false) {
		return nil
	}
	close(r.stopCh)
	if r.udpConn != nil {
		r.udpConn.Close()
	}
	if r.tun != nil {
		r.tun.Close()
	}
	r.wg.Wait()
	return nil
}

func (r *RelayEngine) udpReceiverLoop() {
	defer r.wg.Done()
	rxBuf := make([]byte, protocol.MaxPacketSize)
	plainBuf := make([]byte, protocol.DefaultMTU)

	for r.running.Load() {
		n, remoteAddr, err := r.udpConn.ReadFromUDP(rxBuf)
		if err != nil {
			if !r.running.Load() {
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

		switch hdr.Type {
		case protocol.TypeHandshakeInit:
			r.handleHandshakeInit(remoteAddr, rxBuf[:n], hdr)
		case protocol.TypeData:
			r.handleDataPacket(remoteAddr, rxBuf[:n], hdr, plainBuf)
		case protocol.TypeProbe:
			r.handleProbe(remoteAddr, rxBuf[:n], hdr)
		}
	}
}

func (r *RelayEngine) handleHandshakeInit(remoteAddr *net.UDPAddr, raw []byte, hdr protocol.Header) {
	responder := crypto.NewResponder(r.cfg.StaticKey)
	initMsg := raw[protocol.HeaderSize : protocol.HeaderSize+int(hdr.PayloadLen)]

	sessID := r.nextSessID.Add(1)
	clientIPIndex := r.nextIPIndex.Add(1)
	assignedIP := fmt.Sprintf("10.254.1.%d/24", clientIPIndex)

	relaySession, clientPub, _, respMsg, err := responder.ProcessInitMessage(initMsg, []byte(assignedIP))
	if err != nil {
		return
	}

	sess := &ClientSession{
		SessionID:    sessID,
		ClientAddr:   remoteAddr,
		ClientPub:    clientPub,
		AssignedIP:   assignedIP,
		Session:      relaySession,
		ReplayFilter: crypto.NewReplayFilter(),
	}

	r.sessionsMu.Lock()
	r.sessions[sessID] = sess
	r.addrSession[remoteAddr.String()] = sess
	r.ipSession[fmt.Sprintf("10.254.1.%d", clientIPIndex)] = sess
	r.sessionsMu.Unlock()

	// Send Handshake Response
	respHdr := protocol.Header{
		Magic:      protocol.Magic,
		Type:       protocol.TypeHandshakeResp,
		PathID:     0,
		SessionID:  sessID,
		PayloadLen: uint16(len(respMsg)),
	}
	outBuf := make([]byte, protocol.HeaderSize+len(respMsg))
	_ = respHdr.EncodeHeader(outBuf)
	copy(outBuf[protocol.HeaderSize:], respMsg)

	_, _ = r.udpConn.WriteToUDP(outBuf, remoteAddr)
}

func (r *RelayEngine) handleDataPacket(remoteAddr *net.UDPAddr, raw []byte, hdr protocol.Header, plainBuf []byte) {
	r.sessionsMu.RLock()
	sess, exists := r.sessions[hdr.SessionID]
	r.sessionsMu.RUnlock()

	if !exists {
		return
	}

	// Replay protection
	if !sess.ReplayFilter.CheckAndSet(hdr.PathSeq) {
		return
	}

	ad := raw[:protocol.HeaderSize]
	ciphertext := raw[protocol.HeaderSize : protocol.HeaderSize+int(hdr.PayloadLen)]

	plaintext, err := sess.Session.RxState.Decrypt(plainBuf[:0], hdr.PathID, hdr.PathSeq, ciphertext, ad)
	if err != nil {
		return
	}

	wn, err := r.tun.Write(plaintext)
	if err == nil {
		r.bytesRecv.Add(uint64(wn))
		r.packetsRecv.Add(1)
	}
}

func (r *RelayEngine) handleProbe(remoteAddr *net.UDPAddr, raw []byte, hdr protocol.Header) {
	respHdr := protocol.Header{
		Magic:      protocol.Magic,
		Type:       protocol.TypeProbeAck,
		PathID:     hdr.PathID,
		SessionID:  hdr.SessionID,
		GlobalSeq:  hdr.GlobalSeq,
		PathSeq:    hdr.PathSeq,
		PayloadLen: hdr.PayloadLen,
	}
	outBuf := make([]byte, protocol.HeaderSize+int(hdr.PayloadLen))
	_ = respHdr.EncodeHeader(outBuf)
	if hdr.PayloadLen > 0 && len(raw) >= protocol.HeaderSize+int(hdr.PayloadLen) {
		copy(outBuf[protocol.HeaderSize:], raw[protocol.HeaderSize:protocol.HeaderSize+int(hdr.PayloadLen)])
	}
	_, _ = r.udpConn.WriteToUDP(outBuf, remoteAddr)
}

func (r *RelayEngine) tunReceiverLoop() {
	defer r.wg.Done()
	readBuf := make([]byte, protocol.DefaultMTU)
	txBuf := make([]byte, protocol.MaxPacketSize)

	for r.running.Load() {
		n, err := r.tun.Read(readBuf)
		if err != nil {
			if errors.Is(err, tunnel.ErrDeviceClosed) || !r.running.Load() {
				return
			}
			continue
		}
		if n < 20 { // Min IPv4 header length
			continue
		}

		// Destination IP lookup from IPv4 packet
		dstIP := net.IPv4(readBuf[16], readBuf[17], readBuf[18], readBuf[19]).String()

		r.sessionsMu.RLock()
		sess, exists := r.ipSession[dstIP]
		if !exists {
			// If not found by direct IP, check if there's only 1 session (default route case)
			if len(r.sessions) == 1 {
				for _, s := range r.sessions {
					sess = s
					exists = true
					break
				}
			}
		}
		r.sessionsMu.RUnlock()

		if !exists || sess == nil {
			continue
		}

		gSeq := sess.GlobalSeq.Add(1)
		pSeq := sess.PathSeq.Add(1)

		ad := make([]byte, protocol.HeaderSize)
		hdr := protocol.Header{
			Magic:      protocol.Magic,
			Type:       protocol.TypeData,
			PathID:     0,
			SessionID:  sess.SessionID,
			GlobalSeq:  gSeq,
			PathSeq:    pSeq,
			Flags:      0,
			PayloadLen: uint16(n + protocol.Poly1305TagSize),
		}
		_ = hdr.EncodeHeader(ad)

		ciphertext := sess.Session.TxState.Encrypt(txBuf[protocol.HeaderSize:protocol.HeaderSize], 0, pSeq, readBuf[:n], ad)
		copy(txBuf[:protocol.HeaderSize], ad)

		packetLen := protocol.HeaderSize + len(ciphertext)
		wn, err := r.udpConn.WriteToUDP(txBuf[:packetLen], sess.ClientAddr)
		if err == nil {
			r.bytesSent.Add(uint64(wn))
			r.packetsSent.Add(1)
		}
	}
}

func (r *RelayEngine) Stats() (sentBytes, recvBytes, sentPkts, recvPkts uint64) {
	return r.bytesSent.Load(), r.bytesRecv.Load(), r.packetsSent.Load(), r.packetsRecv.Load()
}
