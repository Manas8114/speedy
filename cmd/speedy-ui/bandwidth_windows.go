//go:build windows

package main

import (
	"net"
	"syscall"
	"unsafe"
)

var (
	modiphlpapi     = syscall.NewLazyDLL("iphlpapi.dll")
	procGetIfEntry2 = modiphlpapi.NewProc("GetIfEntry2")
)

type MIB_IF_ROW2 struct {
	InterfaceLuid               uint64
	InterfaceIndex              uint32
	InterfaceGuid               [16]byte
	Alias                       [257]uint16
	Description                 [257]uint16
	PhysicalAddressLength       uint32
	PhysicalAddress             [32]byte
	PermanentPhysicalAddress    [32]byte
	Mtu                         uint32
	Type                        uint32
	TunnelType                  uint32
	MediaType                   uint32
	PhysicalMediumType          uint32
	AccessType                  uint32
	DirectionType               uint32
	InterfaceAndOperStatusFlags uint32
	OperStatus                  uint32
	AdminStatus                 uint32
	MediaConnectState           uint32
	NetworkGuid                 [16]byte
	ConnectionType              uint32
	_padding                    uint32
	TransmitLinkSpeed           uint64
	ReceiveLinkSpeed            uint64
	InOctets                    uint64
	InUcastPkts                 uint64
	InNUcastPkts                uint64
	InDiscards                  uint64
	InErrors                    uint64
	InUnknownProtos             uint64
	InUcastOctets               uint64
	InMulticastOctets           uint64
	InBroadcastOctets           uint64
	OutOctets                   uint64
	OutUcastPkts                uint64
	OutNUcastPkts               uint64
	OutDiscards                 uint64
	OutErrors                   uint64
	OutUcastOctets              uint64
	OutMulticastOctets          uint64
	OutBroadcastOctets          uint64
	OutQLen                     uint64
}

func getPlatformInterfaceBytes() map[string]uint64 {
	result := make(map[string]uint64)
	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, iface := range ifaces {
		var row MIB_IF_ROW2
		row.InterfaceIndex = uint32(iface.Index)
		ret, _, _ := procGetIfEntry2.Call(uintptr(unsafe.Pointer(&row)))
		if ret == 0 {
			result[iface.Name] = row.InOctets + row.OutOctets
		}
	}
	return result
}
