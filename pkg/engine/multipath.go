package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"speedy/pkg/congestion"
	"speedy/pkg/control"
	"speedy/pkg/crypto"
	"speedy/pkg/fec"
	"speedy/pkg/health"
	"speedy/pkg/nic"
	"speedy/pkg/platform"
	"speedy/pkg/protocol"
	"speedy/pkg/reorder"
	"speedy/pkg/routing"
	"speedy/pkg/scheduler"
	"speedy/pkg/tunnel"
)

// MultipathConfig holds configuration for the bonded multipath client engine
type MultipathConfig struct {
	RelayAddr       string
	RelayPubKeyHex  string
	TunName         string
	TunDevice       tunnel.Device
	InstallDefRoute bool
	RouteManager    *routing.Manager
	StaticKey       crypto.KeyPair
	SelectedTier    int
	EnableFEC       bool
	FEC_K           int
	FEC_M           int
	Interfaces      []nic.InterfaceInfo
}

// EnginePath binds a physical network interface to a UDP connection, health tracker, and BBR controller
type EnginePath struct {
	PathID      uint8
	IfaceName   string
	IfaceIndex  int
	LocalIP     net.IP
	Conn        *net.UDPConn
	SchedPath   *scheduler.Path
	Tracker     *health.PathTracker
	BBR         *congestion.BBRController
	PathSeq     atomic.Uint64
	PacketsSent atomic.Uint64
	PacketsRecv atomic.Uint64
	BytesSent   atomic.Uint64
	BytesRecv   atomic.Uint64
}

// MultipathEngine manages bonded multipath client connections across multiple physical NICs
type MultipathEngine struct {
	mu           sync.RWMutex
	cfg          MultipathConfig
	tun          tunnel.Device
	relayAddr    *net.UDPAddr
	session      *crypto.HandshakeSession
	sessionID    uint32
	assignedIP   string
	paths        []*EnginePath
	schedPaths   []*scheduler.Path
	sched        scheduler.Scheduler
	selectedTier int
	reorderBuf   *reorder.Buffer
	fecEnc       *fec.BlockEncoder
	fecDec       *fec.BlockDecoder
	replayFilter *crypto.ReplayFilter
	globalSeq    atomic.Uint64
	bytesSent    atomic.Uint64
	bytesRecv    atomic.Uint64
	packetsSent  atomic.Uint64
	packetsRecv  atomic.Uint64
	running      atomic.Bool
	stopCh       chan struct{}
	wg           sync.WaitGroup
	startTime    time.Time
}

// NewMultipathEngine creates a new bonded multipath engine
func NewMultipathEngine(cfg MultipathConfig) (*MultipathEngine, error) {
	if cfg.TunName == "" {
		cfg.TunName = "speedy-client0"
	}
	if cfg.SelectedTier == 0 && cfg.SelectedTier != -1 {
		cfg.SelectedTier = 2 // Default Tier 2: Goodput-Weighted
	}
	if cfg.FEC_K <= 0 {
		cfg.FEC_K = 10
	}
	if cfg.FEC_M <= 0 {
		cfg.FEC_M = 1
	}

	sched := createSchedulerForTier(cfg.SelectedTier)

	return &MultipathEngine{
		cfg:          cfg,
		selectedTier: cfg.SelectedTier,
		sched:        sched,
		reorderBuf:   reorder.NewBuffer(reorder.DefaultConfig()),
		fecEnc:       fec.NewBlockEncoder(cfg.FEC_K, cfg.FEC_M),
		fecDec:       fec.NewBlockDecoder(cfg.FEC_K),
		replayFilter: crypto.NewReplayFilter(),
		stopCh:       make(chan struct{}),
		startTime:    time.Now(),
	}, nil
}

func createSchedulerForTier(tier int) scheduler.Scheduler {
	switch tier {
	case 1:
		return scheduler.NewRoundRobinScheduler()
	case 2:
		return scheduler.NewWeightedScheduler()
	case 3:
		return scheduler.NewMinRTTScheduler()
	case 4:
		return scheduler.NewHoLAwareScheduler()
	case 0:
		return scheduler.NewRedundantScheduler()
	default:
		return scheduler.NewWeightedScheduler()
	}
}

