package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/update"
	"github.com/sidenor/toget/internal/version"
)

func testRootPublicKey(t *testing.T) string {
	t.Helper()
	key, err := update.GenerateSigningKey(update.KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.Public)
}

func TestUpdateCommandShape(t *testing.T) {
	cmd := newUpdateCommand()
	if cmd.Name() != "update" {
		t.Fatalf("name = %q", cmd.Name())
	}
	want := map[string]bool{"check": true, "status": true, "apply": true, "finish": true, "rollback": true}
	for _, sub := range cmd.Commands() {
		delete(want, sub.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing subcommands: %v", want)
	}
	for _, name := range []string{"update-root", "channel", "channel-url", "root-key", "health-url", "restart", "health-command", "no-restart"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
}

func TestUpdateClientPrecedence(t *testing.T) {
	public := testRootPublicKey(t)
	cfg := config.Config{Update: config.UpdateConfig{
		Root:       "/managed",
		Channel:    "beta",
		ChannelURL: "https://config.example/channels/beta.json",
		RootKey:    public,
	}}
	opts := &updateCLIOptions{}
	client, err := opts.client(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if client.Channel != "beta" || client.ChannelURL != cfg.Update.ChannelURL || client.Verifier == nil {
		t.Fatalf("client = %+v", client)
	}

	other := testRootPublicKey(t)
	opts = &updateCLIOptions{
		channelURL: "https://flag.example/channels/stable.json",
		channel:    "stable",
		rootKey:    other,
		osName:     "linux",
		archName:   "arm64",
	}
	client, err = opts.client(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if client.Channel != "stable" || client.ChannelURL != "https://flag.example/channels/stable.json" || client.OS != "linux" || client.Arch != "arm64" {
		t.Fatalf("client = %+v", client)
	}

	defaults := cfg
	defaults.Update.Channel = ""
	opts = &updateCLIOptions{}
	client, err = opts.client(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if client.Channel != "stable" {
		t.Fatalf("default channel = %q", client.Channel)
	}

	if _, err := (&updateCLIOptions{}).client(config.Config{}); err == nil || !strings.Contains(err.Error(), "channel URL") {
		t.Fatalf("missing channel URL error = %v", err)
	}
	if _, err := (&updateCLIOptions{}).client(config.Config{Update: config.UpdateConfig{ChannelURL: "https://example/channels/stable.json"}}); err == nil || !strings.Contains(err.Error(), "root key") {
		t.Fatalf("missing root key error = %v", err)
	}
	bad := config.Config{Update: config.UpdateConfig{ChannelURL: "https://example/channels/stable.json", RootKey: "bad"}}
	if _, err := (&updateCLIOptions{}).client(bad); err == nil {
		t.Fatal("accepted an invalid root key")
	}
	onlyOS := config.Config{Update: config.UpdateConfig{ChannelURL: "https://example/channels/stable.json", RootKey: public}}
	if _, err := (&updateCLIOptions{osName: "linux"}).client(onlyOS); err == nil {
		t.Fatal("accepted --os without --arch")
	}
}

func TestUpdateManagerPrecedence(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Update: config.UpdateConfig{
		Root:          filepath.Join(t.TempDir(), "from-config"),
		Restart:       []string{"systemctl", "restart", "toget"},
		HealthURL:     "http://127.0.0.1:7718/healthz",
		HealthCommand: []string{"--json", "version"},
	}}
	opts := &updateCLIOptions{
		root:          root,
		restart:       []string{"service", "toget", "restart"},
		healthCommand: []string{"version"},
		healthURL:     "http://127.0.0.1:9999/healthz",
	}
	manager, err := opts.manager(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Root != root {
		t.Fatalf("root = %q", manager.Root)
	}
	if strings.Join(manager.RestartCommand, " ") != "service toget restart" {
		t.Fatalf("restart = %v", manager.RestartCommand)
	}
	if strings.Join(manager.HealthCommand, " ") != "version" {
		t.Fatalf("health command = %v", manager.HealthCommand)
	}
	if manager.HealthURL != "http://127.0.0.1:9999/healthz" {
		t.Fatalf("health URL = %q", manager.HealthURL)
	}
	if manager.CurrentExecutable == "" {
		t.Fatal("current executable is empty")
	}
	if manager.Client != nil {
		t.Fatal("client constructed without withClient")
	}

	fromConfig, err := (&updateCLIOptions{root: root}).manager(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fromConfig.RestartCommand, " ") != "systemctl restart toget" {
		t.Fatalf("config restart = %v", fromConfig.RestartCommand)
	}
	if fromConfig.HealthURL != cfg.Update.HealthURL {
		t.Fatalf("config health URL = %q", fromConfig.HealthURL)
	}

	staged, err := (&updateCLIOptions{root: root, noRestart: true, restart: []string{"false"}}).manager(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.RestartCommand) != 0 {
		t.Fatalf("no-restart command = %v", staged.RestartCommand)
	}

	if _, err := (&updateCLIOptions{}).manager(config.Config{}, false); err == nil {
		t.Fatal("manager accepted a missing root")
	}
	if _, err := (&updateCLIOptions{root: root}).manager(cfg, true); err == nil {
		t.Fatal("manager built a client without a channel URL")
	}
}

func TestFallbackRolloutID(t *testing.T) {
	first := fallbackRolloutID("/home/a")
	second := fallbackRolloutID("/home/a")
	other := fallbackRolloutID("/home/b")
	if first != second {
		t.Fatalf("rollout ID is not deterministic: %q != %q", first, second)
	}
	if first == other {
		t.Fatal("different homes produced the same rollout ID")
	}
	if len(first) != 64 {
		t.Fatalf("rollout ID length = %d", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("rollout ID is not hex: %v", err)
	}
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte("toget-update-rollout-id:v1\n" + host + "\n/home/a"))
	if first != hex.EncodeToString(sum[:]) {
		t.Fatal("rollout ID does not match its documented derivation")
	}
}

func TestUpdateCurrentAndRolloutUninitialized(t *testing.T) {
	home := t.TempDir()
	paths, err := config.Resolve(home)
	if err != nil {
		t.Fatal(err)
	}
	info := version.Current()
	current, rolloutID, err := updateCurrentAndRollout(paths, config.Config{}, &updateCLIOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current.App != info.App || current.Protocol != info.Protocol || current.Schema != info.Schema {
		t.Fatalf("current = %+v", current)
	}
	if rolloutID != fallbackRolloutID(paths.Home) {
		t.Fatalf("rollout ID = %q", rolloutID)
	}

	root := filepath.Join(home, "managed")
	cfg := config.Config{Update: config.UpdateConfig{Root: root}}
	_, rolloutID, err = updateCurrentAndRollout(paths, cfg, &updateCLIOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rolloutID != fallbackRolloutID(paths.Home) {
		t.Fatalf("uninitialized managed rollout ID = %q", rolloutID)
	}

	badRoot := filepath.Join(home, "not-a-directory")
	if err := os.WriteFile(badRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := updateCurrentAndRollout(paths, config.Config{Update: config.UpdateConfig{Root: badRoot}}, &updateCLIOptions{}); err == nil {
		t.Fatal("accepted a managed root that is a file")
	}
}
