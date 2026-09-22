//go:build windows

package platform

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

const (
	IP_UNICAST_IF = 31 // winsock option for IP_UNICAST_IF
)

// BindSocketToInterface pins an outgoing UDP socket to a specific interface index on Windows
func BindSocketToInterface(conn *net.UDPConn, ifIndex int, ifaceName string) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("failed to get syscall conn: %w", err)
	}

	var sockErr error
	err = raw.Control(func(fd uintptr) {
		// Set IP_UNICAST_IF socket option
		ifIndexBigEndian := uint32(ifIndex)
		// On Windows, IP_UNICAST_IF takes the interface index in network byte order
		nOrder := ((ifIndexBigEndian & 0xFF) << 24) |
			((ifIndexBigEndian & 0xFF00) << 8) |
			((ifIndexBigEndian & 0xFF0000) >> 8) |
			((ifIndexBigEndian & 0xFF000000) >> 24)

		sockErr = syscall.Setsockopt(
			syscall.Handle(fd),
			syscall.IPPROTO_IP,
			IP_UNICAST_IF,
			(*byte)(unsafe.Pointer(&nOrder)),
			int32(unsafe.Sizeof(nOrder)),
		)
	})

	if err != nil {
		return err
	}
	if sockErr != nil {
		return fmt.Errorf("setsockopt IP_UNICAST_IF (%d) failed: %w", ifIndex, sockErr)
	}
	return nil
}
