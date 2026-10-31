// Command toget-official composes the optional public infrastructure: a
// relay and a signed-manifest directory. Both are built from the same
// internal packages as a self-hosted node, not as a separate implementation.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/directory"
	"github.com/sidenor/toget/internal/relay"
	"github.com/sidenor/toget/internal/version"
)

func main() {
	if err := newRootCommand().ExecuteContext(signalContext()); err != nil {
		fmt.Fprintln(os.Stderr, "toget-official:", err)
		os.Exit(1)
	}
}

func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func newRootCommand() *cobra.Command {
	var (
		home            string
		relayListen     string
		relayAdvertise  string
		relayParent     string
		directoryListen string
		directoryAdv    string
		relayURL        string
		noRelay         bool
		noDirectory     bool
	)
	cmd := &cobra.Command{
		Use:   "toget-official",
		Short: "Run the optional Toget relay and directory",
		Long: "Run the optional public Toget services.\n\n" +
			"The relay forwards opaque, signed requests and never has room keys. " +
			"The directory maps short server codes to self-certifying signed manifests. " +
			"Both are optional; every self-hosted node works without them.",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := officialPaths(home)
			if err != nil {
				return err
			}
			if err := config.Ensure(paths); err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			var relayService *relay.Service
			if !noRelay {
				relayService, err = relay.New(relay.Options{
					Listen:    relayListen,
					Advertise: relayAdvertise,
					Paths:     paths,
					Parent:    relayParent,
					Logger:    logger,
				})
				if err != nil {
					return err
				}
				if err := relayService.Start(ctx); err != nil {
					_ = relayService.Close()
					return err
				}
				defer relayService.Close()
				fmt.Printf("Relay:     %s\n", relayService.URL())
				fmt.Printf("Relay ID:  %s\n", relayService.RelayID())
			}
			if noDirectory {
				if relayService == nil {
					return errors.New("both the relay and the directory are disabled")
				}
				fmt.Println("Directory: disabled")
			} else {
				advertisedRelay := relayURL
				if advertisedRelay == "" {
					advertisedRelay = relayService.URL()
				}
				directoryService, err := directory.New(directory.Options{
					Listen:    directoryListen,
					Advertise: directoryAdv,
					Paths:     paths,
					RelayURL:  advertisedRelay,
					Logger:    logger,
				})
				if err != nil {
					return err
				}
				if err := directoryService.Start(ctx); err != nil {
					_ = directoryService.Close()
					return err
				}
				defer directoryService.Close()
				fmt.Printf("Directory: %s\n", directoryService.URL())
			}
			fmt.Printf("Data:      %s\n", paths.Home)
			fmt.Println("Press Ctrl+C to stop.")
			<-ctx.Done()
			return nil
		},
	}
	cmd.Flags().StringVar(&home, "home", "", "data directory (default: ~/.local/share/toget-official)")
	cmd.Flags().StringVar(&relayListen, "relay-listen", "0.0.0.0:7719", "relay listen address")
	cmd.Flags().StringVar(&relayAdvertise, "relay-advertise", "", "host:port advertised by the relay")
	cmd.Flags().StringVar(&relayParent, "relay-parent", "", "pinned parent relay URL for multi-hop routing")
	cmd.Flags().StringVar(&directoryListen, "directory-listen", "0.0.0.0:7720", "directory listen address")
	cmd.Flags().StringVar(&directoryAdv, "directory-advertise", "", "host:port advertised by the directory")
	cmd.Flags().StringVar(&relayURL, "relay-url", "", "default relay advertised to nodes (defaults to this relay)")
	cmd.Flags().BoolVar(&noRelay, "no-relay", false, "do not run the relay")
	cmd.Flags().BoolVar(&noDirectory, "no-directory", false, "do not run the directory")
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Current()
			fmt.Printf("toget-official %s (%s/%s, protocol %d)\n", info.App, info.OS, info.Arch, info.Protocol)
			return nil
		},
	})
	return cmd
}

func officialPaths(home string) (config.Paths, error) {
	if home == "" {
		if env := os.Getenv("TOGET_OFFICIAL_HOME"); env != "" {
			home = env
		} else {
			base, err := config.DefaultHome()
			if err != nil {
				return config.Paths{}, err
			}
			home = filepath.Clean(base) + "-official"
		}
	}
	return config.Resolve(home)
}
