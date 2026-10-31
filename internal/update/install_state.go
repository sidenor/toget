package update

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	// MaxStateBytes bounds the local update state file.
	MaxStateBytes = 64 << 10
	// MaxInstalledBytes bounds one installed-release record.
	MaxInstalledBytes = 64 << 10
	installedFileName = ".toget-release.json"
	stateFileName     = "state.json"
	lockFileName      = ".update.lock"
)

var rolloutIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// Pending records an update that has been switched into place but not yet
// confirmed by a restart and health check.
type Pending struct {
	Release                string `json:"release"`
	ManifestSHA256         string `json:"manifest_sha256"`
	Protocol               int    `json:"protocol"`
	Schema                 int    `json:"schema"`
	Channel                string `json:"channel,omitempty"`
	StartedAt              string `json:"started_at"`
	Previous               string `json:"previous,omitempty"`
	PreviousManifestSHA256 string `json:"previous_manifest_sha256,omitempty"`
	PreviousProtocol       int    `json:"previous_protocol,omitempty"`
	PreviousSchema         int    `json:"previous_schema,omitempty"`
}

// State is the durable managed-install state. The current and previous
// symlinks mirror it; the state file is authoritative.
type State struct {
	Format                 string   `json:"format"`
	Version                int      `json:"version"`
	RolloutID              string   `json:"rollout_id"`
	Current                string   `json:"current,omitempty"`
	CurrentManifestSHA256  string   `json:"current_manifest_sha256,omitempty"`
	CurrentProtocol        int      `json:"current_protocol,omitempty"`
	CurrentSchema          int      `json:"current_schema,omitempty"`
	Previous               string   `json:"previous,omitempty"`
	PreviousManifestSHA256 string   `json:"previous_manifest_sha256,omitempty"`
	PreviousProtocol       int      `json:"previous_protocol,omitempty"`
	PreviousSchema         int      `json:"previous_schema,omitempty"`
	Pending                *Pending `json:"pending,omitempty"`
}

