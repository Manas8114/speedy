//go:build windows

package platform

import (
	"net"
	"testing"
)

func TestWindowsSocketBinding(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP failed: %v", err)
	}
	defer conn.Close()

	// Get local interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces failed: %v", err)
	}

	var validIndex int
	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) != 0 && (iface.Flags&net.FlagLoopback) == 0 {
			validIndex = iface.Index
			break
		}
	}

	if validIndex == 0 {
		t.Skip("No active physical interface found for IP_UNICAST_IF test")
	}

	t.Logf("Testing IP_UNICAST_IF on physical interface index %d...", validIndex)
	if err := BindSocketToInterface(conn, validIndex, ""); err != nil {
		t.Fatalf("BindSocketToInterface with IP_UNICAST_IF failed: %v", err)
	}
	t.Logf("✓ GATE PASS: Windows IP_UNICAST_IF socket interface binding verified on interface %d", validIndex)
}
