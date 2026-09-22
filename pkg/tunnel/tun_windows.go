//go:build windows

package tunnel

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

var (
	wintunDLL                   *syscall.DLL
	wintunCreateAdapter         *syscall.Proc
	wintunOpenAdapter           *syscall.Proc
	wintunCloseAdapter          *syscall.Proc
	wintunStartSession          *syscall.Proc
	wintunEndSession            *syscall.Proc
	wintunGetReadWaitEvent      *syscall.Proc
	wintunReceivePacket         *syscall.Proc
	wintunReleaseReceivePacket  *syscall.Proc
	wintunAllocateSendPacket    *syscall.Proc
	wintunSendPacket            *syscall.Proc
	wintunInitOnce              sync.Once
	wintunInitErr               error
)

func initWintunProcs() error {
	wintunInitOnce.Do(func() {
		// Look in current working directory, executable directory, or system path
		dllPaths := []string{
			"wintun.dll",
			filepath.Join("bin", "wintun.dll"),
			"C:\\Program Files\\O+Connect\\daemon\\wintun.dll",
		}

		var dll *syscall.DLL
		var err error
		for _, p := range dllPaths {
			dll, err = syscall.LoadDLL(p)
			if err == nil {
				break
			}
		}

		if dll == nil {
			wintunInitErr = fmt.Errorf("wintun.dll not found: %w", err)
			return
		}

		wintunDLL = dll
		wintunCreateAdapter = dll.MustFindProc("WintunCreateAdapter")
		wintunOpenAdapter = dll.MustFindProc("WintunOpenAdapter")
		wintunCloseAdapter = dll.MustFindProc("WintunCloseAdapter")
		wintunStartSession = dll.MustFindProc("WintunStartSession")
		wintunEndSession = dll.MustFindProc("WintunEndSession")
		wintunGetReadWaitEvent = dll.MustFindProc("WintunGetReadWaitEvent")
		wintunReceivePacket = dll.MustFindProc("WintunReceivePacket")
		wintunReleaseReceivePacket = dll.MustFindProc("WintunReleaseReceivePacket")
		wintunAllocateSendPacket = dll.MustFindProc("WintunAllocateSendPacket")
		wintunSendPacket = dll.MustFindProc("WintunSendPacket")
	})
	return wintunInitErr
}

// WindowsDevice represents a genuine Wintun TUN network interface on Windows
type WindowsDevice struct {
	name      string
	mtu       int
	adapter   uintptr
	session   uintptr
	readEvent syscall.Handle
	closed    chan struct{}
	closeOnce sync.Once
}

// CreateTUN opens or creates a real Wintun adapter and starts a 4MB ring buffer session
func CreateTUN(name string, mtu int) (Device, error) {
	if mtu <= 0 {
		mtu = 1420
	}

	if err := initWintunProcs(); err != nil {
		return nil, fmt.Errorf("failed to initialize Wintun driver: %w", err)
	}

	adapterName, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	tunnelType, err := syscall.UTF16PtrFromString("Speedy")
	if err != nil {
		return nil, err
	}

	// Try OpenAdapter first, else CreateAdapter
	adapter, _, _ := wintunOpenAdapter.Call(uintptr(unsafe.Pointer(adapterName)))
	if adapter == 0 {
		var guid [16]byte
		adapter, _, err = wintunCreateAdapter.Call(
			uintptr(unsafe.Pointer(adapterName)),
			uintptr(unsafe.Pointer(tunnelType)),
			uintptr(unsafe.Pointer(&guid[0])),
		)
		if adapter == 0 {
			return nil, fmt.Errorf("WintunCreateAdapter(%q) failed: %w", name, err)
		}
	}

	// Start ring buffer session (capacity: 0x400000 = 4MB)
	session, _, err := wintunStartSession.Call(adapter, 0x400000)
	if session == 0 {
		wintunCloseAdapter.Call(adapter)
		return nil, fmt.Errorf("WintunStartSession failed: %w", err)
	}

	readEventHandle, _, _ := wintunGetReadWaitEvent.Call(session)

	return &WindowsDevice{
		name:      name,
		mtu:       mtu,
		adapter:   adapter,
		session:   session,
		readEvent: syscall.Handle(readEventHandle),
		closed:    make(chan struct{}),
	}, nil
}

// Read receives an authentic IP packet from the Wintun ring buffer
func (w *WindowsDevice) Read(b []byte) (int, error) {
	for {
		select {
		case <-w.closed:
			return 0, ErrDeviceClosed
		default:
		}

		var packetSize uint32
		packet, _, _ := wintunReceivePacket.Call(w.session, uintptr(unsafe.Pointer(&packetSize)))
		if packet != 0 {
			size := int(packetSize)
			if len(b) < size {
				wintunReleaseReceivePacket.Call(w.session, packet)
				return 0, ErrBufferSmall
			}

			// Copy packet data from driver ring buffer to b
			srcSlice := unsafe.Slice((*byte)(unsafe.Pointer(packet)), size)
			copy(b, srcSlice)

			wintunReleaseReceivePacket.Call(w.session, packet)
			return size, nil
		}

		// Wait for packet arrival event or 50ms timeout
		event, err := syscall.WaitForSingleObject(w.readEvent, 50)
		if err != nil {
			return 0, err
		}
		if event == syscall.WAIT_FAILED {
			return 0, errors.New("Wintun WaitForSingleObject failed")
		}
	}
}

// Write injects an authentic IP packet into the Wintun ring buffer towards the OS
func (w *WindowsDevice) Write(b []byte) (int, error) {
	select {
	case <-w.closed:
		return 0, ErrDeviceClosed
	default:
	}

	size := uint32(len(b))
	packet, _, err := wintunAllocateSendPacket.Call(w.session, uintptr(size))
	if packet == 0 {
		return 0, fmt.Errorf("WintunAllocateSendPacket failed: %w", err)
	}

	dstSlice := unsafe.Slice((*byte)(unsafe.Pointer(packet)), size)
	copy(dstSlice, b)

	wintunSendPacket.Call(w.session, packet)
	return len(b), nil
}

// Close closes the Wintun session and adapter
func (w *WindowsDevice) Close() error {
	w.closeOnce.Do(func() {
		close(w.closed)
		if w.session != 0 {
			wintunEndSession.Call(w.session)
		}
		if w.adapter != 0 {
			wintunCloseAdapter.Call(w.adapter)
		}
	})
	return nil
}

func (w *WindowsDevice) Name() string {
	return w.name
}

func (w *WindowsDevice) MTU() int {
	return w.mtu
}
