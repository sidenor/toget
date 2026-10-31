package node_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/node"
)

func TestSetRelayRuntimeSwitch(t *testing.T) {
	ctx := context.Background()
	relayService := startTestRelay(t, "", time.Minute)
	var saved []string
	host := newNodeWithOptions(t, "Host", func(opts *node.Options) {
		opts.SaveRelay = func(setting string) error {
			saved = append(saved, setting)
			return nil
		}
	})

	info, err := host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.RelayMode != "off" || info.RelaySetting != "off" || info.RelayURL != "" || info.RelayTunnel {
		t.Fatalf("initial relay info = %#v", info)
	}

	if err := host.node.SetRelay(ctx, relayService.URL()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 20*time.Second, "pinned relay connection", host.node.RelayConnected)
	waitForCondition(t, 20*time.Second, "pinned relay tunnel", host.node.RelayTunnelConnected)
	if len(saved) != 1 || saved[0] != relayService.URL() {
		t.Fatalf("persisted relay settings = %#v", saved)
	}
	info, err = host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.RelayMode != "pinned" || info.RelaySetting != relayService.URL() || info.RelayBase == "" {
		t.Fatalf("pinned relay info = %#v", info)
	}
	if !info.RelayConnected || !info.RelayTunnel || info.RelayURL == "" {
		t.Fatalf("pinned relay session = %#v", info)
	}

	if err := host.node.SetRelay(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 20*time.Second, "relay disconnect", func() bool { return !host.node.RelayConnected() })
	if len(saved) != 2 || saved[1] != "off" {
		t.Fatalf("persisted relay settings = %#v", saved)
	}
	info, err = host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.RelayMode != "off" || info.RelaySetting != "off" || info.RelayBase != "" || info.RelayURL != "" {
		t.Fatalf("relay-off info = %#v", info)
	}

	// Switching back on must wake the sleeping relay loop without a restart.
	if err := host.node.SetRelay(ctx, relayService.URL()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 20*time.Second, "relay reconnect", host.node.RelayConnected)
}

func TestSetRelayAutoUsesDirectory(t *testing.T) {
	ctx := context.Background()
	relayService := startTestRelay(t, "", time.Minute)
	directoryService, _ := startTestDirectory(t, relayService.URL())
	var saved []string
	host := newNodeWithOptions(t, "Host", func(opts *node.Options) {
		opts.Directory = directoryService.URL()
		opts.SaveRelay = func(setting string) error {
			saved = append(saved, setting)
			return nil
		}
	})

	if err := host.node.SetRelay(ctx, "auto"); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0] != "auto" {
		t.Fatalf("persisted relay settings = %#v", saved)
	}
	waitForCondition(t, 20*time.Second, "automatic relay connection", host.node.RelayConnected)
	info, err := host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.RelayMode != "auto" || info.RelaySetting != "auto" || info.RelayBase == "" {
		t.Fatalf("automatic relay info = %#v", info)
	}
	waitForCondition(t, 20*time.Second, "automatic relay tunnel", host.node.RelayTunnelConnected)

	if err := host.node.SetRelay(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 20*time.Second, "automatic relay disconnect", func() bool { return !host.node.RelayConnected() })
}

func TestSetRelayValidationAndPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	var saved []string
	host := newNodeWithOptions(t, "Host", func(opts *node.Options) {
		opts.SaveRelay = func(setting string) error {
			saved = append(saved, setting)
			return errors.New("disk is full")
		}
	})

	for _, setting := range []string{"http://relay.example.com", "https://relay.example.com", "https://relay.example.com#fp=nothex"} {
		if err := host.node.SetRelay(ctx, setting); err == nil {
			t.Fatalf("SetRelay(%q) succeeded", setting)
		}
	}
	if len(saved) != 0 {
		t.Fatalf("invalid settings were persisted: %#v", saved)
	}
	if err := host.node.SetRelay(ctx, "auto"); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("automatic relay without a directory = %v", err)
	}

	pinned := "https://relay.example.com#fp=" + strings.Repeat("ab", 32)
	err := host.node.SetRelay(ctx, pinned)
	if err == nil || !strings.HasPrefix(err.Error(), "save relay setting:") {
		t.Fatalf("persistence failure = %v", err)
	}
	if len(saved) != 1 || saved[0] != pinned {
		t.Fatalf("persisted relay settings = %#v", saved)
	}
	info, err := host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.RelayMode != "off" || info.RelaySetting != "off" || info.RelayBase != "" {
		t.Fatalf("failed save changed the relay setting: %#v", info)
	}
}
