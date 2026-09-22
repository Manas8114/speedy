package routing

import (
	"errors"
	"testing"
)

func TestRoutingLoopGuard(t *testing.T) {
	mockExec := NewMockRouteExecutor()
	mgr := NewManager(mockExec)

	// Invariant 1: Installing default route BEFORE pinning relay MUST fail
	err := mgr.InstallDefaultRoute("speedy0", "10.254.1.2")
	if !errors.Is(err, ErrRelayNotPinned) {
		t.Fatalf("expected ErrRelayNotPinned, got %v", err)
	}
	if mockExec.HasDefaultRoute("speedy0") {
		t.Fatal("default route was installed despite guard violation!")
	}

	// Invariant 2: Pin relay route first
	relayIP := "203.0.113.50"
	err = mgr.PinRelayRoute(relayIP, "192.168.1.1", "eth0")
	if err != nil {
		t.Fatalf("PinRelayRoute failed: %v", err)
	}

	if !mgr.IsRelayRoutePinned() {
		t.Fatal("IsRelayRoutePinned returned false after successful pin")
	}
	if !mockExec.HasHostRoute(relayIP) {
		t.Fatalf("expected host route for %s in routing table", relayIP)
	}

	// Invariant 3: Now install default route
	err = mgr.InstallDefaultRoute("speedy0", "10.254.1.2")
	if err != nil {
		t.Fatalf("InstallDefaultRoute failed: %v", err)
	}
	if !mockExec.HasDefaultRoute("speedy0") {
		t.Fatal("expected default route for speedy0 in routing table")
	}

	// Invariant 4: Clean route restoration
	err = mgr.RestoreRoutes()
	if err != nil {
		t.Fatalf("RestoreRoutes failed: %v", err)
	}
	if mockExec.HasHostRoute(relayIP) {
		t.Fatal("host route for relay was not deleted during restore")
	}
	if mockExec.HasDefaultRoute("speedy0") {
		t.Fatal("default route was not deleted during restore")
	}
}
