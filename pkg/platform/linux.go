//go:build linux || android

package platform

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// BindSocketToInterface pins an outgoing UDP socket to a physical interface name on Linux
func BindSocketToInterface(conn *net.UDPConn, ifIndex int, ifaceName string) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("failed to get syscall conn: %w", err)
	}

	var sockErr error
	err = raw.Control(func(fd uintptr) {
		sockErr = unix.BindToDevice(int(fd), ifaceName)
	})

	if err != nil {
		return err
	}
	if sockErr != nil {
		return fmt.Errorf("BindToDevice (%s) failed: %w", ifaceName, sockErr)
	}
	return nil
}
