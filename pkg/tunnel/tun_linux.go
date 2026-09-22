//go:build linux

package tunnel

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	cloneDevicePath = "/dev/net/tun"
	ifReqSize       = 40
)

type LinuxDevice struct {
	file *os.File
	name string
	mtu  int
}

// CreateTUN opens /dev/net/tun and configures an IFF_TUN device
func CreateTUN(name string, mtu int) (Device, error) {
	if mtu <= 0 {
		mtu = 1420
	}

	file, err := os.OpenFile(cloneDevicePath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s failed: %w", cloneDevicePath, err)
	}

	var req [ifReqSize]byte
	copy(req[:16], []byte(name))
	flags := uint16(unix.IFF_TUN | unix.IFF_NO_PI)
	*(*uint16)(unsafe.Pointer(&req[16])) = flags

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		file.Fd(),
		uintptr(unix.TUNSETIFF),
		uintptr(unsafe.Pointer(&req[0])),
	)
	if errno != 0 {
		file.Close()
		return nil, fmt.Errorf("ioctl TUNSETIFF failed: %w", errno)
	}

	actualName := string(req[:16])
	for i, c := range actualName {
		if c == 0 {
			actualName = actualName[:i]
			break
		}
	}

	return &LinuxDevice{
		file: file,
		name: actualName,
		mtu:  mtu,
	}, nil
}

func (d *LinuxDevice) Read(b []byte) (int, error) {
	return d.file.Read(b)
}

func (d *LinuxDevice) Write(b []byte) (int, error) {
	return d.file.Write(b)
}

func (d *LinuxDevice) Close() error {
	return d.file.Close()
}

func (d *LinuxDevice) Name() string {
	return d.name
}

func (d *LinuxDevice) MTU() int {
	return d.mtu
}