// Start performs the handshake, discovers/binds physical NICs, creates paths, and launches workers
func (m *MultipathEngine) Start(ctx context.Context) error {
	rAddr, err := net.ResolveUDPAddr("udp", m.cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("resolve relay addr %s failed: %w", m.cfg.RelayAddr, err)
	}
	m.relayAddr = rAddr

	relayPub, err := crypto.KeyFromHex(m.cfg.RelayPubKeyHex)
	if err != nil {
		return fmt.Errorf("invalid relay public key: %w", err)
	}

	if m.cfg.StaticKey.Public == (crypto.Key{}) {
		k, err := crypto.GenerateKeyPair()
		if err != nil {
			return err
		}
		m.cfg.StaticKey = k
	}

	// 1. Establish initial handshake using primary UDP socket
	initiator, err := crypto.NewInitiator(m.cfg.StaticKey, relayPub)
	if err != nil {
		return fmt.Errorf("initiate noise failed: %w", err)
	}

	primaryConn, err := net.DialUDP("udp", nil, rAddr)
	if err != nil {
		return fmt.Errorf("dial UDP to %s failed: %w", rAddr, err)
	}

	clientPayload := []byte("SPEEDY_V2_MULTIPATH_INIT")
	initBytes, err := initiator.CreateInitMessage(clientPayload)
	if err != nil {
		primaryConn.Close()
		return fmt.Errorf("create handshake init failed: %w", err)
	}

	initHdr := protocol.Header{
		Magic:      protocol.Magic,
		Type:       protocol.TypeHandshakeInit,
		PathID:     0,
		PayloadLen: uint16(len(initBytes)),
	}
	pktBuf := make([]byte, protocol.HeaderSize+len(initBytes))
	_ = initHdr.EncodeHeader(pktBuf)
	copy(pktBuf[protocol.HeaderSize:], initBytes)

	if _, err := primaryConn.Write(pktBuf); err != nil {
		primaryConn.Close()
		return fmt.Errorf("send handshake init failed: %w", err)
	}

	primaryConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	respBuf := make([]byte, 1024)
	n, err := primaryConn.Read(respBuf)
	if err != nil {
		primaryConn.Close()
		return fmt.Errorf("read handshake response timed out: %w", err)
	}
	primaryConn.SetReadDeadline(time.Time{})

	respHdr, err := protocol.DecodeHeader(respBuf[:n])
	if err != nil {
		primaryConn.Close()
		return fmt.Errorf("decode handshake resp header failed: %w", err)
	}
	if respHdr.Type != protocol.TypeHandshakeResp {
		primaryConn.Close()
		return fmt.Errorf("expected HandshakeResp (0x02), got 0x%02x", respHdr.Type)
	}

	respPayload := respBuf[protocol.HeaderSize : protocol.HeaderSize+int(respHdr.PayloadLen)]
	session, decPayload, err := initiator.ProcessRespMessage(respPayload)
	if err != nil {
		primaryConn.Close()
		return fmt.Errorf("process handshake response failed: %w", err)
	}

	m.session = session
	m.sessionID = respHdr.SessionID
	m.assignedIP = string(decPayload)

	// 2. Discover physical interfaces if none provided
	ifaces := m.cfg.Interfaces
	if len(ifaces) == 0 {
		watcher := nic.NewWatcher(0)
		scanned, _ := watcher.ScanOnce()
		for _, info := range scanned {
			if info.IsUp && len(info.IPs) > 0 {
				ifaces = append(ifaces, info)
			}
		}
	}

	// 3. Create paths for physical interfaces
	pathID := uint8(0)
	for _, iface := range ifaces {
		var lAddr *net.UDPAddr
		if len(iface.IPs) > 0 {
			lAddr = &net.UDPAddr{IP: iface.IPs[0], Port: 0}
		}

		conn, err := net.DialUDP("udp", lAddr, rAddr)
		if err != nil {
			continue
		}

		// Bind socket to specific interface index (IP_UNICAST_IF on Windows / SO_BINDTODEVICE on Linux)
		_ = platform.BindSocketToInterface(conn, iface.Index, iface.Name)

		tracker := health.NewPathTracker(pathID, iface.Name, "unlimited")
		schedP := scheduler.NewPath(pathID, iface.Name, tracker)
		bbr := congestion.NewBBRController(congestion.DefaultBBRConfig())

		ep := &EnginePath{
			PathID:     pathID,
			IfaceName:  iface.Name,
			IfaceIndex: iface.Index,
			LocalIP:    iface.IPs[0],
			Conn:       conn,
			SchedPath:  schedP,
			Tracker:    tracker,
			BBR:        bbr,
		}

		m.paths = append(m.paths, ep)
		m.schedPaths = append(m.schedPaths, schedP)
		pathID++
	}

	// If no physical interfaces could be bound, keep the primary conn as path 0
	if len(m.paths) == 0 {
		tracker := health.NewPathTracker(0, "default", "unlimited")
		schedP := scheduler.NewPath(0, "default", tracker)
		bbr := congestion.NewBBRController(congestion.DefaultBBRConfig())

		ep := &EnginePath{
			PathID:    0,
			IfaceName: "default",
			Conn:      primaryConn,
			SchedPath: schedP,
			Tracker:   tracker,
			BBR:       bbr,
		}
		m.paths = append(m.paths, ep)
		m.schedPaths = append(m.schedPaths, schedP)
	} else {
		// Close initial temporary primaryConn if distinct paths were opened
		primaryConn.Close()
	}

	// 4. Setup TUN Device
	if m.cfg.TunDevice != nil {
		m.tun = m.cfg.TunDevice
	} else {
		dev, err := tunnel.CreateTUN(m.cfg.TunName, protocol.DefaultMTU)
		if err != nil {
			return fmt.Errorf("create TUN %s failed: %w", m.cfg.TunName, err)
		}
		m.tun = dev
	}

	// 5. Install routing-loop guard and default route if requested
	if m.cfg.InstallDefRoute && m.cfg.RouteManager != nil {
		relayHost := rAddr.IP.String()
		if err := m.cfg.RouteManager.PinRelayRoute(relayHost, "", ""); err != nil {
			return fmt.Errorf("routing loop guard failed: %w", err)
		}
		tunIPStr := m.assignedIP
		if ip, _, err := net.ParseCIDR(m.assignedIP); err == nil {
			tunIPStr = ip.String()
		}
		if err := m.cfg.RouteManager.InstallDefaultRoute(m.tun.Name(), tunIPStr); err != nil {
			return fmt.Errorf("install default route failed: %w", err)
		}
	}

	m.running.Store(true)

	// Launch worker loops
	m.wg.Add(1)
	go m.tunToNetLoop()

	for _, ep := range m.paths {
		m.wg.Add(1)
		go m.netToTunLoop(ep)
	}

	m.wg.Add(2)
	go m.probeLoop()
	go m.reorderTimeoutLoop()

	return nil
}

