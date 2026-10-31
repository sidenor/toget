package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func validPendingState() State {
	state, err := newState()
	if err != nil {
		panic(err)
	}
	state.Current = "1.0.0"
	state.CurrentProtocol = 1
	state.CurrentSchema = 9
	state.CurrentManifestSHA256 = strings.Repeat("a", 64)
	state.Pending = &Pending{
		Release:                "1.1.0",
		ManifestSHA256:         strings.Repeat("b", 64),
		Protocol:               1,
		Schema:                 9,
		Channel:                "stable",
		StartedAt:              formatTime(time.Now()),
		Previous:               "1.0.0",
		PreviousManifestSHA256: strings.Repeat("a", 64),
		PreviousProtocol:       1,
		PreviousSchema:         9,
	}
	return state
}

func TestValidateState(t *testing.T) {
	base := func() State {
		state, err := newState()
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	cases := []struct {
		name    string
		mutate  func(*State)
		wantErr string
	}{
		{name: "empty valid", mutate: func(*State) {}},
		{name: "wrong format", mutate: func(s *State) { s.Format = "other" }, wantErr: "unsupported"},
		{name: "wrong version", mutate: func(s *State) { s.Version = 2 }, wantErr: "unsupported"},
		{name: "bad rollout id", mutate: func(s *State) { s.RolloutID = "short" }, wantErr: "rollout id"},
		{name: "rollout id characters", mutate: func(s *State) { s.RolloutID = strings.Repeat("!", 16) }, wantErr: "rollout id"},
		{name: "unclean current", mutate: func(s *State) {
			s.Current = "v1.0.0"
			s.CurrentProtocol = 1
			s.CurrentSchema = 9
		}, wantErr: "current release"},
		{name: "build metadata current", mutate: func(s *State) {
			s.Current = "1.0.0+build"
			s.CurrentProtocol = 1
			s.CurrentSchema = 9
		}, wantErr: "current release"},
		{name: "current equals previous", mutate: func(s *State) {
			s.Current = "1.0.0"
			s.Previous = "1.0.0"
			s.CurrentProtocol = 1
			s.CurrentSchema = 9
			s.PreviousProtocol = 1
			s.PreviousSchema = 9
		}, wantErr: "same"},
		{name: "current metadata without release", mutate: func(s *State) { s.CurrentProtocol = 1 }, wantErr: "without a current release"},
		{name: "previous metadata without release", mutate: func(s *State) { s.PreviousSchema = 9 }, wantErr: "without a previous release"},
		{name: "current protocol out of range", mutate: func(s *State) {
			s.Current = "1.0.0"
			s.CurrentProtocol = 1001
			s.CurrentSchema = 9
		}, wantErr: "protocol"},
		{name: "current schema out of range", mutate: func(s *State) {
			s.Current = "1.0.0"
			s.CurrentProtocol = 1
			s.CurrentSchema = 100001
		}, wantErr: "schema"},
		{name: "bad current manifest hash", mutate: func(s *State) {
			s.Current = "1.0.0"
			s.CurrentProtocol = 1
			s.CurrentSchema = 9
			s.CurrentManifestSHA256 = strings.Repeat("A", 64)
		}, wantErr: "lowercase"},
		{name: "pending release already current", mutate: func(s *State) {
			s.Current = "1.1.0"
			s.CurrentProtocol = 1
			s.CurrentSchema = 9
			s.CurrentManifestSHA256 = strings.Repeat("a", 64)
			s.Pending = &Pending{Release: "1.1.0"}
		}, wantErr: "already current"},
		{name: "pending release uncanonical", mutate: func(s *State) {
			state := validPendingState()
			*s = state
			s.Pending.Release = "v1.1.0"
		}, wantErr: "pending release"},
		{name: "pending hash empty", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.ManifestSHA256 = ""
		}, wantErr: "pending manifest"},
		{name: "pending channel invalid", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.Channel = "Stable!"
		}, wantErr: "channel"},
		{name: "pending time invalid", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.StartedAt = "yesterday"
		}, wantErr: "started_at"},
		{name: "pending previous mismatch", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.Previous = "1.2.0"
		}, wantErr: "does not match current"},
		{name: "pending previous hash mismatch", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.PreviousManifestSHA256 = strings.Repeat("c", 64)
		}, wantErr: "pending previous manifest"},
		{name: "pending previous protocol mismatch", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.PreviousProtocol = 2
		}, wantErr: "protocol or schema"},
		{name: "pending protocol zero", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.Protocol = 0
		}, wantErr: "protocol and schema"},
		{name: "pending schema zero", mutate: func(s *State) {
			*s = validPendingState()
			s.Pending.Schema = 0
		}, wantErr: "protocol and schema"},
		{name: "valid pending", mutate: func(s *State) { *s = validPendingState() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := base()
			tc.mutate(&state)
			err := validateState(state)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateState() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateState() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestStateFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, stateFileName)
	state := validPendingState()
	if err := writeStateFile(path, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 0600", info.Mode().Perm())
	}
	loaded, exists, err := readStateFile(path)
	if err != nil || !exists {
		t.Fatalf("readStateFile() = exists %v, err %v", exists, err)
	}
	if loaded.Current != state.Current || loaded.RolloutID != state.RolloutID || loaded.Pending == nil {
		t.Fatalf("loaded state = %+v", loaded)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := decodeStrict(data, &generic); err != nil {
		t.Fatal(err)
	}
	generic["unknown_field"] = true
	if err := os.WriteFile(path, mustJSON(t, generic), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStateFile(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}

	if err := os.WriteFile(path, append(mustJSON(t, state), []byte(" {}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStateFile(path); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing data error = %v", err)
	}

	if err := os.WriteFile(path, make([]byte, MaxStateBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStateFile(path); err == nil {
		t.Fatal("oversized state was accepted")
	}

	if _, exists, err := readStateFile(filepath.Join(dir, "missing.json")); err != nil || exists {
		t.Fatalf("missing state = exists %v, err %v", exists, err)
	}
}

func validInstalledRecord() InstalledRelease {
	return InstalledRelease{
		Format:           FormatInstalled,
		Version:          CurrentVersion,
		Release:          "1.0.0",
		Protocol:         1,
		Schema:           9,
		Executable:       "toget",
		ExecutableSHA256: strings.Repeat("a", 64),
		ManifestSHA256:   strings.Repeat("b", 64),
		ArchiveSHA256:    strings.Repeat("c", 64),
		Channel:          "stable",
		InstalledAt:      formatTime(time.Now()),
	}
}

func TestValidateInstalled(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*InstalledRelease)
		wantErr string
	}{
		{name: "valid", mutate: func(*InstalledRelease) {}},
		{name: "wrong format", mutate: func(r *InstalledRelease) { r.Format = "other" }, wantErr: "unsupported"},
		{name: "wrong version", mutate: func(r *InstalledRelease) { r.Version = 2 }, wantErr: "unsupported"},
		{name: "release empty", mutate: func(r *InstalledRelease) { r.Release = "" }, wantErr: "installed release"},
		{name: "release uncanonical", mutate: func(r *InstalledRelease) { r.Release = "1.0.0+build" }, wantErr: "installed release"},
		{name: "executable empty", mutate: func(r *InstalledRelease) { r.Executable = "" }, wantErr: "executable"},
		{name: "executable absolute", mutate: func(r *InstalledRelease) { r.Executable = "/toget" }, wantErr: "executable"},
		{name: "executable traversal", mutate: func(r *InstalledRelease) { r.Executable = "bin/../toget" }, wantErr: "executable"},
		{name: "executable backslash", mutate: func(r *InstalledRelease) { r.Executable = `bin\toget` }, wantErr: "executable"},
		{name: "executable hash", mutate: func(r *InstalledRelease) { r.ExecutableSHA256 = "short" }, wantErr: "executable sha256"},
		{name: "manifest hash", mutate: func(r *InstalledRelease) { r.ManifestSHA256 = "short" }, wantErr: "manifest sha256"},
		{name: "archive hash", mutate: func(r *InstalledRelease) { r.ArchiveSHA256 = strings.Repeat("C", 64) }, wantErr: "archive sha256"},
		{name: "channel", mutate: func(r *InstalledRelease) { r.Channel = "Stable!" }, wantErr: "channel"},
		{name: "installed at", mutate: func(r *InstalledRelease) { r.InstalledAt = "now" }, wantErr: "installed_at"},
		{name: "protocol zero", mutate: func(r *InstalledRelease) { r.Protocol = 0 }, wantErr: "protocol and schema"},
		{name: "schema zero", mutate: func(r *InstalledRelease) { r.Schema = 0 }, wantErr: "protocol and schema"},
		{name: "protocol too large", mutate: func(r *InstalledRelease) { r.Protocol = 1001 }, wantErr: "protocol"},
		{name: "schema too large", mutate: func(r *InstalledRelease) { r.Schema = 100001 }, wantErr: "schema"},
		{name: "adopted without hashes", mutate: func(r *InstalledRelease) {
			r.Adopted = true
			r.ManifestSHA256 = ""
			r.ArchiveSHA256 = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := validInstalledRecord()
			tc.mutate(&record)
			err := validateInstalled(record)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateInstalled() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateInstalled() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestInstalledFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, installedFileName)
	record := validInstalledRecord()
	if err := writeInstalled(dir, record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("installed metadata mode = %o, want 0600", info.Mode().Perm())
	}
	loaded, found, err := readInstalled(dir)
	if err != nil || !found {
		t.Fatalf("readInstalled() = found %v, err %v", found, err)
	}
	if loaded.Release != record.Release || loaded.ExecutableSHA256 != record.ExecutableSHA256 {
		t.Fatalf("loaded record = %+v", loaded)
	}
	if _, found, err := readInstalled(filepath.Join(dir, "missing")); err != nil || found {
		t.Fatalf("missing record = found %v, err %v", found, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readInstalled(dir); err == nil {
		t.Fatal("trailing installed metadata was accepted")
	}
}

func TestVersionDirectoryNames(t *testing.T) {
	if got := versionDirectoryName("1.2.3"); got != "v1.2.3" {
		t.Fatalf("versionDirectoryName = %q", got)
	}
	for _, name := range []string{"v1.2.3", "v0.0.1", "v10.20.30"} {
		release, ok := releaseFromDirectoryName(name)
		if !ok || "v"+release != name {
			t.Errorf("releaseFromDirectoryName(%q) = %q, %v", name, release, ok)
		}
	}
	for _, name := range []string{"1.2.3", "v", "v1.2", "v1.2.3+build", "v01.2.3", "v1.2.3-beta+build"} {
		if _, ok := releaseFromDirectoryName(name); ok {
			t.Errorf("releaseFromDirectoryName(%q) unexpectedly succeeded", name)
		}
	}
	if _, err := versionDirAbs(t.TempDir(), "1.2.3+build"); err == nil {
		t.Fatal("versionDirAbs accepted build metadata")
	}
}
