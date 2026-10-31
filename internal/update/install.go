package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoPrevious means there is no release to roll back to.
var ErrNoPrevious = errors.New("no previous release to roll back to")

// ErrNoPending means no update is waiting for a restart.
var ErrNoPending = errors.New("no update is pending")

// Manager operates a managed installation rooted at Root.
type Manager struct {
	Root string
	// Client downloads artifacts. It is only used by Apply.
	Client *Client
	Now    func() time.Time

	// CurrentExecutable is the binary running the update command. It is
	// adopted into the managed layout on the first update so that the first
	// update can be rolled back.
	CurrentExecutable string
	// ExecutableName is the name used inside a managed version directory. It
	// defaults to toget (toget.exe on Windows).
	ExecutableName string

	// RestartCommand is a local command, never a signed value. When it is
	// empty, an update is staged and marked pending until the operator
	// restarts Toget.
	RestartCommand []string
	// RestartTimeout bounds the restart command. It defaults to two minutes.
	RestartTimeout time.Duration
	// RestartHook overrides RestartCommand (used by embedding and tests).
	RestartHook func(context.Context) error

	// HealthCommand is run against the installed binary. It defaults to
	// --json version.
	HealthCommand []string
	// HealthTimeout bounds one health check and URL retries. It defaults to
	// 30 seconds.
	HealthTimeout time.Duration
	// HealthURL is an optional endpoint that must return 200 after a restart.
	HealthURL string
	// HealthCheck overrides the default binary health check.
	HealthCheck func(context.Context, string, HealthExpectation) error
	HTTP        *http.Client
}

// HealthExpectation describes the release a newly started binary must report.
type HealthExpectation struct {
	Release  string
	Protocol int
	Schema   int
}

// ApplyResult describes a completed or staged update.
type ApplyResult struct {
	Release      string `json:"release"`
	Previous     string `json:"previous,omitempty"`
	Executable   string `json:"executable,omitempty"`
	State        State  `json:"state"`
	UpToDate     bool   `json:"up_to_date,omitempty"`
	Restarted    bool   `json:"restarted,omitempty"`
	NeedsRestart bool   `json:"needs_restart,omitempty"`
	Warning      string `json:"warning,omitempty"`
}

// RollbackResult describes a completed or staged rollback.
type RollbackResult struct {
	Release      string `json:"release,omitempty"`
	Previous     string `json:"previous,omitempty"`
	State        State  `json:"state"`
	Restarted    bool   `json:"restarted,omitempty"`
	NeedsRestart bool   `json:"needs_restart,omitempty"`
}

// InstalledVersion is one managed version directory.
type InstalledVersion struct {
	InstalledRelease
	Path string `json:"path"`
}

// Status is a read-only view of a managed installation.
type Status struct {
	Root         string             `json:"root"`
	Initialized  bool               `json:"initialized"`
	State        State              `json:"state"`
	CurrentLink  string             `json:"current_link,omitempty"`
	PreviousLink string             `json:"previous_link,omitempty"`
	Versions     []InstalledVersion `json:"versions,omitempty"`
	Issues       []string           `json:"issues,omitempty"`
}

// ReconcileResult describes what happened to a pending update at startup.
type ReconcileResult struct {
	Action   string `json:"action"`
	Release  string `json:"release,omitempty"`
	Previous string `json:"previous,omitempty"`
}

const (
	// ReconcileNone means there was nothing pending.
	ReconcileNone = "none"
	// ReconcileFinalized means the running release was confirmed.
	ReconcileFinalized = "finalized"
	// ReconcileRolledBack means a pending update was discarded because the
	// running binary did not match it.
	ReconcileRolledBack = "rolled_back"
	// ReconcileRepaired means no update was pending, but the current/previous
	// links did not match the authoritative state and were repaired.
	ReconcileRepaired = "repaired"
)

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) executableName() string {
	if m.ExecutableName != "" {
		return m.ExecutableName
	}
	return executableName()
}