// Stop cleanly terminates all worker loops and closes sockets
func (m *MultipathEngine) Stop() error {
	if !m.running.Swap(false) {
		return nil
	}
	close(m.stopCh)

	for _, p := range m.paths {
		if p.Conn != nil {
			p.Conn.Close()
		}
	}

	if m.tun != nil {
		m.tun.Close()
	}

	if m.cfg.InstallDefRoute && m.cfg.RouteManager != nil {
		_ = m.cfg.RouteManager.RestoreRoutes()
	}

	m.wg.Wait()
	return nil
}

// SetTier dynamically changes the active multipath packet scheduler
func (m *MultipathEngine) SetTier(tier int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selectedTier = tier
	m.sched = createSchedulerForTier(tier)
}

// SetPathCategory sets path classification ("unlimited", "metered", "backup")
func (m *MultipathEngine) SetPathCategory(pathID uint8, category string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.paths {
		if p.PathID == pathID {
			p.Tracker.Category = category
		}
	}
}

// SetPathWeight sets manual weight percentage for a path
func (m *MultipathEngine) SetPathWeight(pathID uint8, weight float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.paths {
		if p.PathID == pathID {
			p.SchedPath.ManualWeight = weight
		}
	}
}

func (m *MultipathEngine) tunToNetLoop() {
	defer m.wg.Done()
	readBuf := make([]byte, protocol.DefaultMTU)
	txBuf := make([]byte, protocol.MaxPacketSize)

	for m.running.Load() {
		n, err := m.tun.Read(readBuf)
		if err != nil {
			if errors.Is(err, tunnel.ErrDeviceClosed) || !m.running.Load() {
				return
			}
			continue
		}
		if n == 0 {
			continue
		}

		m.mu.RLock()
		sched := m.sched
		schedPaths := m.schedPaths
		m.mu.RUnlock()

		gSeq := m.globalSeq.Add(1)

		// Check if REDUNDANT scheduler is active
		if sched.Tier() == 0 {
			targets, err := sched.SelectAllPaths(schedPaths, n)
			if err == nil && len(targets) > 0 {
				for _, sp := range targets {
					m.sendDataPacketOnPath(sp.PathID, gSeq, readBuf[:n], txBuf, protocol.FlagRedundant)
				}
				continue
			}
		}

		// Standard scheduler selection
		selected, err := sched.SelectPath(schedPaths, n)
		if err != nil {
			// Fallback: pick first path if scheduler cannot choose
			if len(schedPaths) > 0 {
				selected = schedPaths[0]
			} else {
				continue
			}
		}

		m.sendDataPacketOnPath(selected.PathID, gSeq, readBuf[:n], txBuf, 0)

		// Optional FEC parity packet generation
		if m.cfg.EnableFEC {
			bID, parities, full := m.fecEnc.AddPacket(readBuf[:n])
			if full && len(parities) > 0 {
				for _, parity := range parities {
					m.sendFECParityPacket(selected.PathID, bID, parity, txBuf)
				}
			}
		}
	}
}

