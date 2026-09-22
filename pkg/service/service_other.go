//go:build !windows
// +build !windows

package service

import (
	"errors"
)

var ErrNotSupported = errors.New("windows service operations are only supported on Windows")

func RunService() error {
	return ErrNotSupported
}

func InstallService(binPath string) error {
	return ErrNotSupported
}

func UninstallService() error {
	return ErrNotSupported
}

func StartService() error {
	return ErrNotSupported
}

func StopService() error {
	return ErrNotSupported
}

func GetServiceStatus() (string, error) {
	return "UNSUPPORTED_PLATFORM", ErrNotSupported
}
