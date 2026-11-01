// Command toget runs the local node, the CLI, and the local web UI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/backup"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/hostedlink"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/mdns"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/notify"
	"github.com/sidenor/toget/internal/policy"
	"github.com/sidenor/toget/internal/relay"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/session"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/tool"
	"github.com/sidenor/toget/internal/ui"
	"github.com/sidenor/toget/internal/vault"
	"github.com/sidenor/toget/internal/version"
)

var (
	homeFlag           string
	jsonFlag           bool
	listenFlag         string
	nodeListenFlag     string
	nodeAdvertiseFlag  string
	nodeRelayFlag      string
	nodeDirectoryFlag  string
	nodeNATFlag        string
	nodeSTUNFlag       string
	nodeNATGatewayFlag string
	noNodeFlag         bool
	noRelayFlag        bool
	noMDNSFlag         bool
	noQUICFlag         bool
	stdinReader        = bufio.NewReader(os.Stdin)
)

func main() {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "toget:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "toget",
		Short:         "A local-first place for people and Agents",
		Long:          "Toget is a local-first room for people and their Agents. This binary runs the local node, the CLI, and the web UI.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&homeFlag, "home", "", "data directory (default: per-user data directory)")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")

	root.AddCommand(newServeCommand())
	root.AddCommand(newStatusCommand())
	root.AddCommand(newSessionCommand())
	root.AddCommand(newAccountCommand())
	root.AddCommand(newDeviceCommand())
	root.AddCommand(newPairCommand())
	root.AddCommand(newCardCommand())
	root.AddCommand(newRoomCommand())
	root.AddCommand(newJoinCommand())
	root.AddCommand(newNodeCommand())
	root.AddCommand(newRelayCommand())
	root.AddCommand(newSendCommand())
	root.AddCommand(newMessagesCommand())
	root.AddCommand(newAttachmentCommand())
	root.AddCommand(newAgentCommand())
	root.AddCommand(newConnectorCommand())
	root.AddCommand(newApprovalCommand())
	root.AddCommand(newToolCommand())
	root.AddCommand(newSearchCommand())
	root.AddCommand(newBackupCommand())
	root.AddCommand(newRestoreCommand())
	root.AddCommand(newUpdateCommand())
	root.AddCommand(newServiceCommand())
	root.AddCommand(newVersionCommand())
	return root
}

func newBackupCommand() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "backup [output]",
		Short: "Write an encrypted snapshot of this node",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, _, err := resolveConfig()
			if err != nil {
				return err
			}
			dest := out
			if len(args) == 1 {
				dest = args[0]
			}
			if dest == "" {
				dir := filepath.Join(paths.Home, "backups")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}
				dest = filepath.Join(dir, "toget-"+time.Now().UTC().Format("20060102-150405")+".toget-backup")
			}
			db, err := storage.Open(paths.DB)
			if err != nil {
				return err
			}
			defer db.Close()
			manifest, err := backup.Create(cmd.Context(), db, paths, dest)
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(manifest)
			}
			fmt.Printf("Backup: %s\n", dest)
			fmt.Printf("Files: %d · schema v%d · %s\n", len(manifest.Files), manifest.SchemaVersion, manifest.CreatedAt.Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output path")
	return cmd
}

func newRestoreCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore a node snapshot (stop the node first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			if running(cfg.Listen) {
				return errors.New("the local node is running; stop `toget serve` before restoring")
			}
			manifest, err := backup.ReadManifest(args[0])
			if err != nil {
				return err
			}
			if !yes {
				fmt.Printf("Restore %d files from %s?\n", len(manifest.Files), args[0])
				fmt.Printf("Created: %s · schema v%d\n", manifest.CreatedAt.Format(time.RFC3339), manifest.SchemaVersion)
				fmt.Print("Existing data will be moved to a pre-restore directory. Type yes to continue: ")
				answer, _ := stdinReader.ReadString('\n')
				if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
					return errors.New("restore cancelled")
				}
			}
			restored, rollback, err := backup.Restore(cmd.Context(), paths, args[0])
			if err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(map[string]any{"manifest": restored, "rollback": rollback})
			}
			fmt.Printf("Restored %d files. Previous data: %s\n", len(restored.Files), rollback)
			fmt.Println("Run `toget serve` and unlock with the backed-up account password.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func running(listen string) bool {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the app, protocol, and schema versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Current()
			if jsonFlag {
				return printJSON(info)
			}
			fmt.Printf("toget %s\ncommit %s\nprotocol %d · schema %d\n%s %s/%s\n",
				info.App, info.Commit, info.Protocol, info.Schema, info.GoVersion, info.OS, info.Arch)
			return nil
		},
	}
}

// newRelayCommand runs a relay without opening an account, vault, database,
// or local web UI. It is the standalone relay-only mode for the ordinary
// binary; toget-official remains the command that adds a directory.
func newRelayCommand() *cobra.Command {
	var (
		dataDir   string
		listen    string
		advertise string
		parent    string
		noWS      bool
		noQUIC    bool
	)
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Run only the Toget relay",
		Long: "Run a standalone Toget relay. The relay forwards opaque, signed " +
			"requests and never receives room keys or message content. It does " +
			"not create or unlock an account.\n\n" +
			"Use --listen/--advertise to publish a reachable address, and " +
			"--parent to place this relay below another relay.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home := dataDir
			if home == "" {
				if homeFlag != "" {
					home = homeFlag
				} else {
					base, err := config.DefaultHome()
					if err != nil {
						return err
					}
					home = filepath.Clean(base) + "-relay"
				}
			}
			paths, err := config.Resolve(home)
			if err != nil {
				return err
			}
			if err := config.Ensure(paths); err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			websocket := "on"
			if noWS {
				websocket = "off"
			}
			quic := "on"
			if noQUIC {
				quic = "off"
			}
			service, err := relay.New(relay.Options{
				Listen: listen, Advertise: advertise, Paths: paths,
				Parent: parent, Logger: logger, WebSocket: websocket, QUIC: quic,
			})
			if err != nil {
				return err
			}
			signalCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := service.Start(signalCtx); err != nil {
				_ = service.Close()
				return err
			}
			defer service.Close()
			if jsonFlag {
				if err := printJSON(map[string]any{
					"url": service.URL(), "relay_id": service.RelayID(),
					"data": paths.Home, "websocket": websocket != "off",
					"quic": quic != "off",
				}); err != nil {
					return err
				}
			} else {
				fmt.Printf("Relay:     %s\n", service.URL())
				fmt.Printf("Relay ID:  %s\n", service.RelayID())
				fmt.Printf("Data:      %s\n", paths.Home)
				fmt.Println("Press Ctrl+C to stop.")
			}
			<-signalCtx.Done()
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "relay data directory (default: <toget home>-relay)")
	cmd.Flags().StringVar(&listen, "listen", "0.0.0.0:7719", "relay listen address")
	cmd.Flags().StringVar(&advertise, "advertise", "", "host:port advertised by the relay")
	cmd.Flags().StringVar(&parent, "parent", "", "pinned parent relay URL for multi-hop routing")
	cmd.Flags().BoolVar(&noWS, "no-websocket", false, "disable the WebSocket tunnel and serve HTTPS long-poll only")
	cmd.Flags().BoolVar(&noQUIC, "no-quic", false, "disable HTTP/3 and serve HTTPS only")
	return cmd
}

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local node and web UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&listenFlag, "listen", "", "loopback listen address (default: 127.0.0.1:7718)")
	cmd.Flags().StringVar(&nodeListenFlag, "node-listen", "", "LAN node listen address (default: 0.0.0.0:7717, \"off\" disables it)")
	cmd.Flags().StringVar(&nodeAdvertiseFlag, "node-advertise", "", "host:port that peers should use for this node")
	cmd.Flags().StringVar(&nodeRelayFlag, "node-relay", "", "pinned relay URL or \"auto\" for directory discovery")
	cmd.Flags().StringVar(&nodeDirectoryFlag, "node-directory", "", "pinned public directory URL")
	cmd.Flags().StringVar(&nodeNATFlag, "node-nat", "", "ask the gateway to forward ports: \"auto\" or \"off\"")
	cmd.Flags().StringVar(&nodeSTUNFlag, "node-stun", "", "STUN host:port for public address discovery")
	cmd.Flags().StringVar(&nodeNATGatewayFlag, "node-nat-gateway", "", "NAT-PMP/PCP gateway IP or host:port")
	cmd.Flags().BoolVar(&noNodeFlag, "no-node", false, "disable the LAN node transport")
	cmd.Flags().BoolVar(&noRelayFlag, "no-relay", false, "disable relay fallback")
	cmd.Flags().BoolVar(&noMDNSFlag, "no-mdns", false, "disable same-LAN multicast discovery")
	cmd.Flags().BoolVar(&noQUICFlag, "no-quic", false, "disable HTTP/3 on the node transport")
	return cmd
}

