//go:build !linux && !darwin && !windows

package main

import (
	"context"
	"errors"
)

func defaultServiceScope() string { return "user" }

func installService(context.Context, serviceOptions) error {
	return errors.New("service installation is not supported on this operating system")
}

func uninstallService(context.Context, serviceOptions) error {
	return errors.New("service installation is not supported on this operating system")
}

func serviceStatus(context.Context, serviceOptions) error {
	return errors.New("service installation is not supported on this operating system")
}

func runService(string) error {
	return errors.New("service installation is not supported on this operating system")
}
