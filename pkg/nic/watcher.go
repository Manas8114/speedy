package nic

import (
	"context"
	"net"
	"sync"
	"time"
)

// InterfaceInfo contains metadata about a physical network interface
type InterfaceInfo struct {
	Index        int
	Name         string
	HardwareAddr string
	IPs          []net.IP
	IsUp         bool
	IsLoopback   bool
}

// ChangeCallback is called when network interfaces are added, removed, or changed
type ChangeCallback func(added []InterfaceInfo, removed []string)

// Watcher monitors physical network interfaces live for hot-plug events
type Watcher struct {
	mu           sync.RWMutex
	pollInterval time.Duration
	known        map[string]InterfaceInfo
	callbacks    []ChangeCallback
	stopCh       chan struct{}
}

func NewWatcher(pollInterval time.Duration) *Watcher {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	return &Watcher{
		pollInterval: pollInterval,
		known:        make(map[string]InterfaceInfo),
		stopCh:       make(chan struct{}),
	}
}

// OnChange registers a listener for hot-plug events
func (w *Watcher) OnChange(cb ChangeCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

// ScanOnce retrieves current physical interfaces
func (w *Watcher) ScanOnce() ([]InterfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var result []InterfaceInfo
	for _, iface := range ifaces {
		isUp := (iface.Flags & net.FlagUp) != 0
		isLoopback := (iface.Flags & net.FlagLoopback) != 0
		if isLoopback {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		var ips []net.IP
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
					ips = append(ips, ipNet.IP)
				}
			}
		}

		if len(ips) == 0 && !isUp {
			continue
		}

		info := InterfaceInfo{
			Index:        iface.Index,
			Name:         iface.Name,
			HardwareAddr: iface.HardwareAddr.String(),
			IPs:          ips,
			IsUp:         isUp,
			IsLoopback:   isLoopback,
		}
		result = append(result, info)
	}
	return result, nil
}

// Start runs the periodic background hot-plug detector
func (w *Watcher) Start(ctx context.Context) error {
	initial, err := w.ScanOnce()
	if err == nil {
		w.mu.Lock()
		for _, iface := range initial {
			w.known[iface.Name] = iface
		}
		w.mu.Unlock()
	}

	go w.pollLoop(ctx)
	return nil
}

func (w *Watcher) Stop() {
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
}

func (w *Watcher) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			current, err := w.ScanOnce()
			if err != nil {
				continue
			}

			w.mu.Lock()
			currentMap := make(map[string]InterfaceInfo)
			var added []InterfaceInfo
			var removed []string

			for _, iface := range current {
				currentMap[iface.Name] = iface
				if _, exists := w.known[iface.Name]; !exists {
					added = append(added, iface)
				}
			}

			for name := range w.known {
				if _, exists := currentMap[name]; !exists {
					removed = append(removed, name)
				}
			}

			w.known = currentMap
			callbacks := append([]ChangeCallback(nil), w.callbacks...)
			w.mu.Unlock()

			if len(added) > 0 || len(removed) > 0 {
				for _, cb := range callbacks {
					cb(added, removed)
				}
			}
		}
	}
}

// SimulateHotplug allows testing unplug and plug events
func (w *Watcher) SimulateHotplug(added []InterfaceInfo, removed []string) {
	w.mu.Lock()
	for _, a := range added {
		w.known[a.Name] = a
	}
	for _, r := range removed {
		delete(w.known, r)
	}
	callbacks := append([]ChangeCallback(nil), w.callbacks...)
	w.mu.Unlock()

	for _, cb := range callbacks {
		cb(added, removed)
	}
}
