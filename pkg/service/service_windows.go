//go:build windows
// +build windows

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	ServiceName        = "speedy-tunnel"
	ServiceDisplayName = "Speedy 2.0 Multi-WAN Bonding Tunnel"
	ServiceDescription = "High-performance packet-level WAN bonding tunnel daemon with Wintun adapter management."
)

// SpeedyService implements svc.Handler for Windows SCM
type SpeedyService struct {
	cancel context.CancelFunc
}

func (s *SpeedyService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	defer cancel()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	// Service background worker loop
	go func() {
		// In a production service, MultipathEngine runs here.
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	for req := range r {
		switch req.Cmd {
		case svc.Interrogate:
			changes <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			cancel()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		default:
		}
	}

	return false, 0
}

// RunService runs the service under Windows Service Control Manager
func RunService() error {
	return svc.Run(ServiceName, &SpeedyService{})
}

// InstallService registers the Speedy tunnel daemon with Windows SCM
func InstallService(binPath string) error {
	if binPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
		binPath = exe
	}
	absBin, err := filepath.Abs(binPath)
	if err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager (Administrator elevation required): %w", err)
	}
	defer m.Disconnect()

	// Check if already exists
	s, err := m.OpenService(ServiceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s is already installed", ServiceName)
	}

	cfg := mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  ServiceDisplayName,
		Description:  ServiceDescription,
	}

	// Execute with --service run parameter
	s, err = m.CreateService(ServiceName, absBin, cfg, "--service", "run")
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}
	defer s.Close()

	return nil
}

// UninstallService stops and removes the service from Windows SCM
func UninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager (Administrator elevation required): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s is not installed: %w", ServiceName, err)
	}
	defer s.Close()

	// Attempt stop first
	_, _ = s.Control(svc.Stop)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}

	return nil
}

// StartService starts the installed Windows Service
func StartService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", ServiceName, err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

// StopService stops the installed Windows Service
func StopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", ServiceName, err)
	}
	defer s.Close()

	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("failed to stop service: %w", err)
	}

	_ = status
	return nil
}

// GetServiceStatus returns human-readable status of the service
func GetServiceStatus() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "UNKNOWN (SCM Connect Error)", err
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return "NOT_INSTALLED", nil
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return "ERROR", err
	}

	switch status.State {
	case svc.Stopped:
		return "STOPPED", nil
	case svc.StartPending:
		return "START_PENDING", nil
	case svc.StopPending:
		return "STOP_PENDING", nil
	case svc.Running:
		return "RUNNING", nil
	case svc.ContinuePending:
		return "CONTINUE_PENDING", nil
	case svc.PausePending:
		return "PAUSE_PENDING", nil
	case svc.Paused:
		return "PAUSED", nil
	default:
		return "UNKNOWN", nil
	}
}
