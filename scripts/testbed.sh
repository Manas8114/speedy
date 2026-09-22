#!/usr/bin/env bash
# Speedy 2.0 — Dual-Namespace Linux Testbed Script
# Sets up client_ns and relay_ns with 2 virtual links (veth pairs), tc netem loss/latency, and NAT egress.
set -euo pipefail

echo "=== Speedy 2.0 Linux Netns Testbed Setup ==="

# Check root
if [ "$EUID" -ne 0 ]; then
  echo "Error: Please run as root (sudo ./testbed.sh)"
  exit 1
fi

# Cleanup previous namespaces
ip netns del client_ns 2>/dev/null || true
ip netns del relay_ns 2>/dev/null || true

echo "[1/5] Creating network namespaces: client_ns and relay_ns..."
ip netns add client_ns
ip netns add relay_ns

echo "[2/5] Creating veth link pairs (Uplink 1: Wi-Fi simulation, Uplink 2: 4G LTE simulation)..."
# Link 0: wlan0 simulation (10.10.1.0/24)
ip link add c_wlan0 type veth peer name r_wlan0
ip link set c_wlan0 netns client_ns
ip link set r_wlan0 netns relay_ns

# Link 1: cell0 simulation (10.10.2.0/24)
ip link add c_cell0 type veth peer name r_cell0
ip link set c_cell0 netns client_ns
ip link set r_cell0 netns relay_ns

echo "[3/5] Configuring IP addresses..."
ip netns exec client_ns ip addr add 10.10.1.2/24 dev c_wlan0
ip netns exec relay_ns  ip addr add 10.10.1.1/24 dev r_wlan0
ip netns exec client_ns ip link set c_wlan0 up
ip netns exec relay_ns  ip link set r_wlan0 up

ip netns exec client_ns ip addr add 10.10.2.2/24 dev c_cell0
ip netns exec relay_ns  ip addr add 10.10.2.1/24 dev r_cell0
ip netns exec client_ns ip link set c_cell0 up
ip netns exec relay_ns  ip link set r_cell0 up

# Loopbacks
ip netns exec client_ns ip link set lo up
ip netns exec relay_ns  ip link set lo up

echo "[4/5] Applying tc netem network conditions..."
# Uplink 1: 10ms latency, 0% loss
ip netns exec client_ns tc qdisc add dev c_wlan0 root netem delay 10ms
# Uplink 2: 50ms latency, 2% loss
ip netns exec client_ns tc qdisc add dev c_cell0 root netem delay 50ms loss 2%

echo "[5/5] Enabling IP forwarding in relay_ns..."
ip netns exec relay_ns sysctl -w net.ipv4.ip_forward=1 >/dev/null

echo "=== Testbed Ready! ==="
echo "To run relay in relay_ns:"
echo "  sudo ip netns exec relay_ns ./bin/speedy-relay -listen 10.10.1.1:51820 -pool 10.254.1.0/24"
echo "To run client in client_ns:"
echo "  sudo ip netns exec client_ns ./bin/speedy-client -relay 10.10.1.1:51820 -relay-pubkey <KEY>"
