package routing

import (
	"net"
	"sync"
)

// MockRouteExecutor tracks routing table operations in memory for testing
type MockRouteExecutor struct {
	mu           sync.Mutex
	Routes       map[string]Route
	DefaultRoute *Route
	BestGateway  net.IP
	BestIface    string
}

func NewMockRouteExecutor() *MockRouteExecutor {
	return &MockRouteExecutor{
		Routes:      make(map[string]Route),
		BestGateway: net.ParseIP("192.168.1.1"),
		BestIface:   "eth0",
	}
}

func (m *MockRouteExecutor) AddHostRoute(dst net.IP, gateway net.IP, iface string, metric int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mask := net.CIDRMask(32, 32)
	ipNet := net.IPNet{IP: dst, Mask: mask}
	m.Routes[dst.String()] = Route{
		Destination: ipNet,
		Gateway:     gateway,
		Interface:   iface,
		Metric:      metric,
	}
	return nil
}

func (m *MockRouteExecutor) DeleteHostRoute(dst net.IP, gateway net.IP, iface string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Routes, dst.String())
	return nil
}

func (m *MockRouteExecutor) AddDefaultRoute(tunIface string, tunIP net.IP, metric int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	zeroIP := net.IPv4zero
	mask := net.CIDRMask(0, 32)
	m.DefaultRoute = &Route{
		Destination: net.IPNet{IP: zeroIP, Mask: mask},
		Gateway:     tunIP,
		Interface:   tunIface,
		Metric:      metric,
	}
	return nil
}

func (m *MockRouteExecutor) DeleteDefaultRoute(tunIface string, tunIP net.IP) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DefaultRoute = nil
	return nil
}

func (m *MockRouteExecutor) GetBestRoute(dst net.IP) (net.IP, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.BestGateway, m.BestIface, nil
}

func (m *MockRouteExecutor) HasHostRoute(ipStr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.Routes[ipStr]
	return exists
}

func (m *MockRouteExecutor) HasDefaultRoute(tunIface string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.DefaultRoute != nil && m.DefaultRoute.Interface == tunIface
}