func runServe(ctx context.Context) error {
	paths, cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	if listenFlag != "" {
		cfg.Listen = listenFlag
	}
	if nodeListenFlag != "" {
		cfg.NodeListen = nodeListenFlag
	}
	if nodeAdvertiseFlag != "" {
		cfg.NodeAdvertise = nodeAdvertiseFlag
	}
	if nodeRelayFlag != "" {
		cfg.NodeRelay = nodeRelayFlag
	}
	if nodeDirectoryFlag != "" {
		cfg.NodeDirectory = nodeDirectoryFlag
	}
	if nodeNATFlag != "" {
		cfg.NodeNAT = nodeNATFlag
	}
	if nodeSTUNFlag != "" {
		cfg.NodeSTUN = nodeSTUNFlag
	}
	if nodeNATGatewayFlag != "" {
		cfg.NodeNATGateway = nodeNATGatewayFlag
	}
	if cfg.NodeNAT == "" {
		cfg.NodeNAT = "off"
	}
	if cfg.NodeNAT != "auto" && cfg.NodeNAT != "off" {
		return fmt.Errorf("node_nat must be \"auto\" or \"off\"")
	}
	if noRelayFlag {
		cfg.NodeRelay = "off"
	}
	if cfg.NodeMDNS == "" {
		cfg.NodeMDNS = "on"
	}
	if noMDNSFlag || cfg.NodeMDNS == "off" {
		cfg.NodeMDNS = "off"
	}
	if cfg.NodeMDNS != "on" && cfg.NodeMDNS != "off" {
		return fmt.Errorf("node_mdns must be \"on\" or \"off\"")
	}
	if cfg.NodeQUIC == "" {
		cfg.NodeQUIC = "on"
	}
	if noQUICFlag || cfg.NodeQUIC == "off" {
		cfg.NodeQUIC = "off"
	}
	if cfg.NodeQUIC != "on" && cfg.NodeQUIC != "off" {
		return fmt.Errorf("node_quic must be \"on\" or \"off\"")
	}
	if noNodeFlag {
		cfg.NodeListen = config.NodeListenOff
	}
	if err := validateLoopback(cfg.Listen); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if staged, err := backup.ReadPending(paths); err == nil {
		fmt.Printf("Restoring staged backup from %s...\n", staged.StagedAt.Local().Format(time.RFC3339))
	}
	if manifest, rollback, err := backup.ApplyPending(ctx, paths); err != nil {
		// ApplyPending quarantines a failed archive, so the node still starts
		// with its existing data and the operator can inspect the failure.
		logger.Error("staged restore failed", "error", err)
		fmt.Fprintf(os.Stderr, "Restore failed; continuing with existing data: %v\n", err)
	} else if manifest != nil {
		fmt.Printf("Restored %d files (schema v%d). Previous data: %s\n", len(manifest.Files), manifest.SchemaVersion, rollback)
	}

	db, err := storage.Open(paths.DB)
	if err != nil {
		return err
	}
	defer db.Close()

	v, err := vault.Load(paths.VaultKey)
	if err != nil {
		return err
	}
	token, err := config.LoadOrCreateToken(paths.APIToken)
	if err != nil {
		return err
	}

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	reconcileUpdateOnStartup(logger, cfg)
	updateMonitor, err := newUpdateMonitor(paths, cfg, logger)
	if err != nil {
		logger.Warn("update monitor disabled", "error", err)
		updateMonitor = nil
	} else {
		updateMonitor.Start(signalCtx)
	}

	accounts := account.New(db, v)
	var rooms *room.Service
	rooms = room.New(db, v, room.WithDataDir(paths.Home), room.WithWriteGuard(
		func(ctx context.Context, roomID, _ string) error {
			record, err := rooms.GetRoom(ctx, roomID)
			if err != nil {
				return err
			}
			snapshot, err := accounts.Get(ctx)
			if err != nil {
				return nil
			}
			if record.HomeServerID != "" && record.HomeServerID != snapshot.Account.ServerID {
				return room.ErrRemoteReadOnly
			}
			return nil
		},
	))
	notificationService, err := notify.New(signalCtx, db, logger, notify.WithSelfAccount(func(ctx context.Context) string {
		snapshot, err := accounts.Get(ctx)
		if err != nil {
			return ""
		}
		return snapshot.Account.ID
	}))
	if err != nil {
		logger.Warn("web push notifications disabled", "error", err)
		notificationService = nil
	} else {
		rooms.SetEventHook(notificationService.HandleRoomEvent)
		notificationService.Start(signalCtx)
	}
	var nodeService *node.Service
	agents := agent.New(db, rooms)
	policies := policy.New(db, rooms, agents)
	approvals := approval.New(db, rooms, policies)
	tools := tool.New(db, rooms, approvals, policies)
	sessions := session.New(session.DefaultTTL)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-ticker.C:
				if unlocked, _ := sessions.State(); !unlocked && accounts.VaultReady() {
					accounts.LockVault()
				}
			}
		}
	}()

	hostSessions := hosted.NewSessionCache()
	nodeService, err = node.New(node.Options{
		Accounts:     accounts,
		Rooms:        rooms,
		Paths:        paths,
		Listen:       cfg.NodeListen,
		Advertise:    cfg.NodeAdvertise,
		Relay:        cfg.NodeRelay,
		Directory:    cfg.NodeDirectory,
		MDNS:         cfg.NodeMDNS,
		QUIC:         cfg.NodeQUIC,
		NAT:          cfg.NodeNAT,
		STUN:         cfg.NodeSTUN,
		NATGateway:   cfg.NodeNATGateway,
		HostSessions: hostSessions,
		SaveRelay: func(setting string) error {
			return saveNodeRelay(paths, setting)
		},
		Logger: logger,
	})
	if err != nil {
		logger.Warn("LAN node disabled", "error", err)
		nodeService = nil
	}
	nodeReady := false
	if nodeService != nil {
		if err := nodeService.Start(signalCtx); err != nil {
			logger.Warn("LAN node did not start; local mode continues", "error", err)
		} else {
			nodeReady = true
			defer nodeService.Close()
		}
	}
	var apiNode *node.Service
	if nodeReady {
		apiNode = nodeService
	}

	localActor := func(ctx context.Context) (room.Actor, error) {
		if nodeService != nil {
			return nodeService.LocalActor(ctx)
		}
		return localRoomActor(ctx, accounts)
	}
	agentRunner := agent.NewRunner(agent.RunnerOptions{
		Agents: agents,
		Rooms:  rooms,
		Actor:  localActor,
		Post: func(ctx context.Context, roomID string, actor room.Actor, body room.MessagePostedPayload) (*room.Event, error) {
			if nodeService != nil {
				return nodeService.PostRemoteMessage(ctx, roomID, actor, body)
			}
			return rooms.PostMessage(ctx, roomID, actor, body)
		},
		SetStatus: func(ctx context.Context, agentID, status, detail, roomID string) error {
			actor, err := localActor(ctx)
			if err != nil {
				return err
			}
			_, _, err = agents.SetStatus(ctx, agentID, status, detail, roomID, actor)
			return err
		},
		PendingApprovals: func(ctx context.Context, roomID, agentID string) (bool, error) {
			return pendingAgentApproval(ctx, approvals, roomID, agentID)
		},
		Home:         paths.Home,
		Workdir:      func(agentID string) string { return filepath.Join(paths.Home, "agents", agentID) },
		Runtimes:     buildAgentRuntimes(cfg, logger),
		RunTimeout:   time.Duration(cfg.AgentTimeoutSeconds) * time.Second,
		HistoryLimit: cfg.AgentHistoryLimit,
		Logger:       logger,
	})
	if err := agentRunner.Start(signalCtx); err != nil {
		logger.Warn("Agent runtime loop did not start", "error", err)
	} else {
		defer agentRunner.Close()
	}

	backupService := backup.NewService(db, paths)
	handler := api.New(api.Options{
		Accounts:      accounts,
		Hosted:        hostedlink.New(accounts, hostSessions),
		Rooms:         rooms,
		Agents:        agents,
		Policies:      policies,
		Approvals:     approvals,
		Tools:         tools,
		Node:          apiNode,
		Updates:       updateMonitor,
		Backups:       backupService,
		Notifications: notificationService,
		Session:       sessions,
		AgentRuns:     agentRunner,
		AgentStreams:  agentRunner,
		Version:       version.Current(),
		Token:         token,
		Listen:        cfg.Listen,
		UI:            ui.FS(),
		Logger:        logger,
	}).Handler()

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	defer listener.Close()

	info := version.Current()
	fmt.Printf("Toget %s\n", info.App)
	fmt.Printf("Local node: http://%s\n", listener.Addr().String())
	if nodeReady {
		fmt.Printf("LAN node:   %s\n", nodeService.URL())
	} else if cfg.NodeListen == config.NodeListenOff {
		fmt.Println("LAN node:   disabled")
	} else {
		fmt.Printf("LAN node:   unavailable (%s)\n", cfg.NodeListen)
	}
	fmt.Printf("Data: %s\n", paths.Home)
	if snapshot, err := accounts.Get(ctx); err == nil {
		fmt.Printf("Account: %s · %s\n", snapshot.Account.Name, formatAccountID(snapshot.Account.ID))
		fmt.Println("Locked: run `toget unlock` before using the CLI or UI.")
	} else if errors.Is(err, account.ErrNotFound) {
		fmt.Println("Account: not created yet")
	}
	fmt.Println("Press Ctrl+C to stop.")

	go func() {
		<-signalCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err = server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func localRoomActor(ctx context.Context, accounts *account.Service) (room.Actor, error) {
	snapshot, err := accounts.Get(ctx)
	if err != nil {
		return room.Actor{}, err
	}
	if snapshot.Device.RevokedAt != nil {
		return room.Actor{}, errors.New("this device has been revoked")
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(snapshot.Device.Certificate), snapshot.Account.PublicKey)
	if err != nil {
		return room.Actor{}, fmt.Errorf("device certificate: %w", err)
	}
	if cert.DeviceID != snapshot.Device.ID || cert.AccountID != snapshot.Account.ID {
		return room.Actor{}, errors.New("device certificate does not match the local device")
	}
	key, err := accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return room.Actor{}, err
	}
	return room.Actor{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		DisplayName: snapshot.Account.Name, SigningKey: key,
		AccountPublicKey:    snapshot.Account.PublicKey,
		EncryptionPublicKey: snapshot.Device.EncryptionPublicKey,
	}, nil
}

