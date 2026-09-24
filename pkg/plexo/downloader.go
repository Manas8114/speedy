package plexo

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ChunkStatus represents the download state of a single file chunk.
type ChunkStatus int32

const (
	ChunkPending     ChunkStatus = 0
	ChunkDownloading ChunkStatus = 1
	ChunkCompleted   ChunkStatus = 2
	ChunkFailed      ChunkStatus = 3
)

const (
	// DefaultChunkSize is 4 MB per chunk.
	DefaultChunkSize = 4 * 1024 * 1024
	// MaxWorkersPerIface is the maximum number of concurrent workers per interface.
	MaxWorkersPerIface = 4
	// MaxRetries is how many times a failed chunk is retried.
	MaxRetries = 5
)

// StallTimeout aborts a connection that has received no data for this duration.
const StallTimeout = 20 * time.Second

// chunk represents one byte-range slice of the file.
type chunk struct {
	index    int
	start    int64
	end      int64 // inclusive; -1 means open-ended (single-stream fallback)
	status   ChunkStatus
	ifaceIdx int // which interface last downloaded this chunk (-1 = none)
	retries  int
}

// SessionStats is a live snapshot of download progress.
type SessionStats struct {
	TotalBytes     int64        `json:"total_bytes"`
	CompletedBytes int64        `json:"completed_bytes"`
	SpeedBps       int64        `json:"speed_bps"`
	PerIface       []IfaceStats `json:"per_iface"`
	Chunks         []ChunkInfo  `json:"chunks"`
	State          string       `json:"state"`
	ElapsedMs      int64        `json:"elapsed_ms"`
	ETAMs          int64        `json:"eta_ms"`
	DestPath       string       `json:"dest_path"`
	Filename       string       `json:"filename"`
}

// IfaceStats is per-interface throughput stats.
type IfaceStats struct {
	Name      string `json:"name"`
	LocalIP   string `json:"local_ip"`
	BytesDone int64  `json:"bytes_done"`
	SpeedBps  int64  `json:"speed_bps"`
}

// ChunkInfo is the public state of a single chunk, for the UI grid.
type ChunkInfo struct {
	Index    int         `json:"index"`
	Start    int64       `json:"start"`
	End      int64       `json:"end"`
	Status   ChunkStatus `json:"status"`
	IfaceIdx int         `json:"iface_idx"`
}

type sessionState int32

const (
	stateReady     sessionState = 0
	stateRunning   sessionState = 1
	statePaused    sessionState = 2
	stateDone      sessionState = 3
	stateCancelled sessionState = 4
)

// Session manages a multi-interface parallel download.
type Session struct {
	mu sync.Mutex

	url       string
	probe     *ProbeResult
	destPath  string
	ifaces    []InterfaceInfo
	chunkSize int64

	chunks    []chunk
	state     sessionState
	startTime time.Time
	cancelFn  context.CancelFunc
	pauseCh   chan struct{}
	resumeCh  chan struct{}

	completedBytes int64   // atomic
	ifaceBytes     []int64 // atomic per interface
	ifaceSpeed     []int64 // rolling speed bps per interface
	totalSpeed     int64   // aggregate atomic

	file *os.File
}

// NewSession creates a new download session. Call Start() to begin downloading.
func NewSession(probe *ProbeResult, destPath string, ifaces []InterfaceInfo, chunkSize int64) (*Session, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("at least one network interface is required")
	}

	var chunks []chunk
	if probe.SupportsRanges && probe.TotalBytes > 0 {
		offset := int64(0)
		idx := 0
		for offset < probe.TotalBytes {
			end := offset + chunkSize - 1
			if end >= probe.TotalBytes {
				end = probe.TotalBytes - 1
			}
			chunks = append(chunks, chunk{
				index:    idx,
				start:    offset,
				end:      end,
				status:   ChunkPending,
				ifaceIdx: -1,
			})
			offset = end + 1
			idx++
		}
	} else {
		// Single chunk, no range support
		chunks = []chunk{{
			index:    0,
			start:    0,
			end:      -1,
			status:   ChunkPending,
			ifaceIdx: -1,
		}}
	}

	return &Session{
		url:        probe.URL,
		probe:      probe,
		destPath:   destPath,
		ifaces:     ifaces,
		chunkSize:  chunkSize,
		chunks:     chunks,
		state:      stateReady,
		ifaceBytes: make([]int64, len(ifaces)),
		ifaceSpeed: make([]int64, len(ifaces)),
		pauseCh:    make(chan struct{}),
		resumeCh:   make(chan struct{}),
	}, nil
}

