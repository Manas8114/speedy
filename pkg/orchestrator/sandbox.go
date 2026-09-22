package orchestrator

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// SandboxProvider provides zero-cost immediate simulation of cloud relay provisioning
type SandboxProvider struct{}

func NewSandboxProvider() *SandboxProvider {
	return &SandboxProvider{}
}

func (s *SandboxProvider) Name() string {
	return "sandbox"
}

func (s *SandboxProvider) SupportedRegions() []Region {
	return []Region{
		{ID: "local-dev", Name: "Local Dev Loopback", Location: "Local Machine", Flag: "💻"},
		{ID: "sandbox-us-east", Name: "Cloud Sandbox US-East", Location: "Virginia", Flag: "🇺🇸"},
		{ID: "sandbox-eu-central", Name: "Cloud Sandbox EU-Central", Location: "Frankfurt", Flag: "🇪🇺"},
		{ID: "sandbox-ap-south", Name: "Cloud Sandbox AP-South", Location: "Mumbai", Flag: "🇮🇳"},
	}
}

func (s *SandboxProvider) DeployRelay(ctx context.Context, req DeployRequest, log LogCallback) (*DeployResult, error) {
	if req.Port <= 0 {
		req.Port = 51820
	}
	if req.Region == "" {
		req.Region = "local-dev"
	}

	log("AUTH", "Validating Sandbox provisioner credentials (Verified)...")
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}

	log("KEYGEN", "Generating Noise_IK Curve25519 (X25519) cryptographic identity keys...")
	serverPriv, serverPub, err := GenerateNoiseKeypair()
	if err != nil {
		return nil, err
	}
	clientPriv, clientPub, err := GenerateNoiseKeypair()
	if err != nil {
		return nil, err
	}

	log("CLOUD_INIT", "Compiling Ubuntu 24.04 cloud-init manifest (BBR, IP forward, systemd)...")
	_ = GenerateCloudInit(CloudInitConfig{
		Port:          req.Port,
		ServerPrivKey: serverPriv,
		ClientPubKey:  clientPub,
		TunnelSubnet:  "10.254.1.0/24",
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(400 * time.Millisecond):
	}

	log("BOOT", fmt.Sprintf("Provisioning virtual server in %s (IP allocation in progress)...", req.Region))
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}

	// For local-dev use 127.0.0.1; otherwise assign a realistic cloud IP
	var publicIP string
	if req.Region == "local-dev" {
		publicIP = "127.0.0.1"
	} else {
		publicIP = fmt.Sprintf("198.51.%d.%d", rand.Intn(200)+1, rand.Intn(250)+2)
	}

	log("SERVICE", "Starting speedy-relay systemd daemon on UDP port 51820...")
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}

	log("READY", fmt.Sprintf("Cloud Relay active and listening! Endpoint: %s:%d", publicIP, req.Port))

	return &DeployResult{
		InstanceID:       fmt.Sprintf("sbx-%d", time.Now().UnixNano()%100000),
		Provider:         "sandbox",
		PublicIP:         publicIP,
		Port:             req.Port,
		ServerPublicKey:  serverPub,
		ClientPrivateKey: clientPriv,
		ClientPublicKey:  clientPub,
		ClientAssignedIP: "10.254.1.2",
		CreatedAt:        time.Now(),
	}, nil
}
