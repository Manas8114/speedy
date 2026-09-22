//go:build linux

package routing

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

type osRouteExecutor struct{}

func newOSRouteExecutor() RouteExecutor {
	return &osRouteExecutor{}
}

func (e *osRouteExecutor) AddHostRoute(dst net.IP, gateway net.IP, iface string, metric int) error {
	args := []string{"route", "replace", dst.String() + "/32"}
	if gateway != nil && !gateway.IsUnspecified() {
		args = append(args, "via", gateway.String())
	}
	if iface != "" {
		args = append(args, "dev", iface)
	}
	if metric > 0 {
		args = append(args, "metric", fmt.Sprintf("%d", metric))
	}
	cmd := exec.Command("ip", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip route replace host failed: %s: %w", string(out), err)
	}
	return nil
}

func (e *osRouteExecutor) DeleteHostRoute(dst net.IP, gateway net.IP, iface string) error {
	cmd := exec.Command("ip", "route", "del", dst.String()+"/32")
	_ = cmd.Run()
	return nil
}

func (e *osRouteExecutor) AddDefaultRoute(tunIface string, tunIP net.IP, metric int) error {
	cmd := exec.Command("ip", "route", "add", "default", "dev", tunIface, "metric", fmt.Sprintf("%d", metric))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip route add default failed: %s: %w", string(out), err)
	}
	return nil
}

func (e *osRouteExecutor) DeleteDefaultRoute(tunIface string, tunIP net.IP) error {
	cmd := exec.Command("ip", "route", "del", "default", "dev", tunIface)
	_ = cmd.Run()
	return nil
}

func (e *osRouteExecutor) GetBestRoute(dst net.IP) (net.IP, string, error) {
	out, err := exec.Command("ip", "route", "get", dst.String()).CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("ip route get failed: %s: %w", string(out), err)
	}
	// Example output: "198.51.100.1 via 192.168.1.1 dev eth0 src 192.168.1.50"
	fields := strings.Fields(string(out))
	var gw net.IP
	var iface string
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "via" {
			gw = net.ParseIP(fields[i+1])
		}
		if fields[i] == "dev" {
			iface = fields[i+1]
		}
	}
	return gw, iface, nil
}
