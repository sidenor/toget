//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func defaultServiceScope() string { return "user" }

func installService(ctx context.Context, opts serviceOptions) error {
	if opts.Scope == "system" && os.Geteuid() != 0 {
		return errors.New("--scope system requires root")
	}
	path, err := systemdUnitPath(opts)
	if err != nil {
		return err
	}
	content := systemdUnit(opts)
	commands := systemdInstallCommands(opts)
	if opts.DryRun {
		fmt.Fprintf(os.Stdout, "# systemd unit: %s\n%s\n# commands\n%s", path, content, commands)
		return nil
	}
	changed, err := writeServiceDefinition(path, []byte(content), opts.Force)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(os.Stdout, "Wrote %s\n", path)
	} else {
		fmt.Fprintf(os.Stdout, "Service definition is up to date: %s\n", path)
	}
	if opts.NoStart {
		fmt.Fprint(os.Stdout, commands)
		return nil
	}
	if err := runServiceCommand(ctx, "systemctl", systemdArgs(opts, "daemon-reload")...); err != nil {
		return fmt.Errorf("the unit was written to %s, but systemd could not reload it: %w; run `systemctl %s daemon-reload` after fixing the session", path, err, systemdScopeHint(opts))
	}
	if err := runServiceCommand(ctx, "systemctl", systemdArgs(opts, "enable", "--now", opts.Name+".service")...); err != nil {
		return fmt.Errorf("the unit was installed at %s, but could not be started: %w\n"+
			"for a user service, enable lingering with `sudo loginctl enable-linger %s` or install with --scope system",
			path, err, currentUsername())
	}
	fmt.Fprintf(os.Stdout, "Started %s.service\n", opts.Name)
	return nil
}

func uninstallService(ctx context.Context, opts serviceOptions) error {
	if opts.Scope == "system" && os.Geteuid() != 0 {
		return errors.New("--scope system requires root")
	}
	path, err := systemdUnitPath(opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Fprintf(os.Stdout, "# remove %s\nsystemctl %s disable --now %s.service\nsystemctl %s daemon-reload\n",
			path, systemdScopeHint(opts), opts.Name, systemdScopeHint(opts))
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stdout, "Toget service is not installed at %s\n", path)
		return nil
	}
	if err := runServiceCommand(ctx, "systemctl", systemdArgs(opts, "disable", "--now", opts.Name+".service")...); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not stop %s.service: %v\n", opts.Name, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := runServiceCommand(ctx, "systemctl", systemdArgs(opts, "daemon-reload")...); err != nil {
		return fmt.Errorf("removed %s, but systemd could not reload: %w", path, err)
	}
	fmt.Fprintf(os.Stdout, "Removed %s\n", path)
	return nil
}

func serviceStatus(ctx context.Context, opts serviceOptions) error {
	path, err := systemdUnitPath(opts)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Toget service is not installed at %s", path)
	}
	fmt.Fprintf(os.Stdout, "Unit: %s\n", path)
	command := exec.CommandContext(ctx, "systemctl", systemdArgs(opts, "status", "--no-pager", "--full", opts.Name+".service")...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			fmt.Fprintln(os.Stderr, "Service is not running.")
			return nil
		}
		return fmt.Errorf("systemctl status %s.service: %w", opts.Name, err)
	}
	return nil
}

func runService(name string) error {
	return errors.New("`service run` is only used by the Windows service manager")
}

func systemdUnitPath(opts serviceOptions) (string, error) {
	if opts.UnitDir != "" {
		return filepath.Join(opts.UnitDir, opts.Name+".service"), nil
	}
	if opts.Scope == "system" {
		return filepath.Join("/etc/systemd/system", opts.Name+".service"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}
	return filepath.Join(dir, "systemd", "user", opts.Name+".service"), nil
}

func systemdUnit(opts serviceOptions) string {
	wantedBy := "default.target"
	if opts.Scope == "system" {
		wantedBy = "multi-user.target"
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Toget local-first room\n")
	b.WriteString("Documentation=https://github.com/sidenor/toget\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "Environment=%s\n", systemdQuote("TOGET_HOME="+opts.Home))
	fmt.Fprintf(&b, "ExecStart=%s serve\n", systemdQuote(opts.Executable))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", systemdQuote(opts.Home))
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5s\n")
	b.WriteString("TimeoutStopSec=30s\n\n")
	b.WriteString("[Install]\n")
	fmt.Fprintf(&b, "WantedBy=%s\n", wantedBy)
	return b.String()
}

func systemdInstallCommands(opts serviceOptions) string {
	hint := systemdScopeHint(opts)
	return fmt.Sprintf("systemctl %s daemon-reload\nsystemctl %s enable --now %s.service\n", hint, hint, opts.Name)
}

func systemdScopeHint(opts serviceOptions) string {
	if opts.Scope == "user" {
		return "--user"
	}
	return ""
}

func systemdArgs(opts serviceOptions, args ...string) []string {
	if opts.Scope == "user" {
		return append([]string{"--user"}, args...)
	}
	return args
}

// systemdQuote quotes one systemd command argument or path. The percent sign
// starts a unit specifier and the dollar sign starts environment substitution,
// so both are doubled to keep user paths literal.
func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "$", "$$")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