func (m *Manager) loadState(root string, create bool) (State, bool, error) {
	state, exists, err := readStateFile(filepath.Join(root, stateFileName))
	if err != nil {
		return State{}, false, err
	}
	if exists || !create {
		return state, exists, nil
	}
	state, err = newState()
	if err != nil {
		return State{}, false, err
	}
	if err := writeStateFile(filepath.Join(root, stateFileName), state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func (m *Manager) saveState(root string, state State) error {
	return writeStateFile(filepath.Join(root, stateFileName), state)
}

// Apply downloads, verifies, installs, switches, and optionally restarts into
// the release selected by Check. If a previous update is pending, Apply
// finishes it instead of starting a new one.
func (m *Manager) Apply(ctx context.Context, result *CheckResult) (*ApplyResult, error) {
	if m.Client == nil {
		return nil, errors.New("update client is not configured")
	}
	root, _, err := m.prepareRoot(true)
	if err != nil {
		return nil, err
	}
	unlock, err := m.acquireLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()

	state, _, err := m.loadState(root, true)
	if err != nil {
		return nil, err
	}
	if err := checkRootLinks(root, state, state.Pending != nil); err != nil {
		return nil, err
	}
	if state.Pending != nil {
		if result != nil && result.Manifest != nil && result.Manifest.Release != state.Pending.Release {
			return nil, fmt.Errorf("update %s is pending; finish or roll it back before applying %s", state.Pending.Release, result.Manifest.Release)
		}
		return m.finishPendingLocked(ctx, root, state)
	}
	if result != nil && result.Manifest != nil && state.Current != "" && result.Manifest.Release == state.Current {
		if _, err := verifyVersionDir(root, state.Current, state.CurrentManifestSHA256, ""); err != nil {
			return nil, err
		}
		if state.CurrentManifestSHA256 != "" && result.ManifestSHA256 != "" && state.CurrentManifestSHA256 != result.ManifestSHA256 {
			return nil, fmt.Errorf("release %s was republished with different contents", state.Current)
		}
		dir, _ := versionDirAbs(root, state.Current)
		record, _, err := readInstalled(dir)
		if err != nil {
			return nil, err
		}
		return &ApplyResult{
			Release:    state.Current,
			Previous:   state.Previous,
			Executable: filepath.Join(dir, filepath.FromSlash(record.Executable)),
			State:      state,
			UpToDate:   true,
		}, nil
	}
	if err := validateApplyResult(result); err != nil {
		return nil, err
	}
	release, _ := canonicalRelease(result.Manifest.Release)
	warning := ""
	if state.Current == "" {
		if m.CurrentExecutable == "" {
			warning = "the running binary was not adopted; this update has no rollback target"
		} else {
			oldState := state
			if err := m.adoptRunningBinary(ctx, root, &state, result.Current); err != nil {
				return nil, err
			}
			// Save the adopted state before switching links. A crash in this
			// window is repaired at startup from state.json.
			if err := m.saveState(root, state); err != nil {
				return nil, err
			}
			if err := switchLinks(root, state.Current, ""); err != nil {
				restoreErr := m.restoreState(root, oldState)
				if restoreErr != nil {
					return nil, fmt.Errorf("link adopted release: %w (restore state: %v)", err, restoreErr)
				}
				return nil, err
			}
		}
	}
	if result.Manifest.Schema > result.Current.Schema {
		if warning != "" {
			warning += " "
		}
		warning += "this release upgrades the database schema; automatic rollback restores the previous binary but not the database; run `toget backup` before restarting"
	}
	if state.Current != "" {
		if _, err := verifyVersionDir(root, state.Current, state.CurrentManifestSHA256, ""); err != nil {
			return nil, err
		}
		currentVersion, _ := canonicalRelease(state.Current)
		switch currentVersion.Compare(release) {
		case 0:
			if state.CurrentManifestSHA256 != "" && state.CurrentManifestSHA256 != result.ManifestSHA256 {
				return nil, fmt.Errorf("release %s was republished with different contents", state.Current)
			}
			dir, _ := versionDirAbs(root, state.Current)
			record, _, err := readInstalled(dir)
			if err != nil {
				return nil, err
			}
			return &ApplyResult{
				Release:    state.Current,
				Previous:   state.Previous,
				Executable: filepath.Join(dir, filepath.FromSlash(record.Executable)),
				State:      state,
				UpToDate:   true,
				Warning:    warning,
			}, nil
		case 1:
			if !result.Downgrade {
				return nil, fmt.Errorf("installed release %s is newer than %s", state.Current, release.String())
			}
		case -1:
			if result.Downgrade {
				return nil, fmt.Errorf("release %s is not a downgrade from %s", release.String(), state.Current)
			}
		}
	}

	versionDir, record, err := m.installRelease(ctx, root, result)
	if err != nil {
		return nil, err
	}
	oldState := state
	oldCurrent := state.Current
	pending := &Pending{
		Release:                release.String(),
		ManifestSHA256:         result.ManifestSHA256,
		Protocol:               result.Manifest.Protocol,
		Schema:                 result.Manifest.Schema,
		StartedAt:              formatTime(m.now()),
		Previous:               oldCurrent,
		PreviousManifestSHA256: oldState.CurrentManifestSHA256,
		PreviousProtocol:       oldState.CurrentProtocol,
		PreviousSchema:         oldState.CurrentSchema,
	}
	if result.Channel != nil {
		pending.Channel = result.Channel.Channel
	}
	state.Pending = pending
	if err := m.saveState(root, state); err != nil {
		return nil, err
	}
	if err := switchLinks(root, release.String(), oldCurrent); err != nil {
		restoreErr := m.restoreState(root, oldState)
		if restoreErr != nil {
			return nil, fmt.Errorf("switch release: %w (restore state: %v)", err, restoreErr)
		}
		return nil, err
	}
	executable := filepath.Join(versionDir, filepath.FromSlash(record.Executable))
	if !m.restartConfigured() {
		return &ApplyResult{
			Release:      release.String(),
			Previous:     oldCurrent,
			Executable:   executable,
			State:        state,
			NeedsRestart: true,
			Warning:      warning,
		}, nil
	}
	expectation := HealthExpectation{Release: release.String(), Protocol: result.Manifest.Protocol, Schema: result.Manifest.Schema}
	if err := m.restartAndVerify(ctx, executable, expectation); err != nil {
		base := oldState
		base.Pending = nil
		rollbackErr := m.abortPending(ctx, root, base)
		if rollbackErr != nil {
			return nil, fmt.Errorf("new release failed health check: %w; automatic rollback failed: %v", err, rollbackErr)
		}
		return nil, fmt.Errorf("new release failed to start: %w; rolled back to %s", err, describeRelease(base.Current))
	}
	final := oldState
	final.Current = release.String()
	final.CurrentProtocol = result.Manifest.Protocol
	final.CurrentSchema = result.Manifest.Schema
	final.CurrentManifestSHA256 = result.ManifestSHA256
	final.Previous = oldCurrent
	final.PreviousProtocol = oldState.CurrentProtocol
	final.PreviousSchema = oldState.CurrentSchema
	final.PreviousManifestSHA256 = oldState.CurrentManifestSHA256
	final.Pending = nil
	if err := m.saveState(root, final); err != nil {
		return nil, err
	}
	return &ApplyResult{
		Release:    release.String(),
		Previous:   oldCurrent,
		Executable: executable,
		State:      final,
		Restarted:  true,
		Warning:    warning,
	}, nil
}

// FinishPending completes an update that was already switched into place.
func (m *Manager) FinishPending(ctx context.Context) (*ApplyResult, error) {
	root, _, err := m.prepareRoot(true)
	if err != nil {
		return nil, err
	}
	unlock, err := m.acquireLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, exists, err := m.loadState(root, false)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNoPending
	}
	if state.Pending == nil {
		return nil, ErrNoPending
	}
	return m.finishPendingLocked(ctx, root, state)
}

func (m *Manager) finishPendingLocked(ctx context.Context, root string, state State) (*ApplyResult, error) {
	pending := state.Pending
	if pending == nil {
		return nil, ErrNoPending
	}
	if err := checkRootLinks(root, state, true); err != nil {
		return nil, err
	}
	ok, err := linkMatches(root, "current", pending.Release)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("pending release %s is not linked as current", pending.Release)
	}
	if pending.Previous == "" {
		_, _, exists, err := readRootLink(root, "previous")
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New("pending update has a previous symlink but no previous release")
		}
	} else {
		ok, err := linkMatches(root, "previous", pending.Previous)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("pending update previous link does not point at %s", pending.Previous)
		}
	}
	record, err := verifyVersionDir(root, pending.Release, pending.ManifestSHA256, "")
	if err != nil {
		return nil, err
	}
	dir, _ := versionDirAbs(root, pending.Release)
	executable := filepath.Join(dir, filepath.FromSlash(record.Executable))
	if !m.restartConfigured() {
		return &ApplyResult{
			Release:      pending.Release,
			Previous:     pending.Previous,
			Executable:   executable,
			State:        state,
			NeedsRestart: true,
		}, nil
	}
	expectation := HealthExpectation{Release: pending.Release, Protocol: pending.Protocol, Schema: pending.Schema}
	if err := m.restartAndVerify(ctx, executable, expectation); err != nil {
		base := state
		base.Pending = nil
		rollbackErr := m.abortPending(ctx, root, base)
		if rollbackErr != nil {
			return nil, fmt.Errorf("pending release failed health check: %w; automatic rollback failed: %v", err, rollbackErr)
		}
		return nil, fmt.Errorf("pending release failed to start: %w; rolled back to %s", err, describeRelease(base.Current))
	}
	final := state
	final.Current = pending.Release
	final.CurrentProtocol = pending.Protocol
	final.CurrentSchema = pending.Schema
	final.CurrentManifestSHA256 = pending.ManifestSHA256
	final.Previous = pending.Previous
	final.PreviousProtocol = pending.PreviousProtocol
	final.PreviousSchema = pending.PreviousSchema
	final.PreviousManifestSHA256 = pending.PreviousManifestSHA256
	final.Pending = nil
	if err := m.saveState(root, final); err != nil {
		return nil, err
	}
	return &ApplyResult{
		Release:    pending.Release,
		Previous:   pending.Previous,
		Executable: executable,
		State:      final,
		Restarted:  true,
	}, nil
}

