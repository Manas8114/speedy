package wifi

import (
	"fmt"
	"time"
)

type WiFiStandard string

const (
	WiFi4  WiFiStandard = "Wi-Fi 4 (802.11n)"
	WiFi5  WiFiStandard = "Wi-Fi 5 (802.11ac)"
	WiFi6  WiFiStandard = "Wi-Fi 6 (802.11ax)"
	WiFi6E WiFiStandard = "Wi-Fi 6E (802.11ax 6GHz)"
	WiFi7  WiFiStandard = "Wi-Fi 7 (802.11be)"
)

type OptimizerMode string

const (
	ModeAuto      OptimizerMode = "auto"
	ModeManualPin OptimizerMode = "pin"
	ModeExclude   OptimizerMode = "exclude"
)

// WiFiCandidate represents a reachable Wi-Fi network/AP
type WiFiCandidate struct {
	SSID                  string        `json:"ssid"`
	BSSID                 string        `json:"bssid"`
	Standard              WiFiStandard  `json:"standard"`
	RadioType             string        `json:"radio_type"`
	Band                  string        `json:"band"` // "2.4 GHz", "5 GHz", "6 GHz"
	Channel               int           `json:"channel"`
	ChannelWidthMHz       int           `json:"channel_width_mhz"` // 20, 40, 80, 160, 320
	SignalPercent         int           `json:"signal_percent"`
	RSSIDbm               int           `json:"rssi_dbm"`
	RxRateMbps            float64       `json:"rx_rate_mbps"`
	TxRateMbps            float64       `json:"tx_rate_mbps"`
	IsConnected           bool          `json:"is_connected"`
	EstimatedThroughput float64       `json:"estimated_throughput_mbps"`
	EstimatedRTT        time.Duration `json:"estimated_rtt"`
	CompositeScore        float64       `json:"composite_score"`
}

func (c WiFiCandidate) String() string {
	return fmt.Sprintf("%s (%s, %s, %s, Ch %d, %d%%, %.0f Mbps)",
		c.SSID, c.BSSID, c.Standard, c.Band, c.Channel, c.SignalPercent, c.TxRateMbps)
}

// Config controls user-defined pinning and exclusion
type Config struct {
	Mode         OptimizerMode `json:"mode"`
	PinnedBSSID  string        `json:"pinned_bssid"`
	ExcludedSSIDs []string      `json:"excluded_ssids"`
	ExcludedBands []string      `json:"excluded_bands"`
}

func DefaultConfig() Config {
	return Config{
		Mode:          ModeAuto,
		ExcludedSSIDs: []string{},
		ExcludedBands: []string{},
	}
}
