#!/usr/bin/env bash
# Speedy 2.0 — One-Click Relay Deployment Script
# Automatically deploys Speedy 2.0 Relay on Ubuntu/Debian VPS (DigitalOcean, Hetzner, AWS, etc.)
set -euo pipefail

echo "=========================================="
echo "    Speedy 2.0 Relay Deployment Script    "
echo "=========================================="

PORT=${PORT:-51820}
POOL=${POOL:-"10.254.1.0/24"}
EGRESS_IFACE=$(ip route show default | awk '{print $5}' | head -n1)

echo "[1/4] Detecting egress network interface: ${EGRESS_IFACE}..."

echo "[2/4] Enabling IP forwarding..."
sysctl -w net.ipv4.ip_forward=1
echo "net.ipv4.ip_forward = 1" > /etc/sysctl.d/99-speedy.conf

echo "[3/4] Configuring iptables NAT masquerade..."
iptables -t nat -C POSTROUTING -s "${POOL}" -o "${EGRESS_IFACE}" -j MASQUERADE 2>/dev/null || \
iptables -t nat -A POSTROUTING -s "${POOL}" -o "${EGRESS_IFACE}" -j MASQUERADE

# Save iptables rules
if command -v netfilter-persistent >/dev/null 2>&1; then
    netfilter-persistent save
fi

echo "[4/4] Generating systemd service unit..."
cat <<EOF > /etc/systemd/system/speedy-relay.service
[Unit]
Description=Speedy 2.0 WAN Bonding Relay
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/speedy
ExecStart=/opt/speedy/speedy-relay -listen :${PORT} -pool ${POOL} -nat-iface ${EGRESS_IFACE}
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
echo "=========================================="
echo "Speedy 2.0 Relay Configured!"
echo "Binary location: /opt/speedy/speedy-relay"
echo "Start service:   systemctl start speedy-relay"
echo "View logs:       journalctl -u speedy-relay -f"
echo "=========================================="
