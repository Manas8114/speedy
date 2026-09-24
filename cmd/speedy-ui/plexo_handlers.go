package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"speedy/pkg/plexo"
)


// plexoState holds the single active download session.
var plexoState = struct {
	mu      sync.Mutex
	session *plexo.Session
}{}

// handlePlexoInterfaces returns all usable physical network interfaces.
func handlePlexoInterfaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ifaces, err := plexo.AvailableInterfaces()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":    true,
		"interfaces": ifaces,
	})
}

// handlePlexoProbe probes a URL for byte-range support and file metadata.
func handlePlexoProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		http.Error(w, `{"error":"missing url in request body"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := plexo.ProbeURL(ctx, req.URL)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"probe":   result,
	})
}

// handlePlexoStart starts a new multi-interface download session.
func handlePlexoStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL      string   `json:"url"`
		DestDir  string   `json:"dest_dir"`
		Ifaces   []string `json:"ifaces"` // selected interface names; empty = all
		ChunkMB  int      `json:"chunk_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		http.Error(w, `{"error":"missing url in request body"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	probe, err := plexo.ProbeURL(ctx, req.URL)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"probe failed: %s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	// Resolve interfaces
	all, err := plexo.AvailableInterfaces()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"interface scan: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	var selected []plexo.InterfaceInfo
	if len(req.Ifaces) == 0 {
		selected = all
	} else {
		nameSet := map[string]bool{}
		for _, n := range req.Ifaces {
			nameSet[n] = true
		}
		for _, iface := range all {
			if nameSet[iface.Name] {
				selected = append(selected, iface)
			}
		}
	}
	if len(selected) == 0 {
		selected = all // fallback
	}

	// Destination path: Default to user's standard Downloads folder
	destDir := req.DestDir
	if destDir == "" {
		home, _ := os.UserHomeDir()
		dl := filepath.Join(home, "Downloads")
		if stat, err := os.Stat(dl); err == nil && stat.IsDir() {
			destDir = dl
		} else {
			destDir = home
		}
	}
	_ = os.MkdirAll(destDir, 0755)
	destPath := filepath.Join(destDir, probe.Filename)


	chunkSize := int64(req.ChunkMB) * 1024 * 1024
	if chunkSize <= 0 {
		chunkSize = plexo.DefaultChunkSize
	}

	plexoState.mu.Lock()
	// Cancel existing session
	if plexoState.session != nil {
		plexoState.session.Cancel()
	}

	sess, err := plexo.NewSession(probe, destPath, selected, chunkSize)
	if err != nil {
		plexoState.mu.Unlock()
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	plexoState.session = sess
	plexoState.mu.Unlock()

	if err := sess.Start(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"start failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"filename": probe.Filename,
		"dest":     destPath,
		"chunks":   len(sess.Stats().Chunks),
		"ifaces":   len(selected),
	})
}

// handlePlexoStatus returns live download progress.
func handlePlexoStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	plexoState.mu.Lock()
	sess := plexoState.session
	plexoState.mu.Unlock()

	if sess == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"state":   "idle",
		})
		return
	}
	stats := sess.Stats()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"stats":   stats,
	})
}

// handlePlexoPause pauses the active session.
func handlePlexoPause(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	plexoState.mu.Lock()
	sess := plexoState.session
	plexoState.mu.Unlock()
	if sess != nil {
		sess.Pause()
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// handlePlexoResume resumes a paused session.
func handlePlexoResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	plexoState.mu.Lock()
	sess := plexoState.session
	plexoState.mu.Unlock()
	if sess != nil {
		sess.Resume()
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// handlePlexoCancel cancels the active session.
func handlePlexoCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	plexoState.mu.Lock()
	sess := plexoState.session
	plexoState.session = nil
	plexoState.mu.Unlock()
	if sess != nil {
		sess.Cancel()
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// handlePlexoOpenFolder opens the destination file/folder in the OS file explorer
func handlePlexoOpenFolder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var req struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	targetPath := req.Path
	if targetPath == "" {
		plexoState.mu.Lock()
		if plexoState.session != nil {
			targetPath = plexoState.session.Stats().DestPath
		}
		plexoState.mu.Unlock()
	}
	if targetPath == "" {
		home, _ := os.UserHomeDir()
		targetPath = filepath.Join(home, "Downloads")
	}

	dir := targetPath
	if stat, err := os.Stat(targetPath); err == nil && !stat.IsDir() {
		dir = filepath.Dir(targetPath)
	}

	go func() {
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(targetPath); err == nil {
				exec.Command("explorer.exe", "/select,"+targetPath).Run()
			} else {
				exec.Command("explorer.exe", dir).Run()
			}
		} else if runtime.GOOS == "darwin" {
			exec.Command("open", "-R", targetPath).Run()
		} else {
			exec.Command("xdg-open", dir).Run()
		}
	}()

	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "path": targetPath})
}