func (m *MultipathEngine) sendDataPacketOnPath(pathID uint8, gSeq uint64, payload, txBuf []byte, flags uint16) {
	var ep *EnginePath
	m.mu.RLock()
	for _, p := range m.paths {
		if p.PathID == pathID {
			ep = p
			break
		}
	}
	m.mu.RUnlock()

	if ep == nil || ep.Conn == nil {
		return
	}

	pSeq := ep.PathSeq.Add(1)

	ad := make([]byte, protocol.HeaderSize)
	hdr := protocol.Header{
		Magic:      protocol.Magic,
		Type:       protocol.TypeData,
		PathID:     pathID,
		SessionID:  m.sessionID,
		GlobalSeq:  gSeq,
		PathSeq:    pSeq,
		Flags:      flags,
		PayloadLen: uint16(len(payload) + protocol.Poly1305TagSize),
	}
	_ = hdr.EncodeHeader(ad)

	ciphertext := m.session.TxState.Encrypt(txBuf[protocol.HeaderSize:protocol.HeaderSize], pathID, pSeq, payload, ad)
	copy(txBuf[:protocol.HeaderSize], ad)

	pktLen := protocol.HeaderSize + len(ciphertext)
	wn, err := ep.Conn.Write(txBuf[:pktLen])
	if err == nil {
		ep.BytesSent.Add(uint64(wn))
		ep.PacketsSent.Add(1)
		ep.SchedPath.PacketCount.Add(1)
		ep.SchedPath.ByteCount.Add(uint64(wn))
		m.bytesSent.Add(uint64(wn))
		m.packetsSent.Add(1)
	}
}

func (m *MultipathEngine) sendFECParityPacket(pathID uint8, blockID uint64, parity, txBuf []byte) {
	var ep *EnginePath
	m.mu.RLock()
	for _, p := range m.paths {
		if p.PathID == pathID {
			ep = p
			break
		}
	}
	m.mu.RUnlock()

	if ep == nil || ep.Conn == nil {
		return
	}

	pSeq := ep.PathSeq.Add(1)
	gSeq := m.globalSeq.Add(1)

	ad := make([]byte, protocol.HeaderSize)
	hdr := protocol.Header{
		Magic:      protocol.Magic,
		Type:       protocol.TypeFECParity,
		PathID:     pathID,
		SessionID:  m.sessionID,
		GlobalSeq:  gSeq,
		PathSeq:    pSeq,
		Flags:      protocol.FlagFEC,
		PayloadLen: uint16(len(parity) + protocol.Poly1305TagSize),
	}
	_ = hdr.EncodeHeader(ad)

	ciphertext := m.session.TxState.Encrypt(txBuf[protocol.HeaderSize:protocol.HeaderSize], pathID, pSeq, parity, ad)
	copy(txBuf[:protocol.HeaderSize], ad)

	pktLen := protocol.HeaderSize + len(ciphertext)
	_, _ = ep.Conn.Write(txBuf[:pktLen])
}

