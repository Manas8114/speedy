//go:build !windows

package main

func getPlatformInterfaceBytes() map[string]uint64 {
	return make(map[string]uint64)
}
