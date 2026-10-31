package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/update"
	"github.com/sidenor/toget/internal/version"
)

type updateCLIOptions struct {
	root       string
	channel    string
	channelURL string
	rootKey    string
	healthURL  string

	restart       []string
	healthCommand []string

	force          bool
	allowDowngrade bool
	noRestart      bool
	osName         string
	archName       string
}

func newUpdateCommand() *cobra.Command {
	opts := &updateCLIOptions{}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check, install, and roll back signed Toget releases",
		Long: "Check a signed release channel, install a verified release atomically, " +
			"finish a staged update after a restart, or roll back to the previous release.\n\n" +
			"Updates are disabled until update.root, update.channel_url, and update.root_key " +
			"are configured (or the matching flags are supplied).",
	}
	cmd.PersistentFlags().StringVar(&opts.root, "update-root", "", "managed installation root (overrides update.root)")
	cmd.PersistentFlags().StringVar(&opts.channel, "channel", "", "release channel, stable or beta (overrides update.channel)")
	cmd.PersistentFlags().StringVar(&opts.channelURL, "channel-url", "", "signed channel document URL (overrides update.channel_url)")
	cmd.PersistentFlags().StringVar(&opts.rootKey, "root-key", "", "release root public key or key file (overrides update.root_key)")
	cmd.PersistentFlags().StringVar(&opts.healthURL, "health-url", "", "post-restart health URL (overrides update.health_url)")
	cmd.PersistentFlags().StringArrayVar(&opts.restart, "restart", nil, "local restart command argument; repeat for each argument (overrides update.restart)")
	cmd.PersistentFlags().StringArrayVar(&opts.healthCommand, "health-command", nil, "health probe argument; repeat for each argument (overrides update.health_command)")
	cmd.PersistentFlags().BoolVar(&opts.noRestart, "no-restart", false, "stage the update without running a restart command")

	cmd.AddCommand(newUpdateCheckCommand(opts))
	cmd.AddCommand(newUpdateStatusCommand(opts))
	cmd.AddCommand(newUpdateApplyCommand(opts))
	cmd.AddCommand(newUpdateFinishCommand(opts))
	cmd.AddCommand(newUpdateRollbackCommand(opts))
	return cmd
}

func (o *updateCLIOptions) effectiveRoot(cfg config.Config) (string, error) {
	root := firstNonEmpty(o.root, cfg.Update.Root)
	if root == "" {
		return "", errors.New("update root is not configured; set update.root in config.json or pass --update-root")
	}
	return root, nil
}

func (o *updateCLIOptions) client(cfg config.Config) (*update.Client, error) {
	channelURL := firstNonEmpty(o.channelURL, cfg.Update.ChannelURL)
	if channelURL == "" {
		return nil, errors.New("update channel URL is not configured; set update.channel_url in config.json or pass --channel-url")
	}
	rootKey := firstNonEmpty(o.rootKey, cfg.Update.RootKey, version.ReleaseRootKey)
	if rootKey == "" {
		return nil, errors.New("release root key is not configured; set update.root_key in config.json or pass --root-key")
	}
	anchor, err := update.ParseTrustAnchor(rootKey)
	if err != nil {
		return nil, fmt.Errorf("release root key: %w", err)
	}
	if (o.osName == "") != (o.archName == "") {
		return nil, errors.New("--os and --arch must be provided together")
	}
	client := &update.Client{
		Verifier:   update.NewVerifier(anchor),
		ChannelURL: channelURL,
		Channel:    firstNonEmpty(o.channel, cfg.Update.Channel, "stable"),
		OS:         o.osName,
		Arch:       o.archName,
	}
	return client, nil
}

func (o *updateCLIOptions) manager(cfg config.Config, withClient bool) (*update.Manager, error) {
	root, err := o.effectiveRoot(cfg)
	if err != nil {
		return nil, err
	}
	manager := &update.Manager{Root: root}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	manager.CurrentExecutable = executable
	restart := o.restart
	if len(restart) == 0 {
		restart = cfg.Update.Restart
	}
	if !o.noRestart {
		manager.RestartCommand = append([]string(nil), restart...)
	}
	healthCommand := o.healthCommand
	if len(healthCommand) == 0 {
		healthCommand = cfg.Update.HealthCommand
	}
	manager.HealthCommand = append([]string(nil), healthCommand...)
	manager.HealthURL = firstNonEmpty(o.healthURL, cfg.Update.HealthURL)
	if withClient {
		client, err := o.client(cfg)
		if err != nil {
			return nil, err
		}
		manager.Client = client
	}
	return manager, nil
}

func (o *updateCLIOptions) checkOptions() update.CheckOptions {
	return update.CheckOptions{Force: o.force, AllowDowngrade: o.allowDowngrade}
}