// InstalledRelease is the metadata stored inside each versions/v<release>
// directory.
type InstalledRelease struct {
	Format           string `json:"format"`
	Version          int    `json:"version"`
	Release          string `json:"release"`
	Protocol         int    `json:"protocol,omitempty"`
	Schema           int    `json:"schema,omitempty"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
	ManifestSHA256   string `json:"manifest_sha256,omitempty"`
	ArchiveSHA256    string `json:"archive_sha256,omitempty"`
	Channel          string `json:"channel,omitempty"`
	InstalledAt      string `json:"installed_at"`
	Adopted          bool   `json:"adopted,omitempty"`
}

func newRolloutID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate rollout id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newState() (State, error) {
	id, err := newRolloutID()
	if err != nil {
		return State{}, err
	}
	return State{Format: FormatState, Version: CurrentVersion, RolloutID: id}, nil
}

func validateReleaseField(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if _, err := canonicalRelease(value); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

func validateOptionalRelease(field, value string) error {
	if value == "" {
		return nil
	}
	return validateReleaseField(field, value)
}

func validateOptionalSHA(field, value string) error {
	if value == "" {
		return nil
	}
	if err := validateSHA256(value); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

func validateProtocolSchema(protocol, schema int) error {
	if protocol < 0 || protocol > 1000 {
		return fmt.Errorf("protocol %d is out of range", protocol)
	}
	if schema < 0 || schema > 100000 {
		return fmt.Errorf("schema %d is out of range", schema)
	}
	return nil
}

func validateState(state State) error {
	if state.Format != FormatState || state.Version != CurrentVersion {
		return fmt.Errorf("unsupported update state format %q version %d", state.Format, state.Version)
	}
	if !rolloutIDPattern.MatchString(state.RolloutID) {
		return errors.New("update state has an invalid rollout id")
	}
	if err := validateOptionalRelease("current release", state.Current); err != nil {
		return err
	}
	if err := validateOptionalRelease("previous release", state.Previous); err != nil {
		return err
	}
	if state.Current != "" && state.Current == state.Previous {
		return errors.New("current and previous releases are the same")
	}
	if err := validateOptionalSHA("current manifest sha256", state.CurrentManifestSHA256); err != nil {
		return err
	}
	if err := validateOptionalSHA("previous manifest sha256", state.PreviousManifestSHA256); err != nil {
		return err
	}
	if err := validateProtocolSchema(state.CurrentProtocol, state.CurrentSchema); err != nil {
		return err
	}
	if err := validateProtocolSchema(state.PreviousProtocol, state.PreviousSchema); err != nil {
		return err
	}
	if state.Current == "" && (state.CurrentManifestSHA256 != "" || state.CurrentProtocol != 0 || state.CurrentSchema != 0) {
		return errors.New("update state has current metadata without a current release")
	}
	if state.Previous == "" && (state.PreviousManifestSHA256 != "" || state.PreviousProtocol != 0 || state.PreviousSchema != 0) {
		return errors.New("update state has previous metadata without a previous release")
	}
	pending := state.Pending
	if pending == nil {
		return nil
	}
	if err := validateReleaseField("pending release", pending.Release); err != nil {
		return err
	}
	if pending.Release == state.Current {
		return errors.New("pending release is already current")
	}
	if err := validateSHA256(pending.ManifestSHA256); err != nil {
		return fmt.Errorf("pending manifest sha256: %w", err)
	}
	if pending.Channel != "" && !channelNamePattern.MatchString(pending.Channel) {
		return fmt.Errorf("pending channel %q is invalid", pending.Channel)
	}
	if _, err := parseTime("pending started_at", pending.StartedAt); err != nil {
		return err
	}
	if err := validateOptionalRelease("pending previous release", pending.Previous); err != nil {
		return err
	}
	if pending.Previous != state.Current {
		return errors.New("pending previous release does not match current")
	}
	if err := validateOptionalSHA("pending previous manifest sha256", pending.PreviousManifestSHA256); err != nil {
		return err
	}
	if pending.PreviousManifestSHA256 != state.CurrentManifestSHA256 {
		return errors.New("pending previous manifest does not match current")
	}
	if pending.PreviousProtocol != state.CurrentProtocol || pending.PreviousSchema != state.CurrentSchema {
		return errors.New("pending previous protocol or schema does not match current")
	}
	if err := validateProtocolSchema(pending.Protocol, pending.Schema); err != nil {
		return fmt.Errorf("pending release: %w", err)
	}
	if pending.Protocol < 1 || pending.Schema < 1 {
		return errors.New("pending release must record a protocol and schema version")
	}
	return nil
}

func validateInstalled(record InstalledRelease) error {
	if record.Format != FormatInstalled || record.Version != CurrentVersion {
		return fmt.Errorf("unsupported installed release format %q version %d", record.Format, record.Version)
	}
	if err := validateReleaseField("installed release", record.Release); err != nil {
		return err
	}
	if err := validateExecutablePath(record.Executable); err != nil {
		return fmt.Errorf("installed executable: %w", err)
	}
	if err := validateSHA256(record.ExecutableSHA256); err != nil {
		return fmt.Errorf("installed executable sha256: %w", err)
	}
	if err := validateOptionalSHA("installed manifest sha256", record.ManifestSHA256); err != nil {
		return err
	}
	if err := validateOptionalSHA("installed archive sha256", record.ArchiveSHA256); err != nil {
		return err
	}
	if record.Channel != "" && !channelNamePattern.MatchString(record.Channel) {
		return fmt.Errorf("installed channel %q is invalid", record.Channel)
	}
	if _, err := parseTime("installed_at", record.InstalledAt); err != nil {
		return err
	}
	if err := validateProtocolSchema(record.Protocol, record.Schema); err != nil {
		return err
	}
	if record.Protocol < 1 || record.Schema < 1 {
		return errors.New("installed release must record a protocol and schema version")
	}
	return nil
}

func readStateFile(path string) (State, bool, error) {
	data, err := readLimitedFile(path, MaxStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var state State
	if err := decodeStrict(data, &state); err != nil {
		return State{}, false, fmt.Errorf("parse update state: %w", err)
	}
	if err := validateState(state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func writeStateFile(path string, state State) error {
	state.Format = FormatState
	state.Version = CurrentVersion
	if err := validateState(state); err != nil {
		return err
	}
	return writeDocumentAtomic(path, state, 0o600)
}

func readInstalled(dir string) (InstalledRelease, bool, error) {
	path := filepath.Join(dir, installedFileName)
	data, err := readLimitedFile(path, MaxInstalledBytes)
	if errors.Is(err, os.ErrNotExist) {
		return InstalledRelease{}, false, nil
	}
	if err != nil {
		return InstalledRelease{}, false, err
	}
	var record InstalledRelease
	if err := decodeStrict(data, &record); err != nil {
		return InstalledRelease{}, false, fmt.Errorf("parse installed release: %w", err)
	}
	if err := validateInstalled(record); err != nil {
		return InstalledRelease{}, false, err
	}
	return record, true, nil
}

func writeInstalled(dir string, record InstalledRelease) error {
	record.Format = FormatInstalled
	record.Version = CurrentVersion
	if err := validateInstalled(record); err != nil {
		return err
	}
	return writeDocumentAtomic(filepath.Join(dir, installedFileName), record, 0o600)
}

func writeDocumentAtomic(path string, value any, mode os.FileMode) error {
	data, err := marshalDocument(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".toget-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	syncDir(dir)
	return nil
}

func syncDir(path string) {
	dir, err := os.Open(path)
	if err != nil {
		return
	}
	_ = dir.Sync()
	_ = dir.Close()
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "toget.exe"
	}
	return "toget"
}

func versionDirectoryName(release string) string {
	return "v" + release
}

func releaseFromDirectoryName(name string) (string, bool) {
	if !strings.HasPrefix(name, "v") {
		return "", false
	}
	release := strings.TrimPrefix(name, "v")
	if _, err := canonicalRelease(release); err != nil {
		return "", false
	}
	return release, true
}