// buildAgentRuntimes maps configured runtime names to local adapters. Echo is
// always available for demos; claude-code uses Claude Code's print mode and
// session resume. Other names run a command with the request on stdin.
func buildAgentRuntimes(cfg config.Config, logger *slog.Logger) map[string]agent.Runtime {
	runtimes := map[string]agent.Runtime{"echo": agent.EchoRuntime{}}
	env := runtimeSubprocessEnv()
	names := make([]string, 0, len(cfg.AgentRuntimes))
	for name := range cfg.AgentRuntimes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rc := cfg.AgentRuntimes[name]
		command := strings.TrimSpace(rc.Command)
		if command == "" {
			logger.Warn("Agent runtime has no command", "runtime", name)
			continue
		}
		if name == "claude-code" {
			runtimes[name] = agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{
				Binary: command, Args: rc.Args, Workdir: rc.Workdir,
				Model: rc.Model, Env: env,
			})
			continue
		}
		runtimes[name] = agent.NewCommandRuntime(name, agent.CommandOptions{
			Command: command, Args: rc.Args, Workdir: rc.Workdir, Env: env,
		})
	}
	return runtimes
}

func runtimeSubprocessEnv() []string {
	executable, err := os.Executable()
	if err != nil || executable == "" {
		return nil
	}
	env := []string{"TOGET_BIN=" + executable}
	dir := filepath.Dir(executable)
	path := os.Getenv("PATH")
	if path == "" {
		env = append(env, "PATH="+dir)
	} else {
		env = append(env, "PATH="+dir+string(os.PathListSeparator)+path)
	}
	return env
}

func pendingAgentApproval(ctx context.Context, approvals *approval.Service, roomID, agentID string) (bool, error) {
	list, err := approvals.List(ctx, roomID, approval.StatusPending)
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, item := range list {
		if item.AgentID == agentID && item.ExpiresAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the local node, account, and room count",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var status api.StatusResponse
			if err := c.Get(cmd.Context(), "/api/v1/status", &status); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(status)
			}
			fmt.Printf("Toget %s (%s/%s)\n", status.Version.App, status.Version.OS, status.Version.Arch)
			fmt.Printf("Node:    http://%s\n", status.Listen)
			if status.Account == nil {
				fmt.Println("Account: not created yet")
				fmt.Println("Create one with: toget account create")
				return nil
			}
			fmt.Printf("Account: %s · %s\n", status.Account.Name, formatAccountID(status.Account.ID))
			fmt.Printf("Server:  %s\n", status.Account.ServerID)
			if status.Locked {
				fmt.Println("State:   locked (run `toget unlock`)")
				return nil
			}
			fmt.Printf("Rooms:   %d\nAgents:  %d\nPending: %d approval(s)\n", status.Rooms, status.Agents, status.PendingApproval)
			return nil
		},
	}
}

func newSessionCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Inspect or change the local unlock state"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show whether the node is unlocked",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var state api.SessionView
			if err := c.Get(cmd.Context(), "/api/v1/session", &state); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(state)
			}
			if !state.HasAccount {
				fmt.Println("No account yet.")
			} else if state.Locked {
				fmt.Println("Locked. Run `toget unlock`.")
			} else {
				fmt.Printf("Unlocked until %s.\n", state.ExpiresAt.Local().Format(time.RFC3339))
			}
			return nil
		},
	})
	cmd.AddCommand(newUnlockCommand())
	cmd.AddCommand(&cobra.Command{
		Use:   "lock",
		Short: "Lock the node now",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/api/v1/session", nil); err != nil {
				return err
			}
			fmt.Println("Locked.")
			return nil
		},
	})
	return cmd
}

func newUnlockCommand() *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Unlock the local node with your password",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if password == "" {
				password, err = promptPassword("Password: ")
				if err != nil {
					return err
				}
			}
			var state api.SessionView
			if err := c.Post(cmd.Context(), "/api/v1/session", map[string]string{"password": password}, &state); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(state)
			}
			fmt.Printf("Unlocked until %s.\n", state.ExpiresAt.Local().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "password (prompted when omitted)")
	return cmd
}

func newAccountCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Manage the local account"}
	cmd.AddCommand(newAccountCreateCommand())
	cmd.AddCommand(newAccountLoginCommand())
	cmd.AddCommand(newAccountShowCommand())
	cmd.AddCommand(newAccountSyncCommand())
	cmd.AddCommand(newAccountRotateCommand())
	cmd.AddCommand(newAccountChangePasswordCommand())
	cmd.AddCommand(newAccountRecoveryCommand())
	cmd.AddCommand(newAccountResetPasswordCommand())
	return cmd
}

func newAccountChangePasswordCommand() *cobra.Command {
	var currentPassword, newPassword string
	cmd := &cobra.Command{
		Use:   "change-password",
		Short: "Change the account password on this device",
		Long: "Change the local password verifier and re-wrap this device's vault key.\n" +
			"Other devices keep their own local password until changed there.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if currentPassword == "" {
				currentPassword, err = promptPassword("Current password: ")
				if err != nil {
					return err
				}
			}
			if newPassword == "" {
				newPassword, err = promptPassword("New password (at least 8 characters): ")
				if err != nil {
					return err
				}
				confirm, err := promptPassword("Confirm new password: ")
				if err != nil {
					return err
				}
				if newPassword != confirm {
					return errors.New("passwords do not match")
				}
			}
			var accountView api.AccountView
			official := c.Get(cmd.Context(), "/api/v1/account", &accountView) == nil && accountView.HostURL != ""
			var state api.SessionView
			if err := c.Post(cmd.Context(), "/api/v1/account/change-password", map[string]string{
				"current_password": currentPassword, "new_password": newPassword,
			}, &state); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(state)
			}
			if official {
				fmt.Println("Password changed on the official host and this device. Other devices keep their existing local unlock password until changed there.")
			} else {
				fmt.Println("Password changed. This device is unlocked; other devices are unchanged.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&currentPassword, "current-password", "", "current password (prompted when omitted)")
	cmd.Flags().StringVar(&newPassword, "new-password", "", "new password (prompted when omitted)")
	return cmd
}

func newAccountRecoveryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recovery",
		Short: "Manage the recovery file for this device",
	}
	cmd.AddCommand(newAccountRecoveryStatusCommand())
	cmd.AddCommand(newAccountRecoveryCreateCommand())
	cmd.AddCommand(newAccountRecoveryRotateOfficialCommand())
	cmd.AddCommand(newAccountRecoveryRemoveCommand())
	return cmd
}

func newAccountRecoveryRotateOfficialCommand() *cobra.Command {
	var out, password string
	cmd := &cobra.Command{
		Use:   "rotate-official",
		Short: "Rotate the official account recovery file",
		Long: "Rotate the recovery file stored by the official account host. This is separate\n" +
			"from this device's local recovery file. The account password is required.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if password == "" {
				password, err = promptPassword("Account password: ")
				if err != nil {
					return err
				}
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			// Unlock with the same password when needed; a hosted account uses
			// one password on the host and in this device's vault.
			if err := c.Post(cmd.Context(), "/api/v1/session", map[string]string{"password": password}, nil); err != nil {
				return err
			}
			var created api.RecoveryCredentialView
			if err := c.Post(cmd.Context(), "/api/v1/account/recovery", map[string]any{
				"official": true, "password": password,
			}, &created); err != nil {
				return err
			}
			path, err := saveRecoveryCredential(created, out)
			if err != nil {
				return fmt.Errorf("recovery was rotated but the file could not be saved: %w; run the command again", err)
			}
			if jsonFlag {
				return printJSON(map[string]any{"path": path, "filename": created.Filename, "created_at": created.CreatedAt})
			}
			fmt.Printf("Official recovery file written to:\n  %s\n\n", path)
			fmt.Println("Store it offline. The previous official recovery file no longer works.")
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output path (default: the suggested recovery filename)")
	cmd.Flags().StringVar(&password, "password", "", "account password (prompted when omitted)")
	return cmd
}

func newAccountRecoveryStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether this device has a recovery file",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var status api.RecoveryStatus
			if err := c.Get(cmd.Context(), "/api/v1/account/recovery", &status); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(status)
			}
			if !status.Enabled {
				fmt.Println("Recovery file: not configured")
				fmt.Println("Without one, a forgotten password cannot open sealed history on this device.")
				return nil
			}
			fmt.Printf("Recovery file: enabled since %s\nDevice: %s\n",
				status.CreatedAt.Local().Format(time.RFC3339), status.DeviceID)
			return nil
		},
	}
}

