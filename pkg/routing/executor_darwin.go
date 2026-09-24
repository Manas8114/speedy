//go:build darwin

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
	args := []string{"add", "-host", dst.String()}
	if gateway != nil && !gateway.IsUnspecified() {
		args = append(args, gateway.String())
	} else if iface != "" {
		args = append(args, "-interface", iface)
	}
	cmd := exec.Command("route", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("route add host failed: %s: %w", string(out), err)
	}
	return nil
}

func (e *osRouteExecutor) DeleteHostRoute(dst net.IP, gateway net.IP, iface string) error {
	cmd := exec.Command("route", "delete", "-host", dst.String())
	_ = cmd.Run()
	return nil
}

func (e *osRouteExecutor) AddDefaultRoute(tunIface string, tunIP net.IP, metric int) error {
	cmd := exec.Command("route", "add", "default", "-interface", tunIface)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("route add default failed: %s: %w", string(out), err)
	}
	return nil
}

func (e *osRouteExecutor) DeleteDefaultRoute(tunIface string, tunIP net.IP) error {
	cmd := exec.Command("route", "delete", "default", "-interface", tunIface)
	_ = cmd.Run()
	return nil
}

func (e *osRouteExecutor) GetBestRoute(dst net.IP) (net.IP, string, error) {
	out, err := exec.Command("route", "-n", "get", dst.String()).CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("route get failed: %s: %w", string(out), err)
	}
	gw := parseDarwinGateway(string(out))
	iface := parseDarwinInterface(string(out))
	return gw, iface, nil
}

func parseDarwinGateway(out string) net.IP {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return net.ParseIP(parts[1])
			}
		}
	}
	return nil
}

func parseDarwinInterface(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}
