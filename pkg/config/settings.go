package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Settings represents persistent user settings for Speedy 2.0
type Settings struct {
	mu           sync.RWMutex
	SelectedTier int                `json:"selected_tier"` // 1: RR, 2: Goodput, 3: Min-RTT, 4: HoL, 0: REDUNDANT
	Categories   map[string]string  `json:"categories"`    // Adapter -> "unlimited", "metered", "backup"
	Weights      map[string]float64 `json:"weights"`       // Adapter -> percentage (auto-normalizing)
	RelayAddr    string             `json:"relay_addr"`
	RelayPubKey  string             `json:"relay_pub_key"`
	BypassList   []string           `json:"bypass_list"`
	FilePath     string             `json:"-"`
}

func DefaultSettings(filePath string) *Settings {
	if filePath == "" {
		filePath = "speedy_config.json"
	}
	return &Settings{
		SelectedTier: 2, // Default to Tier 2: Goodput-Weighted
		Categories:   make(map[string]string),
		Weights:      make(map[string]float64),
		BypassList:   []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12"},
		FilePath:     filePath,
	}
}

// Load loads settings from file or creates default
func Load(filePath string) (*Settings, error) {
	s := DefaultSettings(filePath)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			_ = s.Save()
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	s.FilePath = filePath
	return s, nil
}

// Save writes current settings to disk
func (s *Settings) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := filepath.Dir(s.FilePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.FilePath, data, 0644)
}

// SetCategory sets link category (unlimited, metered, backup)
func (s *Settings) SetCategory(iface, category string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Categories[iface] = category
}

// SetWeight sets manual weight and auto-normalizes
func (s *Settings) SetWeight(iface string, weight float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if weight < 0 {
		weight = 0
	}
	s.Weights[iface] = weight
}

// GetNormalizedWeights returns weights normalized to sum to 100.0 without blocking
func (s *Settings) GetNormalizedWeights() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sum := 0.0
	for _, w := range s.Weights {
		sum += w
	}

	res := make(map[string]float64)
	if sum <= 0 {
		return res
	}

	for k, w := range s.Weights {
		res[k] = (w / sum) * 100.0
	}
	return res
}
