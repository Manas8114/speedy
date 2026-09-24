//go:build darwin

package platform

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// IP_BOUND_IF is the socket option on Darwin (macOS/iOS) to bind an IP socket to a specific interface index.
const IP_BOUND_IF = 25

// BindSocketToInterface pins an outgoing UDP socket to a physical interface index on Darwin.
func BindSocketToInterface(conn *net.UDPConn, ifIndex int, ifaceName string) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("failed to get syscall conn: %w", err)
	}

	var sockErr error
	err = raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, IP_BOUND_IF, ifIndex)
	})

	if err != nil {
		return err
	}
	if sockErr != nil {
		return fmt.Errorf("BindSocketToInterface (IP_BOUND_IF %s/%d) failed: %w", ifaceName, ifIndex, sockErr)
	}
	return nil
}
