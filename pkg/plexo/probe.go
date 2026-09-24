package plexo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProbeResult holds all metadata extracted from a URL probe.
type ProbeResult struct {
	URL            string `json:"url"`
	Filename       string `json:"filename"`
	TotalBytes     int64  `json:"total_bytes"`
	SupportsRanges bool   `json:"supports_ranges"`
	ETag           string `json:"etag,omitempty"`
	LastModified   string `json:"last_modified,omitempty"`
	ContentType    string `json:"content_type,omitempty"`
	TTFBMs         int64  `json:"ttfb_ms"`
}

// InterfaceInfo describes a physical network adapter available for socket binding.
type InterfaceInfo struct {
	Name         string   `json:"name"`
	HardwareAddr string   `json:"hardware_addr"`
	LocalIPs     []string `json:"local_ips"`
	Routable     bool     `json:"routable"`
}

// filenameRe matches Content-Disposition filename= or filename*= values.
var filenameRe = regexp.MustCompile(`filename[*]?=["']?([^"';\r\n]+)`)

// ProbeURL sends a 1-byte ranged GET request to verify HTTP byte-range support,
// extract total file size, ETag, Content-Disposition filename, and MIME type.
// If the server returns 200 instead of 206, SupportsRanges is false and we fall
// back to a single-stream download.
func ProbeURL(ctx context.Context, rawURL string) (*ProbeResult, error) {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q (use http or https)", parsed.Scheme)
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building probe request: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "Speedy-Plexo/2.0")

	t0 := time.Now()
	resp, err := client.Do(req)
	ttfbMs := time.Since(t0).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("probe request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	result := &ProbeResult{
		URL:          resp.Request.URL.String(),
		TTFBMs:       ttfbMs,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}

	result.SupportsRanges = resp.StatusCode == http.StatusPartialContent

	if result.SupportsRanges {
		cr := resp.Header.Get("Content-Range")
		if cr != "" {
			parts := strings.SplitN(cr, "/", 2)
			if len(parts) == 2 && parts[1] != "*" {
				n, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
				if err == nil {
					result.TotalBytes = n
				}
			}
		}
	} else {
		cl := resp.Header.Get("Content-Length")
		if cl != "" {
			n, err := strconv.ParseInt(cl, 10, 64)
			if err == nil {
				result.TotalBytes = n
			}
		}
	}

	cd := resp.Header.Get("Content-Disposition")
	if cd != "" {
		if m := filenameRe.FindStringSubmatch(cd); len(m) > 1 {
			result.Filename = strings.TrimSpace(m[1])
		}
	}
	if result.Filename == "" {
		p := parsed.Path
		if idx := strings.LastIndex(p, "/"); idx >= 0 {
			result.Filename = p[idx+1:]
		}
		if result.Filename == "" {
			result.Filename = "download"
		}
	}

	return result, nil
}

// AvailableInterfaces returns all non-loopback network interfaces that are UP
// and have at least one IPv4 address, suitable for socket binding.
func AvailableInterfaces() ([]InterfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []InterfaceInfo
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var ipv4s []string
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.To4() != nil {
				ipv4s = append(ipv4s, ip.String())
			}
		}
		if len(ipv4s) == 0 {
			continue
		}
		routable := false
		for _, ipStr := range ipv4s {
			if isIPRoutable(ipStr) {
				routable = true
				break
			}
		}
		result = append(result, InterfaceInfo{
			Name:         iface.Name,
			HardwareAddr: iface.HardwareAddr.String(),
			LocalIPs:     ipv4s,
			Routable:     routable,
		})
	}
	return result, nil
}

// isIPRoutable verifies if an IP has an active route toward the public internet
func isIPRoutable(ipStr string) bool {
	laddr := &net.UDPAddr{IP: net.ParseIP(ipStr)}
	raddr := &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 53}
	conn, err := net.DialUDP("udp4", laddr, raddr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

