//go:build windows

package wifi

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// ScanVisibleNetworks queries Windows native netsh wlan APIs
func ScanVisibleNetworks(ctx context.Context) ([]WiFiCandidate, error) {
	// 1. Get currently connected interface details
	connectedCandidate, _ := queryConnectedInterface(ctx)

	// 2. Query all visible BSSIDs
	cmd := exec.CommandContext(ctx, "netsh", "wlan", "show", "networks", "mode=bssid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// If query fails (e.g. no Wi-Fi card), return connectedCandidate if present
		if connectedCandidate != nil {
			return []WiFiCandidate{*connectedCandidate}, nil
		}
		return nil, err
	}

	candidates := parseNetshNetworks(string(out), connectedCandidate)
	if len(candidates) == 0 && connectedCandidate != nil {
		candidates = append(candidates, *connectedCandidate)
	}

	return candidates, nil
}

func queryConnectedInterface(ctx context.Context) (*WiFiCandidate, error) {
	cmd := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(out), "\n")
	cand := &WiFiCandidate{
		IsConnected: true,
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "SSID":
			cand.SSID = val
		case "AP BSSID", "BSSID":
			cand.BSSID = val
		case "Radio type":
			cand.RadioType = val
			cand.Standard = determineStandard(val, cand.Band)
		case "Band":
			cand.Band = val
			cand.Standard = determineStandard(cand.RadioType, val)
		case "Channel":
			if ch, err := strconv.Atoi(val); err == nil {
				cand.Channel = ch
			}
		case "Signal":
			val = strings.TrimSuffix(val, "%")
			if sig, err := strconv.Atoi(val); err == nil {
				cand.SignalPercent = sig
			}
		case "Rssi":
			if rssi, err := strconv.Atoi(val); err == nil {
				cand.RSSIDbm = rssi
			}
		case "Receive rate (Mbps)":
			if rx, err := strconv.ParseFloat(val, 64); err == nil {
				cand.RxRateMbps = rx
			}
		case "Transmit rate (Mbps)":
			if tx, err := strconv.ParseFloat(val, 64); err == nil {
				cand.TxRateMbps = tx
			}
		}
	}

	if cand.SSID != "" {
		return cand, nil
	}
	return nil, nil
}

func parseNetshNetworks(output string, connected *WiFiCandidate) []WiFiCandidate {
	var candidates []WiFiCandidate
	lines := strings.Split(output, "\n")

	var currentSSID string
	var currentCandidate *WiFiCandidate

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID ") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				currentSSID = strings.TrimSpace(parts[1])
			}
			continue
		}

		if strings.HasPrefix(line, "BSSID ") {
			if currentCandidate != nil && currentCandidate.BSSID != "" {
				candidates = append(candidates, *currentCandidate)
			}
			parts := strings.SplitN(line, ":", 2)
			bssid := ""
			if len(parts) == 2 {
				bssid = strings.TrimSpace(parts[1])
			}
			currentCandidate = &WiFiCandidate{
				SSID:  currentSSID,
				BSSID: bssid,
			}
			continue
		}

		if currentCandidate == nil {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "Signal":
			val = strings.TrimSuffix(val, "%")
			if sig, err := strconv.Atoi(val); err == nil {
				currentCandidate.SignalPercent = sig
				currentCandidate.RSSIDbm = -100 + (sig / 2) // Approximate dBm from %
			}
		case "Radio type":
			currentCandidate.RadioType = val
			currentCandidate.Standard = determineStandard(val, currentCandidate.Band)
		case "Band":
			currentCandidate.Band = val
			currentCandidate.Standard = determineStandard(currentCandidate.RadioType, val)
		case "Channel":
			if ch, err := strconv.Atoi(val); err == nil {
				currentCandidate.Channel = ch
			}
		}
	}

	if currentCandidate != nil && currentCandidate.BSSID != "" {
		candidates = append(candidates, *currentCandidate)
	}

	// Correlate with connected interface
	if connected != nil {
		found := false
		for i := range candidates {
			if strings.EqualFold(candidates[i].BSSID, connected.BSSID) ||
				(candidates[i].SSID == connected.SSID && candidates[i].Band == connected.Band) {
				candidates[i].IsConnected = true
				candidates[i].RxRateMbps = connected.RxRateMbps
				candidates[i].TxRateMbps = connected.TxRateMbps
				candidates[i].RSSIDbm = connected.RSSIDbm
				found = true
				break
			}
		}
		if !found {
			candidates = append([]WiFiCandidate{*connected}, candidates...)
		}
	}

	return candidates
}

func determineStandard(radioType, band string) WiFiStandard {
	radio := strings.ToLower(radioType)
	b := strings.ToLower(band)

	if strings.Contains(radio, "802.11be") {
		return WiFi7
	}
	if strings.Contains(radio, "802.11ax") {
		if strings.Contains(b, "6 ghz") {
			return WiFi6E
		}
		return WiFi6
	}
	if strings.Contains(radio, "802.11ac") {
		return WiFi5
	}
	if strings.Contains(radio, "802.11n") {
		return WiFi4
	}
	return WiFi5
}

// ConnectBSSID commands Windows to pin association to a specific BSSID
func ConnectBSSID(ctx context.Context, profile, ssid, bssid string) error {
	args := []string{"wlan", "connect"}
	if profile != "" {
		args = append(args, "name="+profile)
	}
	if ssid != "" {
		args = append(args, "ssid="+ssid)
	}
	if bssid != "" {
		args = append(args, "bssid="+bssid)
	}

	cmd := exec.CommandContext(ctx, "netsh", args...)
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	return cmd.Run()
}