func newAccountRecoveryCreateCommand() *cobra.Command {
	var out, password string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new recovery file (voids the previous one)",
		Long: "Create a high-entropy recovery file for this device's vault. Store it offline.\n" +
			"Anyone with the file and local access to this node can reset the password and open history.\n" +
			"Creating a new file voids the previous one.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var explicitPath string
			if out != "" {
				var err error
				explicitPath, err = filepath.Abs(out)
				if err != nil {
					return err
				}
				if _, err := os.Stat(explicitPath); err == nil {
					return fmt.Errorf("output file already exists: %s; choose another path or remove it before creating a new recovery file", explicitPath)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if password != "" {
				if err := c.Post(cmd.Context(), "/api/v1/session", map[string]string{"password": password}, nil); err != nil {
					return err
				}
			}
			var created api.RecoveryCredentialView
			if err := c.Post(cmd.Context(), "/api/v1/account/recovery", nil, &created); err != nil {
				return err
			}
			path := created.Filename
			if explicitPath != "" {
				path = explicitPath
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			content := []byte(created.Content)
			written, err := writeRecoveryFile(path, content, explicitPath == "")
			if err != nil {
				return fmt.Errorf("recovery was created but the file could not be saved: %w; run the command again while unlocked", err)
			}
			path = written
			if jsonFlag {
				return printJSON(map[string]any{
					"path": path, "filename": created.Filename, "created_at": created.CreatedAt,
				})
			}
			fmt.Printf("Recovery file written to:\n  %s\n\n", path)
			fmt.Println("Store it offline like a private key. Do not send it through chat, email, or an unencrypted cloud drive.")
			fmt.Println("The previous recovery file, if any, no longer works.")
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output path (default: the suggested recovery filename)")
	cmd.Flags().StringVar(&password, "password", "", "unlock password, used when the node is locked")
	return cmd
}

func newAccountRecoveryRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove this device's recovery file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				answer := strings.ToLower(strings.TrimSpace(prompt("Remove the recovery file? A forgotten password will no longer be recoverable on this device. [y/N]: ")))
				if answer != "y" && answer != "yes" {
					return errors.New("cancelled")
				}
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/api/v1/account/recovery", nil); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(map[string]bool{"ok": true})
			}
			fmt.Println("Recovery file removed. The password still works.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

func newAccountResetPasswordCommand() *cobra.Command {
	var recoveryPath, newPassword string
	var local bool
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset the password with a recovery file or a local admin reset",
		Long: "With --recovery, reset the password while the node is locked. The recovery file\n" +
			"unwraps this device's vault key locally, so sealed history stays readable.\n" +
			"With --local, reset a forgotten password while this device is unlocked; no old\n" +
			"password or recovery file is needed. Other devices are unchanged either way.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if local && recoveryPath != "" {
				return errors.New("choose either --local or --recovery, not both")
			}
			if !local && recoveryPath == "" {
				return errors.New("--recovery or --local is required")
			}
			var content []byte
			var err error
			if recoveryPath != "" {
				content, err = readRecoveryFile(recoveryPath)
				if err != nil {
					return err
				}
			}
			if newPassword == "" {
				newPassword, err = promptPassword("New password (at least 8 characters): ")
				if err != nil {
					return err
				}
				confirm, err := promptPassword("Confirm new password: ")
				if err != nil {
					return err
				}
				if newPassword != confirm {
					return errors.New("passwords do not match")
				}
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			request := map[string]any{"new_password": newPassword}
			if local {
				request["local"] = true
			} else {
				request["credential"] = string(content)
			}
			var state api.SessionView
			if err := c.Post(cmd.Context(), "/api/v1/account/reset-password", request, &state); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(state)
			}
			fmt.Println("Password reset. This device is unlocked; other devices keep their old local password.")
			return nil
		},
	}
	cmd.Flags().StringVar(&recoveryPath, "recovery", "", "path to a .togetkey recovery file")
	cmd.Flags().StringVar(&newPassword, "new-password", "", "new password (prompted when omitted)")
	cmd.Flags().BoolVar(&local, "local", false, "reset while this device is unlocked, without the old password")
	return cmd
}

// writeRecoveryFile writes a credential without overwriting an existing file.
// For the default filename, an occupied path is suffixed rather than failing
// after the server has already invalidated the previous credential.
func writeRecoveryFile(path string, content []byte, allowSuffix bool) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		candidate := path
		if attempt > 0 {
			ext := filepath.Ext(path)
			base := strings.TrimSuffix(path, ext)
			candidate = fmt.Sprintf("%s-%d%s", base, attempt+1, ext)
		}
		err := writePrivateFile(candidate, content)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, os.ErrExist) || !allowSuffix {
			return "", err
		}
	}
	return "", errors.New("could not find an unused recovery filename")
}

func readRecoveryFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > account.MaxRecoveryCredentialBytes {
		return nil, fmt.Errorf("%s is too large to be a recovery file", path)
	}
	return os.ReadFile(path)
}

func writePrivateFile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func newAccountRotateCommand() *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate the account signing key and re-sign active devices",
		Long: "Rotate the account signing key. For an official account, the password is\n" +
			"also required so the new key bundle can be encrypted for the host.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var accountView api.AccountView
			official := c.Get(cmd.Context(), "/api/v1/account", &accountView) == nil && accountView.HostURL != ""
			if official && password == "" {
				password, err = promptPassword("Account password: ")
				if err != nil {
					return err
				}
			}
			var result struct {
				Generation int      `json:"generation"`
				Devices    int      `json:"devices"`
				Warnings   []string `json:"warnings"`
			}
			var request any
			if official {
				request = map[string]string{"password": password}
			}
			if err := c.Post(cmd.Context(), "/api/v1/account/rotate-key", request, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			fmt.Printf("Account key rotated to generation %d; %d active device(s) re-signed.\n",
				result.Generation, result.Devices)
			for _, warning := range result.Warnings {
				fmt.Printf("warning: %s\n", warning)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "account password, required for an official account (prompted when omitted)")
	return cmd
}

func newAccountSyncCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Exchange account records with paired devices now",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result api.AccountSyncResult
			if err := c.Post(cmd.Context(), "/api/v1/account/sync", nil, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			fmt.Printf("Account sync: %d peer(s), %d reachable.\n", result.Peers, result.Synced)
			if result.Host {
				if result.HostSynced {
					fmt.Println("Official host: synced.")
				} else {
					fmt.Println("Official host: not synced; see warnings below.")
				}
			}
			if len(result.Revoked) > 0 {
				fmt.Printf("Newly revoked: %s\n", strings.Join(result.Revoked, ", "))
			}
			for _, warning := range result.Warnings {
				fmt.Printf("warning: %s\n", warning)
			}
			return nil
		},
	}
}

func newAccountCreateCommand() *cobra.Command {
	var name, password, deviceName, hostURL, email, out string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create the account for this node",
		Long: "Create a local account, or with --host an official-hosted account.\n" +
			"An official account returns a recovery file once; save it offline before continuing.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if name == "" {
				name = prompt("Your name: ")
			}
			if name == "" {
				return errors.New("a name is required")
			}
			if password == "" {
				password, err = promptPassword("Password (at least 8 characters): ")
				if err != nil {
					return err
				}
				confirm, err := promptPassword("Confirm password: ")
				if err != nil {
					return err
				}
				if password != confirm {
					return errors.New("passwords do not match")
				}
			}
			if deviceName == "" {
				deviceName, _ = os.Hostname()
			}
			if err := validateOfficialOutput(hostURL, out); err != nil {
				return err
			}
			var created struct {
				api.AccountView
				Recovery *api.RecoveryCredentialView `json:"recovery,omitempty"`
			}
			request := map[string]string{
				"name": name, "password": password, "device_name": deviceName,
			}
			if hostURL != "" {
				request["host_url"] = hostURL
				request["email"] = email
			}
			if err := c.Post(cmd.Context(), "/api/v1/account", request, &created); err != nil {
				return err
			}
			var recoveryPath string
			if created.Recovery != nil {
				recoveryPath, err = saveRecoveryCredential(*created.Recovery, out)
				if err != nil {
					return fmt.Errorf("account created, but the official recovery file could not be saved: %w; create a new one with `toget account recovery rotate-official`", err)
				}
			} else if hostURL != "" {
				return errors.New("the official host did not return a recovery file; create a new one with `toget account recovery rotate-official`")
			}
			if jsonFlag {
				return printJSON(map[string]any{
					"account": created.AccountView, "recovery_path": recoveryPath,
				})
			}
			fmt.Printf("Account created.\n\nID:     %s\nName:   %s\nServer: %s\n", formatAccountID(created.ID), created.Name, created.ServerID)
			if created.HostURL != "" {
				fmt.Printf("Host:   %s\n", created.HostURL)
			}
			fmt.Println()
			if recoveryPath != "" {
				fmt.Printf("Recovery file written to:\n  %s\n\n", recoveryPath)
				fmt.Println("Store it offline like a private key. Without it, a forgotten official password cannot recover the account.")
				return nil
			}
			fmt.Println("Keep your password safe. This account lives on this machine.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&password, "password", "", "password (prompted when omitted)")
	cmd.Flags().StringVar(&deviceName, "device-name", "", "name of this device")
	cmd.Flags().StringVar(&hostURL, "host", "", "official account host URL (creates a hosted account)")
	cmd.Flags().StringVar(&email, "email", "", "email for official account recovery (optional)")
	cmd.Flags().StringVar(&out, "out", "", "recovery file path for an official account")
	return cmd
}

func validateOfficialOutput(hostURL, out string) error {
	if out != "" && hostURL == "" {
		return errors.New("--out is only used when creating an official account with --host")
	}
	return nil
}