func newUpdateCheckCommand(opts *updateCLIOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check the signed channel for a newer release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			current, rolloutID, err := updateCurrentAndRollout(paths, cfg, opts)
			if err != nil {
				return err
			}
			client, err := opts.client(cfg)
			if err != nil {
				return err
			}
			result, err := client.Check(cmd.Context(), current, rolloutID, opts.checkOptions())
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			printUpdateCheck(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.force, "force", false, "ignore staged rollout")
	cmd.Flags().BoolVar(&opts.allowDowngrade, "allow-downgrade", false, "allow a release older than the installed one")
	cmd.Flags().StringVar(&opts.osName, "os", "", "check for another operating system (requires --arch)")
	cmd.Flags().StringVar(&opts.archName, "arch", "", "check for another architecture (requires --os)")
	return cmd
}

func newUpdateStatusCommand(opts *updateCLIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the managed installation and pending update",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			root, err := opts.effectiveRoot(cfg)
			if err != nil {
				return err
			}
			status, err := (&update.Manager{Root: root}).Status()
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(status)
			}
			printUpdateStatus(status)
			return nil
		},
	}
}

func newUpdateApplyCommand(opts *updateCLIOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Download, verify, install, and optionally restart into a release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			manager, err := opts.manager(cfg, true)
			if err != nil {
				return err
			}
			current, managed, err := manager.Current()
			if err != nil {
				return err
			}
			if !managed {
				info := version.Current()
				current = update.Current{App: info.App, Protocol: info.Protocol, Schema: info.Schema}
			}
			rolloutID := ""
			if status, err := manager.Status(); err != nil {
				return err
			} else if status.Initialized {
				rolloutID = status.State.RolloutID
			}
			if rolloutID == "" {
				rolloutID = fallbackRolloutID(paths.Home)
			}
			result, err := manager.Client.Check(cmd.Context(), current, rolloutID, opts.checkOptions())
			if err != nil {
				return err
			}
			upToDate := result.Reason == update.ReasonUpToDate && managed && result.Manifest != nil &&
				current.App == result.Manifest.Release
			if !result.Available && !upToDate {
				if jsonFlag {
					return printJSON(result)
				}
				printUpdateCheck(result)
				return nil
			}
			if !jsonFlag && result.Warning != "" {
				fmt.Printf("Warning: %s\n", result.Warning)
			}
			applied, err := manager.Apply(cmd.Context(), result)
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(applied)
			}
			printUpdateApply(applied)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.force, "force", false, "ignore staged rollout")
	cmd.Flags().BoolVar(&opts.allowDowngrade, "allow-downgrade", false, "allow a release older than the installed one")
	return cmd
}

func newUpdateFinishCommand(opts *updateCLIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "finish",
		Short: "Confirm a staged update after restarting Toget",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			manager, err := opts.manager(cfg, false)
			if err != nil {
				return err
			}
			result, err := manager.FinishPending(cmd.Context())
			if errors.Is(err, update.ErrNoPending) {
				if jsonFlag {
					return printJSON(map[string]any{"pending": false})
				}
				fmt.Println("No update is pending.")
				return nil
			}
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			printUpdateFinish(result)
			return nil
		},
	}
}

func newUpdateRollbackCommand(opts *updateCLIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Switch back to the previous release or discard a pending update",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			manager, err := opts.manager(cfg, false)
			if err != nil {
				return err
			}
			result, err := manager.Rollback(cmd.Context())
			if errors.Is(err, update.ErrNoPrevious) {
				return errors.New("no previous release to roll back to")
			}
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			printUpdateRollback(result)
			return nil
		},
	}
}

func updateCurrentAndRollout(paths config.Paths, cfg config.Config, opts *updateCLIOptions) (update.Current, string, error) {
	info := version.Current()
	current := update.Current{App: info.App, Protocol: info.Protocol, Schema: info.Schema}
	root := firstNonEmpty(opts.root, cfg.Update.Root)
	if root != "" {
		manager := &update.Manager{Root: root}
		status, err := manager.Status()
		if err != nil {
			return update.Current{}, "", err
		}
		if status.Initialized {
			if status.State.Current != "" {
				installed, ok, err := manager.Current()
				if err != nil {
					return update.Current{}, "", err
				}
				if ok {
					current = installed
				}
			}
			return current, status.State.RolloutID, nil
		}
	}
	return current, fallbackRolloutID(paths.Home), nil
}

func fallbackRolloutID(home string) string {
	return update.LocalRolloutID(home)
}

// newUpdateMonitor builds the in-daemon monitor. A misconfiguration disables
// background checks without preventing Toget from serving local data.
func newUpdateMonitor(paths config.Paths, cfg config.Config, logger *slog.Logger) (*update.Monitor, error) {
	mode, err := cfg.Update.NormalizedMode()
	if err != nil {
		return nil, err
	}
	interval, err := cfg.Update.CheckEvery()
	if err != nil {
		return nil, err
	}
	return update.NewMonitor(update.MonitorOptions{
		Home:          paths.Home,
		StatePath:     paths.UpdateState,
		Root:          cfg.Update.Root,
		Channel:       cfg.Update.Channel,
		ChannelURL:    cfg.Update.ChannelURL,
		RootKey:       firstNonEmpty(cfg.Update.RootKey, version.ReleaseRootKey),
		Restart:       cfg.Update.Restart,
		HealthCommand: cfg.Update.HealthCommand,
		HealthURL:     cfg.Update.HealthURL,
		Mode:          mode,
		CheckInterval: interval,
		Logger:        logger,
	})
}

