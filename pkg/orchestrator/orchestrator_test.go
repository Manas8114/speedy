package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCloudInitGeneration(t *testing.T) {
	cfg := CloudInitConfig{
		Port:          51820,
		ServerPrivKey: "mock-priv-key",
		ClientPubKey:  "mock-pub-key",
		TunnelSubnet:  "10.254.1.0/24",
	}

	initScript := GenerateCloudInit(cfg)
	if !strings.Contains(initScript, "51820") {
		t.Fatalf("Cloud-init missing configured port 51820")
	}
	if !strings.Contains(initScript, "net.ipv4.ip_forward = 1") {
		t.Fatalf("Cloud-init missing ip_forward sysctl")
	}
	if !strings.Contains(initScript, "speedy-relay.service") {
		t.Fatalf("Cloud-init missing systemd service definition")
	}
	t.Logf("✓ Cloud-init script synthesis verified")
}

func TestNoiseKeypairGeneration(t *testing.T) {
	priv, pub, err := GenerateNoiseKeypair()
	if err != nil {
		t.Fatalf("Keypair generation failed: %v", err)
	}
	if len(priv) == 0 || len(pub) == 0 {
		t.Fatalf("Generated keys cannot be empty")
	}
	if priv == pub {
		t.Fatalf("Private key and Public key must be different")
	}
	t.Logf("✓ Noise_IK Curve25519 keypair generation verified (Pub: %s)", pub)
}

func TestSandboxDeployFlow(t *testing.T) {
	sbx := NewSandboxProvider()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var logs []string
	logCallback := func(step, msg string) {
		logs = append(logs, step+": "+msg)
	}

	req := DeployRequest{
		Provider: "sandbox",
		Region:   "local-dev",
		Port:     51820,
	}

	res, err := sbx.DeployRelay(ctx, req, logCallback)
	if err != nil {
		t.Fatalf("Sandbox deployment failed: %v", err)
	}

	if res.PublicIP != "127.0.0.1" {
		t.Fatalf("Expected 127.0.0.1 for local-dev, got %s", res.PublicIP)
	}
	if res.Port != 51820 {
		t.Fatalf("Expected port 51820, got %d", res.Port)
	}
	if len(res.ServerPublicKey) == 0 || len(res.ClientPrivateKey) == 0 {
		t.Fatalf("Missing generated keys in deploy result")
	}
	if len(logs) < 5 {
		t.Fatalf("Expected at least 5 log events, got %d", len(logs))
	}

	t.Logf("✓ Sandbox deploy workflow verified with %d streaming log events", len(logs))
}
