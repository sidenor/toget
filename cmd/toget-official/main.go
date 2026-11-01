// Command toget-official composes the optional public infrastructure: an
// account host, relay, signed-manifest directory, and update service. They are
// built from the same internal packages as a self-hosted node, not as a
// separate implementation.
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
	"time"

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
		noQUIC          bool
		updateDir       string
		updateListen    string
		updateTLSCert   string
		updateTLSKey    string
		updateInsecure  bool

		noHost              bool
		hostListen          string
		hostPublicURL       string
		hostDirectAddress   string
		hostName            string
		hostTLSCert         string
		hostTLSKey          string
		hostDB              string
		hostResetDir        string
		hostSiteDir         string
		hostSMTPAddr        string
		hostSMTPUser        string
		hostSMTPPassword    string
		hostSMTPFrom        string
		hostSMTPFromName    string
		hostSMTPImplicitTLS bool
		hostRoomMembers     int
		hostRoomDevices     int
		hostTrustedProxies  []string
		hostTrustCloudflare bool
	)
	cmd := &cobra.Command{
		Use:   "toget-official",
		Short: "Run the optional Toget account host, relay, and directory",
		Long: "Run the optional public Toget services.\n\n" +
			"The account host stores only ciphertext and OPAQUE password records, so it " +
			"cannot read account keys. The relay forwards opaque, signed requests and " +
			"never has room keys. The directory maps short server codes to " +
			"self-certifying signed manifests. All of them are optional; every " +
			"self-hosted node works without them.",
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
			if noRelay && noDirectory && updateDir == "" && noHost {
				return errors.New("the relay, directory, update service, and account host are all disabled")
			}

			if !noHost {
				host, err := startAccountHost(ctx, paths, hostOptions{
					Listen: hostListen, PublicURL: hostPublicURL, DirectAddress: hostDirectAddress, Name: hostName,
					CertPath: hostTLSCert, KeyPath: hostTLSKey, DatabasePath: hostDB,
					ResetDir: hostResetDir, SiteDir: hostSiteDir,
					SMTPAddr: hostSMTPAddr, SMTPUser: hostSMTPUser,
					SMTPPassword: hostSMTPPassword, SMTPFrom: hostSMTPFrom,
					SMTPFromName: hostSMTPFromName, SMTPImplicitTLS: hostSMTPImplicitTLS,
					RoomMemberLimit: hostRoomMembers, RoomDeviceLimit: hostRoomDevices,
					TrustedProxies: hostTrustedProxies, TrustCloudflare: hostTrustCloudflare,
				}, logger)
				if err != nil {
					return err
				}
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = host.Close(shutdownCtx)
				}()
				if host.pinnedURL != "" {
					fmt.Printf("Accounts:  %s\n", host.pinnedURL)
				} else {
					fmt.Println("Accounts:  not advertised (set --host-direct-address host:port)")
				}
				if host.identityURL != "" {
					fmt.Printf("Proxy:     %s\n", host.identityURL)
				}
				if host.publicURL != "" {
					fmt.Printf("Public:    %s\n", host.publicURL)
				} else {
					fmt.Println("Public:    not configured (set --host-public-url)")
				}
			}

			if updateDir != "" {
				shutdown, address, err := startUpdateService(updateDir, updateListen, updateTLSCert, updateTLSKey, updateInsecure)
				if err != nil {
					return err
				}
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = shutdown(shutdownCtx)
				}()
				fmt.Printf("Updates:   %s\n", address)
				fmt.Printf("Releases:  %s\n", updateDir)
			}

			var relayService *relay.Service
			if !noRelay {
				quic := "on"
				if noQUIC {
					quic = "off"
				}
				relayService, err = relay.New(relay.Options{
					Listen:    relayListen,
					Advertise: relayAdvertise,
					Paths:     paths,
					Parent:    relayParent,
					Logger:    logger,
					QUIC:      quic,
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
				fmt.Println("Directory: disabled")
			} else {
				advertisedRelay := relayURL
				if advertisedRelay == "" && relayService != nil {
					advertisedRelay = relayService.URL()
				}
				if advertisedRelay == "" {
					return errors.New("the directory requires --relay-url when the relay is disabled")
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
	cmd.Flags().BoolVar(&noQUIC, "no-quic", false, "disable HTTP/3 on the relay")
	cmd.Flags().BoolVar(&noHost, "no-host", false, "do not run the official account host")
	cmd.Flags().StringVar(&hostListen, "host-listen", "0.0.0.0:7722", "official account host listen address")
	cmd.Flags().StringVar(&hostPublicURL, "host-public-url", "", "browser-facing HTTPS origin for account email links (required with --host-smtp-addr)")
	cmd.Flags().StringVar(&hostDirectAddress, "host-direct-address", "", "public host:port advertised for direct pinned clients (required behind NAT)")
	cmd.Flags().StringVar(&hostName, "host-name", "Toget Official Accounts", "account host display name")
	cmd.Flags().StringVar(&hostTLSCert, "host-tls-cert", "", "TLS certificate for the account host (default: <home>/host/tls.crt)")
	cmd.Flags().StringVar(&hostTLSKey, "host-tls-key", "", "TLS private key for the account host (default: <home>/host/tls.key)")
	cmd.Flags().StringVar(&hostDB, "host-db", "", "account host database (default: <home>/host/accounts.db)")
	cmd.Flags().StringVar(&hostResetDir, "host-reset-dir", "", "override the embedded reset page directory")
	cmd.Flags().StringVar(&hostSiteDir, "host-site-dir", "", "override the embedded public site directory")
	cmd.Flags().StringVar(&hostSMTPAddr, "host-smtp-addr", "", "SMTP relay host:port for verification and recovery email")
	cmd.Flags().StringVar(&hostSMTPUser, "host-smtp-user", "", "SMTP username")
	cmd.Flags().StringVar(&hostSMTPPassword, "host-smtp-password", "", "SMTP password (prefer TOGET_HOST_SMTP_PASSWORD)")
	cmd.Flags().StringVar(&hostSMTPFrom, "host-smtp-from", "", "sender address for account email")
	cmd.Flags().StringVar(&hostSMTPFromName, "host-smtp-from-name", "Toget Accounts", "sender display name for account email")
	cmd.Flags().BoolVar(&hostSMTPImplicitTLS, "host-smtp-implicit-tls", false, "use implicit TLS (SMTPS, usually port 465)")
	cmd.Flags().IntVar(&hostRoomMembers, "host-room-member-limit", 0, "maximum active members per official room (default 5000)")
	cmd.Flags().IntVar(&hostRoomDevices, "host-room-device-limit", 0, "maximum devices per official room (default 20000)")
	cmd.Flags().StringArrayVar(&hostTrustedProxies, "host-trusted-proxies", nil, "proxy CIDR or IP allowed to set client-IP headers (repeatable, comma-separated)")
	cmd.Flags().BoolVar(&hostTrustCloudflare, "host-trust-cloudflare", false, "trust Cloudflare's published edge ranges for client-IP headers (refresh ranges periodically)")
	cmd.Flags().StringVar(&updateDir, "update-dir", "", "release tree to serve (channels/, manifests/, daemon/, sbom/)")
	cmd.Flags().StringVar(&updateListen, "update-listen", "127.0.0.1:7721", "update service listen address")
	cmd.Flags().StringVar(&updateTLSCert, "update-tls-cert", "", "TLS certificate for the update service")
	cmd.Flags().StringVar(&updateTLSKey, "update-tls-key", "", "TLS private key for the update service")
	cmd.Flags().BoolVar(&updateInsecure, "update-insecure-http", false, "allow a non-loopback update listener without TLS")
	cmd.AddCommand(newReleaseCommand())
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
