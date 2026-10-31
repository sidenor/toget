package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func requireExecutableScripts(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install tests use shell-script executables")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh is unavailable: %v", err)
	}
}

// publish creates an independent release tree signed by the same keys. It is
// used to move the channel forward while keeping the original server alive.
func (r *releaseServer) publish(t *testing.T, release string, binary []byte) *releaseServer {
	t.Helper()
	dir := t.TempDir()
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	published := buildTestRelease(t, dir, release, server.URL, r.root, r.key, r.cert, binary)
	return &releaseServer{
		server:   server,
		dir:      dir,
		release:  published,
		root:     r.root,
		key:      r.key,
		cert:     r.cert,
		verifier: r.verifier,
	}
}

func newInstallTestManager(t *testing.T, rs *releaseServer) (*Manager, string) {
	t.Helper()
	requireExecutableScripts(t)
	oldDir := t.TempDir()
	oldPath := filepath.Join(oldDir, "toget")
	if err := os.WriteFile(oldPath, fakeBinary("1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		Root:              filepath.Join(t.TempDir(), "managed"),
		Client:            rs.client(),
		CurrentExecutable: oldPath,
		RestartTimeout:    10 * time.Second,
		HealthTimeout:     10 * time.Second,
	}
	return manager, oldPath
}

func checkInstalledRelease(t *testing.T, manager *Manager, app string) *CheckResult {
	t.Helper()
	result, err := manager.Client.Check(context.Background(), Current{App: app, Protocol: 1, Schema: 9}, "install-test-rollout-0123456789", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func validApplyResult(t *testing.T, rs *releaseServer) *CheckResult {
	t.Helper()
	result, err := rs.client().Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "install-test-rollout-0123456789", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func writeHealthExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "health-toget")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertRootLink(t *testing.T, root, name, want string) {
	t.Helper()
	raw, _, exists, err := readRootLink(root, name)
	if err != nil {
		t.Fatalf("read %s link: %v", name, err)
	}
	if want == "" {
		if exists {
			t.Fatalf("%s link exists and points at %s, want no link", name, raw)
		}
		return
	}
	if !exists || raw != want {
		t.Fatalf("%s link = %q, exists %v; want %q", name, raw, exists, want)
	}
}

func mustManagedCurrent(t *testing.T, manager *Manager) Current {
	t.Helper()
	current, ok, err := manager.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("managed installation has no current release")
	}
	return current
}

func TestManagerApplyFinishRollback(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)

	if _, ok, err := manager.Current(); err != nil || ok {
		t.Fatalf("fresh installation current = ok %v, err %v", ok, err)
	}
	result := checkInstalledRelease(t, manager, "1.0.0")
	applied, err := manager.Apply(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Release != "1.1.0" || applied.Previous != "1.0.0" {
		t.Fatalf("applied = %+v", applied)
	}
	if !applied.NeedsRestart || applied.Restarted || applied.UpToDate {
		t.Fatalf("staged result = %+v", applied)
	}
	if applied.State.Pending == nil || applied.State.Pending.Release != "1.1.0" {
		t.Fatalf("pending state = %+v", applied.State.Pending)
	}

	current := mustManagedCurrent(t, manager)
	if current.App != "1.0.0" || current.Protocol != 1 || current.Schema != 9 {
		t.Fatalf("current before finish = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.1.0")
	assertRootLink(t, manager.Root, "previous", "versions/v1.0.0")

	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || status.State.Pending == nil || len(status.Versions) != 2 {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Issues) != 0 {
		t.Fatalf("status issues = %v", status.Issues)
	}
	if _, err := manager.Apply(context.Background(), &CheckResult{Manifest: &Manifest{Release: "9.9.9"}}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("apply of a different release while pending error = %v", err)
	}

	again, err := manager.FinishPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !again.NeedsRestart || again.Restarted {
		t.Fatalf("finish without restart = %+v", again)
	}
	if status, _ := manager.Status(); status.State.Pending == nil {
		t.Fatal("finish without a restart cleared the pending marker")
	}

	restarts := 0
	manager.RestartHook = func(context.Context) error {
		restarts++
		return nil
	}
	finished, err := manager.FinishPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Restarted || restarts != 1 {
		t.Fatalf("finished = %+v, restarts = %d", finished, restarts)
	}
	if finished.Release != "1.1.0" || finished.Previous != "1.0.0" || finished.State.Pending != nil {
		t.Fatalf("finished state = %+v", finished)
	}
	current = mustManagedCurrent(t, manager)
	if current.App != "1.1.0" || current.ManifestSHA256 == "" {
		t.Fatalf("current after finish = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.1.0")
	assertRootLink(t, manager.Root, "previous", "versions/v1.0.0")

	upToDate := checkInstalledRelease(t, manager, "1.1.0")
	if upToDate.Available || upToDate.Reason != ReasonUpToDate {
		t.Fatalf("up-to-date check = %+v", upToDate)
	}
	noop, err := manager.Apply(context.Background(), upToDate)
	if err != nil {
		t.Fatal(err)
	}
	if !noop.UpToDate || noop.Release != "1.1.0" || noop.Restarted || restarts != 1 {
		t.Fatalf("up-to-date apply = %+v, restarts = %d", noop, restarts)
	}

	if _, err := manager.FinishPending(context.Background()); !errors.Is(err, ErrNoPending) {
		t.Fatalf("second finish error = %v", err)
	}

	rolled, err := manager.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Release != "1.0.0" || rolled.Previous != "1.1.0" || !rolled.Restarted || restarts != 2 {
		t.Fatalf("rollback = %+v, restarts = %d", rolled, restarts)
	}
	current = mustManagedCurrent(t, manager)
	if current.App != "1.0.0" {
		t.Fatalf("current after rollback = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.0.0")
	assertRootLink(t, manager.Root, "previous", "versions/v1.1.0")

	// Applying the rolled-back release again reuses the installed directory.
	result = checkInstalledRelease(t, manager, "1.0.0")
	reapplied, err := manager.Apply(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if reapplied.Release != "1.1.0" || !reapplied.Restarted || restarts != 3 {
		t.Fatalf("reapplied = %+v, restarts = %d", reapplied, restarts)
	}
	if versions, issues := listInstalledVersions(manager.Root); len(versions) != 2 || len(issues) != 0 {
		t.Fatalf("versions after reuse = %+v, issues = %v", versions, issues)
	}
}

func TestManagerRollbackPending(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	manager.RestartHook = func(context.Context) error {
		restarts++
		return nil
	}
	rolled, err := manager.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Release != "1.0.0" || !rolled.Restarted || rolled.NeedsRestart || restarts != 1 {
		t.Fatalf("pending rollback = %+v, restarts = %d", rolled, restarts)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State.Pending != nil {
		t.Fatal("pending rollback left a pending marker")
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.0.0")
	assertRootLink(t, manager.Root, "previous", "")
	if _, err := manager.FinishPending(context.Background()); !errors.Is(err, ErrNoPending) {
		t.Fatalf("finish after pending rollback error = %v", err)
	}
	if _, err := manager.Rollback(context.Background()); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("rollback without previous error = %v", err)
	}
}

func TestManagerAutomaticRollbackOnHealthFailure(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	manager.HealthCheck = func(_ context.Context, executable string, want HealthExpectation) error {
		inStaging := strings.Contains(executable, filepath.Join("tmp", "stage-")) ||
			strings.Contains(executable, filepath.Join("tmp", "adopt-"))
		if want.Release == "1.1.0" && !inStaging {
			return errors.New("simulated startup failure")
		}
		return nil
	}
	restarts := 0
	manager.RestartHook = func(context.Context) error {
		restarts++
		return nil
	}
	result := checkInstalledRelease(t, manager, "1.0.0")
	_, err := manager.Apply(context.Background(), result)
	if err == nil {
		t.Fatal("failing release was applied")
	}
	if !strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("apply error = %v", err)
	}
	if restarts != 2 {
		t.Fatalf("restarts = %d, want restart and rollback", restarts)
	}
	current := mustManagedCurrent(t, manager)
	if current.App != "1.0.0" {
		t.Fatalf("current after automatic rollback = %+v", current)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State.Pending != nil {
		t.Fatal("automatic rollback left a pending marker")
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.0.0")
	assertRootLink(t, manager.Root, "previous", "")

	// The failed release directory remains, but it is not selected.
	if versions, issues := listInstalledVersions(manager.Root); len(versions) != 2 || len(issues) != 0 {
		t.Fatalf("versions after failed update = %+v, issues = %v", versions, issues)
	}
}

func TestManagerHealthURL(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	var hits atomic.Int32
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	manager.HealthURL = health.URL
	restarts := 0
	manager.RestartHook = func(context.Context) error {
		restarts++
		return nil
	}
	result := checkInstalledRelease(t, manager, "1.0.0")
	applied, err := manager.Apply(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Restarted || applied.State.Current != "1.1.0" {
		t.Fatalf("applied = %+v", applied)
	}
	if hits.Load() == 0 {
		t.Fatal("health URL was not checked")
	}
	rolled, err := manager.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rolled.Restarted || rolled.Release != "1.0.0" {
		t.Fatalf("rollback = %+v", rolled)
	}
	if hits.Load() < 2 {
		t.Fatalf("health URL hits = %d, want both update and rollback", hits.Load())
	}
}

func TestManagerUnmanagedApply(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager := &Manager{
		Root:           filepath.Join(t.TempDir(), "managed"),
		Client:         rs.client(),
		RestartTimeout: 10 * time.Second,
		HealthTimeout:  10 * time.Second,
	}
	restarts := 0
	manager.RestartHook = func(context.Context) error {
		restarts++
		return nil
	}
	result := checkInstalledRelease(t, manager, "1.0.0")
	applied, err := manager.Apply(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Previous != "" || !strings.Contains(applied.Warning, "not adopted") {
		t.Fatalf("unmanaged apply = %+v", applied)
	}
	if applied.State.Current != "1.1.0" || applied.State.Previous != "" {
		t.Fatalf("unmanaged state = %+v", applied.State)
	}
	assertRootLink(t, manager.Root, "previous", "")
	if _, err := manager.Rollback(context.Background()); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("unmanaged rollback error = %v", err)
	}
	if restarts != 1 {
		t.Fatalf("restarts = %d", restarts)
	}
}

func TestManagerFinishPendingDetectsCorruption(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(manager.Root, "versions", "v1.1.0", "toget")
	original, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, fakeBinary("9.9.9"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager.RestartHook = func(context.Context) error { return nil }
	if _, err := manager.FinishPending(context.Background()); err == nil {
		t.Fatal("finish accepted a corrupted pending release")
	}
	if err := os.WriteFile(executable, original, 0o755); err != nil {
		t.Fatal(err)
	}
	finished, err := manager.FinishPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if finished.Release != "1.1.0" || finished.State.Current != "1.1.0" {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestManagerReusesReleaseAndDetectsMismatch(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	manager.RestartHook = func(context.Context) error { return nil }

	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The release directory from the first install is reused.
	result = checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(manager.Root, "versions", "v1.1.0")
	record, found, err := readInstalled(dir)
	if err != nil || !found {
		t.Fatalf("installed record = %+v, found %v, err %v", record, found, err)
	}
	record.ArchiveSHA256 = strings.Repeat("0", 64)
	if err := writeInstalled(dir, record); err != nil {
		t.Fatal(err)
	}
	result = checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("mismatched release directory error = %v", err)
	}
	if current := mustManagedCurrent(t, manager); current.App != "1.0.0" {
		t.Fatalf("current after refused update = %+v", current)
	}
}

func TestManagerReconcileOnStartup(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	reconciled, err := manager.ReconcileOnStartup(Current{App: "0.9.0", Protocol: 1, Schema: 9})
	if err == nil {
		t.Fatalf("unexpected version reconciled: %+v", reconciled)
	}
	if status, _ := manager.Status(); status.State.Pending == nil {
		t.Fatal("failed reconciliation cleared the pending marker")
	}
	reconciled, err = manager.ReconcileOnStartup(Current{App: "1.1.0", Protocol: 1, Schema: 9})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Action != ReconcileFinalized || reconciled.Release != "1.1.0" || reconciled.Previous != "1.0.0" {
		t.Fatalf("reconciled = %+v", reconciled)
	}
	if current := mustManagedCurrent(t, manager); current.App != "1.1.0" {
		t.Fatalf("current after reconciliation = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.1.0")
	assertRootLink(t, manager.Root, "previous", "versions/v1.0.0")

	next := rs.publish(t, "1.2.0", fakeBinary("1.2.0"))
	manager.Client = next.client()
	result = checkInstalledRelease(t, manager, "1.1.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.2.0")
	reconciled, err = manager.ReconcileOnStartup(Current{App: "1.1.0", Protocol: 1, Schema: 9})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Action != ReconcileRolledBack || reconciled.Release != "1.1.0" {
		t.Fatalf("rolled back = %+v", reconciled)
	}
	current := mustManagedCurrent(t, manager)
	if current.App != "1.1.0" {
		t.Fatalf("current after rollback reconciliation = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.1.0")
	assertRootLink(t, manager.Root, "previous", "versions/v1.0.0")
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State.Pending != nil || len(status.Issues) != 0 {
		t.Fatalf("status after reconciliation = %+v", status)
	}

	// ReconcileOnStartup is a no-op without a pending update and without a
	// managed installation.
	reconciled, err = manager.ReconcileOnStartup(Current{App: "1.1.0"})
	if err != nil || reconciled.Action != ReconcileNone {
		t.Fatalf("idle reconcile = %+v, err %v", reconciled, err)
	}
	fresh := &Manager{Root: filepath.Join(t.TempDir(), "missing")}
	reconciled, err = fresh.ReconcileOnStartup(Current{App: "1.1.0"})
	if err != nil || reconciled.Action != ReconcileNone {
		t.Fatalf("missing root reconcile = %+v, err %v", reconciled, err)
	}
}

func TestManagerReconcileRepairsIdleLinks(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	manager.RestartHook = func(context.Context) error { return nil }
	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State.Current != "1.1.0" || status.State.Previous != "1.0.0" || status.State.Pending != nil {
		t.Fatalf("installed state = %+v", status.State)
	}

	t.Run("missing links", func(t *testing.T) {
		root, _, err := manager.prepareRoot(false)
		if err != nil {
			t.Fatal(err)
		}
		if err := removeRootLink(root, "current"); err != nil {
			t.Fatal(err)
		}
		if err := removeRootLink(root, "previous"); err != nil {
			t.Fatal(err)
		}
		reconciled, err := manager.ReconcileOnStartup(Current{App: "1.1.0", Protocol: 1, Schema: 9})
		if err != nil {
			t.Fatal(err)
		}
		if reconciled.Action != ReconcileRepaired || reconciled.Release != "1.1.0" || reconciled.Previous != "1.0.0" {
			t.Fatalf("reconciled = %+v", reconciled)
		}
		assertRootLink(t, root, "current", "versions/v1.1.0")
		assertRootLink(t, root, "previous", "versions/v1.0.0")
	})

	t.Run("links ahead of state", func(t *testing.T) {
		root, _, err := manager.prepareRoot(false)
		if err != nil {
			t.Fatal(err)
		}
		if err := switchLinks(root, "1.0.0", "1.1.0"); err != nil {
			t.Fatal(err)
		}
		reconciled, err := manager.ReconcileOnStartup(Current{App: "1.0.0", Protocol: 1, Schema: 9})
		if err != nil {
			t.Fatal(err)
		}
		if reconciled.Action != ReconcileRepaired || reconciled.Release != "1.1.0" {
			t.Fatalf("reconciled = %+v", reconciled)
		}
		assertRootLink(t, root, "current", "versions/v1.1.0")
		assertRootLink(t, root, "previous", "versions/v1.0.0")
	})

	t.Run("state ahead of links", func(t *testing.T) {
		root, _, err := manager.prepareRoot(false)
		if err != nil {
			t.Fatal(err)
		}
		state, exists, err := manager.loadState(root, false)
		if err != nil || !exists {
			t.Fatalf("load state = %+v, exists %v, err %v", state, exists, err)
		}
		oldCurrent, oldPrevious := state.Current, state.Previous
		state.Current = oldPrevious
		state.CurrentProtocol = state.PreviousProtocol
		state.CurrentSchema = state.PreviousSchema
		state.CurrentManifestSHA256 = state.PreviousManifestSHA256
		state.Previous = oldCurrent
		state.PreviousProtocol = status.State.CurrentProtocol
		state.PreviousSchema = status.State.CurrentSchema
		state.PreviousManifestSHA256 = status.State.CurrentManifestSHA256
		if err := manager.saveState(root, state); err != nil {
			t.Fatal(err)
		}
		reconciled, err := manager.ReconcileOnStartup(Current{App: "1.1.0", Protocol: 1, Schema: 9})
		if err != nil {
			t.Fatal(err)
		}
		if reconciled.Action != ReconcileRepaired || reconciled.Release != "1.0.0" || reconciled.Previous != "1.1.0" {
			t.Fatalf("reconciled = %+v", reconciled)
		}
		assertRootLink(t, root, "current", "versions/v1.0.0")
		assertRootLink(t, root, "previous", "versions/v1.1.0")
	})
}

func TestManagerReconcileRemovesOrphanLinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	manager := &Manager{Root: root}
	managerRoot, _, err := manager.prepareRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(managerRoot, "versions", "v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := switchLinks(managerRoot, "1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	state, err := newState()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveState(managerRoot, state); err != nil {
		t.Fatal(err)
	}
	reconciled, err := manager.ReconcileOnStartup(Current{App: "1.0.0", Protocol: 1, Schema: 9})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Action != ReconcileRepaired {
		t.Fatalf("reconciled = %+v", reconciled)
	}
	assertRootLink(t, managerRoot, "current", "")
	assertRootLink(t, managerRoot, "previous", "")
}

func TestPrepareRootRejectsSymlinkedSubdirectories(t *testing.T) {
	for _, name := range []string{"versions", "tmp"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "managed")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			manager := &Manager{Root: root}
			if _, err := manager.Status(); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
				t.Fatalf("status error = %v", err)
			}
			if _, _, err := manager.prepareRoot(true); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
				t.Fatalf("prepareRoot error = %v", err)
			}
		})
	}
}

func TestManagerStateAndLinkSafety(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	t.Run("before initialization", func(t *testing.T) {
		manager := &Manager{Root: filepath.Join(t.TempDir(), "missing")}
		status, err := manager.Status()
		if err != nil {
			t.Fatal(err)
		}
		if status.Initialized || len(status.Versions) != 0 {
			t.Fatalf("status = %+v", status)
		}
		if _, ok, err := manager.Current(); err != nil || ok {
			t.Fatalf("current = ok %v, err %v", ok, err)
		}
		if _, err := manager.Rollback(context.Background()); err == nil {
			t.Fatal("rollback initialized a missing installation")
		}
	})

	t.Run("corrupt state", func(t *testing.T) {
		manager := &Manager{Root: filepath.Join(t.TempDir(), "managed"), Client: rs.client()}
		root, _, err := manager.prepareRoot(true)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, stateFileName), []byte("{not json"))
		if _, err := manager.Status(); err == nil {
			t.Fatal("status accepted corrupt state")
		}
		if _, _, err := manager.Current(); err == nil {
			t.Fatal("current accepted corrupt state")
		}
		if _, err := manager.Apply(context.Background(), nil); err == nil {
			t.Fatal("apply accepted corrupt state")
		}
	})

	t.Run("state without links", func(t *testing.T) {
		manager := &Manager{Root: filepath.Join(t.TempDir(), "managed"), Client: rs.client()}
		root, _, err := manager.prepareRoot(true)
		if err != nil {
			t.Fatal(err)
		}
		state, err := newState()
		if err != nil {
			t.Fatal(err)
		}
		state.Current = "1.0.0"
		state.CurrentProtocol = 1
		state.CurrentSchema = 9
		if err := manager.saveState(root, state); err != nil {
			t.Fatal(err)
		}
		status, err := manager.Status()
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Issues) == 0 {
			t.Fatal("status did not report missing links")
		}
		if _, err := manager.Apply(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "current symlink is missing") {
			t.Fatalf("apply error = %v", err)
		}
	})

	t.Run("link without state", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "manual")
		if err := os.MkdirAll(filepath.Join(root, "versions", "v1.0.0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("versions", "v1.0.0"), filepath.Join(root, "current")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		manager := &Manager{Root: root, Client: rs.client()}
		if _, err := manager.Apply(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "no matching release") {
			t.Fatalf("apply error = %v", err)
		}
	})

	t.Run("missing apply result", func(t *testing.T) {
		manager := &Manager{Root: filepath.Join(t.TempDir(), "managed"), Client: rs.client()}
		if _, err := manager.Apply(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "check result is required") {
			t.Fatalf("apply error = %v", err)
		}
	})

	t.Run("invalid roots", func(t *testing.T) {
		rootManager := &Manager{Root: string(os.PathSeparator)}
		if _, err := rootManager.Status(); err == nil {
			t.Fatal("filesystem root accepted")
		}
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, []byte("x"))
		fileManager := &Manager{Root: file}
		if _, err := fileManager.Status(); err == nil {
			t.Fatal("regular file accepted as an update root")
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		linkManager := &Manager{Root: link}
		if _, err := linkManager.Status(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink root error = %v", err)
		}
	})
}

func TestManagerLockContention(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	root, _, err := manager.prepareRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := manager.acquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := manager.Apply(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "another update") {
		t.Fatalf("concurrent apply error = %v", err)
	}
}

func TestManagerWarnsOnSchemaUpgrade(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinaryWithSchema("1.1.0", 10))
	manager, _ := newInstallTestManager(t, rs)
	result := checkInstalledRelease(t, manager, "1.0.0")
	result.Manifest.Schema = result.Current.Schema + 1
	applied, err := manager.Apply(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(applied.Warning, "database schema") {
		t.Fatalf("warning = %q, want a schema-upgrade warning", applied.Warning)
	}
}

func TestManagerApplyRestartFailureRollsBack(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	attempts := 0
	manager.RestartHook = func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("simulated restart failure")
		}
		return nil
	}
	result := checkInstalledRelease(t, manager, "1.0.0")
	_, err := manager.Apply(context.Background(), result)
	if err == nil || !strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("apply error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("restart attempts = %d, want restart and rollback", attempts)
	}
	current := mustManagedCurrent(t, manager)
	if current.App != "1.0.0" {
		t.Fatalf("current after restart failure = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.0.0")
	assertRootLink(t, manager.Root, "previous", "")
	if status, _ := manager.Status(); status.State.Pending != nil {
		t.Fatal("restart failure left a pending marker")
	}
}

func TestManagerFinishPendingRestartFailureRollsBack(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	manager.RestartHook = func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("simulated restart failure")
		}
		return nil
	}
	if _, err := manager.FinishPending(context.Background()); err == nil || !strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("finish error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("restart attempts = %d, want restart and rollback", attempts)
	}
	if current := mustManagedCurrent(t, manager); current.App != "1.0.0" {
		t.Fatalf("current after failed finish = %+v", current)
	}
	assertRootLink(t, manager.Root, "current", "versions/v1.0.0")
	assertRootLink(t, manager.Root, "previous", "")
}

func TestManagerAdoptionFailures(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	t.Run("missing executable", func(t *testing.T) {
		manager, _ := newInstallTestManager(t, rs)
		manager.CurrentExecutable = filepath.Join(t.TempDir(), "missing")
		result := checkInstalledRelease(t, manager, "1.0.0")
		if _, err := manager.Apply(context.Background(), result); err == nil || !strings.Contains(err.Error(), "adopt running binary") {
			t.Fatalf("apply error = %v", err)
		}
		assertRootLink(t, manager.Root, "current", "")
	})
	t.Run("missing current version", func(t *testing.T) {
		manager, _ := newInstallTestManager(t, rs)
		result := checkInstalledRelease(t, manager, "1.0.0")
		result.Current = Current{}
		if _, err := manager.Apply(context.Background(), result); err == nil || !strings.Contains(err.Error(), "current app version is required") {
			t.Fatalf("apply error = %v", err)
		}
	})
	t.Run("missing current protocol", func(t *testing.T) {
		manager, _ := newInstallTestManager(t, rs)
		result := checkInstalledRelease(t, manager, "1.0.0")
		result.Current = Current{App: "1.0.0", Schema: 9}
		if _, err := manager.Apply(context.Background(), result); err == nil || !strings.Contains(err.Error(), "protocol and schema versions") {
			t.Fatalf("apply error = %v", err)
		}
	})
	t.Run("unclean current version", func(t *testing.T) {
		manager, _ := newInstallTestManager(t, rs)
		result := checkInstalledRelease(t, manager, "1.0.0")
		result.Current = Current{App: "v1.0.0", Protocol: 1, Schema: 9}
		if _, err := manager.Apply(context.Background(), result); err == nil || !strings.Contains(err.Error(), "current app version") {
			t.Fatalf("apply error = %v", err)
		}
	})
}

func TestManagerRestartCommand(t *testing.T) {
	requireExecutableScripts(t)
	manager := &Manager{RestartTimeout: 2 * time.Second}
	if err := manager.runRestart(context.Background()); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("empty restart error = %v", err)
	}
	manager.RestartCommand = []string{"/bin/sh", "-c", "exit 0"}
	if err := manager.runRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.RestartCommand = []string{"/bin/sh", "-c", "echo restart-problem >&2; exit 3"}
	if err := manager.runRestart(context.Background()); err == nil || !strings.Contains(err.Error(), "restart-problem") {
		t.Fatalf("failing restart error = %v", err)
	}
	manager.RestartCommand = []string{filepath.Join(t.TempDir(), "missing")}
	if err := manager.runRestart(context.Background()); err == nil {
		t.Fatal("missing restart command was accepted")
	}
	manager.RestartCommand = []string{"/bin/sh", "-c", "exec sleep 5"}
	manager.RestartTimeout = 50 * time.Millisecond
	start := time.Now()
	if err := manager.runRestart(context.Background()); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timed-out restart error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("restart timeout took %v", elapsed)
	}
}