func saveRecoveryCredential(credential api.RecoveryCredentialView, out string) (string, error) {
	path := credential.Filename
	if out != "" {
		path = out
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	written, err := writeRecoveryFile(absolute, []byte(credential.Content), out == "")
	if err != nil {
		return "", err
	}
	return written, nil
}

func newAccountLoginCommand() *cobra.Command {
	var hostURL, accountID, password, deviceName string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to an official account on this device",
		Long: "Authenticate to an official account host and add this device to the account.\n" +
			"The account password is used once to create the local vault; it is not stored.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostURL == "" {
				hostURL = prompt("Official host URL: ")
			}
			if accountID == "" {
				accountID = prompt("Account ID (14 digits): ")
			}
			if accountID == "" {
				return errors.New("an account ID is required")
			}
			var err error
			if password == "" {
				password, err = promptPassword("Account password: ")
				if err != nil {
					return err
				}
			}
			if deviceName == "" {
				deviceName, _ = os.Hostname()
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var loggedIn struct {
				api.AccountView
				Warnings []string `json:"warnings,omitempty"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/account/hosted/login", map[string]string{
				"host_url": hostURL, "account_id": accountID,
				"password": password, "device_name": deviceName,
			}, &loggedIn); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(loggedIn)
			}
			fmt.Printf("Signed in.\n\nID:     %s\nName:   %s\nHost:   %s\nDevice: %s\n\n",
				formatAccountID(loggedIn.ID), loggedIn.Name, loggedIn.HostURL, deviceName)
			for _, warning := range loggedIn.Warnings {
				fmt.Printf("warning: %s\n", warning)
			}
			fmt.Println("This device is now part of the account. Use account sync to reach your other devices.")
			return nil
		},
	}
	cmd.Flags().StringVar(&hostURL, "host", "", "official account host URL")
	cmd.Flags().StringVar(&accountID, "account", "", "14-digit account ID")
	cmd.Flags().StringVar(&password, "password", "", "account password (prompted when omitted)")
	cmd.Flags().StringVar(&deviceName, "device-name", "", "name of this device")
	return cmd
}

func newAccountShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the local account",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var a api.AccountView
			if err := c.Get(cmd.Context(), "/api/v1/account", &a); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(a)
			}
			fmt.Printf("ID:     %s\nName:   %s\nServer: %s\nKey:    generation %d\n",
				formatAccountID(a.ID), a.Name, a.ServerID, a.KeyGeneration)
			if a.HostURL != "" {
				fmt.Printf("Host:   %s\n", a.HostURL)
			} else {
				fmt.Println("Host:   local (no official host)")
			}
			return nil
		},
	}
}

func newDeviceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "device", Short: "Manage devices on this account"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Devices []api.DeviceView `json:"devices"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/devices", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Devices)
			}
			for _, d := range result.Devices {
				marker := " "
				if d.Current {
					marker = "*"
				}
				state := "active"
				if d.RevokedAt != nil {
					state = "revoked"
				}
				fmt.Printf("%s %s  %-24s %-8s %s\n", marker, d.ID, d.Name, d.Role, state)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke another device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/api/v1/devices/"+args[0], nil); err != nil {
				return err
			}
			fmt.Println("Device revoked.")
			return nil
		},
	})
	return cmd
}

func newRoomCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "room", Short: "Manage rooms"}
	cmd.AddCommand(newRoomCreateCommand())
	cmd.AddCommand(newRoomListCommand())
	cmd.AddCommand(newRoomShowCommand())
	cmd.AddCommand(newRoomRenameCommand())
	cmd.AddCommand(newRoomArchiveCommand())
	cmd.AddCommand(newRoomMembersCommand())
	cmd.AddCommand(newRoomInviteCommand())
	cmd.AddCommand(newRoomRedeemCommand())
	cmd.AddCommand(newRoomLeaveCommand())
	cmd.AddCommand(newRoomAgentsCommand())
	cmd.AddCommand(newRoomAddAgentCommand())
	cmd.AddCommand(newRoomRemoveAgentCommand())
	cmd.AddCommand(newRoomAuditCommand())
	cmd.AddCommand(newRoomSearchCommand())
	cmd.AddCommand(newRoomReadCommand())
	return cmd
}

func newRoomCreateCommand() *cobra.Command {
	var name, topic string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a room",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if name == "" {
				name = prompt("Room name: ")
			}
			if name == "" {
				return errors.New("a room name is required")
			}
			var created struct {
				Room  room.Room  `json:"room"`
				Event room.Event `json:"event"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms", map[string]string{"name": name, "topic": topic}, &created); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(created)
			}
			fmt.Printf("Room created.\n\nID:   %s\nName: %s\n", created.Room.ID, created.Room.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "room name")
	cmd.Flags().StringVar(&topic, "topic", "", "room topic")
	return cmd
}

func newRoomListCommand() *cobra.Command {
	var includeArchived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List rooms",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			path := "/api/v1/rooms"
			if includeArchived {
				path += "?include_archived=1"
			}
			var result struct {
				Rooms []room.Room `json:"rooms"`
			}
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Rooms)
			}
			if len(result.Rooms) == 0 {
				fmt.Println("No rooms yet. Create one with: toget room create")
				return nil
			}
			for _, r := range result.Rooms {
				suffix := ""
				if r.ArchivedAt != nil {
					suffix = " (archived)"
				}
				fmt.Printf("%s  %s%s\n", r.ID, r.Name, suffix)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived rooms")
	return cmd
}

func newRoomShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <room-id>",
		Short: "Show a room",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var r room.Room
			if err := c.Get(cmd.Context(), "/api/v1/rooms/"+args[0], &r); err != nil {
				return err
			}
			return printJSON(r)
		},
	}
}

func newRoomRenameCommand() *cobra.Command {
	var name, topic string
	cmd := &cobra.Command{
		Use:   "rename <room-id>",
		Short: "Change a room's name or topic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			body := map[string]any{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("topic") {
				body["topic"] = topic
			}
			if len(body) == 0 {
				return errors.New("pass --name and/or --topic")
			}
			var updated struct {
				Room room.Room `json:"room"`
			}
			if err := c.Patch(cmd.Context(), "/api/v1/rooms/"+args[0], body, &updated); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(updated.Room)
			}
			fmt.Printf("Updated %s: %s\n", updated.Room.ID, updated.Room.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&topic, "topic", "", "new topic")
	return cmd
}

func newRoomArchiveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "archive <room-id>",
		Short: "Archive a room",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Room room.Room `json:"room"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+args[0]+"/archive", nil, &result); err != nil {
				return err
			}
			fmt.Printf("Archived %s\n", result.Room.ID)
			return nil
		},
	}
}

func newRoomMembersCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "members <room-id>",
		Short: "List room members",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Members []room.Member `json:"members"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/rooms/"+args[0]+"/members", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Members)
			}
			for _, m := range result.Members {
				fmt.Printf("%s  %-20s %-8s %s\n", m.AccountID, m.DisplayName, m.Role, m.Status)
			}
			return nil
		},
	}
}

func newRoomInviteCommand() *cobra.Command {
	var history string
	var ttlSeconds int
	var qrFlag bool
	var pngPath string
	cmd := &cobra.Command{
		Use:   "invite <room-id>",
		Short: "Create a single-use invitation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Invite room.Invite `json:"invite"`
			}
			body := map[string]any{"history": history, "ttl_seconds": ttlSeconds}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+args[0]+"/invites", body, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Invite)
			}
			if result.Invite.Code == "" {
				fmt.Printf("Share this secret once: %s\nHistory: %s\nExpires: %s\n",
					result.Invite.Secret, result.Invite.History, result.Invite.ExpiresAt.Local().Format(time.RFC3339))
				if qrFlag || pngPath != "" {
					return errors.New("this invite has no portable code; enable the node transport to share it as a QR code")
				}
				return nil
			}
			fmt.Printf("Share this invite code once: %s\nHistory: %s\nExpires: %s\n",
				result.Invite.Code, result.Invite.History, result.Invite.ExpiresAt.Local().Format(time.RFC3339))
			if qrFlag || pngPath != "" {
				if err := writeSecretQR(result.Invite.Code, pngPath, qrFlag); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&history, "history", "none", "history attached: none, recent, or all")
	cmd.Flags().IntVar(&ttlSeconds, "ttl-seconds", 7*24*3600, "invitation lifetime in seconds")
	cmd.Flags().BoolVar(&qrFlag, "qr", false, "print a QR code in the terminal")
	cmd.Flags().StringVar(&pngPath, "png", "", "write a PNG QR code to this file")
	return cmd
}

func newRoomRedeemCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "redeem <secret-or-code>",
		Aliases: []string{"join"},
		Short:   "Join a room with an invitation secret or portable code",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return redeemInvite(cmd, args[0])
		},
	}
}

func newJoinCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "join <invite-code>",
		Short: "Join a room on another node with an invite code",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return redeemInvite(cmd, args[0])
		},
	}
}

func redeemInvite(cmd *cobra.Command, secret string) error {
	c, _, _, err := localClient()
	if err != nil {
		return err
	}
	var result struct {
		Room room.Room `json:"room"`
	}
	if err := c.Post(cmd.Context(), "/api/v1/invites/redeem", map[string]string{"secret": secret}, &result); err != nil {
		return err
	}
	if jsonFlag {
		return printJSON(result.Room)
	}
	fmt.Printf("Joined %s (%s)\n", result.Room.Name, result.Room.ID)
	return nil
}

func newNodeCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "node", Short: "Inspect and sync the LAN node transport"}
	cmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show this node's address, identity, and TLS fingerprint",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Enabled bool       `json:"enabled"`
				Info    *node.Info `json:"info,omitempty"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/node", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			if !result.Enabled || result.Info == nil {
				fmt.Println("LAN node transport is disabled.")
				return nil
			}
			fmt.Printf("URL:         %s\n", result.Info.URL)
			fmt.Printf("Direct:      %s\n", result.Info.DirectURL)
			if result.Info.RelayURL != "" {
				fmt.Printf("Relay:       %s (%s)\n", result.Info.RelayURL, relayStatus(result.Info.RelayConnected))
			}
			if result.Info.DirectoryURL != "" {
				fmt.Printf("Directory:   %s\n", result.Info.DirectoryURL)
			}
			if result.Info.MDNS {
				fmt.Printf("Discovery:   on (%d nearby)\n", result.Info.MDNSPeers)
			} else {
				fmt.Printf("Discovery:   off\n")
			}
			fmt.Printf("Server:      %s\n", result.Info.ServerID)
			fmt.Printf("Account:     %s\n", result.Info.AccountID)
			fmt.Printf("Device:      %s\n", result.Info.DeviceID)
			fmt.Printf("Fingerprint: %s\n", result.Info.TLSFingerprint)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "discover",
		Short: "List Toget nodes visible on the local network",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Enabled bool        `json:"enabled"`
				Peers   []mdns.Peer `json:"peers"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/node/discover", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			if !result.Enabled {
				fmt.Println("LAN discovery is disabled.")
				return nil
			}
			if len(result.Peers) == 0 {
				fmt.Println("No Toget nodes found on this network.")
				return nil
			}
			fmt.Printf("%-8s  %-18s  %-28s  %s\n", "CODE", "SERVER", "ADDRESS", "SEEN")
			for _, peer := range result.Peers {
				address := ""
				if len(peer.Addresses) > 0 {
					address = net.JoinHostPort(peer.Addresses[0], fmt.Sprint(peer.Port))
					if len(peer.Addresses) > 1 {
						address += fmt.Sprintf(" (+%d)", len(peer.Addresses)-1)
					}
				}
				server := peer.ServerID
				if len(server) > 18 {
					server = server[:15] + "..."
				}
				seen := "just now"
				if !peer.LastSeen.IsZero() {
					age := time.Since(peer.LastSeen).Round(time.Second)
					if age > 0 {
						seen = age.String() + " ago"
					}
				}
				fmt.Printf("%-8s  %-18s  %-28s  %s\n", peer.ServerCode, server, address, seen)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "lookup <server-code>",
		Short: "Resolve a server code over the LAN or public directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var record node.DirectoryRecord
			if err := c.Get(cmd.Context(), "/api/v1/node/lookup?code="+url.QueryEscape(args[0]), &record); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(record)
			}
			fmt.Printf("Server:      %s\n", record.ServerID)
			fmt.Printf("Code:        %s\n", record.ServerCode)
			if record.Source != "" {
				fmt.Printf("Source:      %s\n", record.Source)
			}
			fmt.Printf("URL:         %s\n", record.URL)
			if record.RelayURL != "" {
				fmt.Printf("Relay:       %s\n", record.RelayURL)
			}
			fmt.Printf("Fingerprint: %s\n", record.TLSFingerprint)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "sync [room-id]",
		Short: "Pull updates from home nodes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				var result struct {
					OK bool `json:"ok"`
				}
				if err := c.Post(cmd.Context(), "/api/v1/node/sync", nil, &result); err != nil {
					return err
				}
				if jsonFlag {
					return printJSON(result)
				}
				fmt.Println("Sync requested for all remote rooms.")
				return nil
			}
			var result struct {
				Imported int `json:"imported"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+args[0]+"/sync", nil, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			fmt.Printf("Imported %d event(s) into %s\n", result.Imported, args[0])
			return nil
		},
	})
	return cmd
}

func newRoomLeaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "leave <room-id>",
		Short: "Leave a room",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+args[0]+"/leave", nil, nil); err != nil {
				return err
			}
			fmt.Println("Left the room.")
			return nil
		},
	}
}

func newRoomAgentsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "agents <room-id>",
		Short: "List Agents in a room",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Agents []agent.Agent `json:"agents"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/rooms/"+args[0]+"/agents", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Agents)
			}
			for _, a := range result.Agents {
				fmt.Printf("%s  %-20s %-16s %s\n", a.ID, a.Name, a.Status, a.Runtime)
			}
			return nil
		},
	}
}

func newRoomAddAgentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "add-agent <room-id> <agent-id>",
		Short: "Add an Agent to a room",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+args[0]+"/agents", map[string]string{"agent_id": args[1]}, nil); err != nil {
				return err
			}
			fmt.Println("Agent joined.")
			return nil
		},
	}
}

func newRoomRemoveAgentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-agent <room-id> <agent-id>",
		Short: "Remove an Agent from a room",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/api/v1/rooms/"+args[0]+"/agents/"+args[1], nil); err != nil {
				return err
			}
			fmt.Println("Agent left.")
			return nil
		},
	}
}

func newRoomAuditCommand() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "audit <room-id>",
		Short: "Export and verify a room's audit bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Audit    room.Audit `json:"audit"`
				Verified bool       `json:"verified"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/rooms/"+args[0]+"/audit", &result); err != nil {
				return err
			}
			if out != "" {
				data, err := json.MarshalIndent(result.Audit, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(out, append(data, '\n'), 0o600); err != nil {
					return err
				}
			}
			if jsonFlag {
				return printJSON(result)
			}
			fmt.Printf("Events: %d\nHead:   %s\nChain:  %s\n", len(result.Audit.Events), result.Audit.Head, verifiedLabel(result.Verified))
			if out != "" {
				fmt.Printf("Written to %s (mode 0600).\n", out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the audit bundle to a file")
	return cmd
}

func newRoomSearchCommand() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <room-id> <query>",
		Short: "Search messages in a room",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			query := strings.Join(args[1:], " ")
			path := fmt.Sprintf("/api/v1/rooms/%s/search?q=%s&limit=%d", args[0], urlQuery(query), limit)
			var result struct {
				Hits []room.SearchHit `json:"hits"`
			}
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Hits)
			}
			for _, hit := range result.Hits {
				fmt.Printf("[%d] %s: %s\n", hit.Seq, hit.Author, hit.Text)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum hits")
	return cmd
}

func newRoomReadCommand() *cobra.Command {
	var seq int64
	var reset bool
	cmd := &cobra.Command{
		Use:   "read <room-id>",
		Short: "Show or set this device's read cursor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if reset || cmd.Flags().Changed("seq") {
				if err := c.Post(cmd.Context(), "/api/v1/rooms/"+args[0]+"/read", map[string]int64{"seq": seq}, nil); err != nil {
					return err
				}
				fmt.Printf("Read cursor set to %d.\n", seq)
				return nil
			}
			var result struct {
				Seq      int64  `json:"seq"`
				DeviceID string `json:"device_id"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/rooms/"+args[0]+"/read", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result)
			}
			fmt.Printf("Read through %d on %s.\n", result.Seq, result.DeviceID)
			return nil
		},
	}
	cmd.Flags().Int64Var(&seq, "seq", 0, "set the cursor")
	cmd.Flags().BoolVar(&reset, "reset", false, "reset the cursor to zero")
	return cmd
}

func newSendCommand() *cobra.Command {
	var roomID, format, agentID string
	var files []string
	cmd := &cobra.Command{
		Use:   "send [text...]",
		Short: "Send a message, optionally with attachments",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && len(files) == 0 {
				return errors.New("message text or --file is required")
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			if agentID == "" {
				agentID = os.Getenv("TOGET_AGENT_ID")
			}
			text := strings.Join(args, " ")
			attachments, err := uploadFiles(cmd.Context(), c, roomID, files)
			if err != nil {
				return err
			}
			var event room.Event
			body := map[string]any{"text": text, "format": format}
			if agentID != "" {
				body["agent_id"] = agentID
			}
			if len(attachments) > 0 {
				body["attachments"] = attachments
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+roomID+"/messages", body, &event); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(event)
			}
			fmt.Printf("Sent #%d\n", event.Seq)
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to $TOGET_ROOM_ID or the only room)")
	cmd.Flags().StringVar(&format, "format", "plain", "plain or markdown")
	cmd.Flags().StringVar(&agentID, "agent", "", "attribute the message to an Agent (defaults to $TOGET_AGENT_ID)")
	cmd.Flags().StringArrayVar(&files, "file", nil, "attach a file (repeatable)")
	return cmd
}

func uploadFiles(ctx context.Context, c *api.Client, roomID string, files []string) ([]string, error) {
	ids := make([]string, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		if len(data) > room.MaxAttachmentSize {
			return nil, fmt.Errorf("%s: %w", file, room.ErrAttachmentTooLarge)
		}
		name := filepath.Base(file)
		mimeType := mime.TypeByExtension(filepath.Ext(name))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		path := "/api/v1/rooms/" + roomID + "/attachments?name=" + url.QueryEscape(name) + "&mime=" + url.QueryEscape(mimeType)
		body, err := c.Upload(ctx, path, data, mimeType)
		if err != nil {
			return nil, err
		}
		var attachment room.Attachment
		if err := json.Unmarshal(body, &attachment); err != nil {
			return nil, fmt.Errorf("decode attachment: %w", err)
		}
		ids = append(ids, attachment.ID)
	}
	return ids, nil
}

func newAttachmentCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "attachment", Short: "Upload and download encrypted attachments"}
	cmd.AddCommand(newAttachmentListCommand())
	cmd.AddCommand(newAttachmentUploadCommand())
	cmd.AddCommand(newAttachmentDownloadCommand())
	return cmd
}

func newAttachmentListCommand() *cobra.Command {
	var roomID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List attachments in a room",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			var result struct {
				Attachments []room.Attachment `json:"attachments"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/rooms/"+roomID+"/attachments", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Attachments)
			}
			for _, a := range result.Attachments {
				state := "unattached"
				if a.EventID != "" {
					state = "event " + a.EventID
				}
				fmt.Printf("%s  %-32s %8d  %s\n", a.ID, a.Name, a.Size, state)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	return cmd
}

func newAttachmentUploadCommand() *cobra.Command {
	var roomID string
	cmd := &cobra.Command{
		Use:   "upload <file>...",
		Short: "Encrypt and upload files to a room",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			for _, file := range args {
				data, err := os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("read %s: %w", file, err)
				}
				name := filepath.Base(file)
				mimeType := mime.TypeByExtension(filepath.Ext(name))
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
				path := "/api/v1/rooms/" + roomID + "/attachments?name=" + url.QueryEscape(name) + "&mime=" + url.QueryEscape(mimeType)
				body, err := c.Upload(cmd.Context(), path, data, mimeType)
				if err != nil {
					return err
				}
				var attachment room.Attachment
				if err := json.Unmarshal(body, &attachment); err != nil {
					return fmt.Errorf("decode attachment: %w", err)
				}
				if jsonFlag {
					if err := printJSON(attachment); err != nil {
						return err
					}
					continue
				}
				fmt.Printf("%s  %s (%d bytes)\n", attachment.ID, attachment.Name, attachment.Size)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	return cmd
}

func newAttachmentDownloadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download <attachment-id> <output>",
		Short: "Download and decrypt an attachment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			data, _, err := c.Download(cmd.Context(), "/api/v1/attachments/"+args[0]+"/content")
			if err != nil {
				return err
			}
			if args[1] == "-" {
				_, err := os.Stdout.Write(data)
				return err
			}
			if err := os.WriteFile(args[1], data, 0o600); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(map[string]any{"path": args[1], "size": len(data)})
			}
			fmt.Printf("Wrote %s (%d bytes)\n", args[1], len(data))
			return nil
		},
	}
	return cmd
}

