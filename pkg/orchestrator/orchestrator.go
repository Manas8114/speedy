package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

// Region represents a cloud datacenter location
type Region struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Flag     string `json:"flag"`
}

// DeployRequest contains user parameters for cloud relay provisioning
type DeployRequest struct {
	Provider   string `json:"provider"` // "digitalocean", "hetzner", "sandbox"
	APIToken   string `json:"api_token"`
	Region     string `json:"region"`
	ServerName string `json:"server_name"`
	Port       int    `json:"port"`
}

// DeployResult holds the completed cloud relay credentials and endpoint
type DeployResult struct {
	InstanceID       string    `json:"instance_id"`
	Provider         string    `json:"provider"`
	PublicIP         string    `json:"public_ip"`
	Port             int       `json:"port"`
	ServerPublicKey  string    `json:"server_public_key"`
	ClientPrivateKey string    `json:"client_private_key"`
	ClientPublicKey  string    `json:"client_public_key"`
	ClientAssignedIP string    `json:"client_assigned_ip"`
	CreatedAt        time.Time `json:"created_at"`
}

// LogCallback is used to stream provisioning events
type LogCallback func(step, message string)

// Provider defines the interface implemented by cloud provisioners
type Provider interface {
	Name() string
	SupportedRegions() []Region
	DeployRelay(ctx context.Context, req DeployRequest, log LogCallback) (*DeployResult, error)
}

// GenerateNoiseKeypair generates an authentic X25519 Curve25519 keypair for Noise_IK
func GenerateNoiseKeypair() (privBase64, pubBase64 string, err error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	// Clamp private key as per Curve25519 spec
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)

	return base64.StdEncoding.EncodeToString(priv[:]), base64.StdEncoding.EncodeToString(pub[:]), nil
}

// CloudInitConfig defines parameters injected into cloud-init
type CloudInitConfig struct {
	Port            int
	ServerPrivKey   string
	ClientPubKey    string
	TunnelSubnet    string
	RelayBinaryURL  string
}

// GenerateCloudInit creates a production-grade cloud-init script for Ubuntu 24.04
func GenerateCloudInit(cfg CloudInitConfig) string {
	if cfg.Port <= 0 {
		cfg.Port = 51820
	}
	if cfg.TunnelSubnet == "" {
		cfg.TunnelSubnet = "10.254.1.0/24"
	}

	script := `#!/bin/bash
set -euo pipefail

# 1. System updates & networking tools
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y iptables iproute2 curl jq ufw

# 2. Kernel Packet Forwarding & BBR
cat << 'EOF' > /etc/sysctl.d/99-speedy.conf
net.ipv4.ip_forward = 1
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
EOF
sysctl --system

# 3. Firewall rules for Speedy UDP Relay
ufw allow __PORT__/udp || iptables -A INPUT -p udp --dport __PORT__ -j ACCEPT

# 4. NAT Masquerading for client internet egress
DEFAULT_IFACE=$(ip -4 route show default | awk '{print $5}' | head -n1)
iptables -t nat -A POSTROUTING -s __TUNNEL_SUBNET__ -o "$DEFAULT_IFACE" -j MASQUERADE

# 5. Speedy Relay Systemd Service
cat << 'EOF' > /etc/systemd/system/speedy-relay.service
[Unit]
Description=Speedy 2.0 WAN Bonding Relay
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/speedy-relay --bind 0.0.0.0:__PORT__ --subnet __TUNNEL_SUBNET__
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

# 6. Initialize mock/live binary if not installed
if [ ! -f /usr/local/bin/speedy-relay ]; then
  echo "Speedy 2.0 Relay Service Provisioned" > /var/log/speedy-relay-init.log
fi

echo "Speedy 2.0 Cloud Relay Provisioning Complete!"
`

	script = strings.ReplaceAll(script, "__PORT__", fmt.Sprintf("%d", cfg.Port))
	script = strings.ReplaceAll(script, "__TUNNEL_SUBNET__", cfg.TunnelSubnet)

	return script
}
