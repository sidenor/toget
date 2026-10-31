package update

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testMonitorLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type monitorFixture struct {
	monitor   *Monitor
	manager   *Manager
	server    *releaseServer
	home      string
	statePath string
	now       time.Time
	clock     *time.Time
}

func newMonitorFixture(t *testing.T, mode string) *monitorFixture {
	t.Helper()
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	// The in-process version is 0.1.0-dev, so keep the minimum supported app
	// below it while exercising the monitor.
	rs.publishManifest(t, "1.1.0", 1, 9, 1, "0.0.1")
	manager, _ := newInstallTestManager(t, rs)
	// The manager adopts the running binary before the first update. Make the
	// fake executable report the same development version as this process.
	oldPath := filepath.Join(t.TempDir(), "toget")
	if err := os.WriteFile(oldPath, fakeBinary("0.1.0-dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager.CurrentExecutable = oldPath
	home := t.TempDir()
	statePath := filepath.Join(home, "update-check.json")
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	clock := now
	monitor, err := NewMonitor(MonitorOptions{
		Home:       home,
		StatePath:  statePath,
		Root:       manager.Root,
		Channel:    "stable",
		ChannelURL: rs.server.URL + "/channels/stable.json",
		Client:     rs.client(),
		Manager:    manager,
		Mode:       mode,
		Logger:     testMonitorLogger(),
		Now:        func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &monitorFixture{
		monitor: monitor, manager: manager, server: rs,
		home: home, statePath: statePath, now: now, clock: &clock,
	}
}

func TestMonitorCheckAndDismiss(t *testing.T) {
	f := newMonitorFixture(t, "notify")

	result, err := f.monitor.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Manifest.Release != "1.1.0" {
		t.Fatalf("result = %+v", result)
	}
	view := f.monitor.View()
	if !view.Enabled || view.Mode != "notify" || view.LastCheck == nil || view.Result == nil {
		t.Fatalf("view = %+v", view)
	}
	if view.Dismissed {
		t.Fatal("fresh update is already dismissed")
	}
	if _, err := os.Stat(f.statePath); err != nil {
		t.Fatalf("state file: %v", err)
	}
	data, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), MonitorFormat) || !strings.Contains(string(data), "1.1.0") {
		t.Fatalf("state file = %s", data)
	}

	if err := f.monitor.Dismiss("1.1.0", time.Hour); err != nil {
		t.Fatal(err)
	}
	if view := f.monitor.View(); !view.Dismissed || view.DismissedUntil == nil {
		t.Fatalf("after dismiss: %+v", view)
	}
	*f.clock = f.now.Add(2 * time.Hour)
	if view := f.monitor.View(); view.Dismissed {
		t.Fatalf("dismissal did not expire: %+v", view)
	}

	if err := f.monitor.Dismiss("1.1.0", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := f.monitor.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if view := f.monitor.View(); view.Dismissed {
		t.Fatal("forced check did not clear the dismissal")
	}
}

func TestMonitorDismissalLimits(t *testing.T) {
	f := newMonitorFixture(t, "notify")
	if err := f.monitor.Dismiss("1.1.0", MaxDismissDuration+time.Hour); !errors.Is(err, ErrDismissalTooLong) {
		t.Fatalf("recommended dismissal error = %v", err)
	}

	f.monitor.mu.Lock()
	f.monitor.snapshot.Result = &CheckResult{
		Available: true,
		Manifest:  &Manifest{Release: "1.1.0", Level: LevelForced},
	}
	f.monitor.mu.Unlock()
	if err := f.monitor.Dismiss("1.1.0", MaxForcedDismissDuration+time.Hour); !errors.Is(err, ErrDismissalTooLong) {
		t.Fatalf("forced dismissal error = %v", err)
	}
	if err := f.monitor.Dismiss("1.1.0", MaxForcedDismissDuration); err != nil {
		t.Fatalf("forced dismissal at the limit: %v", err)
	}
	if view := f.monitor.View(); !view.Dismissed {
		t.Fatalf("forced update was not postponed: %+v", view)
	}
	*f.clock = f.now.Add(MaxForcedDismissDuration + time.Minute)
	if view := f.monitor.View(); view.Dismissed {
		t.Fatalf("forced postponement did not expire: %+v", view)
	}
}

func TestMonitorApplyStagesAndRollback(t *testing.T) {
	f := newMonitorFixture(t, "auto")
	result, err := f.monitor.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available {
		t.Fatalf("result = %+v", result)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		view := f.monitor.View()
		if view.Staged != nil {
			if !view.Staged.NeedsRestart {
				t.Fatalf("staged result = %+v, want a pending restart", view.Staged)
			}
			if view.Status == nil || !view.Status.Initialized || view.Status.State.Pending == nil {
				t.Fatalf("pending state = %+v", view.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic staging did not finish: %+v", view)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A fresh monitor loads the staged marker, but keeps it while the update
	// is still pending.
	reloaded, err := NewMonitor(MonitorOptions{
		Home: f.home, StatePath: f.statePath, Root: f.manager.Root,
		Channel: "stable", ChannelURL: f.server.server.URL + "/channels/stable.json",
		Client: f.server.client(), Manager: f.manager, Mode: "auto",
		Logger: testMonitorLogger(), Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if view := reloaded.View(); view.Staged == nil {
		t.Fatalf("staged marker was lost across restart: %+v", view)
	}

	rolled, err := reloaded.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Release != "0.1.0-dev" {
		t.Fatalf("rollback = %+v", rolled)
	}
	view := reloaded.View()
	if view.Staged != nil || view.Status == nil || view.Status.State.Pending != nil {
		t.Fatalf("rollback left staged state: %+v", view)
	}
}

func TestMonitorManualApplyRejectsOtherRelease(t *testing.T) {
	f := newMonitorFixture(t, "notify")
	if _, err := f.monitor.Apply(context.Background(), "9.9.9"); err == nil {
		t.Fatal("apply accepted an unrelated release")
	} else if !errors.Is(err, ErrReleaseUnavailable) {
		t.Fatalf("apply error = %v", err)
	}
}

func TestMonitorDisabledWithoutConfiguration(t *testing.T) {
	monitor, err := NewMonitor(MonitorOptions{Home: t.TempDir(), Mode: "notify", Logger: testMonitorLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if monitor.Enabled() {
		t.Fatal("unconfigured monitor is enabled")
	}
	if view := monitor.View(); view.Enabled || view.ConfigError == "" {
		t.Fatalf("view = %+v", view)
	}
	if _, err := monitor.Check(context.Background(), false); !errors.Is(err, ErrUpdateNotConfigured) {
		t.Fatalf("check error = %v", err)
	}
}

func TestMonitorCorruptStateIsIgnored(t *testing.T) {
	f := newMonitorFixture(t, "notify")
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	monitor, err := NewMonitor(MonitorOptions{
		Home: f.home, StatePath: f.statePath, Root: f.manager.Root,
		Channel: "stable", ChannelURL: f.server.server.URL + "/channels/stable.json",
		Client: f.server.client(), Manager: f.manager, Mode: "notify",
		Logger: testMonitorLogger(), Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if view := monitor.View(); view.LastCheck != nil || view.Result != nil {
		t.Fatalf("corrupt state was loaded: %+v", view)
	}
	if _, err := monitor.Check(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), MonitorFormat) {
		t.Fatalf("valid state was not rewritten: %s", data)
	}
}

func TestMonitorStartChecksInBackground(t *testing.T) {
	f := newMonitorFixture(t, "notify")
	f.monitor.initial = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.monitor.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if view := f.monitor.View(); view.LastCheck != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background check did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLaunchRestartDetached(t *testing.T) {
	if !restartSupported() {
		t.Skip("detached restart is not supported on this platform")
	}
	requireExecutableScripts(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "restarted")
	logPath := filepath.Join(dir, "restart.log")
	command := []string{"/bin/sh", "-c", `printf restarted > "$1"`, "restart-test", marker}
	if err := launchRestart(command, 10*time.Millisecond, logPath); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			if string(data) != "restarted" {
				t.Fatalf("marker = %q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart command did not run: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