// Start begins the download. Non-blocking; runs workers in the background.
func (s *Session) Start() error {
	s.mu.Lock()
	if s.state != stateReady {
		s.mu.Unlock()
		return fmt.Errorf("session already started or finished")
	}

	f, err := os.OpenFile(s.destPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("creating output file: %w", err)
	}
	if s.probe.TotalBytes > 0 {
		if err := f.Truncate(s.probe.TotalBytes); err != nil {
			f.Close()
			s.mu.Unlock()
			return fmt.Errorf("pre-allocating file: %w", err)
		}
	}
	s.file = f
	s.state = stateRunning
	s.startTime = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel
	s.mu.Unlock()

	go s.run(ctx)
	return nil
}

// Pause suspends active chunk downloads at the next safe checkpoint.
func (s *Session) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateRunning {
		s.state = statePaused
		close(s.pauseCh)
	}
}

// Resume continues a paused download.
func (s *Session) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == statePaused {
		s.state = stateRunning
		oldResume := s.resumeCh
		s.pauseCh = make(chan struct{})
		s.resumeCh = make(chan struct{})
		close(oldResume)
	}
}

// Cancel terminates the download.
func (s *Session) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateDone && s.state != stateCancelled {
		s.state = stateCancelled
		if s.cancelFn != nil {
			s.cancelFn()
		}
	}
}

// Stats returns a snapshot of current download progress.
func (s *Session) Stats() SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	stateStr := "ready"
	switch s.state {
	case stateRunning:
		stateStr = "running"
	case statePaused:
		stateStr = "paused"
	case stateDone:
		stateStr = "done"
	case stateCancelled:
		stateStr = "cancelled"
	}

	completed := atomic.LoadInt64(&s.completedBytes)
	speed := atomic.LoadInt64(&s.totalSpeed)

	var etaMs int64
	remaining := s.probe.TotalBytes - completed
	if speed > 0 && remaining > 0 {
		etaMs = int64(float64(remaining) / float64(speed) * 1000)
	}

	perIface := make([]IfaceStats, len(s.ifaces))
	for i, iface := range s.ifaces {
		localIP := ""
		if len(iface.LocalIPs) > 0 {
			localIP = iface.LocalIPs[0]
		}
		perIface[i] = IfaceStats{
			Name:      iface.Name,
			LocalIP:   localIP,
			BytesDone: atomic.LoadInt64(&s.ifaceBytes[i]),
			SpeedBps:  atomic.LoadInt64(&s.ifaceSpeed[i]),
		}
	}

	chunkInfos := make([]ChunkInfo, len(s.chunks))
	for i, c := range s.chunks {
		chunkInfos[i] = ChunkInfo{
			Index:    c.index,
			Start:    c.start,
			End:      c.end,
			Status:   c.status,
			IfaceIdx: c.ifaceIdx,
		}
	}

	elapsedMs := time.Since(s.startTime).Milliseconds()
	if s.state == stateReady {
		elapsedMs = 0
	}

	return SessionStats{
		TotalBytes:     s.probe.TotalBytes,
		CompletedBytes: completed,
		SpeedBps:       speed,
		PerIface:       perIface,
		Chunks:         chunkInfos,
		State:          stateStr,
		ElapsedMs:      elapsedMs,
		ETAMs:          etaMs,
		DestPath:       s.destPath,
		Filename:       s.probe.Filename,
	}
}

// workQueue is a thread-safe pending chunk queue (work-stealing design).
type workQueue struct {
	mu      sync.Mutex
	pending []int // indices into s.chunks
}

func (q *workQueue) take() (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return 0, false
	}
	idx := q.pending[0]
	q.pending = q.pending[1:]
	return idx, true
}

func (q *workQueue) requeue(idx int) {
	q.mu.Lock()
	q.pending = append(q.pending, idx)
	q.mu.Unlock()
}

func (s *Session) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		if s.file != nil {
			_ = s.file.Sync()
			_ = s.file.Close()
			s.file = nil
		}
		if s.state == stateRunning {
			s.state = stateDone
		}
		s.mu.Unlock()
	}()

	q := &workQueue{}
	for i := range s.chunks {
		q.pending = append(q.pending, i)
	}

	var wg sync.WaitGroup
	numWorkers := len(s.ifaces) * MaxWorkersPerIface
	if numWorkers > 32 {
		numWorkers = 32
	}

	for w := 0; w < numWorkers; w++ {
		ifaceIdx := w % len(s.ifaces)
		wg.Add(1)
		go func(ifIdx int) {
			defer wg.Done()
			s.worker(ctx, ifIdx, q)
		}(ifaceIdx)
	}

	go s.measureSpeed(ctx)
	wg.Wait()
}

