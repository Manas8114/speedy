package platform

import (
	"os"
	"runtime"
	"strings"
)

// CheckMPTCPSupport checks whether the host operating system supports Linux Kernel MPTCP
func CheckMPTCPSupport() (supported bool, reason string) {
	if runtime.GOOS != "linux" {
		return false, "Kernel MPTCP is only available on Linux (kernel 5.6+)"
	}

	// Check /proc/sys/net/mptcp/enabled
	data, err := os.ReadFile("/proc/sys/net/mptcp/enabled")
	if err != nil {
		return false, "MPTCP sysctl (/proc/sys/net/mptcp/enabled) not found"
	}

	val := strings.TrimSpace(string(data))
	if val == "1" {
		return true, "Kernel MPTCP is enabled and available as a zero-overhead fast path"
	}

	return false, "Kernel MPTCP is present but disabled (/proc/sys/net/mptcp/enabled != 1)"
}
