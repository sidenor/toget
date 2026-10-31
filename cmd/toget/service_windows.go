//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func defaultServiceScope() string { return "system" }

func installService(ctx context.Context, opts serviceOptions) error {
	if opts.Scope != "system" {
		return errors.New("Windows services use the system scope")
	}
	if opts.UnitDir != "" {
		return errors.New("--unit-dir is not supported on Windows")
	}
	args := []string{"service", "run", "--name", opts.Name, "--home", opts.Home}
	if opts.DryRun {
		fmt.Fprintf(os.Stdout, "# Windows service: %s\n", opts.Name)
		fmt.Fprintf(os.Stdout, "# executable: %s\n", opts.Executable)
		fmt.Fprintf(os.Stdout, "# arguments: service run --name %s --home %q\n", opts.Name, opts.Home)
		fmt.Fprintf(os.Stdout, "sc.exe create %s binPath= \"\\\"%s\\\" service run --name %s --home \\\"%s\\\"\" start= delayed-auto\n",
			opts.Name, opts.Executable, opts.Name, opts.Home)
		fmt.Fprintf(os.Stdout, "sc.exe start %s\n", opts.Name)
		return nil
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as Administrator): %w", err)
	}
	defer manager.Disconnect()
	if existing, err := manager.OpenService(opts.Name); err == nil {
		if !opts.Force {
			existing.Close()
			return fmt.Errorf("service %s is already installed (use --force to replace it)", opts.Name)
		}
		removeErr := stopWindowsService(existing)
		if removeErr == nil {
			removeErr = existing.Delete()
		}
		existing.Close()
		if removeErr != nil {
			return fmt.Errorf("replace service %s: %w", opts.Name, removeErr)
		}
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	config := mgr.Config{
		DisplayName:      "Toget",
		Description:      "Toget local-first rooms for people and Agents",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}
	service, err := manager.CreateService(opts.Name, opts.Executable, config, args...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer service.Close()
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	}, 86400); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not configure service recovery: %v\n", err)
	}
	if opts.NoStart {
		fmt.Fprintf(os.Stdout, "Installed %s (not started)\nStart it with: sc.exe start %s\n", opts.Name, opts.Name)
		return nil
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("the service was installed but could not be started: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Installed and started %s\n", opts.Name)
	fmt.Fprintf(os.Stdout, "Logs: %s\n", filepath.Join(opts.Home, "logs", "toget-service.log"))
	return nil
}

func uninstallService(ctx context.Context, opts serviceOptions) error {
	if opts.Scope != "system" {
		return errors.New("Windows services use the system scope")
	}
	if opts.UnitDir != "" {
		return errors.New("--unit-dir is not supported on Windows")
	}
	if opts.DryRun {
		fmt.Fprintf(os.Stdout, "# stop and delete Windows service %s\nsc.exe stop %s\nsc.exe delete %s\n", opts.Name, opts.Name, opts.Name)
		return nil
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as Administrator): %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(opts.Name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		fmt.Fprintf(os.Stdout, "Toget service %s is not installed\n", opts.Name)
		return nil
	}
	if err != nil {
		return err
	}
	defer service.Close()
	if err := stopWindowsService(service); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Removed %s\n", opts.Name)
	return nil
}

func serviceStatus(ctx context.Context, opts serviceOptions) error {
	if opts.Scope != "system" {
		return errors.New("Windows services use the system scope")
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as Administrator): %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(opts.Name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return fmt.Errorf("Toget service %s is not installed", opts.Name)
	}
	if err != nil {
		return err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Service: %s\n", opts.Name)
	fmt.Fprintf(os.Stdout, "State:   %s\n", windowsServiceState(status.State))
	if status.ProcessId != 0 {
		fmt.Fprintf(os.Stdout, "PID:     %d\n", status.ProcessId)
	}
	if status.Win32ExitCode != 0 {
		fmt.Fprintf(os.Stdout, "Exit:    %d (service-specific: %d)\n", status.Win32ExitCode, status.ServiceSpecificExitCode)
	}
	return nil
}

func runService(name string) error {
	home, err := resolveServiceHome()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "toget-service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open service log: %w", err)
	}
	defer logFile.Close()
	// A Windows service has no console. Point the process output at a file
	// before runServe constructs its logger.
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = logFile, logFile
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
	}()
	return svc.Run(name, windowsServiceHandler{})
}

type windowsServiceHandler struct{}

func (windowsServiceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx)
	}()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- svc.Status{State: svc.Running, Accepts: accepted}
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-done:
					if err != nil {
						fmt.Fprintf(os.Stderr, "toget service stopped with an error: %v\n", err)
						return false, 1
					}
				case <-time.After(20 * time.Second):
					fmt.Fprintln(os.Stderr, "toget service did not stop within 20 seconds")
					return false, 1
				}
				return false, 0
			}
		case err := <-done:
			if err != nil {
				fmt.Fprintf(os.Stderr, "toget service stopped: %v\n", err)
				return false, 1
			}
			return false, 0
		}
	}
}

func stopWindowsService(service *mgr.Service) error {
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err = service.Query()
		if err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("timed out waiting for the service to stop")
}

func windowsServiceState(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start pending"
	case svc.StopPending:
		return "stop pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue pending"
	case svc.PausePending:
		return "pause pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown (%d)", state)
	}
}
