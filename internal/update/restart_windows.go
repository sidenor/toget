//go:build windows

package update

import (
	"errors"
	"time"
)

// restartSupported is false because restarting a Windows service from inside
// its own process requires a service-specific helper. Staged updates still
// work; the operator restarts Toget manually.
func restartSupported() bool { return false }

func launchRestart(_ []string, _ time.Duration, _ string) error {
	return errors.New("automatic restart is not supported on Windows; restart Toget manually")
}

// LaunchDetached reports that automatic restart is unavailable on Windows.
func LaunchDetached(_ []string, _ time.Duration, _ string) error {
	return launchRestart(nil, 0, "")
}
