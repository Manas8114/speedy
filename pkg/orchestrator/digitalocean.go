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

// DigitalOceanProvider implements the Provider interface for DigitalOcean Droplets API
type DigitalOceanProvider struct {
	client *http.Client
}

func NewDigitalOceanProvider() *DigitalOceanProvider {
	return &DigitalOceanProvider{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (d *DigitalOceanProvider) Name() string {
	return "digitalocean"
}

func (d *DigitalOceanProvider) SupportedRegions() []Region {
	return []Region{
		{ID: "nyc1", Name: "New York 1", Location: "United States", Flag: "🇺🇸"},
		{ID: "sfo3", Name: "San Francisco 3", Location: "United States", Flag: "🇺🇸"},
		{ID: "fra1", Name: "Frankfurt 1", Location: "Germany", Flag: "🇩🇪"},
		{ID: "lon1", Name: "London 1", Location: "United Kingdom", Flag: "🇬🇧"},
		{ID: "sgp1", Name: "Singapore 1", Location: "Singapore", Flag: "🇸🇬"},
		{ID: "blr1", Name: "Bangalore 1", Location: "India", Flag: "🇮🇳"},
	}
}

type doCreateDropletRequest struct {
	Name     string   `json:"name"`
	Region   string   `json:"region"`
	Size     string   `json:"size"`
	Image    string   `json:"image"`
	UserData string   `json:"user_data"`
	Tags     []string `json:"tags"`
}

type doDropletResponse struct {
	Droplet struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Networks struct {
			V4 []struct {
				IPAddress string `json:"ip_address"`
				Type      string `json:"type"`
			} `json:"v4"`
		} `json:"networks"`
	} `json:"droplet"`
}

func (d *DigitalOceanProvider) DeployRelay(ctx context.Context, req DeployRequest, log LogCallback) (*DeployResult, error) {
	if req.APIToken == "" {
		return nil, fmt.Errorf("digitalocean API token is required")
	}
	if req.Region == "" {
		req.Region = "nyc1"
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

	log("INIT", "Synthesizing cloud-init bootstrap script with BBR and kernel packet forwarding...")
	cloudInit := GenerateCloudInit(CloudInitConfig{
		Port:          req.Port,
		ServerPrivKey: serverPriv,
		ClientPubKey:  clientPub,
		TunnelSubnet:  "10.254.1.0/24",
	})

	log("API", fmt.Sprintf("Calling DigitalOcean Droplets API (Region: %s, Size: s-1vcpu-1gb)...", req.Region))
	createPayload := doCreateDropletRequest{
		Name:     req.ServerName,
		Region:   req.Region,
		Size:     "s-1vcpu-1gb",
		Image:    "ubuntu-24-04-x64",
		UserData: cloudInit,
		Tags:     []string{"speedy", "bonding-relay"},
	}

	bodyBytes, _ := json.Marshal(createPayload)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.digitalocean.com/v2/droplets", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIToken)

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to call DigitalOcean API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("digitalocean API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var dropletResp doDropletResponse
	if err := json.NewDecoder(resp.Body).Decode(&dropletResp); err != nil {
		return nil, fmt.Errorf("failed to decode droplet response: %w", err)
	}

	dropletID := dropletResp.Droplet.ID
	log("BOOT", fmt.Sprintf("Droplet created (ID: %d). Polling for public IPv4 allocation...", dropletID))

	// Poll until public IP is assigned (max 60 seconds)
	var publicIP string
	for attempt := 0; attempt < 20; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}

		pollReq, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.digitalocean.com/v2/droplets/%d", dropletID), nil)
		pollReq.Header.Set("Authorization", "Bearer "+req.APIToken)
		pollResp, err := d.client.Do(pollReq)
		if err != nil {
			continue
		}
		var pResp doDropletResponse
		_ = json.NewDecoder(pollResp.Body).Decode(&pResp)
		pollResp.Body.Close()

		for _, net := range pResp.Droplet.Networks.V4 {
			if net.Type == "public" && net.IPAddress != "" {
				publicIP = net.IPAddress
				break
			}
		}

		if publicIP != "" {
			break
		}
		log("WAIT", fmt.Sprintf("Waiting for IPv4 assignment (attempt %d/20)...", attempt+1))
	}

	if publicIP == "" {
		return nil, fmt.Errorf("timed out waiting for DigitalOcean public IP assignment")
	}

	log("READY", fmt.Sprintf("DigitalOcean Relay provisioned successfully! Endpoint: %s:%d", publicIP, req.Port))

	return &DeployResult{
		InstanceID:       fmt.Sprintf("%d", dropletID),
		Provider:         "digitalocean",
		PublicIP:         publicIP,
		Port:             req.Port,
		ServerPublicKey:  serverPub,
		ClientPrivateKey: clientPriv,
		ClientPublicKey:  clientPub,
		ClientAssignedIP: "10.254.1.2",
		CreatedAt:        time.Now(),
	}, nil
}