// Rollback switches back to the previous release, or discards a pending one.
func (m *Manager) Rollback(ctx context.Context) (*RollbackResult, error) {
	root, _, err := m.prepareRoot(true)
	if err != nil {
		return nil, err
	}
	unlock, err := m.acquireLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, _, err := m.loadState(root, true)
	if err != nil {
		return nil, err
	}
	if state.Pending != nil {
		if err := checkRootLinks(root, state, true); err != nil {
			return nil, err
		}
		base := state
		base.Pending = nil
		if base.Current != "" {
			if _, err := verifyVersionDir(root, base.Current, base.CurrentManifestSHA256, ""); err != nil {
				return nil, err
			}
		}
		if err := m.saveState(root, base); err != nil {
			return nil, err
		}
		if err := switchLinks(root, base.Current, base.Previous); err != nil {
			restoreErr := m.restoreState(root, state)
			if restoreErr != nil {
				return nil, fmt.Errorf("discard pending update: %w (restore state: %v)", err, restoreErr)
			}
			return nil, err
		}
		restarted, err := m.restartRollback(ctx, root, base.Current, base.CurrentProtocol, base.CurrentSchema)
		if err != nil {
			return nil, err
		}
		return &RollbackResult{
			Release:      base.Current,
			Previous:     base.Previous,
			State:        base,
			Restarted:    restarted,
			NeedsRestart: !restarted,
		}, nil
	}
	if state.Previous == "" {
		return nil, ErrNoPrevious
	}
	if err := checkRootLinks(root, state, false); err != nil {
		return nil, err
	}
	target := state.Previous
	if _, err := verifyVersionDir(root, target, state.PreviousManifestSHA256, ""); err != nil {
		return nil, err
	}
	oldCurrent := state.Current
	if oldCurrent != "" {
		if _, err := verifyVersionDir(root, oldCurrent, state.CurrentManifestSHA256, ""); err != nil {
			return nil, err
		}
	}
	next := state
	next.Current = target
	next.CurrentProtocol = state.PreviousProtocol
	next.CurrentSchema = state.PreviousSchema
	next.CurrentManifestSHA256 = state.PreviousManifestSHA256
	next.Previous = oldCurrent
	next.PreviousProtocol = state.CurrentProtocol
	next.PreviousSchema = state.CurrentSchema
	next.PreviousManifestSHA256 = state.CurrentManifestSHA256
	next.Pending = nil
	if err := m.saveState(root, next); err != nil {
		return nil, err
	}
	if err := switchLinks(root, next.Current, next.Previous); err != nil {
		restoreErr := m.restoreState(root, state)
		if restoreErr != nil {
			return nil, fmt.Errorf("switch to previous release: %w (restore state: %v)", err, restoreErr)
		}
		return nil, err
	}
	restarted, err := m.restartRollback(ctx, root, next.Current, next.CurrentProtocol, next.CurrentSchema)
	if err != nil {
		return nil, err
	}
	return &RollbackResult{
		Release:      next.Current,
		Previous:     next.Previous,
		State:        next,
		Restarted:    restarted,
		NeedsRestart: !restarted,
	}, nil
}

