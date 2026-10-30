// Command toget runs the local node, the CLI, and the local web UI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/ui"
	"github.com/sidenor/toget/internal/vault"
	"github.com/sidenor/toget/internal/version"
)

var (
	homeFlag    string
	jsonFlag    bool
	listenFlag  string
	stdinReader = bufio.NewReader(os.Stdin)
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
	root.AddCommand(newAccountCommand())
	root.AddCommand(newRoomCommand())
	root.AddCommand(newSendCommand())
	root.AddCommand(newMessagesCommand())
	root.AddCommand(newVersionCommand())
	return root
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

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local node and web UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&listenFlag, "listen", "", "loopback listen address (default: 127.0.0.1:7718)")
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
	if err := validateLoopback(cfg.Listen); err != nil {
		return err
	}

	db, err := storage.Open(paths.DB)
	if err != nil {
		return err
	}
	defer db.Close()

	v, err := vault.LoadOrCreate(paths.VaultKey)
	if err != nil {
		return err
	}
	token, err := config.LoadOrCreateToken(paths.APIToken)
	if err != nil {
		return err
	}

	accounts := account.New(db, v)
	rooms := room.New(db, v)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	handler := api.New(api.Options{
		Accounts: accounts,
		Rooms:    rooms,
		Version:  version.Current(),
		Token:    token,
		Listen:   cfg.Listen,
		UI:       ui.FS(),
		Logger:   logger,
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
	fmt.Printf("Data: %s\n", paths.Home)
	if snapshot, err := accounts.Get(ctx); err == nil {
		fmt.Printf("Account: %s · %s\n", snapshot.Account.Name, formatAccountID(snapshot.Account.ID))
	} else if errors.Is(err, account.ErrNotFound) {
		fmt.Println("Account: not created yet")
	}
	fmt.Println("Press Ctrl+C to stop.")

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
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
			} else {
				fmt.Printf("Account: %s · %s\n", status.Account.Name, formatAccountID(status.Account.ID))
				fmt.Printf("Server:  %s\n", status.Account.ServerID)
			}
			fmt.Printf("Rooms:   %d\n", status.Rooms)
			return nil
		},
	}
}

func newAccountCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Manage the local account"}
	cmd.AddCommand(newAccountCreateCommand())
	cmd.AddCommand(newAccountShowCommand())
	return cmd
}

func newAccountCreateCommand() *cobra.Command {
	var name, password, deviceName string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create the account for this node",
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
			var created api.AccountView
			if err := c.Post(cmd.Context(), "/api/v1/account", map[string]string{
				"name": name, "password": password, "device_name": deviceName,
			}, &created); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(created)
			}
			fmt.Printf("Account created.\n\nID:     %s\nName:   %s\nServer: %s\n\n", formatAccountID(created.ID), created.Name, created.ServerID)
			fmt.Println("Keep your password safe. This account lives on this machine.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&password, "password", "", "password (prompted when omitted)")
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
			fmt.Printf("ID:     %s\nName:   %s\nServer: %s\n", formatAccountID(a.ID), a.Name, a.ServerID)
			return nil
		},
	}
}

func newRoomCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "room", Short: "Manage rooms"}
	cmd.AddCommand(newRoomCreateCommand())
	cmd.AddCommand(newRoomListCommand())
	return cmd
}

func newRoomCreateCommand() *cobra.Command {
	var name string
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
			if err := c.Post(cmd.Context(), "/api/v1/rooms", map[string]string{"name": name}, &created); err != nil {
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
	return cmd
}

func newRoomListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List rooms",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Rooms []room.Room `json:"rooms"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/rooms", &result); err != nil {
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
				fmt.Printf("%s  %s\n", r.ID, r.Name)
			}
			return nil
		},
	}
}

func newSendCommand() *cobra.Command {
	var roomID string
	cmd := &cobra.Command{
		Use:   "send [text...]",
		Short: "Send a message to a room",
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
			text := strings.Join(args, " ")
			var event room.Event
			if err := c.Post(cmd.Context(), "/api/v1/rooms/"+roomID+"/messages", map[string]string{"text": text}, &event); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(event)
			}
			fmt.Printf("Sent #%d\n", event.Seq)
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	return cmd
}

func newMessagesCommand() *cobra.Command {
	var roomID string
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
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Events)
			}
			for _, event := range result.Events {
				switch event.Type {
				case room.EventMessagePosted:
					var payload room.MessagePostedPayload
					_ = json.Unmarshal(event.Payload, &payload)
					fmt.Printf("[%d] %s: %s\n", event.Seq, event.AuthorID, payload.Text)
				case room.EventRoomCreated:
					var payload room.RoomCreatedPayload
					_ = json.Unmarshal(event.Payload, &payload)
					fmt.Printf("[%d] · room created: %s\n", event.Seq, payload.Name)
				default:
					fmt.Printf("[%d] · %s\n", event.Seq, event.Type)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roomID, "room", "", "room ID (defaults to the only room)")
	cmd.Flags().Int64Var(&after, "after", 0, "only show events after this sequence")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum number of events")
	return cmd
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

func printJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
