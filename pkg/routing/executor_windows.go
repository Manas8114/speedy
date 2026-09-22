//go:build windows

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
	// route add <dst> mask 255.255.255.255 <gateway> metric <metric>
	args := []string{"add", dst.String(), "mask", "255.255.255.255", gateway.String(), "metric", fmt.Sprintf("%d", metric)}
	cmd := exec.Command("route", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("route add failed: %s: %w", string(out), err)
	}
	return nil
}

func (e *osRouteExecutor) DeleteHostRoute(dst net.IP, gateway net.IP, iface string) error {
	cmd := exec.Command("route", "delete", dst.String())
	_ = cmd.Run()
	return nil
}

func (e *osRouteExecutor) AddDefaultRoute(tunIface string, tunIP net.IP, metric int) error {
	// Install 0.0.0.0/1 and 128.0.0.0/1 routes (Wireguard / OpenVPN style so original default route is preserved)
	cmd1 := exec.Command("route", "add", "0.0.0.0", "mask", "128.0.0.0", tunIP.String(), "metric", fmt.Sprintf("%d", metric))
	cmd2 := exec.Command("route", "add", "128.0.0.0", "mask", "128.0.0.0", tunIP.String(), "metric", fmt.Sprintf("%d", metric))
	if out, err := cmd1.CombinedOutput(); err != nil {
		return fmt.Errorf("route add 0/1 failed: %s: %w", string(out), err)
	}
	if out, err := cmd2.CombinedOutput(); err != nil {
		return fmt.Errorf("route add 128/1 failed: %s: %w", string(out), err)
	}
	return nil
}

func (e *osRouteExecutor) DeleteDefaultRoute(tunIface string, tunIP net.IP) error {
	_ = exec.Command("route", "delete", "0.0.0.0", "mask", "128.0.0.0").Run()
	_ = exec.Command("route", "delete", "128.0.0.0", "mask", "128.0.0.0").Run()
	return nil
}

func (e *osRouteExecutor) GetBestRoute(dst net.IP) (net.IP, string, error) {
	// Parse default gateway from route print 0.0.0.0
	out, err := exec.Command("route", "print", "0.0.0.0").CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("failed to run 'route print 0.0.0.0': %w", err)
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			gw := net.ParseIP(fields[2])
			if gw != nil {
				return gw, fields[3], nil
			}
		}
	}
	return nil, "", fmt.Errorf("no default gateway found in route table")
}