func (m *Manager) restartRollback(ctx context.Context, root, release string, protocol, schema int) (bool, error) {
	if !m.restartConfigured() {
		return false, nil
	}
	if release == "" {
		if err := m.runRestart(ctx); err != nil {
			return false, fmt.Errorf("restart after rollback: %w", err)
		}
		if err := m.waitHealthURL(ctx); err != nil {
			return false, fmt.Errorf("health after rollback: %w", err)
		}
		return true, nil
	}
	dir, err := versionDirAbs(root, release)
	if err != nil {
		return false, err
	}
	record, err := verifyVersionDir(root, release, "", "")
	if err != nil {
		return false, err
	}
	executable := filepath.Join(dir, filepath.FromSlash(record.Executable))
	if err := m.restartAndVerify(ctx, executable, HealthExpectation{Release: release, Protocol: protocol, Schema: schema}); err != nil {
		return false, fmt.Errorf("restart after rollback: %w", err)
	}
	return true, nil
}

// ReconcileOnStartup resolves a pending update using the version of the binary
// that actually started. It is intended to run once at daemon startup.
func (m *Manager) ReconcileOnStartup(current Current) (*ReconcileResult, error) {
	root, exists, err := m.prepareRoot(false)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &ReconcileResult{Action: ReconcileNone}, nil
	}
	unlock, err := m.acquireLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, stateExists, err := m.loadState(root, false)
	if err != nil {
		return nil, err
	}
	if !stateExists {
		return &ReconcileResult{Action: ReconcileNone}, nil
	}
	if state.Pending == nil {
		return m.reconcileIdleLocked(root, state)
	}
	pending := state.Pending
	if current.App == pending.Release {
		ok, err := linkMatches(root, "current", pending.Release)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("running release %s is pending but the current link does not match", pending.Release)
		}
		if pending.Previous == "" {
			_, _, previousExists, err := readRootLink(root, "previous")
			if err != nil {
				return nil, err
			}
			if previousExists {
				return nil, errors.New("pending update has a previous link but no previous release")
			}
		} else {
			ok, err := linkMatches(root, "previous", pending.Previous)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("pending previous link does not match %s", pending.Previous)
			}
		}
		if _, err := verifyVersionDir(root, pending.Release, pending.ManifestSHA256, ""); err != nil {
			return nil, err
		}
		state.Current = pending.Release
		state.CurrentProtocol = pending.Protocol
		state.CurrentSchema = pending.Schema
		state.CurrentManifestSHA256 = pending.ManifestSHA256
		state.Previous = pending.Previous
		state.PreviousProtocol = pending.PreviousProtocol
		state.PreviousSchema = pending.PreviousSchema
		state.PreviousManifestSHA256 = pending.PreviousManifestSHA256
		state.Pending = nil
		if err := m.saveState(root, state); err != nil {
			return nil, err
		}
		return &ReconcileResult{Action: ReconcileFinalized, Release: state.Current, Previous: state.Previous}, nil
	}
	if current.App != state.Current {
		return nil, fmt.Errorf("running version %s is neither the current release %s nor the pending release %s", current.App, state.Current, pending.Release)
	}
	if state.Current != "" {
		if _, err := verifyVersionDir(root, state.Current, state.CurrentManifestSHA256, ""); err != nil {
			return nil, err
		}
	}
	if err := switchLinks(root, state.Current, state.Previous); err != nil {
		return nil, err
	}
	state.Pending = nil
	if err := m.saveState(root, state); err != nil {
		return nil, err
	}
	return &ReconcileResult{Action: ReconcileRolledBack, Release: state.Current, Previous: state.Previous}, nil
}

