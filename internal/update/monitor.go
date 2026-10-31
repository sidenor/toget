package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/version"
)

const (
	// MonitorFormat identifies the persisted update-monitor snapshot.
	MonitorFormat = "toget-update-monitor"
	// MonitorVersion is the snapshot format version.
	MonitorVersion = 1

	// DefaultDismissDuration is how long "later" hides a recommended update.
	DefaultDismissDuration = 24 * time.Hour
	// MaxDismissDuration caps a "later" choice for recommended or security
	// releases.
	MaxDismissDuration = 7 * 24 * time.Hour
	// MaxForcedDismissDuration caps a "later" choice for a forced release.
	// Forced updates are not silently forgotten.
	MaxForcedDismissDuration = 24 * time.Hour

	// DefaultInitialCheckDelay lets the daemon finish starting before it
	// touches the network.
	DefaultInitialCheckDelay = 10 * time.Second
	// monitorCheckTimeout bounds one scheduled check.
	monitorCheckTimeout = 2 * time.Minute
	// monitorApplyTimeout bounds an automatic stage.
	monitorApplyTimeout  = 30 * time.Minute
	maxMonitorStateBytes = 2 << 20
)

var (
	// ErrUpdateNotConfigured means the channel URL, root, or root key is
	// missing.
	ErrUpdateNotConfigured = errors.New("updates are not configured")
	// ErrReleaseUnavailable means the requested release is not the currently
	// offered release.
	ErrReleaseUnavailable = errors.New("release is not available")
	// ErrNoRestartCommand means the node has no local restart command.
	ErrNoRestartCommand = errors.New("no restart command is configured")
	// ErrDismissalTooLong means the requested quiet period exceeds the policy.
	ErrDismissalTooLong = errors.New("dismissal period is too long")
)

// MonitorOptions configures the in-daemon update monitor.
type MonitorOptions struct {
	Home          string
	StatePath     string
	Root          string
	Channel       string
	ChannelURL    string
	RootKey       string
	Restart       []string
	HealthCommand []string
	HealthURL     string
	Mode          string
	CheckInterval time.Duration

	// Client, Manager, Now, Logger, and InitialDelay are injectable for tests.
	Client       *Client
	Manager      *Manager
	Now          func() time.Time
	Logger       *slog.Logger
	InitialDelay time.Duration
}

// MonitorStatus is the local API/UI view of the update monitor.
type MonitorStatus struct {
	Enabled           bool         `json:"enabled"`
	Mode              string       `json:"mode"`
	Channel           string       `json:"channel"`
	Interval          string       `json:"check_interval"`
	LastCheck         *time.Time   `json:"last_check,omitempty"`
	LastError         string       `json:"last_error,omitempty"`
	Current           Current      `json:"current"`
	Result            *CheckResult `json:"result,omitempty"`
	Dismissed         bool         `json:"dismissed,omitempty"`
	DismissedUntil    *time.Time   `json:"dismissed_until,omitempty"`
	Staged            *ApplyResult `json:"staged,omitempty"`
	Status            *Status      `json:"status,omitempty"`
	RestartConfigured bool         `json:"restart_configured"`
	RestartSupported  bool         `json:"restart_supported"`
	ConfigError       string       `json:"config_error,omitempty"`
}