func (s *Session) worker(ctx context.Context, ifaceIdx int, q *workQueue) {
	iface := s.ifaces[ifaceIdx]
	localIP := ""
	if len(iface.LocalIPs) > 0 {
		localIP = iface.LocalIPs[0]
	}

	transport := &http.Transport{
		DialContext: func(dctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 8 * time.Second}
			if localIP != "" {
				dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(localIP)}
			}
			conn, err := dialer.DialContext(dctx, network, addr)
			if err != nil && localIP != "" {
				// If local IP is not routable to destination, fallback to system default route
				unbound := &net.Dialer{Timeout: 8 * time.Second}
				return unbound.DialContext(dctx, network, addr)
			}
			return conn, err
		},
		DisableKeepAlives:   false,
		MaxIdleConnsPerHost: MaxWorkersPerIface,
	}

	client := &http.Client{Transport: transport}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.mu.Lock()
		if s.state == statePaused {
			pauseCh := s.pauseCh
			resumeCh := s.resumeCh
			s.mu.Unlock()
			select {
			case <-pauseCh:
				// paused - wait for resume signal
			case <-ctx.Done():
				return
			}
			select {
			case <-resumeCh:
			case <-ctx.Done():
				return
			}
			continue
		}
		s.mu.Unlock()

		chunkIdx, ok := q.take()
		if !ok {
			return
		}

		s.mu.Lock()
		if s.chunks[chunkIdx].status == ChunkCompleted {
			s.mu.Unlock()
			continue
		}
		s.chunks[chunkIdx].status = ChunkDownloading
		s.chunks[chunkIdx].ifaceIdx = ifaceIdx
		chunkStart := s.chunks[chunkIdx].start
		chunkEnd := s.chunks[chunkIdx].end
		s.mu.Unlock()

		err := s.downloadChunk(ctx, client, chunkIdx, chunkStart, chunkEnd, ifaceIdx)

		s.mu.Lock()
		if err != nil {
			s.chunks[chunkIdx].status = ChunkFailed
			s.chunks[chunkIdx].retries++
			if s.chunks[chunkIdx].retries <= MaxRetries {
				q.requeue(chunkIdx)
			}
		} else {
			s.chunks[chunkIdx].status = ChunkCompleted
		}
		s.mu.Unlock()
	}
}

func (s *Session) downloadChunk(ctx context.Context, client *http.Client, chunkIdx int, start, end int64, ifaceIdx int) error {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	if end >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		if s.probe.ETag != "" {
			req.Header.Set("If-Range", s.probe.ETag)
		}
	}


	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP %d for chunk %d", resp.StatusCode, chunkIdx)
	}

	writeOffset := start
	buf := make([]byte, 32*1024) // 32 KB read buffer
	stallTimer := time.NewTimer(StallTimeout)
	defer stallTimer.Stop()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			s.mu.Lock()
			_, writeErr := s.file.WriteAt(buf[:n], writeOffset)
			s.mu.Unlock()
			if writeErr != nil {
				return fmt.Errorf("write error at offset %d: %w", writeOffset, writeErr)
			}
			writeOffset += int64(n)
			atomic.AddInt64(&s.completedBytes, int64(n))
			atomic.AddInt64(&s.ifaceBytes[ifaceIdx], int64(n))
			stallTimer.Reset(StallTimeout)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stallTimer.C:
			return fmt.Errorf("chunk %d stalled (no data for %s)", chunkIdx, StallTimeout)
		default:
		}
	}
	return nil
}

func (s *Session) measureSpeed(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	prevTotal := atomic.LoadInt64(&s.completedBytes)
	prevIface := make([]int64, len(s.ifaces))
	for i := range s.ifaces {
		prevIface[i] = atomic.LoadInt64(&s.ifaceBytes[i])
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			curr := atomic.LoadInt64(&s.completedBytes)
			atomic.StoreInt64(&s.totalSpeed, curr-prevTotal)
			prevTotal = curr
			for i := range s.ifaces {
				c := atomic.LoadInt64(&s.ifaceBytes[i])
				atomic.StoreInt64(&s.ifaceSpeed[i], c-prevIface[i])
				prevIface[i] = c
			}
		}
	}
}
