//go:build darwin

package main

import (
	"context"
	"encoding/xml"
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
	path, err := launchdPlistPath(opts)
	if err != nil {
		return err
	}
	if !opts.DryRun {
		if err := os.MkdirAll(filepath.Join(opts.Home, "logs"), 0o700); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}
	content, err := launchdPlist(opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Fprintf(os.Stdout, "# launchd property list: %s\n%s\n# commands\n%s", path, content, launchdInstallCommands(opts, path))
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
		fmt.Fprint(os.Stdout, launchdInstallCommands(opts, path))
		return nil
	}
	if err := launchdBootstrap(ctx, opts, path); err != nil {
		return fmt.Errorf("the property list was installed at %s, but launchd could not load it: %w", path, err)
	}
	fmt.Fprintf(os.Stdout, "Started %s\n", launchdLabel(opts.Name))
	return nil
}

func uninstallService(ctx context.Context, opts serviceOptions) error {
	if opts.Scope == "system" && os.Geteuid() != 0 {
		return errors.New("--scope system requires root")
	}
	path, err := launchdPlistPath(opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Fprintf(os.Stdout, "# remove %s\n%s\n", path, launchdUninstallCommands(opts, path))
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stdout, "Toget service is not installed at %s\n", path)
		return nil
	}
	if err := launchdBootout(ctx, opts, path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not unload %s: %v\n", launchdLabel(opts.Name), err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(os.Stdout, "Removed %s\n", path)
	return nil
}

func serviceStatus(ctx context.Context, opts serviceOptions) error {
	path, err := launchdPlistPath(opts)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Toget service is not installed at %s", path)
	}
	fmt.Fprintf(os.Stdout, "Property list: %s\n", path)
	command := exec.CommandContext(ctx, "launchctl", "print", launchdServiceTarget(opts))
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Service is not loaded.")
		return nil
	}
	return nil
}

func runService(name string) error {
	return errors.New("`service run` is only used by the Windows service manager")
}

func launchdLabel(name string) string {
	if name == defaultServiceName {
		return "dev.toget.node"
	}
	return "dev.toget." + name
}

func launchdPlistPath(opts serviceOptions) (string, error) {
	file := launchdLabel(opts.Name) + ".plist"
	if opts.UnitDir != "" {
		return filepath.Join(opts.UnitDir, file), nil
	}
	if opts.Scope == "system" {
		return filepath.Join("/Library/LaunchDaemons", file), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", file), nil
}

func launchdPlist(opts serviceOptions) (string, error) {
	label := launchdLabel(opts.Name)
	logDir := filepath.Join(opts.Home, "logs")
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	plistKey(&b, "Label")
	plistString(&b, label)
	plistKey(&b, "ProgramArguments")
	b.WriteString("  <array>\n")
	plistString(&b, opts.Executable)
	plistString(&b, "serve")
	b.WriteString("  </array>\n")
	plistKey(&b, "EnvironmentVariables")
	b.WriteString("  <dict>\n")
	plistKey(&b, "TOGET_HOME")
	plistString(&b, opts.Home)
	b.WriteString("  </dict>\n")
	plistKey(&b, "WorkingDirectory")
	plistString(&b, opts.Home)
	plistKey(&b, "RunAtLoad")
	b.WriteString("  <true/>\n")
	plistKey(&b, "KeepAlive")
	b.WriteString("  <dict>\n")
	plistKey(&b, "SuccessfulExit")
	b.WriteString("    <false/>\n")
	b.WriteString("  </dict>\n")
	plistKey(&b, "ProcessType")
	plistString(&b, "Background")
	plistKey(&b, "StandardOutPath")
	plistString(&b, filepath.Join(logDir, "toget.out.log"))
	plistKey(&b, "StandardErrorPath")
	plistString(&b, filepath.Join(logDir, "toget.err.log"))
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}

func plistKey(b *strings.Builder, key string) {
	fmt.Fprintf(b, "  <key>%s</key>\n", xmlEscape(key))
}

func plistString(b *strings.Builder, value string) {
	fmt.Fprintf(b, "  <string>%s</string>\n", xmlEscape(value))
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func launchdDomain(opts serviceOptions) string {
	if opts.Scope == "system" {
		return "system"
	}
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func launchdServiceTarget(opts serviceOptions) string {
	return launchdDomain(opts) + "/" + launchdLabel(opts.Name)
}

func launchdInstallCommands(opts serviceOptions, path string) string {
	return fmt.Sprintf("launchctl bootstrap %s %s\nlaunchctl enable %s\n",
		launchdDomain(opts), shellQuote(path), launchdServiceTarget(opts))
}

func launchdUninstallCommands(opts serviceOptions, path string) string {
	return fmt.Sprintf("launchctl bootout %s\n# fallback: launchctl unload -w %s\n",
		launchdServiceTarget(opts), shellQuote(path))
}

func launchdBootstrap(ctx context.Context, opts serviceOptions, path string) error {
	if err := runServiceCommand(ctx, "launchctl", "enable", launchdServiceTarget(opts)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enable %s: %v\n", launchdServiceTarget(opts), err)
	}
	if err := runServiceCommand(ctx, "launchctl", "bootstrap", launchdDomain(opts), path); err == nil {
		return nil
	}
	fmt.Fprintln(os.Stderr, "modern launchctl bootstrap failed; trying legacy load")
	return runServiceCommand(ctx, "launchctl", "load", "-w", path)
}

func launchdBootout(ctx context.Context, opts serviceOptions, path string) error {
	if err := runServiceCommand(ctx, "launchctl", "bootout", launchdServiceTarget(opts)); err == nil {
		return nil
	}
	return runServiceCommand(ctx, "launchctl", "unload", "-w", path)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
