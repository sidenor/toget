package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/config"
)

const defaultServiceName = "toget"

var serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type serviceOptions struct {
	Name       string
	Home       string
	Executable string
	UnitDir    string
	Scope      string
	Force      bool
	DryRun     bool
	NoStart    bool
}

func newServiceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install Toget as a system service",
		Long: "Install the local node as a systemd user service, a launchd agent, or a Windows service. " +
			"The service runs `toget serve` and reads this node's config.json.",
	}
	cmd.AddCommand(newServiceInstallCommand())
	cmd.AddCommand(newServiceUninstallCommand())
	cmd.AddCommand(newServiceStatusCommand())
	run := &cobra.Command{
		Use:    "run",
		Short:  "Run the Windows service entry point",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			if err := validateServiceName(name); err != nil {
				return err
			}
			return runService(name)
		},
	}
	run.Flags().String("name", defaultServiceName, "service name")
	cmd.AddCommand(run)
	return cmd
}

func newServiceInstallCommand() *cobra.Command {
	var opts serviceOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the Toget service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			normalized, err := normalizeServiceOptions(opts, true)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(normalized.Home, 0o700); err != nil {
				return fmt.Errorf("create data directory: %w", err)
			}
			return installService(cmd.Context(), normalized)
		},
	}
	addServiceFlags(cmd, &opts, true)
	return cmd
}

func newServiceUninstallCommand() *cobra.Command {
	var opts serviceOptions
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the Toget service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			normalized, err := normalizeServiceOptions(opts, false)
			if err != nil {
				return err
			}
			return uninstallService(cmd.Context(), normalized)
		},
	}
	addServiceFlags(cmd, &opts, false)
	return cmd
}

func newServiceStatusCommand() *cobra.Command {
	var opts serviceOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the Toget service status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			normalized, err := normalizeServiceOptions(opts, false)
			if err != nil {
				return err
			}
			return serviceStatus(cmd.Context(), normalized)
		},
	}
	addServiceFlags(cmd, &opts, false)
	return cmd
}

func addServiceFlags(cmd *cobra.Command, opts *serviceOptions, install bool) {
	cmd.Flags().StringVar(&opts.Name, "name", defaultServiceName, "service name")
	cmd.Flags().StringVar(&opts.Scope, "scope", defaultServiceScope(), "service scope (user or system)")
	cmd.Flags().StringVar(&opts.UnitDir, "unit-dir", "", "override the service definition directory")
	if install {
		cmd.Flags().StringVar(&opts.Executable, "executable", "", "binary to install (default: this executable)")
		cmd.Flags().BoolVar(&opts.Force, "force", false, "overwrite an existing service definition")
		cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "print the service definition without writing it")
		cmd.Flags().BoolVar(&opts.NoStart, "no-start", false, "write the definition but do not enable or start it")
	}
}

func normalizeServiceOptions(opts serviceOptions, needExecutable bool) (serviceOptions, error) {
	if err := validateServiceName(opts.Name); err != nil {
		return serviceOptions{}, err
	}
	opts.Scope = strings.ToLower(strings.TrimSpace(opts.Scope))
	if opts.Scope == "" {
		opts.Scope = defaultServiceScope()
	}
	if opts.Scope != "user" && opts.Scope != "system" {
		return serviceOptions{}, errors.New("--scope must be user or system")
	}
	home, err := resolveServiceHome()
	if err != nil {
		return serviceOptions{}, err
	}
	opts.Home = home
	if err := validateServicePath("data directory", opts.Home); err != nil {
		return serviceOptions{}, err
	}
	if needExecutable {
		executable, err := serviceExecutable(opts.Executable)
		if err != nil {
			return serviceOptions{}, err
		}
		if err := validateServicePath("executable", executable); err != nil {
			return serviceOptions{}, err
		}
		opts.Executable = executable
	}
	if opts.UnitDir != "" {
		opts.UnitDir, err = filepath.Abs(opts.UnitDir)
		if err != nil {
			return serviceOptions{}, err
		}
		if err := validateServicePath("definition directory", opts.UnitDir); err != nil {
			return serviceOptions{}, err
		}
	}
	return opts, nil
}

func validateServicePath(label, value string) error {
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s contains control characters and cannot be used in a service definition", label)
	}
	return nil
}

func resolveServiceHome() (string, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(homeFlag) != "" {
		home = homeFlag
	}
	return filepath.Abs(home)
}

func validateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return errors.New("service name must start with a lowercase letter and contain only lowercase letters, digits, and dashes")
	}
	return nil
}

func serviceExecutable(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		path, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", path)
		}
		return path, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	// Preserve a stable path (for example versions/current/toget) when the
	// process was started through a symlink. This keeps a service restart on
	// the current release instead of pinning one version directory.
	if args0, err := filepath.Abs(os.Args[0]); err == nil && args0 != executable {
		fromArgs, argsErr := os.Stat(args0)
		fromExecutable, executableErr := os.Stat(executable)
		if argsErr == nil && executableErr == nil && os.SameFile(fromArgs, fromExecutable) {
			return args0, nil
		}
	}
	return executable, nil
}
