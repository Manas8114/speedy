package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HetznerProvider implements the Provider interface for Hetzner Cloud API
type HetznerProvider struct {
	client *http.Client
}

func NewHetznerProvider() *HetznerProvider {
	return &HetznerProvider{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HetznerProvider) Name() string {
	return "hetzner"
}

func (h *HetznerProvider) SupportedRegions() []Region {
	return []Region{
		{ID: "fsn1", Name: "Falkenstein 1", Location: "Germany", Flag: "🇩🇪"},
		{ID: "nbg1", Name: "Nuremberg 1", Location: "Germany", Flag: "🇩🇪"},
		{ID: "hel1", Name: "Helsinki 1", Location: "Finland", Flag: "🇫🇮"},
		{ID: "ash", Name: "Ashburn", Location: "United States", Flag: "🇺🇸"},
		{ID: "hil", Name: "Hillsboro", Location: "United States", Flag: "🇺🇸"},
		{ID: "sin", Name: "Singapore", Location: "Singapore", Flag: "🇸🇬"},
	}
}

type hzCreateServerRequest struct {
	Name       string `json:"name"`
	ServerType string `json:"server_type"`
	Location   string `json:"location"`
	Image      string `json:"image"`
	UserData   string `json:"user_data"`
	StartAfterCreate bool `json:"start_after_create"`
}

type hzServerResponse struct {
	Server struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		PublicNet  struct {
			IPv4 struct {
				IP string `json:"ip"`
			} `json:"ipv4"`
		} `json:"public_net"`
	} `json:"server"`
}

func (h *HetznerProvider) DeployRelay(ctx context.Context, req DeployRequest, log LogCallback) (*DeployResult, error) {
	if req.APIToken == "" {
		return nil, fmt.Errorf("hetzner API token is required")
	}
	if req.Region == "" {
		req.Region = "fsn1"
	}
	if req.ServerName == "" {
		req.ServerName = fmt.Sprintf("speedy-relay-%d", time.Now().Unix()%10000)
	}
	if req.Port <= 0 {
		req.Port = 51820
	}

	log("KEYGEN", "Generating Noise_IK Curve25519 cryptographic keypairs...")
	serverPriv, serverPub, err := GenerateNoiseKeypair()
	if err != nil {
		return nil, err
	}
	clientPriv, clientPub, err := GenerateNoiseKeypair()
	if err != nil {
		return nil, err
	}

	log("INIT", "Synthesizing cloud-init bootstrap script...")
	cloudInit := GenerateCloudInit(CloudInitConfig{
		Port:          req.Port,
		ServerPrivKey: serverPriv,
		ClientPubKey:  clientPub,
		TunnelSubnet:  "10.254.1.0/24",
	})

	log("API", fmt.Sprintf("Calling Hetzner Cloud API (Location: %s, Type: cx22)...", req.Region))
	createPayload := hzCreateServerRequest{
		Name:             req.ServerName,
		ServerType:       "cx22",
		Location:         req.Region,
		Image:            "ubuntu-24.04",
		UserData:         cloudInit,
		StartAfterCreate: true,
	}

	bodyBytes, _ := json.Marshal(createPayload)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.hetzner.cloud/v1/servers", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIToken)

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to call Hetzner API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("hetzner API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var hzResp hzServerResponse
	if err := json.NewDecoder(resp.Body).Decode(&hzResp); err != nil {
		return nil, fmt.Errorf("failed to decode Hetzner response: %w", err)
	}

	serverID := hzResp.Server.ID
	publicIP := hzResp.Server.PublicNet.IPv4.IP

	log("BOOT", fmt.Sprintf("Hetzner server created (ID: %d, IPv4: %s). Waiting for status running...", serverID, publicIP))

	// If IP wasn't returned on creation, poll for it
	if publicIP == "" {
		for attempt := 0; attempt < 20; attempt++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}

			pollReq, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.hetzner.cloud/v1/servers/%d", serverID), nil)
			pollReq.Header.Set("Authorization", "Bearer "+req.APIToken)
			pollResp, err := h.client.Do(pollReq)
			if err != nil {
				continue
			}
			var pResp hzServerResponse
			_ = json.NewDecoder(pollResp.Body).Decode(&pResp)
			pollResp.Body.Close()

			publicIP = pResp.Server.PublicNet.IPv4.IP
			if publicIP != "" {
				break
			}
		}
	}

	if publicIP == "" {
		return nil, fmt.Errorf("timed out waiting for Hetzner public IP assignment")
	}

	log("READY", fmt.Sprintf("Hetzner Cloud Relay ready! Endpoint: %s:%d", publicIP, req.Port))

	return &DeployResult{
		InstanceID:       fmt.Sprintf("%d", serverID),
		Provider:         "hetzner",
		PublicIP:         publicIP,
		Port:             req.Port,
		ServerPublicKey:  serverPub,
		ClientPrivateKey: clientPriv,
		ClientPublicKey:  clientPub,
		ClientAssignedIP: "10.254.1.2",
		CreatedAt:        time.Now(),
	}, nil
}
