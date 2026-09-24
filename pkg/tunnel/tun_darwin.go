//go:build darwin

package tunnel

import (
	"fmt"
	"os"
)

type DarwinDevice struct {
	file *os.File
	name string
	mtu  int
}

// CreateTUN configures a utun device on Darwin (macOS)
func CreateTUN(name string, mtu int) (Device, error) {
	if mtu <= 0 {
		mtu = 1420
	}
	return nil, fmt.Errorf("utun interface creation on darwin requires root kernel control socket")
}

func (d *DarwinDevice) Name() string {
	return d.name
}

func (d *DarwinDevice) MTU() int {
	return d.mtu
}

func (d *DarwinDevice) Read(buf []byte) (int, error) {
	return d.file.Read(buf)
}

func (d *DarwinDevice) Write(buf []byte) (int, error) {
	return d.file.Write(buf)
}

func (d *DarwinDevice) Close() error {
	return d.file.Close()
}
