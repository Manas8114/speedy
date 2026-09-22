package routing

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

var (
	ErrRelayNotPinned = errors.New("routing loop guard violation: relay IP must be pinned before installing default route")
	ErrInvalidIP      = errors.New("invalid IP address")
)

// Route represents a routing table entry
type Route struct {
	Destination net.IPNet
	Gateway     net.IP
	Interface   string
	Metric      int
}

// Manager manages routing-loop protection and default route installations
type Manager struct {
	mu            sync.Mutex
	relayPinned   bool
	pinnedRelayIP net.IP
	pinnedGateway net.IP
	pinnedIface   string
	defaultActive bool
	tunIface      string
	tunIP         net.IP
	appliedRoutes []Route
	executor      RouteExecutor
}

// RouteExecutor abstracts the underlying OS route commands (ip route / route.exe / mock)
type RouteExecutor interface {
	AddHostRoute(dst net.IP, gateway net.IP, iface string, metric int) error
	DeleteHostRoute(dst net.IP, gateway net.IP, iface string) error
	AddDefaultRoute(tunIface string, tunIP net.IP, metric int) error
	DeleteDefaultRoute(tunIface string, tunIP net.IP) error
	GetBestRoute(dst net.IP) (gateway net.IP, iface string, err error)
}

// NewManager creates a route manager with the given executor
func NewManager(executor RouteExecutor) *Manager {
	if executor == nil {
		executor = newOSRouteExecutor()
	}
	return &Manager{
		executor: executor,
	}
}

// PinRelayRoute pins the relay's public IP to the physical uplink gateway BEFORE default route installation
func (m *Manager) PinRelayRoute(relayIPStr, gatewayStr, iface string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	relayIP := net.ParseIP(relayIPStr)
	if relayIP == nil {
		return fmt.Errorf("%w: %s", ErrInvalidIP, relayIPStr)
	}

	var gateway net.IP
	if gatewayStr != "" {
		gateway = net.ParseIP(gatewayStr)
	} else {
		// Discover best route automatically
		gw, detectedIface, err := m.executor.GetBestRoute(relayIP)
		if err != nil {
			return fmt.Errorf("failed to discover route to relay %s: %w", relayIPStr, err)
		}
		gateway = gw
		if iface == "" {
			iface = detectedIface
		}
	}

	// Add host route with metric 1 (highest priority for the relay itself)
	if err := m.executor.AddHostRoute(relayIP, gateway, iface, 1); err != nil {
		return fmt.Errorf("failed to pin relay route %s via %s (%s): %w", relayIP, gateway, iface, err)
	}

	m.relayPinned = true
	m.pinnedRelayIP = relayIP
	m.pinnedGateway = gateway
	m.pinnedIface = iface
	return nil
}

// InstallDefaultRoute installs the tunnel default route.
// CRITICAL: Fails immediately if PinRelayRoute was not called, preventing self-inflicted routing loops!
func (m *Manager) InstallDefaultRoute(tunIface string, tunIPStr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.relayPinned {
		return ErrRelayNotPinned
	}

	tunIP := net.ParseIP(tunIPStr)
	if tunIP == nil {
		return fmt.Errorf("%w: %s", ErrInvalidIP, tunIPStr)
	}

	if err := m.executor.AddDefaultRoute(tunIface, tunIP, 10); err != nil {
		return fmt.Errorf("failed to install tunnel default route via %s: %w", tunIface, err)
	}

	m.defaultActive = true
	m.tunIface = tunIface
	m.tunIP = tunIP
	return nil
}

// IsRelayRoutePinned returns whether the relay route has been pinned
func (m *Manager) IsRelayRoutePinned() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.relayPinned
}

// RestoreRoutes removes the tunnel default route and unpins the relay route cleanly
func (m *Manager) RestoreRoutes() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error

	if m.defaultActive {
		if err := m.executor.DeleteDefaultRoute(m.tunIface, m.tunIP); err != nil {
			firstErr = err
		}
		m.defaultActive = false
	}

	if m.relayPinned {
		if err := m.executor.DeleteHostRoute(m.pinnedRelayIP, m.pinnedGateway, m.pinnedIface); err != nil && firstErr == nil {
			firstErr = err
		}
		m.relayPinned = false
	}

	return firstErr
}
