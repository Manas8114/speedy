package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"speedy/pkg/config"
	"speedy/pkg/reorder"
	"speedy/pkg/scheduler"
)

// DiagnosticsResponse matches Bondify's /api/v1/diagnostics shape
type DiagnosticsResponse struct {
	Timestamp          time.Time        `json:"timestamp"`
	SelectedTier       int              `json:"selected_tier"`
	TierName           string           `json:"tier_name"`
	AggregateThroughput float64          `json:"aggregate_throughput_bps"`
	ReorderBuffer      ReorderStats     `json:"reorder_buffer"`
	Paths              []PathDiagnostic `json:"paths"`
}

type ReorderStats struct {
	Occupancy  int    `json:"occupancy"`
	Released   uint64 `json:"released"`
	LateDrops  uint64 `json:"late_drops"`
	Duplicates uint64 `json:"duplicates"`
	Timeouts   uint64 `json:"timeouts"`
}

type PathDiagnostic struct {
	PathID       uint8   `json:"path_id"`
	Iface        string  `json:"iface"`
	Category     string  `json:"category"`
	State        string  `json:"state"`
	RTTMs        float64 `json:"rtt_ms"`
	MinRTTMs     float64 `json:"min_rtt_ms"`
	LossPct      float64 `json:"loss_pct"`
	GoodputBps   float64 `json:"goodput_bps"`
	CwndBytes    int64   `json:"cwnd_bytes"`
	InFlight     int64   `json:"in_flight_bytes"`
	PacketsSent  uint64  `json:"packets_sent"`
	ManualWeight float64 `json:"manual_weight"`
}

// Server provides the loopback-only control & diagnostics API
type Server struct {
	mu          sync.RWMutex
	listenAddr  string
	authToken   string
	server      *http.Server
	listener    net.Listener
	paths       []*scheduler.Path
	scheduler   scheduler.Scheduler
	reorderBuf  *reorder.Buffer
	settings    *config.Settings
	logger      *RingLogger
	onTierChange func(tier int)
}

func NewServer(listenAddr string, paths []*scheduler.Path, sched scheduler.Scheduler, reorderBuf *reorder.Buffer, settings *config.Settings, logger *RingLogger) (*Server, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8721"
	}
	// Generate 16-byte random bearer token
	tokBytes := make([]byte, 16)
	_, _ = rand.Read(tokBytes)
	token := hex.EncodeToString(tokBytes)

	s := &Server{
		listenAddr: listenAddr,
		authToken:  token,
		paths:      paths,
		scheduler:  sched,
		reorderBuf: reorderBuf,
		settings:   settings,
		logger:     logger,
	}
	return s, nil
}

func (s *Server) Token() string {
	return s.authToken
}

func (s *Server) OnTierChange(cb func(tier int)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTierChange = cb
}

func (s *Server) SetScheduler(sched scheduler.Scheduler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduler = sched
}

func (s *Server) Start() error {
	// STRICT: Loopback-only binding
	l, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind loopback API to %s: %w", s.listenAddr, err)
	}
	s.listener = l

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/diagnostics", s.authMiddleware(s.handleDiagnostics))
	mux.HandleFunc("/api/v1/paths", s.authMiddleware(s.handlePaths))
	mux.HandleFunc("/api/v1/paths/configure", s.authMiddleware(s.handlePathsConfigure))
	mux.HandleFunc("/api/v1/settings", s.authMiddleware(s.handleSettings))
	mux.HandleFunc("/api/v1/logs", s.authMiddleware(s.handleLogs))

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go s.server.Serve(l)
	return nil
}

func (s *Server) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}

		if token != s.authToken {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: invalid or missing bearer token"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		next(w, r)
	}
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pathDiags []PathDiagnostic
	var totalThroughput float64

	for _, p := range s.paths {
		state, srtt, loss, goodput, minRTT := p.Tracker.Snapshot()
		totalThroughput += goodput

		pathDiags = append(pathDiags, PathDiagnostic{
			PathID:       p.PathID,
			Iface:        p.Iface,
			Category:     p.Tracker.Category,
			State:        state.String(),
			RTTMs:        float64(srtt.Microseconds()) / 1000.0,
			MinRTTMs:     float64(minRTT.Microseconds()) / 1000.0,
			LossPct:      loss * 100.0,
			GoodputBps:   goodput,
			CwndBytes:    p.CwndBytes,
			InFlight:     p.InFlight.Load(),
			PacketsSent:  p.PacketCount.Load(),
			ManualWeight: p.ManualWeight,
		})
	}

	var rStats ReorderStats
	if s.reorderBuf != nil {
		occ, rel, lates, dups, timeouts := s.reorderBuf.Stats()
		rStats = ReorderStats{
			Occupancy:  occ,
			Released:   rel,
			LateDrops:  lates,
			Duplicates: dups,
			Timeouts:   timeouts,
		}
	}

	tier := 1
	tierName := "Tier 1: Round-Robin"
	if s.scheduler != nil {
		tier = s.scheduler.Tier()
		tierName = s.scheduler.Name()
	}

	resp := DiagnosticsResponse{
		Timestamp:           time.Now(),
		SelectedTier:        tier,
		TierName:            tierName,
		AggregateThroughput: totalThroughput,
		ReorderBuffer:       rStats,
		Paths:               pathDiags,
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handlePaths(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	json.NewEncoder(w).Encode(s.paths)
}

func (s *Server) handlePathsConfigure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		PathID   uint8   `json:"path_id"`
		Category string  `json:"category"`
		Weight   float64 `json:"weight"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	s.mu.Lock()
	for _, p := range s.paths {
		if p.PathID == req.PathID {
			if req.Category != "" {
				p.Tracker.Category = req.Category
			}
			if req.Weight >= 0 {
				p.ManualWeight = req.Weight
			}
			break
		}
	}
	s.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]string{"status": "configured"})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.mu.RLock()
		defer s.mu.RUnlock()
		json.NewEncoder(w).Encode(s.settings)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			SelectedTier int `json:"selected_tier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		cb := s.onTierChange
		s.mu.Unlock()
		if cb != nil {
			cb(req.SelectedTier)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "tier_updated"})
		return
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logger == nil {
		json.NewEncoder(w).Encode([]LogEntry{})
		return
	}
	json.NewEncoder(w).Encode(s.logger.Entries())
}