func (m *MultipathEngine) netToTunLoop(ep *EnginePath) {
	defer m.wg.Done()
	rxBuf := make([]byte, protocol.MaxPacketSize)
	plainBuf := make([]byte, protocol.DefaultMTU)

	for m.running.Load() {
		n, err := ep.Conn.Read(rxBuf)
		if err != nil {
			if !m.running.Load() {
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
		case protocol.TypeProbeAck:
			// Process path telemetry measurement
			if hdr.PayloadLen >= 8 {
				sentNano := binary.BigEndian.Uint64(rxBuf[protocol.HeaderSize : protocol.HeaderSize+8])
				nowNano := uint64(time.Now().UnixNano())
				if nowNano > sentNano {
					rtt := time.Duration(nowNano - sentNano)
					ep.Tracker.OnProbeAck(hdr.PathSeq, int64(nowNano))
					ep.BBR.Update(int(ep.BytesRecv.Load()), rtt)
					ep.SchedPath.CwndBytes = ep.BBR.Cwnd()
				}
			}

		case protocol.TypeData:
			if !m.replayFilter.CheckAndSet(hdr.PathSeq) {
				continue
			}

			ad := rxBuf[:protocol.HeaderSize]
			ciphertext := rxBuf[protocol.HeaderSize:n]

			plaintext, err := m.session.RxState.Decrypt(plainBuf[:0], hdr.PathID, hdr.PathSeq, ciphertext, ad)
			if err != nil {
				continue
			}

			ep.BytesRecv.Add(uint64(len(plaintext)))
			ep.PacketsRecv.Add(1)
			m.bytesRecv.Add(uint64(len(plaintext)))
			m.packetsRecv.Add(1)

			// Deliver through Reorder Buffer
			ready := m.reorderBuf.Push(hdr.GlobalSeq, plaintext)
			for _, pkt := range ready {
				_, _ = m.tun.Write(pkt)
			}
		}
	}
}

func (m *MultipathEngine) probeLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	probeBuf := make([]byte, protocol.HeaderSize+8)

	for {
		select {
		case <-m.stopCh:
			return
		case now := <-ticker.C:
			nowNano := uint64(now.UnixNano())
			binary.BigEndian.PutUint64(probeBuf[protocol.HeaderSize:], nowNano)

			m.mu.RLock()
			paths := m.paths
			m.mu.RUnlock()

			for _, ep := range paths {
				if ep.Conn == nil {
					continue
				}
				pSeq := ep.PathSeq.Add(1)
				hdr := protocol.Header{
					Magic:      protocol.Magic,
					Type:       protocol.TypeProbe,
					PathID:     ep.PathID,
					SessionID:  m.sessionID,
					GlobalSeq:  0,
					PathSeq:    pSeq,
					PayloadLen: 8,
				}
				_ = hdr.EncodeHeader(probeBuf[:protocol.HeaderSize])

				ep.Tracker.OnProbeSent(pSeq, int64(nowNano))
				_, _ = ep.Conn.Write(probeBuf)
			}
		}
	}
}

func (m *MultipathEngine) reorderTimeoutLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			stale := m.reorderBuf.FlushTimeout()
			for _, pkt := range stale {
				if m.tun != nil {
					_, _ = m.tun.Write(pkt)
				}
			}
		}
	}
}

// Diagnostics returns real-time snapshot matching control.DiagnosticsResponse
func (m *MultipathEngine) Diagnostics() control.DiagnosticsResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var pathDiags []control.PathDiagnostic
	var totalGoodput float64

	for _, ep := range m.paths {
		state, rtt, loss, goodput, minRTT := ep.Tracker.Snapshot()
		totalGoodput += goodput

		pathDiags = append(pathDiags, control.PathDiagnostic{
			PathID:       ep.PathID,
			Iface:        ep.IfaceName,
			Category:     ep.Tracker.Category,
			State:        state.String(),
			RTTMs:        float64(rtt.Microseconds()) / 1000.0,
			MinRTTMs:     float64(minRTT.Microseconds()) / 1000.0,
			LossPct:      loss,
			GoodputBps:   goodput,
			CwndBytes:    ep.SchedPath.CwndBytes,
			InFlight:     ep.SchedPath.InFlight.Load(),
			PacketsSent:  ep.PacketsSent.Load(),
			ManualWeight: ep.SchedPath.ManualWeight,
		})
	}

	occ, rel, late, dup, to := m.reorderBuf.Stats()
	reorderStats := control.ReorderStats{
		Occupancy:  occ,
		Released:   rel,
		LateDrops:  late,
		Duplicates: dup,
		Timeouts:   to,
	}

	tierName := "Tier 2: Goodput-Weighted"
	if m.sched != nil {
		tierName = m.sched.Name()
	}

	return control.DiagnosticsResponse{
		Timestamp:           time.Now(),
		SelectedTier:        m.selectedTier,
		TierName:            tierName,
		AggregateThroughput: totalGoodput,
		ReorderBuffer:       reorderStats,
		Paths:               pathDiags,
	}
}

// Stats returns client counters
func (m *MultipathEngine) Stats() (sentBytes, recvBytes, sentPkts, recvPkts uint64) {
	return m.bytesSent.Load(), m.bytesRecv.Load(), m.packetsSent.Load(), m.packetsRecv.Load()
}

func (m *MultipathEngine) AssignedIP() string {
	return m.assignedIP
}

func (m *MultipathEngine) SessionID() uint32 {
	return m.sessionID
}