func newMessagesCommand() *cobra.Command {
	var roomID, eventType string
	var after int64
	var limit int
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "Read the timeline of a room",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			var result struct {
				Events []room.Event `json:"events"`
			}
			path := fmt.Sprintf("/api/v1/rooms/%s/events?after=%d&limit=%d", roomID, after, limit)
			if eventType != "" {
				path += "&type=" + urlQuery(eventType)
			}
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Events)
			}
			for _, event := range result.Events {
				printEvent(event)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	cmd.Flags().Int64Var(&after, "after", 0, "only show events after this sequence")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum number of events")
	cmd.Flags().StringVar(&eventType, "type", "", "only show this event type")
	return cmd
}

func printEvent(event room.Event) {
	switch event.Type {
	case room.EventMessagePosted:
		var payload room.MessagePostedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		fmt.Printf("[%d] %s: %s\n", event.Seq, event.AuthorID, payload.Text)
	case room.EventMessageEdited:
		var payload room.MessageEditedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		fmt.Printf("[%d] · edited: %s\n", event.Seq, payload.Text)
	case room.EventRoomCreated:
		var payload room.RoomCreatedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		fmt.Printf("[%d] · room created: %s\n", event.Seq, payload.Name)
	case room.EventApprovalRequested:
		var payload room.ApprovalRequestedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		fmt.Printf("[%d] · approval requested: %s\n", event.Seq, payload.Description)
	case room.EventApprovalGranted, room.EventApprovalRejected, room.EventApprovalExpired, room.EventApprovalConsumed:
		fmt.Printf("[%d] · %s\n", event.Seq, event.Type)
	case room.EventToolRequested, room.EventToolResult, room.EventToolFailed:
		var payload room.ToolEventPayload
		_ = json.Unmarshal(event.Payload, &payload)
		fmt.Printf("[%d] · tool %s %s\n", event.Seq, payload.Tool, payload.Status)
	default:
		fmt.Printf("[%d] · %s\n", event.Seq, event.Type)
	}
}

func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Manage local Agents"}
	cmd.AddCommand(newAgentAddCommand())
	cmd.AddCommand(newAgentListCommand())
	cmd.AddCommand(newAgentShowCommand())
	cmd.AddCommand(newAgentStatusCommand())
	cmd.AddCommand(newAgentStopCommand())
	cmd.AddCommand(newAgentSessionsCommand())
	cmd.AddCommand(newAgentResetSessionCommand())
	cmd.AddCommand(newAgentRevokeCommand())
	return cmd
}

func newAgentAddCommand() *cobra.Command {
	var name, runtime, permissions string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a local Agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if name == "" {
				name = prompt("Agent name: ")
			}
			if runtime == "" {
				runtime = "cli"
			}
			var perms json.RawMessage
			if strings.TrimSpace(permissions) != "" {
				perms = json.RawMessage(permissions)
			}
			var created agent.Agent
			body := map[string]any{"name": name, "runtime": runtime, "permissions": perms}
			if err := c.Post(cmd.Context(), "/api/v1/agents", body, &created); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(created)
			}
			fmt.Printf("Agent registered: %s (%s)\n", created.ID, created.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&runtime, "runtime", "cli", "runtime name, e.g. claude-code")
	cmd.Flags().StringVar(&permissions, "permissions", "", "permissions as a JSON object")
	return cmd
}

func newAgentListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local Agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Agents []agent.Agent `json:"agents"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/agents", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Agents)
			}
			for _, a := range result.Agents {
				fmt.Printf("%s  %-20s %-16s %s\n", a.ID, a.Name, a.Status, a.Runtime)
			}
			return nil
		},
	}
}

func newAgentShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <agent-id>",
		Short: "Show an Agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var got agent.Agent
			if err := c.Get(cmd.Context(), "/api/v1/agents/"+args[0], &got); err != nil {
				return err
			}
			return printJSON(got)
		},
	}
}

func newAgentSessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions <agent-id>",
		Short: "Show an Agent's per-room runtime sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Sessions []struct {
					agent.Session
					RoomName string `json:"room_name,omitempty"`
				} `json:"sessions"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/agents/"+args[0]+"/sessions", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Sessions)
			}
			for _, session := range result.Sessions {
				name := session.RoomName
				if name == "" {
					name = session.RoomID
				}
				fmt.Printf("%s  %-20s %-16s seen %d  run %d  %d runs\n",
					session.RoomID, name, session.Status, session.LastSeenSeq, session.LastRunSeq, session.RunCount)
			}
			return nil
		},
	}
}

func newAgentResetSessionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-session <agent-id> <room-id>",
		Short: "Clear an Agent's runtime conversation and catch-up cursor in a room",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var session agent.Session
			if err := c.Delete(cmd.Context(), "/api/v1/agents/"+args[0]+"/sessions/"+args[1], &session); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(session)
			}
			fmt.Printf("Session reset at #%d. The Agent will start a fresh runtime conversation on the next mention.\n", session.LastSeenSeq)
			return nil
		},
	}
}

func newAgentStatusCommand() *cobra.Command {
	var detail, roomID string
	cmd := &cobra.Command{
		Use:   "status <agent-id> <status>",
		Short: "Set an Agent's presence",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			body := map[string]string{"status": args[1], "detail": detail, "room_id": roomID}
			if err := c.Patch(cmd.Context(), "/api/v1/agents/"+args[0], body, nil); err != nil {
				return err
			}
			fmt.Println("Status updated.")
			return nil
		},
	}
	cmd.Flags().StringVar(&detail, "detail", "", "short status detail")
	cmd.Flags().StringVar(&roomID, "room", "", "also post a status event in this room")
	return cmd
}

func newAgentStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <agent-id>",
		Short: "Stop the Agent's active run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Post(cmd.Context(), "/api/v1/agents/"+args[0]+"/stop", nil, nil); err != nil {
				return err
			}
			fmt.Println("Agent run stopped.")
			return nil
		},
	}
}

func newAgentRevokeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <agent-id>",
		Short: "Permanently revoke an Agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Post(cmd.Context(), "/api/v1/agents/"+args[0]+"/revoke", nil, nil); err != nil {
				return err
			}
			fmt.Println("Agent revoked.")
			return nil
		},
	}
}

func newApprovalCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "approval", Short: "Review exact Agent actions"}
	cmd.AddCommand(newApprovalListCommand())
	cmd.AddCommand(newApprovalShowCommand())
	cmd.AddCommand(newApprovalRequestCommand())
	cmd.AddCommand(newApprovalDecideCommand("grant", approval.DecisionGrant))
	cmd.AddCommand(newApprovalDecideCommand("reject", approval.DecisionReject))
	return cmd
}

func newApprovalListCommand() *cobra.Command {
	var roomID, status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List approvals",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			path := "/api/v1/rooms/" + roomID + "/approvals"
			if status != "" {
				path += "?status=" + urlQuery(status)
			}
			var result struct {
				Approvals []approval.Approval `json:"approvals"`
			}
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Approvals)
			}
			for _, a := range result.Approvals {
				fmt.Printf("%s  %-10s %-10s %s\n", a.ID, a.Status, a.AgentID, a.Description)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	cmd.Flags().StringVar(&status, "status", "", "pending, granted, rejected, or expired")
	return cmd
}

func newApprovalShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <approval-id>",
		Short: "Show an approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var got approval.Approval
			if err := c.Get(cmd.Context(), "/api/v1/approvals/"+args[0], &got); err != nil {
				return err
			}
			return printJSON(got)
		},
	}
}

func newApprovalRequestCommand() *cobra.Command {
	var roomID, agentID, kind, toolName, target, recipient, amount, currency string
	var ttlSeconds int
	var params []string
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Ask a human to approve an exact action",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			if agentID == "" {
				agentID = os.Getenv("TOGET_AGENT_ID")
			}
			if agentID == "" {
				return errors.New("--agent is required (or set $TOGET_AGENT_ID)")
			}
			values := map[string]any{}
			for _, p := range params {
				key, raw, ok := strings.Cut(p, "=")
				if !ok || strings.TrimSpace(key) == "" {
					return fmt.Errorf("invalid --param %q; use key=value", p)
				}
				var parsed any
				if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
					values[key] = parsed
				} else {
					values[key] = raw
				}
			}
			action := approval.Action{
				Kind: kind, Tool: toolName, Target: target, Recipient: recipient,
				Amount: amount, Currency: currency, Parameters: values,
			}
			body := map[string]any{"agent_id": agentID, "action": action, "ttl_seconds": ttlSeconds}
			var result struct {
				Approval approval.Approval `json:"approval"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+roomID+"/approvals", body, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Approval)
			}
			fmt.Printf("Approval requested: %s\n%s\nExpires: %s\n",
				result.Approval.ID, result.Approval.Description,
				result.Approval.ExpiresAt.Local().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID")
	cmd.Flags().StringVar(&kind, "kind", "", "action kind, e.g. shell.run or money.spend")
	cmd.Flags().StringVar(&toolName, "tool", "", "tool name")
	cmd.Flags().StringVar(&target, "target", "", "target resource")
	cmd.Flags().StringVar(&recipient, "recipient", "", "recipient")
	cmd.Flags().StringVar(&amount, "amount", "", "amount as a decimal string")
	cmd.Flags().StringVar(&currency, "currency", "", "currency code")
	cmd.Flags().IntVar(&ttlSeconds, "ttl-seconds", 900, "approval lifetime in seconds")
	cmd.Flags().StringArrayVar(&params, "param", nil, "extra parameter key=value (repeatable)")
	return cmd
}

func newApprovalDecideCommand(use, decision string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <approval-id>",
		Short: strings.Title(use) + " an approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Approval approval.Approval `json:"approval"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/approvals/"+args[0]+"/decide",
				map[string]string{"decision": decision}, &result); err != nil {
				return err
			}
			fmt.Printf("%s\n", strings.Title(result.Approval.Status))
			return nil
		},
	}
}

func newToolCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "tool", Short: "Inspect and record Agent tool calls"}
	cmd.AddCommand(newToolListCommand())
	cmd.AddCommand(newToolRequestCommand())
	cmd.AddCommand(newToolCompleteCommand())
	return cmd
}

func newToolListCommand() *cobra.Command {
	var roomID string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tool receipts",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			var result struct {
				Receipts []tool.Receipt `json:"receipts"`
			}
			if err := c.Get(cmd.Context(), fmt.Sprintf("/api/v1/rooms/%s/tools?limit=%d", roomID, limit), &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Receipts)
			}
			for _, r := range result.Receipts {
				fmt.Printf("%s  %-10s %-8s %-16s %s\n", r.ID, r.Status, r.Kind, r.Tool, r.AgentID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum receipts")
	return cmd
}

func newToolRequestCommand() *cobra.Command {
	var roomID, agentID, kind, name, input, approvalID string
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Record a tool call (risky kinds need an approved action)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			roomID, err = resolveRoom(cmd.Context(), c, roomID)
			if err != nil {
				return err
			}
			if agentID == "" {
				agentID = os.Getenv("TOGET_AGENT_ID")
			}
			if agentID == "" {
				return errors.New("--agent is required (or set $TOGET_AGENT_ID)")
			}
			if name == "" {
				return errors.New("--tool is required")
			}
			raw := json.RawMessage(input)
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			body := map[string]any{
				"agent_id": agentID, "kind": kind, "tool": name,
				"input": raw, "approval_id": approvalID,
			}
			var result struct {
				Receipt tool.Receipt `json:"receipt"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+roomID+"/tools", body, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Receipt)
			}
			fmt.Printf("Receipt: %s (%s)\n", result.Receipt.ID, result.Receipt.Status)
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent ID")
	cmd.Flags().StringVar(&kind, "kind", "read", "read, write, external, shell, or native")
	cmd.Flags().StringVar(&name, "tool", "", "tool name")
	cmd.Flags().StringVar(&input, "input", "{}", "tool input JSON")
	cmd.Flags().StringVar(&approvalID, "approval", "", "granted approval ID for risky tools")
	return cmd
}

func newToolCompleteCommand() *cobra.Command {
	var output string
	var success bool
	cmd := &cobra.Command{
		Use:   "complete <receipt-id>",
		Short: "Record a tool result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			body := map[string]any{"output": output, "success": success}
			var result struct {
				Receipt tool.Receipt `json:"receipt"`
			}
			if err := c.Patch(cmd.Context(), "/api/v1/tools/"+args[0], body, &result); err != nil {
				return err
			}
			fmt.Printf("Receipt %s: %s\n", result.Receipt.ID, result.Receipt.Status)
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "tool output")
	cmd.Flags().BoolVar(&success, "success", true, "whether the call succeeded")
	return cmd
}

func newSearchCommand() *cobra.Command {
	var limit int
	var roomID string
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search messages across rooms",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			path := fmt.Sprintf("/api/v1/search?q=%s&limit=%d", urlQuery(query), limit)
			var result struct {
				Hits []room.SearchHit `json:"hits"`
			}
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			if roomID != "" {
				filtered := result.Hits[:0]
				for _, hit := range result.Hits {
					if hit.RoomID == roomID {
						filtered = append(filtered, hit)
					}
				}
				result.Hits = filtered
			}
			if jsonFlag {
				return printJSON(result.Hits)
			}
			for _, hit := range result.Hits {
				fmt.Printf("%s [%d] %s: %s\n", hit.RoomName, hit.Seq, hit.Author, hit.Text)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum hits")
	cmd.Flags().StringVar(&roomID, "room", "", "only this room")
	return cmd
}

func relayStatus(connected bool) string {
	if connected {
		return "connected"
	}
	return "disconnected"
}

// saveNodeRelay merges one runtime setting into config.json without
// overwriting unrelated settings changed by another process.
func saveNodeRelay(paths config.Paths, setting string) error {
	cfg, err := config.Load(paths)
	if err != nil {
		return err
	}
	cfg.NodeRelay = setting
	return config.Save(paths, cfg)
}

func resolveConfig() (config.Paths, config.Config, error) {
	paths, err := config.Resolve(homeFlag)
	if err != nil {
		return config.Paths{}, config.Config{}, err
	}
	if err := config.Ensure(paths); err != nil {
		return config.Paths{}, config.Config{}, err
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return config.Paths{}, config.Config{}, err
	}
	return paths, cfg, nil
}

func localClient() (*api.Client, config.Paths, config.Config, error) {
	paths, cfg, err := resolveConfig()
	if err != nil {
		return nil, config.Paths{}, config.Config{}, err
	}
	token, err := config.ReadToken(paths.APIToken)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, config.Paths{}, config.Config{}, errors.New("the local node is not running; start it with `toget serve`")
		}
		return nil, config.Paths{}, config.Config{}, err
	}
	return api.NewClient(cfg.Listen, token), paths, cfg, nil
}

func resolveRoom(ctx context.Context, c *api.Client, roomID string) (string, error) {
	if roomID == "" {
		roomID = os.Getenv("TOGET_ROOM_ID")
	}
	if roomID != "" {
		return roomID, nil
	}
	var result struct {
		Rooms []room.Room `json:"rooms"`
	}
	if err := c.Get(ctx, "/api/v1/rooms", &result); err != nil {
		return "", err
	}
	switch len(result.Rooms) {
	case 0:
		return "", errors.New("there are no rooms yet; create one with `toget room create`")
	case 1:
		return result.Rooms[0].ID, nil
	default:
		ids := make([]string, 0, len(result.Rooms))
		for _, r := range result.Rooms {
			ids = append(ids, r.ID+" ("+r.Name+")")
		}
		sort.Strings(ids)
		return "", fmt.Errorf("there is more than one room; pass --room <id>:\n  %s", strings.Join(ids, "\n  "))
	}
}

func validateLoopback(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	switch host {
	case "localhost":
		return nil
	case "":
		return errors.New("the local API must bind to loopback, not all interfaces")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the local API must bind to loopback; %q is not a loopback address", host)
	}
	return nil
}

func prompt(label string) string {
	fmt.Fprint(os.Stderr, label)
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func promptPassword(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func formatAccountID(id string) string {
	if len(id) != 14 {
		return id
	}
	return id[:8] + "-" + id[8:]
}

func verifiedLabel(ok bool) string {
	if ok {
		return "verified"
	}
	return "FAILED"
}

func urlQuery(value string) string {
	return url.QueryEscape(value)
}

func printJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
