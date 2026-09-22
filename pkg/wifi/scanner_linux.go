//go:build linux || android

package wifi

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// ScanVisibleNetworks queries Linux iw and nl80211 APIs
func ScanVisibleNetworks(ctx context.Context) ([]WiFiCandidate, error) {
	// Query current link
	cmd := exec.CommandContext(ctx, "iw", "dev", "wlan0", "link")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	cand := parseIwLink(string(out))
	if cand != nil {
		return []WiFiCandidate{*cand}, nil
	}
	return nil, nil
}

func parseIwLink(output string) *WiFiCandidate {
	lines := strings.Split(output, "\n")
	cand := &WiFiCandidate{
		IsConnected: true,
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Connected to ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				cand.BSSID = parts[2]
			}
		}
		if strings.HasPrefix(line, "SSID: ") {
			cand.SSID = strings.TrimPrefix(line, "SSID: ")
		}
		if strings.HasPrefix(line, "freq: ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				freq, _ := strconv.Atoi(parts[1])
				if freq > 5900 {
					cand.Band = "6 GHz"
					cand.Standard = WiFi6E
				} else if freq > 4900 {
					cand.Band = "5 GHz"
					cand.Standard = WiFi6
				} else {
					cand.Band = "2.4 GHz"
					cand.Standard = WiFi4
				}
			}
		}
		if strings.HasPrefix(line, "signal: ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				sig, _ := strconv.Atoi(parts[1])
				cand.RSSIDbm = sig
				cand.SignalPercent = (sig + 100) * 2
			}
		}
		if strings.HasPrefix(line, "tx bitrate: ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				rate, _ := strconv.ParseFloat(parts[2], 64)
				cand.TxRateMbps = rate
				cand.RxRateMbps = rate
			}
		}
	}

	if cand.SSID != "" {
		return cand
	}
	return nil
}

func ConnectBSSID(ctx context.Context, profile, ssid, bssid string) error {
	cmd := exec.CommandContext(ctx, "iw", "dev", "wlan0", "connect", ssid, bssid)
	return cmd.Run()
}
