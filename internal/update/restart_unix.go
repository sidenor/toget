//go:build !windows

package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// restartSupported reports whether the monitor can launch a restart command.
// Unix uses a detached shell so the HTTP response can be flushed first.
func restartSupported() bool { return true }

// launchRestart starts command in its own session after delay. The command is
// passed as positional arguments, never interpolated into the shell script.
// Output is appended to logPath for post-restart diagnosis.
func launchRestart(command []string, delay time.Duration, logPath string) error {
	if len(command) == 0 {
		return ErrNoRestartCommand
	}
	if delay < 0 {
		delay = 0
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("prepare restart log: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open restart log: %w", err)
	}
	defer logFile.Close()

	script := fmt.Sprintf("sleep %g; exec \"$@\"", delay.Seconds())
	args := append([]string{"-c", script, "toget-update-restart"}, command...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start restart command: %w", err)
	}
	return cmd.Process.Release()
}

// LaunchDetached is a small helper for platform code that needs the same
// detached-process behavior without duplicating build tags.
func LaunchDetached(command []string, delay time.Duration, logPath string) error {
	if !restartSupported() {
		return errors.New("detached process launch is not supported on this platform")
	}
	return launchRestart(command, delay, logPath)
}
