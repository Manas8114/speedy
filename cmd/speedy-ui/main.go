package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"speedy/pkg/nic"
	"speedy/pkg/orchestrator"
	"speedy/pkg/wifi"
)

type ServerState struct {
	mu        sync.RWMutex
	optimizer *wifi.Optimizer
	cfg       wifi.Config
}

var (
	startTime = time.Now()
	state     = &ServerState{
		cfg: wifi.DefaultConfig(),
	}
)

func init() {
	state.optimizer = wifi.NewOptimizer(state.cfg)
}

func main() {
	port := ":8787"
	mux := http.NewServeMux()

	// API Handlers
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/v1/status", handleStatus)
	mux.HandleFunc("/api/wifi/scan", handleWiFiScan)
	mux.HandleFunc("/api/wifi/config", handleWiFiConfig)
	mux.HandleFunc("/api/wifi/benchmark", handleWiFiBenchmark)
	mux.HandleFunc("/api/wifi/estimate", handleWiFiBenchmark)

	// Cloud Orchestrator API Handlers
	mux.HandleFunc("/api/orchestrate/providers", handleOrchestratorProviders)
	mux.HandleFunc("/api/orchestrate/deploy", handleOrchestratorDeploy)

	// Static UI assets
	fs := http.FileServer(http.Dir("./ui"))
	mux.Handle("/", fs)

	fmt.Printf("Speedy 2.0 Web Dashboard & Wi-Fi Optimizer running at http://127.0.0.1%s/\n", port)
	server := &http.Server{
		Addr:         port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	watcher := nic.NewWatcher(0)
	ifaces, _ := watcher.ScanOnce()

	var nicSummaries []map[string]interface{}
	var pathSummaries []map[string]interface{}

	for idx, iface := range ifaces {
		ipStrs := make([]string, 0, len(iface.IPs))
		for _, ip := range iface.IPs {
			ipStrs = append(ipStrs, ip.String())
		}

		nicSummaries = append(nicSummaries, map[string]interface{}{
			"index":         iface.Index,
			"name":          iface.Name,
			"hardware_addr": iface.HardwareAddr,
			"ips":           ipStrs,
			"is_up":         iface.IsUp,
		})

		stateStr := "STANDBY"
		if !iface.IsUp || len(iface.IPs) == 0 {
			stateStr = "DOWN"
		}

		pathSummaries = append(pathSummaries, map[string]interface{}{
			"id":          idx,
			"name":        iface.Name,
			"iface":       iface.Name,
			"category":    "unlimited",
			"state":       stateStr,
			"rtt":         0.0,
			"loss":        0.0,
			"goodput":     0.0,
			"weight":      100 / max(1, len(ifaces)),
			"ip":          strings.Join(ipStrs, ", "),
			"is_physical": true,
		})
	}

	resp := map[string]interface{}{
		"tunnel_active":            false,
		"status":                   "STANDBY",
		"selected_tier":            2,
		"tier_name":                "Tier 2: Goodput-Weighted",
		"aggregate_throughput_bps": 0.0,
		"aggregate_throughput_mbps": 0.0,
		"reorder_buffer": map[string]interface{}{
			"occupancy":  0,
			"released":   0,
			"late_drops": 0,
			"duplicates": 0,
			"timeouts":   0,
		},
		"nics":           nicSummaries,
		"paths":          pathSummaries,
		"uptime_seconds": time.Since(startTime).Seconds(),
		"go_version":     runtime.Version(),
		"timestamp":      time.Now().Format(time.RFC3339),
	}

	json.NewEncoder(w).Encode(resp)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func handleWiFiScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	state.mu.Lock()
	candidates, selected, err := state.optimizer.RefreshAndEstimate(ctx)
	cfg := state.cfg
	state.mu.Unlock()

	if err != nil && len(candidates) == 0 {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success":           true,
		"mode":              cfg.Mode,
		"pinned_bssid":      cfg.PinnedBSSID,
		"excluded_bands":    cfg.ExcludedBands,
		"excluded_ssids":    cfg.ExcludedSSIDs,
		"candidates":        candidates,
		"selected":          selected,
		"timestamp":         time.Now().Format(time.RFC3339),
	}
	json.NewEncoder(w).Encode(resp)
}

func handleWiFiConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Mode        string   `json:"mode"`
		PinnedBSSID string   `json:"pinned_bssid"`
		ExcludeBand string   `json:"exclude_band"`
		ClearExclude bool    `json:"clear_exclude"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	state.mu.Lock()
	switch strings.ToLower(req.Mode) {
	case "auto":
		state.cfg.Mode = wifi.ModeAuto
		state.cfg.PinnedBSSID = ""
	case "pin":
		state.cfg.Mode = wifi.ModeManualPin
		if req.PinnedBSSID != "" {
			state.cfg.PinnedBSSID = req.PinnedBSSID
		}
	case "exclude":
		state.cfg.Mode = wifi.ModeExclude
	}

	if req.ClearExclude {
		state.cfg.ExcludedBands = []string{}
		state.cfg.ExcludedSSIDs = []string{}
	}
	if req.ExcludeBand != "" {
		exists := false
		for _, b := range state.cfg.ExcludedBands {
			if strings.EqualFold(b, req.ExcludeBand) {
				exists = true
				break
			}
		}
		if !exists {
			state.cfg.ExcludedBands = append(state.cfg.ExcludedBands, req.ExcludeBand)
		}
	}

	state.optimizer = wifi.NewOptimizer(state.cfg)
	state.mu.Unlock()

	// Re-run scan with updated config
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	state.mu.Lock()
	candidates, selected, _ := state.optimizer.RefreshAndEstimate(ctx)
	cfg := state.cfg
	state.mu.Unlock()

	resp := map[string]interface{}{
		"success":        true,
		"mode":           cfg.Mode,
		"pinned_bssid":   cfg.PinnedBSSID,
		"excluded_bands": cfg.ExcludedBands,
		"candidates":     candidates,
		"selected":       selected,
	}
	json.NewEncoder(w).Encode(resp)
}

func handleWiFiBenchmark(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	state.mu.Lock()
	candidates, selected, err := state.optimizer.RefreshAndEstimate(ctx)
	state.mu.Unlock()

	if err != nil && len(candidates) == 0 {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success":    true,
		"candidates": candidates,
		"selected":   selected,
		"message":    "Wi-Fi candidate estimation complete",
	}
	json.NewEncoder(w).Encode(resp)
}

func handleOrchestratorProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	do := orchestrator.NewDigitalOceanProvider()
	hz := orchestrator.NewHetznerProvider()
	sbx := orchestrator.NewSandboxProvider()

	resp := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"id":          "sandbox",
				"name":        "Speedy Cloud Sandbox (Instant / Zero-Cost)",
				"description": "Instant test deployment with authentic Noise_IK cryptographic keys without cloud API costs",
				"requires_token": false,
				"regions":     sbx.SupportedRegions(),
			},
			{
				"id":          "digitalocean",
				"name":        "DigitalOcean Droplets",
				"description": "Deploy to DigitalOcean (Ubuntu 24.04, 1 vCPU, 1GB RAM) with automated firewall & BBR",
				"requires_token": true,
				"regions":     do.SupportedRegions(),
			},
			{
				"id":          "hetzner",
				"name":        "Hetzner Cloud",
				"description": "Deploy high-performance CX22 cloud server in European or US datacenters",
				"requires_token": true,
				"regions":     hz.SupportedRegions(),
			},
		},
	}
	json.NewEncoder(w).Encode(resp)
}

func handleOrchestratorDeploy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req orchestrator.DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "invalid request body: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	var provider orchestrator.Provider
	switch req.Provider {
	case "digitalocean":
		provider = orchestrator.NewDigitalOceanProvider()
	case "hetzner":
		provider = orchestrator.NewHetznerProvider()
	case "sandbox", "":
		provider = orchestrator.NewSandboxProvider()
	default:
		http.Error(w, fmt.Sprintf(`{"error": "unknown provider: %s"}`, req.Provider), http.StatusBadRequest)
		return
	}

	var logEvents []map[string]string
	logFn := func(step, message string) {
		logEvents = append(logEvents, map[string]string{
			"step":    step,
			"message": message,
			"time":    time.Now().Format("15:04:05"),
		})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, err := provider.DeployRelay(ctx, req, logFn)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"logs":    logEvents,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"result":  result,
		"logs":    logEvents,
	})
}

