//go:build darwin

package wifi

import (
	"context"
)

// ScanVisibleNetworks scans visible Wi-Fi networks on Darwin (macOS)
func ScanVisibleNetworks(ctx context.Context) ([]WiFiCandidate, error) {
	// macOS CoreWLAN or airport utility
	return nil, nil
}