// reconcileIdleLocked repairs links left inconsistent by a process crash
// between saving state and switching links. The state file is authoritative:
// rollback, adoption, and abort paths save state first, so completing the
// saved transition is the safe recovery.
func (m *Manager) reconcileIdleLocked(root string, state State) (*ReconcileResult, error) {
	if state.Current == "" {
		_, _, currentExists, err := readRootLink(root, "current")
		if err != nil {
			return nil, err
		}
		_, _, previousExists, err := readRootLink(root, "previous")
		if err != nil {
			return nil, err
		}
		if !currentExists && !previousExists {
			return &ReconcileResult{Action: ReconcileNone}, nil
		}
		if err := switchLinks(root, "", ""); err != nil {
			return nil, fmt.Errorf("remove orphaned release links: %w", err)
		}
		return &ReconcileResult{Action: ReconcileRepaired}, nil
	}
	if _, err := verifyVersionDir(root, state.Current, state.CurrentManifestSHA256, ""); err != nil {
		return nil, err
	}
	if state.Previous != "" {
		if _, err := verifyVersionDir(root, state.Previous, state.PreviousManifestSHA256, ""); err != nil {
			return nil, err
		}
	}
	if err := checkRootLinks(root, state, false); err == nil {
		return &ReconcileResult{Action: ReconcileNone}, nil
	}
	if err := switchLinks(root, state.Current, state.Previous); err != nil {
		return nil, fmt.Errorf("repair release links: %w", err)
	}
	return &ReconcileResult{Action: ReconcileRepaired, Release: state.Current, Previous: state.Previous}, nil
}

// Current returns the managed current release. It does not create anything.
func (m *Manager) Current() (Current, bool, error) {
	root, exists, err := m.prepareRoot(false)
	if err != nil {
		return Current{}, false, err
	}
	if !exists {
		return Current{}, false, nil
	}
	state, stateExists, err := m.loadState(root, false)
	if err != nil {
		return Current{}, false, err
	}
	if !stateExists {
		return Current{}, false, nil
	}
	return installedCurrent(root, state)
}