type monitorSnapshot struct {
	Format           string       `json:"format"`
	Version          int          `json:"version"`
	LastCheck        *time.Time   `json:"last_check,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
	Result           *CheckResult `json:"result,omitempty"`
	DismissedRelease string       `json:"dismissed_release,omitempty"`
	DismissedUntil   *time.Time   `json:"dismissed_until,omitempty"`
	Staged           *ApplyResult `json:"staged,omitempty"`
}

// Monitor periodically checks a signed channel and exposes an update prompt
// to the local API. It never restarts the daemon by itself; staging and an
// explicit restart are separate steps so an automatic update cannot interrupt
// an active Agent run without the operator's policy saying so.
type Monitor struct {
	home      string
	statePath string
	root      string
	channel   string
	channelUR string
	restart   []string
	mode      string
	interval  time.Duration
	logger    *slog.Logger
	now       func() time.Time

	client  *Client
	manager *Manager
	// configErr is set when the monitor is created without enough settings
	// to check; the UI shows it instead of pretending updates are healthy.
	configErr error
	initial   time.Duration

	checkMu  sync.Mutex
	opMu     sync.Mutex
	mu       sync.RWMutex
	snapshot monitorSnapshot
	started  atomic.Bool
}

// NewMonitor creates a monitor. Missing update settings produce a disabled
// monitor rather than an error; invalid settings produce an error.
func NewMonitor(opts MonitorOptions) (*Monitor, error) {
	if strings.TrimSpace(opts.Home) == "" {
		return nil, errors.New("update monitor: home is required")
	}
	mode, err := (config.UpdateConfig{Mode: opts.Mode}).NormalizedMode()
	if err != nil {
		return nil, err
	}
	interval := opts.CheckInterval
	if interval <= 0 {
		interval = config.DefaultUpdateCheckInterval
	}
	if interval < config.MinUpdateCheckInterval {
		return nil, fmt.Errorf("update monitor: interval must be at least %s", config.MinUpdateCheckInterval)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	statePath := strings.TrimSpace(opts.StatePath)
	if statePath == "" {
		statePath = filepath.Join(opts.Home, "update-check.json")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	initial := opts.InitialDelay
	if initial <= 0 {
		initial = DefaultInitialCheckDelay
	}
	m := &Monitor{
		home:      opts.Home,
		statePath: statePath,
		root:      strings.TrimSpace(opts.Root),
		channel:   strings.TrimSpace(opts.Channel),
		channelUR: strings.TrimSpace(opts.ChannelURL),
		restart:   append([]string(nil), opts.Restart...),
		mode:      mode,
		interval:  interval,
		logger:    logger,
		now:       now,
		client:    opts.Client,
		manager:   opts.Manager,
		initial:   initial,
	}
	if m.channel == "" {
		m.channel = "stable"
	}
	rootKey := strings.TrimSpace(opts.RootKey)
	if m.root == "" || m.channelUR == "" || (m.client == nil && rootKey == "") {
		m.configErr = fmt.Errorf("%w: set update.root, update.channel_url, and update.root_key", ErrUpdateNotConfigured)
	} else if m.client == nil {
		anchor, err := ParseTrustAnchor(rootKey)
		if err != nil {
			return nil, fmt.Errorf("update monitor: release root key: %w", err)
		}
		m.client = &Client{
			Verifier:   NewVerifier(anchor),
			ChannelURL: m.channelUR,
			Channel:    m.channel,
		}
	}
	if m.manager == nil && m.root != "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("update monitor: resolve executable: %w", err)
		}
		m.manager = &Manager{Root: m.root, CurrentExecutable: executable}
	}
	if m.manager != nil {
		if m.manager.Client == nil {
			m.manager.Client = m.client
		}
		if len(m.manager.HealthCommand) == 0 {
			m.manager.HealthCommand = append([]string(nil), opts.HealthCommand...)
		}
		if m.manager.HealthURL == "" {
			m.manager.HealthURL = strings.TrimSpace(opts.HealthURL)
		}
	}
	if err := m.loadSnapshot(); err != nil {
		logger.Warn("update monitor state could not be read", "error", err)
	}
	m.reconcileSnapshot()
	return m, nil
}

// reconcileSnapshot clears a staged marker that a previous process already
// finalized (or rolled back) at startup.
func (m *Monitor) reconcileSnapshot() {
	if m.manager == nil {
		return
	}
	m.mu.RLock()
	staged := m.snapshot.Staged
	m.mu.RUnlock()
	if staged == nil {
		return
	}
	status, err := m.manager.Status()
	if err != nil {
		return
	}
	if status.Initialized && status.State.Pending != nil {
		return
	}
	if status.Initialized && (status.State.Current == staged.Release || status.State.Current == staged.Previous) {
		m.mu.Lock()
		m.snapshot.Staged = nil
		if err := m.saveSnapshotLocked(); err != nil {
			m.logger.Warn("update monitor state could not be saved", "error", err)
		}
		m.mu.Unlock()
	}
}

// Enabled reports whether a channel and trust anchor are configured.
func (m *Monitor) Enabled() bool { return m != nil && m.configErr == nil }

// Mode returns notify, auto, or off.
func (m *Monitor) Mode() string {
	if m == nil {
		return config.UpdateModeOff
	}
	return m.mode
}

// Start runs the periodic check loop until ctx is cancelled. It is a no-op
// when updates are not configured or the mode is off.
func (m *Monitor) Start(ctx context.Context) {
	if m == nil || !m.Enabled() || m.mode == config.UpdateModeOff {
		return
	}
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		timer := time.NewTimer(m.initial)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.scheduledCheck(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.scheduledCheck(ctx)
			}
		}
	}()
}

func (m *Monitor) scheduledCheck(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, monitorCheckTimeout)
	defer cancel()
	result, err := m.Check(ctx, false)
	switch {
	case err != nil:
		m.logger.Warn("update check failed", "error", err)
	case result != nil && result.Available:
		m.logger.Info("update available", "release", result.Manifest.Release,
			"level", result.Manifest.Level, "channel", m.channel)
	default:
		m.logger.Debug("no update available")
	}
}

// Check fetches and verifies the channel. A forced check ignores rollout.
func (m *Monitor) Check(ctx context.Context, force bool) (*CheckResult, error) {
	if m == nil {
		return nil, ErrUpdateNotConfigured
	}
	if !m.Enabled() {
		return nil, m.configErr
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if force {
		m.mu.Lock()
		m.snapshot.DismissedRelease = ""
		m.snapshot.DismissedUntil = nil
		m.mu.Unlock()
	}
	result, err := m.checkLocked(ctx, force)
	if err != nil {
		return nil, err
	}
	if m.mode == config.UpdateModeAuto && result.Available && !m.isDismissedLocked(result) {
		go m.autoStage(result)
	}
	return result, nil
}

func (m *Monitor) homeDir() string { return m.home }

func (m *Monitor) autoStage(result *CheckResult) {
	ctx, cancel := context.WithTimeout(context.Background(), monitorApplyTimeout)
	defer cancel()
	applied, err := m.applyResult(ctx, result)
	if err != nil {
		m.logger.Warn("automatic update staging failed", "release", result.Manifest.Release, "error", err)
		return
	}
	m.logger.Info("update staged automatically", "release", applied.Release, "needs_restart", applied.NeedsRestart)
}

// Apply re-checks the channel and stages the requested release. It never
// restarts the daemon; call Restart after showing the user the result.
func (m *Monitor) Apply(ctx context.Context, release string) (*ApplyResult, error) {
	if m == nil || !m.Enabled() {
		return nil, ErrUpdateNotConfigured
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	result := m.cachedResult()
	if result == nil || result.Manifest == nil || result.Manifest.Release != release || !result.Available {
		checked, err := m.checkLocked(ctx, false)
		if err != nil {
			return nil, err
		}
		result = checked
	}
	if result == nil || result.Manifest == nil {
		return nil, fmt.Errorf("%w: %s", ErrReleaseUnavailable, release)
	}
	if result.Manifest == nil || result.Manifest.Release != release || !result.Available {
		return nil, fmt.Errorf("%w: %s", ErrReleaseUnavailable, release)
	}
	return m.applyResult(ctx, result)
}

func (m *Monitor) applyResult(ctx context.Context, result *CheckResult) (*ApplyResult, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	status, err := m.manager.Status()
	if err != nil {
		return nil, err
	}
	if status.Initialized && status.State.Pending != nil {
		return nil, errors.New("another update is already pending; restart Toget to finish it")
	}
	applied, err := m.manager.Apply(ctx, result)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.snapshot.Staged = applied
	m.snapshot.DismissedRelease = ""
	m.snapshot.DismissedUntil = nil
	stateErr := m.saveSnapshotLocked()
	m.mu.Unlock()
	if stateErr != nil {
		m.logger.Warn("update monitor state could not be saved", "error", stateErr)
	}
	return applied, nil
}

// checkLocked is Check without taking checkMu. The caller holds checkMu.
func (m *Monitor) checkLocked(ctx context.Context, force bool) (*CheckResult, error) {
	manager := m.manager
	info := version.Current()
	current := Current{App: info.App, Protocol: info.Protocol, Schema: info.Schema}
	rolloutID := LocalRolloutID(m.homeDir())
	if manager != nil {
		if installed, managed, err := manager.Current(); err != nil {
			return nil, err
		} else if managed {
			current = installed
		}
		if status, err := manager.Status(); err == nil && status.Initialized && status.State.RolloutID != "" {
			rolloutID = status.State.RolloutID
		}
	}
	result, err := m.client.Check(ctx, current, rolloutID, CheckOptions{Force: force})
	now := m.now().UTC().Truncate(time.Millisecond)
	m.mu.Lock()
	m.snapshot.LastCheck = &now
	if err != nil {
		m.snapshot.LastError = err.Error()
		m.snapshot.Result = nil
	} else {
		m.snapshot.LastError = ""
		m.snapshot.Result = result
	}
	stateErr := m.saveSnapshotLocked()
	m.mu.Unlock()
	if stateErr != nil {
		m.logger.Warn("update monitor state could not be saved", "error", stateErr)
	}
	return result, err
}

func (m *Monitor) cachedResult() *CheckResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snapshot.Result == nil {
		return nil
	}
	copy := *m.snapshot.Result
	return &copy
}

// Rollback switches back to the retained previous release or discards a
// pending update. A restart is still required when the result says so.
func (m *Monitor) Rollback(ctx context.Context) (*RollbackResult, error) {
	if m == nil || !m.Enabled() {
		return nil, ErrUpdateNotConfigured
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	result, err := m.manager.Rollback(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.snapshot.Staged = nil
	m.snapshot.Result = nil
	stateErr := m.saveSnapshotLocked()
	m.mu.Unlock()
	if stateErr != nil {
		m.logger.Warn("update monitor state could not be saved", "error", stateErr)
	}
	return result, nil
}

// Dismiss hides the current available release for a bounded period. A forced
// release can be postponed for at most 24 hours.
func (m *Monitor) Dismiss(release string, duration time.Duration) error {
	if m == nil || !m.Enabled() {
		return ErrUpdateNotConfigured
	}
	release = strings.TrimSpace(release)
	if release == "" || len(release) > 64 {
		return errors.New("invalid release")
	}
	if duration <= 0 {
		duration = DefaultDismissDuration
	}
	max := MaxDismissDuration
	if level := m.availableLevel(); level == LevelForced {
		max = MaxForcedDismissDuration
	}
	if duration > max {
		return fmt.Errorf("%w: %s maximum", ErrDismissalTooLong, max)
	}
	until := m.now().UTC().Add(duration).Truncate(time.Millisecond)
	m.mu.Lock()
	m.snapshot.DismissedRelease = release
	m.snapshot.DismissedUntil = &until
	err := m.saveSnapshotLocked()
	m.mu.Unlock()
	return err
}

// ClearDismissal makes a postponed release visible again.
func (m *Monitor) ClearDismissal() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.snapshot.DismissedRelease = ""
	m.snapshot.DismissedUntil = nil
	err := m.saveSnapshotLocked()
	m.mu.Unlock()
	return err
}

// Restart launches the configured local restart command after a short delay
// so the HTTP response can be flushed. The command must point at the managed
// current binary (for example, a systemd unit).
func (m *Monitor) Restart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || len(m.restart) == 0 {
		return ErrNoRestartCommand
	}
	if !restartSupported() {
		return errors.New("automatic restart is not supported on this platform")
	}
	return launchRestart(m.restart, 2*time.Second, filepath.Join(m.homeDir(), "update-restart.log"))
}

// View returns a copy of the monitor state for the local API.
func (m *Monitor) View() MonitorStatus {
	if m == nil {
		return MonitorStatus{Mode: config.UpdateModeOff, Channel: "stable"}
	}
	m.mu.RLock()
	snapshot := m.snapshot
	m.mu.RUnlock()
	view := MonitorStatus{
		Enabled:           m.Enabled(),
		Mode:              m.mode,
		Channel:           m.channel,
		Interval:          m.interval.String(),
		LastCheck:         snapshot.LastCheck,
		LastError:         snapshot.LastError,
		Result:            snapshot.Result,
		Staged:            snapshot.Staged,
		RestartConfigured: len(m.restart) > 0,
		RestartSupported:  restartSupported(),
	}
	if m.configErr != nil {
		view.ConfigError = m.configErr.Error()
	}
	if m.manager != nil {
		if status, err := m.manager.Status(); err == nil {
			view.Status = status
		}
		if current, managed, err := m.manager.Current(); err == nil && managed {
			view.Current = current
		}
	}
	if view.Current.App == "" {
		info := version.Current()
		view.Current = Current{App: info.App, Protocol: info.Protocol, Schema: info.Schema}
	}
	view.Dismissed = m.dismissed(snapshot, view.Result)
	if view.Dismissed && snapshot.DismissedUntil != nil {
		until := *snapshot.DismissedUntil
		view.DismissedUntil = &until
	}
	return view
}

func (m *Monitor) availableLevel() string {
	result := m.cachedResult()
	if result == nil || result.Manifest == nil {
		return ""
	}
	return result.Manifest.Level
}

func (m *Monitor) dismissed(snapshot monitorSnapshot, result *CheckResult) bool {
	if snapshot.DismissedRelease == "" || snapshot.DismissedUntil == nil {
		return false
	}
	if !m.now().UTC().Before(*snapshot.DismissedUntil) {
		return false
	}
	if result == nil || result.Manifest == nil || !result.Available {
		return false
	}
	return snapshot.DismissedRelease == result.Manifest.Release
}

func (m *Monitor) isDismissedLocked(result *CheckResult) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dismissed(m.snapshot, result)
}

func (m *Monitor) loadSnapshot() error {
	data, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		m.snapshot = monitorSnapshot{Format: MonitorFormat, Version: MonitorVersion}
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > maxMonitorStateBytes {
		return errors.New("update monitor state is too large")
	}
	var snapshot monitorSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if snapshot.Format != MonitorFormat || snapshot.Version != MonitorVersion {
		return fmt.Errorf("unsupported update monitor state %q v%d", snapshot.Format, snapshot.Version)
	}
	m.snapshot = snapshot
	return nil
}

func (m *Monitor) saveSnapshotLocked() error {
	// Keep the format/version current even if an older file was loaded.
	m.snapshot.Format = MonitorFormat
	m.snapshot.Version = MonitorVersion
	data, err := json.MarshalIndent(m.snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxMonitorStateBytes {
		return errors.New("update monitor state is too large")
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.statePath), ".toget-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.statePath); err != nil {
		return err
	}
	cleanup = false
	return nil
}