func printUpdateCheck(result *update.CheckResult) {
	channel := ""
	if result.Channel != nil {
		channel = result.Channel.Channel
	}
	if !result.Available {
		switch result.Reason {
		case update.ReasonUpToDate:
			fmt.Printf("Toget %s is up to date on %s.\n", result.Current.App, channel)
		default:
			release := ""
			if result.Manifest != nil {
				release = result.Manifest.Release
			}
			fmt.Printf("No update available: %s (channel release %s, current %s).\n", result.Reason, release, result.Current.App)
		}
		return
	}
	level := ""
	rollout := 0
	if result.Manifest != nil {
		level = result.Manifest.Level
	}
	if result.Channel != nil {
		rollout = result.Channel.Rollout
	}
	fmt.Printf("Toget %s is available (current %s, channel %s).\n", result.Manifest.Release, result.Current.App, channel)
	fmt.Printf("Level: %s · rollout: %d%%\n", level, rollout)
	if result.Downgrade {
		fmt.Println("This is a downgrade.")
	}
	if result.Warning != "" {
		fmt.Printf("Warning: %s\n", result.Warning)
	}
	if result.Manifest.Changelog != "" {
		fmt.Printf("\n%s\n", result.Manifest.Changelog)
	}
	fmt.Println("Install with: toget update apply")
}

func printUpdateStatus(status *update.Status) {
	fmt.Printf("Update root: %s\n", status.Root)
	if !status.Initialized {
		fmt.Println("No managed installation.")
		return
	}
	fmt.Printf("Current:  %s", describeUpdateRelease(status.State.Current))
	if status.CurrentLink != "" {
		fmt.Printf(" (%s)", status.CurrentLink)
	}
	fmt.Println()
	fmt.Printf("Previous: %s", describeUpdateRelease(status.State.Previous))
	if status.PreviousLink != "" {
		fmt.Printf(" (%s)", status.PreviousLink)
	}
	fmt.Println()
	if pending := status.State.Pending; pending != nil {
		fmt.Printf("Pending:  %s (started %s; restart, then run `toget update finish`)\n", pending.Release, pending.StartedAt)
	}
	if len(status.Versions) > 0 {
		fmt.Println("Installed versions:")
		for _, installed := range status.Versions {
			marker := " "
			if installed.Release == status.State.Current {
				marker = "*"
			}
			fmt.Printf("  %s %s  %s\n", marker, installed.Release, installed.Path)
		}
	}
	for _, issue := range status.Issues {
		fmt.Printf("Issue: %s\n", issue)
	}
}

func printUpdateApply(result *update.ApplyResult) {
	if result.UpToDate {
		fmt.Printf("Toget %s is already installed.\n", result.Release)
		return
	}
	if result.NeedsRestart {
		fmt.Printf("Installed %s (previous %s).\n", result.Release, describeUpdateRelease(result.Previous))
		fmt.Println("Restart Toget, then run `toget update finish` to confirm it.")
	} else if result.Restarted {
		fmt.Printf("Updated to %s from %s.\n", result.Release, describeUpdateRelease(result.Previous))
	} else {
		fmt.Printf("Installed %s (previous %s).\n", result.Release, describeUpdateRelease(result.Previous))
	}
	if result.Warning != "" {
		fmt.Printf("Warning: %s\n", result.Warning)
	}
}

func printUpdateFinish(result *update.ApplyResult) {
	if result.NeedsRestart {
		fmt.Printf("Update to %s is staged but has not been confirmed.\n", result.Release)
		fmt.Println("Restart Toget with the configured restart command or `--restart`, then run this command again.")
		return
	}
	fmt.Printf("Update to %s confirmed (previous %s).\n", result.Release, describeUpdateRelease(result.Previous))
}

func printUpdateRollback(result *update.RollbackResult) {
	if result.NeedsRestart {
		fmt.Printf("Rolled back to %s. Restart Toget to use it.\n", describeUpdateRelease(result.Release))
		return
	}
	fmt.Printf("Rolled back to %s (replaced %s).\n", describeUpdateRelease(result.Release), describeUpdateRelease(result.Previous))
}

func describeUpdateRelease(release string) string {
	if release == "" {
		return "none"
	}
	return release
}

func reconcileUpdateOnStartup(logger *slog.Logger, cfg config.Config) {
	if strings.TrimSpace(cfg.Update.Root) == "" {
		return
	}
	manager := &update.Manager{Root: cfg.Update.Root}
	info := version.Current()
	result, err := manager.ReconcileOnStartup(update.Current{App: info.App, Protocol: info.Protocol, Schema: info.Schema})
	if err != nil {
		logger.Warn("update reconciliation failed", "error", err)
		return
	}
	switch result.Action {
	case update.ReconcileFinalized:
		logger.Info("update confirmed", "release", result.Release, "previous", result.Previous)
	case update.ReconcileRolledBack:
		logger.Warn("discarded unconfirmed update; running previous release", "release", result.Release)
	case update.ReconcileRepaired:
		logger.Info("repaired release links", "release", result.Release, "previous", result.Previous)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