func TestManagerHealthCheck(t *testing.T) {
	requireExecutableScripts(t)
	want := HealthExpectation{Release: "1.1.0", Protocol: 1, Schema: 9}
	validJSON := fmt.Sprintf(`{"app":"1.1.0","protocol":1,"schema":9,"os":%q,"arch":%q}`, runtime.GOOS, runtime.GOARCH)
	cases := []struct {
		name    string
		body    string
		timeout time.Duration
		wantErr string
	}{
		{name: "valid", body: "echo '" + validJSON + "'"},
		{name: "no os or arch", body: `echo '{"app":"1.1.0","protocol":1,"schema":9}'`},
		{name: "wrong app", body: `echo '{"app":"9.9.9","protocol":1,"schema":9}'`, wantErr: "reports version"},
		{name: "wrong protocol", body: `echo '{"app":"1.1.0","protocol":2,"schema":9}'`, wantErr: "protocol"},
		{name: "wrong schema", body: `echo '{"app":"1.1.0","protocol":1,"schema":10}'`, wantErr: "schema"},
		{name: "wrong os", body: `echo '{"app":"1.1.0","protocol":1,"schema":9,"os":"plan9"}'`, wantErr: "os"},
		{name: "wrong arch", body: `echo '{"app":"1.1.0","protocol":1,"schema":9,"arch":"mips"}'`, wantErr: "arch"},
		{name: "invalid json", body: `echo 'not json'`, wantErr: "invalid JSON"},
		{name: "exit failure", body: "echo broken >&2; exit 4", wantErr: "version check failed"},
		{name: "timeout", body: "exec sleep 5", timeout: 50 * time.Millisecond, wantErr: "timed out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := &Manager{HealthTimeout: 2 * time.Second}
			if tc.timeout > 0 {
				manager.HealthTimeout = tc.timeout
			}
			executable := writeHealthExecutable(t, tc.body)
			err := manager.healthCheck(context.Background(), executable, want)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("healthCheck() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("healthCheck() = %v, want %q", err, tc.wantErr)
			}
		})
	}
	if err := (&Manager{}).healthCheck(context.Background(), filepath.Join(t.TempDir(), "missing"), want); err == nil {
		t.Fatal("missing binary passed a health check")
	}

	manager := &Manager{HealthTimeout: 2 * time.Second}
	executable := writeHealthExecutable(t, `case "$1" in --ready) echo '{"app":"1.1.0","protocol":1,"schema":9}';; *) exit 2;; esac`)
	manager.HealthCommand = []string{"--ready"}
	if err := manager.healthCheck(context.Background(), executable, want); err != nil {
		t.Fatalf("custom health command: %v", err)
	}
	manager.HealthCommand = nil
	if err := manager.healthCheck(context.Background(), executable, want); err == nil {
		t.Fatal("default health command unexpectedly succeeded")
	}
}