// Status reports the managed installation without modifying it.
func (m *Manager) Status() (*Status, error) {
	root, exists, err := m.prepareRoot(false)
	if err != nil {
		return nil, err
	}
	status := &Status{Root: root}
	if !exists {
		return status, nil
	}
	state, initialized, err := m.loadState(root, false)
	if err != nil {
		return nil, err
	}
	status.Initialized = initialized
	status.State = state
	if raw, _, linkExists, err := readRootLink(root, "current"); err != nil {
		status.Issues = append(status.Issues, err.Error())
	} else if linkExists {
		status.CurrentLink = raw
	}
	if raw, _, linkExists, err := readRootLink(root, "previous"); err != nil {
		status.Issues = append(status.Issues, err.Error())
	} else if linkExists {
		status.PreviousLink = raw
	}
	if initialized {
		if err := checkRootLinks(root, state, true); err != nil {
			status.Issues = append(status.Issues, err.Error())
		}
		if state.Current != "" {
			if _, err := verifyVersionDir(root, state.Current, state.CurrentManifestSHA256, ""); err != nil {
				status.Issues = append(status.Issues, err.Error())
			}
		}
		if state.Previous != "" {
			if _, err := verifyVersionDir(root, state.Previous, state.PreviousManifestSHA256, ""); err != nil {
				status.Issues = append(status.Issues, err.Error())
			}
		}
		if state.Pending != nil {
			if _, err := verifyVersionDir(root, state.Pending.Release, state.Pending.ManifestSHA256, ""); err != nil {
				status.Issues = append(status.Issues, err.Error())
			}
		}
	}
	versions, issues := listInstalledVersions(root)
	status.Versions = versions
	status.Issues = append(status.Issues, issues...)
	return status, nil
}

func (m *Manager) installRelease(ctx context.Context, root string, result *CheckResult) (string, InstalledRelease, error) {
	release, err := canonicalRelease(result.Manifest.Release)
	if err != nil {
		return "", InstalledRelease{}, err
	}
	dir, err := versionDirAbs(root, release.String())
	if err != nil {
		return "", InstalledRelease{}, err
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", InstalledRelease{}, fmt.Errorf("%s is not a release directory", dir)
		}
		record, found, err := readInstalled(dir)
		if err != nil {
			return "", InstalledRelease{}, err
		}
		if !found {
			return "", InstalledRelease{}, fmt.Errorf("release %s already exists without installation metadata", release.String())
		}
		if record.Adopted || record.ManifestSHA256 != result.ManifestSHA256 || record.ArchiveSHA256 != result.Artifact.SHA256 {
			return "", InstalledRelease{}, fmt.Errorf("release directory %s already exists with different contents", dir)
		}
		if _, err := verifyVersionDir(root, release.String(), result.ManifestSHA256, result.Artifact.SHA256); err != nil {
			return "", InstalledRelease{}, err
		}
		return dir, record, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", InstalledRelease{}, err
	}

	tmpDir := filepath.Join(root, "tmp")
	archivePath := filepath.Join(tmpDir, "release-"+randomSuffix()+".archive")
	defer os.Remove(archivePath)
	if _, err := m.Client.DownloadArtifact(ctx, result, archivePath); err != nil {
		return "", InstalledRelease{}, err
	}
	staging := filepath.Join(tmpDir, "stage-"+randomSuffix())
	executablePath, err := ExtractArchive(archivePath, result.Artifact.Format, staging, result.Artifact.Executable)
	if err != nil {
		return "", InstalledRelease{}, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()
	sum, size, err := hashFile(executablePath)
	if err != nil {
		return "", InstalledRelease{}, err
	}
	if size <= 0 {
		return "", InstalledRelease{}, errors.New("release executable is empty")
	}
	record := InstalledRelease{
		Release:          release.String(),
		Protocol:         result.Manifest.Protocol,
		Schema:           result.Manifest.Schema,
		Executable:       result.Artifact.Executable,
		ExecutableSHA256: sum,
		ManifestSHA256:   result.ManifestSHA256,
		ArchiveSHA256:    result.Artifact.SHA256,
		InstalledAt:      formatTime(m.now()),
	}
	if result.Channel != nil {
		record.Channel = result.Channel.Channel
	}
	if err := writeInstalled(staging, record); err != nil {
		return "", InstalledRelease{}, err
	}
	expectation := HealthExpectation{Release: release.String(), Protocol: result.Manifest.Protocol, Schema: result.Manifest.Schema}
	if err := m.healthCheck(ctx, executablePath, expectation); err != nil {
		return "", InstalledRelease{}, fmt.Errorf("release executable failed its health check: %w", err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", InstalledRelease{}, fmt.Errorf("install release: %w", err)
	}
	installed = true
	syncDir(filepath.Join(root, "versions"))
	return dir, record, nil
}

func (m *Manager) adoptRunningBinary(ctx context.Context, root string, state *State, current Current) error {
	if state.Current != "" {
		return errors.New("cannot adopt a running binary over a managed current release")
	}
	if current.App == "" {
		return errors.New("current app version is required to adopt the running binary")
	}
	release, err := canonicalRelease(current.App)
	if err != nil {
		return fmt.Errorf("current app version: %w", err)
	}
	if current.Protocol < 1 || current.Schema < 1 {
		return errors.New("current protocol and schema versions are required to adopt the running binary")
	}
	if err := validateOptionalSHA("current manifest sha256", current.ManifestSHA256); err != nil {
		return err
	}
	dir, err := versionDirAbs(root, release.String())
	if err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a release directory", dir)
		}
		record, err := verifyVersionDir(root, release.String(), current.ManifestSHA256, "")
		if err != nil {
			return err
		}
		if record.Release != release.String() {
			return errors.New("adopted release directory has the wrong release")
		}
		if record.Protocol != current.Protocol || record.Schema != current.Schema {
			return errors.New("adopted release directory has different protocol or schema versions")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if m.CurrentExecutable == "" {
			return errors.New("current executable path is required to adopt the running binary")
		}
		staging := filepath.Join(root, "tmp", "adopt-"+randomSuffix())
		if err := os.Mkdir(staging, 0o755); err != nil {
			return err
		}
		installed := false
		defer func() {
			if !installed {
				_ = os.RemoveAll(staging)
			}
		}()
		name := m.executableName()
		if err := validateExecutablePath(name); err != nil {
			return err
		}
		target := filepath.Join(staging, filepath.FromSlash(name))
		sum, err := copyExecutable(m.CurrentExecutable, target)
		if err != nil {
			return fmt.Errorf("adopt running binary: %w", err)
		}
		record := InstalledRelease{
			Release:          release.String(),
			Protocol:         current.Protocol,
			Schema:           current.Schema,
			Executable:       name,
			ExecutableSHA256: sum,
			ManifestSHA256:   current.ManifestSHA256,
			InstalledAt:      formatTime(m.now()),
			Adopted:          true,
		}
		if err := writeInstalled(staging, record); err != nil {
			return err
		}
		expectation := HealthExpectation{Release: release.String(), Protocol: current.Protocol, Schema: current.Schema}
		if err := m.healthCheck(ctx, target, expectation); err != nil {
			return fmt.Errorf("adopted binary failed its health check: %w", err)
		}
		if err := os.Rename(staging, dir); err != nil {
			return fmt.Errorf("adopt running binary: %w", err)
		}
		installed = true
		syncDir(filepath.Join(root, "versions"))
	}
	state.Current = release.String()
	state.CurrentProtocol = current.Protocol
	state.CurrentSchema = current.Schema
	state.CurrentManifestSHA256 = current.ManifestSHA256
	state.Previous = ""
	state.PreviousProtocol = 0
	state.PreviousSchema = 0
	state.PreviousManifestSHA256 = ""
	return nil
}