func TestValidateApplyResult(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	cases := []struct {
		name      string
		nilResult bool
		mutate    func(*CheckResult)
		wantErr   string
	}{
		{name: "nil", nilResult: true, wantErr: "required"},
		{name: "not available", mutate: func(r *CheckResult) { r.Available = false; r.Reason = "up_to_date" }, wantErr: "not available"},
		{name: "no manifest", mutate: func(r *CheckResult) { r.Manifest = nil }, wantErr: "no manifest"},
		{name: "no artifact", mutate: func(r *CheckResult) { r.Artifact = nil }, wantErr: "no manifest or artifact"},
		{name: "unclean release", mutate: func(r *CheckResult) { r.Manifest.Release = "v1.1.0" }, wantErr: "must not start with v"},
		{name: "channel mismatch", mutate: func(r *CheckResult) { r.Channel.Release = "9.9.9" }, wantErr: "channel and manifest"},
		{name: "bad manifest hash", mutate: func(r *CheckResult) { r.ManifestSHA256 = "short" }, wantErr: "manifest sha256"},
		{name: "protocol zero", mutate: func(r *CheckResult) { r.Manifest.Protocol = 0 }, wantErr: "protocol is out of range"},
		{name: "protocol too large", mutate: func(r *CheckResult) { r.Manifest.Protocol = 1001 }, wantErr: "protocol is out of range"},
		{name: "schema zero", mutate: func(r *CheckResult) { r.Manifest.Schema = 0 }, wantErr: "schema is out of range"},
		{name: "schema too large", mutate: func(r *CheckResult) { r.Manifest.Schema = 100001 }, wantErr: "schema is out of range"},
		{name: "wrong os", mutate: func(r *CheckResult) { r.Artifact.OS = "plan9" }, wantErr: "artifact is for"},
		{name: "wrong arch", mutate: func(r *CheckResult) { r.Artifact.Arch = "mips" }, wantErr: "artifact is for"},
		{name: "bad executable", mutate: func(r *CheckResult) { r.Artifact.Executable = "/toget" }, wantErr: "executable"},
		{name: "bad size", mutate: func(r *CheckResult) { r.Artifact.Size = 0 }, wantErr: "size"},
		{name: "bad format", mutate: func(r *CheckResult) { r.Artifact.Format = "rar" }, wantErr: "format"},
		{name: "valid", mutate: func(*CheckResult) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var result *CheckResult
			if !tc.nilResult {
				result = validApplyResult(t, rs)
				tc.mutate(result)
			}
			err := validateApplyResult(result)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateApplyResult() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateApplyResult() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestManagerStatusReportsCorruption(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	manager, _ := newInstallTestManager(t, rs)
	manager.RestartHook = func(context.Context) error { return nil }
	result := checkInstalledRelease(t, manager, "1.0.0")
	if _, err := manager.Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(manager.Root, "versions", "v1.0.0", installedFileName)); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Issues) == 0 {
		t.Fatal("status did not report missing installation metadata")
	}
	writeFile(t, filepath.Join(manager.Root, "versions", "v9.9.9"), []byte("not a directory"))
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range status.Issues {
		if strings.Contains(issue, "not a directory") {
			found = true
		}
	}
	if !found {
		t.Fatalf("status issues = %v", status.Issues)
	}
	if err := os.WriteFile(filepath.Join(manager.Root, "versions", "v1.1.0", "toget"), []byte("corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, issue := range status.Issues {
		if strings.Contains(issue, "does not match") {
			found = true
		}
	}
	if !found {
		t.Fatalf("status issues = %v", status.Issues)
	}
}

func TestSwitchLinksValidation(t *testing.T) {
	root := t.TempDir()
	for _, release := range []string{"1.0.0", "1.1.0"} {
		if err := os.MkdirAll(filepath.Join(root, "versions", "v"+release), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchLinks(root, "v1.0.0", ""); err == nil {
		t.Fatal("unclean release was accepted")
	}
	if err := switchLinks(root, "1.0.0", "1.2.0"); err == nil {
		t.Fatal("uninstalled release was accepted")
	}
	if err := switchLinks(root, "1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	assertRootLink(t, root, "current", "versions/v1.0.0")
	assertRootLink(t, root, "previous", "")
	if err := switchLinks(root, "1.1.0", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	assertRootLink(t, root, "current", "versions/v1.1.0")
	assertRootLink(t, root, "previous", "versions/v1.0.0")
	if err := switchLinks(root, "1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	assertRootLink(t, root, "previous", "")
	if err := switchLinks(root, "", ""); err != nil {
		t.Fatal(err)
	}
	assertRootLink(t, root, "current", "")
	assertRootLink(t, root, "previous", "")
}

func TestRemoveRootLinkRefusesRegularFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "current"), []byte("not a link"))
	if _, _, _, err := readRootLink(root, "current"); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("readRootLink() = %v", err)
	}
	if err := removeRootLink(root, "current"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("removeRootLink() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "current")); err != nil {
		t.Fatal("regular file was removed")
	}
}