func copyExecutable(source, dest string) (string, error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("current executable is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > MaxArtifactBytes {
		return "", errors.New("current executable has an invalid size")
	}
	src, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer src.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(src, MaxArtifactBytes+1))
	if copyErr != nil {
		out.Close()
		return "", copyErr
	}
	if written != info.Size() || written > MaxArtifactBytes {
		out.Close()
		return "", errors.New("current executable changed while it was being copied")
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (m *Manager) restoreState(root string, state State) error {
	if err := m.saveState(root, state); err != nil {
		return err
	}
	return switchLinks(root, state.Current, state.Previous)
}

func (m *Manager) abortPending(ctx context.Context, root string, base State) error {
	if err := m.saveState(root, base); err != nil {
		return err
	}
	if err := switchLinks(root, base.Current, base.Previous); err != nil {
		return err
	}
	if !m.restartConfigured() {
		return nil
	}
	if err := m.runRestart(ctx); err != nil {
		return fmt.Errorf("restart previous release: %w", err)
	}
	if base.Current != "" {
		if _, err := verifyVersionDir(root, base.Current, base.CurrentManifestSHA256, ""); err != nil {
			return err
		}
		dir, _ := versionDirAbs(root, base.Current)
		record, _, err := readInstalled(dir)
		if err != nil {
			return err
		}
		executable := filepath.Join(dir, filepath.FromSlash(record.Executable))
		expectation := HealthExpectation{Release: base.Current, Protocol: base.CurrentProtocol, Schema: base.CurrentSchema}
		if err := m.healthCheck(ctx, executable, expectation); err != nil {
			return fmt.Errorf("health of restored release: %w", err)
		}
	}
	if err := m.waitHealthURL(ctx); err != nil {
		return fmt.Errorf("health of restored release: %w", err)
	}
	return nil
}

func validateApplyResult(result *CheckResult) error {
	if result == nil {
		return errors.New("update check result is required")
	}
	if !result.Available {
		return fmt.Errorf("release is not available: %s", result.Reason)
	}
	if result.Manifest == nil || result.Artifact == nil {
		return errors.New("update check result has no manifest or artifact")
	}
	release, err := canonicalRelease(result.Manifest.Release)
	if err != nil {
		return err
	}
	if result.Channel != nil && result.Channel.Release != release.String() {
		return errors.New("channel and manifest releases differ")
	}
	if err := validateSHA256(result.ManifestSHA256); err != nil {
		return fmt.Errorf("manifest sha256: %w", err)
	}
	if result.Manifest.Protocol < 1 || result.Manifest.Protocol > 1000 {
		return errors.New("manifest protocol is out of range")
	}
	if result.Manifest.Schema < 1 || result.Manifest.Schema > 100000 {
		return errors.New("manifest schema is out of range")
	}
	if err := validateArtifact(*result.Artifact); err != nil {
		return err
	}
	if result.Artifact.OS != runtime.GOOS || result.Artifact.Arch != runtime.GOARCH {
		return fmt.Errorf("artifact is for %s/%s, not %s/%s", result.Artifact.OS, result.Artifact.Arch, runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

func describeRelease(release string) string {
	if release == "" {
		return "an unmanaged installation"
	}
	return release
}

func (m *Manager) restartConfigured() bool {
	return m.RestartHook != nil || len(m.RestartCommand) > 0
}

func (m *Manager) restartAndVerify(ctx context.Context, executable string, want HealthExpectation) error {
	if err := m.runRestart(ctx); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	if err := m.healthCheck(ctx, executable, want); err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	if err := m.waitHealthURL(ctx); err != nil {
		return fmt.Errorf("health URL: %w", err)
	}
	return nil
}

func (m *Manager) runRestart(ctx context.Context) error {
	if m.RestartHook != nil {
		return m.RestartHook(ctx)
	}
	if len(m.RestartCommand) == 0 {
		return errors.New("restart command is not configured")
	}
	timeout := m.RestartTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, m.RestartCommand[0], m.RestartCommand[1:]...)
	output, err := command.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("restart command timed out: %w", runCtx.Err())
		}
		return fmt.Errorf("restart command failed: %w: %s", err, truncateOutput(output))
	}
	return nil
}

func (m *Manager) healthCheck(ctx context.Context, executable string, want HealthExpectation) error {
	if m.HealthCheck != nil {
		return m.HealthCheck(ctx, executable, want)
	}
	args := m.HealthCommand
	if len(args) == 0 {
		args = []string{"--json", "version"}
	}
	timeout := m.HealthTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, executable, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("version check timed out: %w", runCtx.Err())
		}
		return fmt.Errorf("version check failed: %w: %s", err, truncateOutput(output))
	}
	var info struct {
		App      string `json:"app"`
		Protocol int    `json:"protocol"`
		Schema   int    `json:"schema"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return fmt.Errorf("version check returned invalid JSON: %w", err)
	}
	if info.App != want.Release {
		return fmt.Errorf("binary reports version %q, expected %q", info.App, want.Release)
	}
	if want.Protocol > 0 && info.Protocol != want.Protocol {
		return fmt.Errorf("binary reports protocol %d, expected %d", info.Protocol, want.Protocol)
	}
	if want.Schema > 0 && info.Schema != want.Schema {
		return fmt.Errorf("binary reports schema %d, expected %d", info.Schema, want.Schema)
	}
	if info.OS != "" && info.OS != runtime.GOOS {
		return fmt.Errorf("binary reports os %q, expected %q", info.OS, runtime.GOOS)
	}
	if info.Arch != "" && info.Arch != runtime.GOARCH {
		return fmt.Errorf("binary reports arch %q, expected %q", info.Arch, runtime.GOARCH)
	}
	return nil
}

func (m *Manager) waitHealthURL(ctx context.Context) error {
	if strings.TrimSpace(m.HealthURL) == "" {
		return nil
	}
	parsed, err := url.Parse(m.HealthURL)
	if err != nil {
		return err
	}
	if parsed.User != nil {
		return errors.New("health URL must not contain user information")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return errors.New("health URL must use HTTPS or loopback HTTP")
	}
	timeout := m.HealthTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	var last string
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			last = response.Status
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			if last == "" {
				last = "no response"
			}
			return fmt.Errorf("health endpoint did not become ready: %s", last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func truncateOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 4096 {
		text = text[:4096] + "…"
	}
	if text == "" {
		return "(no output)"
	}
	return text
}
