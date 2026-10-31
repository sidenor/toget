package update

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func randomSuffix() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(raw)
}

func (m *Manager) prepareRoot(create bool) (string, bool, error) {
	if strings.TrimSpace(m.Root) == "" {
		return "", false, errors.New("update root is not configured")
	}
	abs, err := filepath.Abs(m.Root)
	if err != nil {
		return "", false, err
	}
	abs = filepath.Clean(abs)
	volume := filepath.VolumeName(abs)
	if abs == volume+string(os.PathSeparator) {
		return "", false, errors.New("refusing to use a filesystem root as the update directory")
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return abs, false, nil
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", false, err
		}
		info, err = os.Lstat(abs)
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("update root must not be a symlink")
	}
	if !info.IsDir() {
		return "", false, errors.New("update root is not a directory")
	}
	for _, dir := range []struct {
		name string
		mode os.FileMode
	}{
		{"versions", 0o755},
		{"tmp", 0o700},
	} {
		path := filepath.Join(abs, dir.name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				continue
			}
			if err := os.Mkdir(path, dir.mode); err != nil && !errors.Is(err, os.ErrExist) {
				return "", false, err
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("update %s directory must not be a symlink", dir.name)
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("update %s path is not a directory", dir.name)
		}
	}
	return abs, true, nil
}

func (m *Manager) acquireLock(root string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(root, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockUpdateFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("another update is already running: %w", err)
	}
	return func() {
		_ = unlockUpdateFile(file)
		_ = file.Close()
	}, nil
}

func versionDirAbs(root, release string) (string, error) {
	if _, err := canonicalRelease(release); err != nil {
		return "", err
	}
	return filepath.Join(root, "versions", versionDirectoryName(release)), nil
}

func readRootLink(root, name string) (raw, resolved string, exists bool, err error) {
	path := filepath.Join(root, name)
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return "", "", false, nil
	}
	if statErr != nil {
		return "", "", false, statErr
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", "", false, fmt.Errorf("%s exists but is not a symlink", path)
	}
	target, readErr := os.Readlink(path)
	if readErr != nil {
		return "", "", false, readErr
	}
	resolved = target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	return target, filepath.Clean(resolved), true, nil
}

func linkMatches(root, name, release string) (bool, error) {
	expected, err := versionDirAbs(root, release)
	if err != nil {
		return false, err
	}
	_, resolved, exists, err := readRootLink(root, name)
	if err != nil {
		return false, err
	}
	return exists && resolved == filepath.Clean(expected), nil
}

func atomicSymlink(root, name, target string) error {
	tmp := filepath.Join(root, ".link-"+randomSuffix())
	if err := os.Symlink(target, tmp); err != nil {
		if runtimeIsWindows() {
			return fmt.Errorf("create %s symlink: %w (on Windows, enable Developer Mode or grant symlink privilege)", name, err)
		}
		return fmt.Errorf("create %s symlink: %w", name, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := replaceSymlink(tmp, filepath.Join(root, name)); err != nil {
		return fmt.Errorf("replace %s symlink: %w", name, err)
	}
	cleanup = false
	syncDir(root)
	return nil
}

func removeRootLink(root, name string) error {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refusing to remove %s: it is not a symlink", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	syncDir(root)
	return nil
}

// switchLinks atomically points current and previous at the given releases.
// Empty release names remove the corresponding symlink.
func switchLinks(root, current, previous string) error {
	if current != "" {
		if _, err := canonicalRelease(current); err != nil {
			return err
		}
		if err := requireVersionDir(root, current); err != nil {
			return err
		}
	}
	if previous != "" {
		if _, err := canonicalRelease(previous); err != nil {
			return err
		}
		if err := requireVersionDir(root, previous); err != nil {
			return err
		}
	}
	if previous == "" {
		if err := removeRootLink(root, "previous"); err != nil {
			return err
		}
	} else if err := atomicSymlink(root, "previous", filepath.Join("versions", versionDirectoryName(previous))); err != nil {
		return err
	}
	if current == "" {
		if err := removeRootLink(root, "current"); err != nil {
			return err
		}
	} else if err := atomicSymlink(root, "current", filepath.Join("versions", versionDirectoryName(current))); err != nil {
		return err
	}
	return nil
}

func requireVersionDir(root, release string) error {
	dir, err := versionDirAbs(root, release)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("release %s is not installed: %w", release, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("release directory %s must not be a symlink", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("release path %s is not a directory", dir)
	}
	return nil
}

func checkRootLinks(root string, state State, allowPending bool) error {
	currentAllowed := []string{}
	if state.Current != "" {
		currentAllowed = append(currentAllowed, state.Current)
	}
	previousAllowed := []string{}
	if state.Previous != "" {
		previousAllowed = append(previousAllowed, state.Previous)
	}
	if allowPending && state.Pending != nil {
		currentAllowed = append(currentAllowed, state.Pending.Release)
		if state.Pending.Previous != "" {
			previousAllowed = append(previousAllowed, state.Pending.Previous)
		}
	}
	for _, item := range []struct {
		name    string
		allowed []string
	}{
		{"current", currentAllowed},
		{"previous", previousAllowed},
	} {
		raw, _, exists, err := readRootLink(root, item.name)
		if err != nil {
			return err
		}
		if !exists {
			if len(item.allowed) == 0 {
				continue
			}
			return fmt.Errorf("%s symlink is missing", item.name)
		}
		matched := false
		for _, release := range item.allowed {
			ok, err := linkMatches(root, item.name, release)
			if err != nil {
				return err
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			if len(item.allowed) == 0 {
				return fmt.Errorf("%s symlink exists but the update state has no matching release (points at %s)", item.name, raw)
			}
			return fmt.Errorf("%s symlink does not match the update state (points at %s)", item.name, raw)
		}
	}
	return nil
}

func verifyVersionDir(root, release, manifestHash, archiveHash string) (InstalledRelease, error) {
	dir, err := versionDirAbs(root, release)
	if err != nil {
		return InstalledRelease{}, err
	}
	if err := requireVersionDir(root, release); err != nil {
		return InstalledRelease{}, err
	}
	record, found, err := readInstalled(dir)
	if err != nil {
		return InstalledRelease{}, fmt.Errorf("release %s metadata: %w", release, err)
	}
	if !found {
		return InstalledRelease{}, fmt.Errorf("release %s has no installation metadata", release)
	}
	if record.Release != release {
		return InstalledRelease{}, fmt.Errorf("release directory %s contains release %s", release, record.Release)
	}
	if manifestHash != "" && record.ManifestSHA256 != "" && record.ManifestSHA256 != manifestHash {
		return InstalledRelease{}, fmt.Errorf("release %s manifest does not match the update state", release)
	}
	if archiveHash != "" && record.ArchiveSHA256 != "" && record.ArchiveSHA256 != archiveHash {
		return InstalledRelease{}, fmt.Errorf("release %s archive does not match the manifest", release)
	}
	executable := filepath.Join(dir, filepath.FromSlash(record.Executable))
	info, err := os.Lstat(executable)
	if err != nil {
		return InstalledRelease{}, fmt.Errorf("release %s executable: %w", release, err)
	}
	if !info.Mode().IsRegular() {
		return InstalledRelease{}, fmt.Errorf("release %s executable is not a regular file", release)
	}
	sum, size, err := hashFile(executable)
	if err != nil {
		return InstalledRelease{}, err
	}
	if size <= 0 {
		return InstalledRelease{}, fmt.Errorf("release %s executable is empty", release)
	}
	if sum != record.ExecutableSHA256 {
		return InstalledRelease{}, fmt.Errorf("release %s executable does not match its installation record", release)
	}
	return record, nil
}

func installedCurrent(root string, state State) (Current, bool, error) {
	if state.Current == "" {
		return Current{}, false, nil
	}
	record, err := verifyVersionDir(root, state.Current, state.CurrentManifestSHA256, "")
	if err != nil {
		return Current{}, false, err
	}
	current := Current{
		App:            state.Current,
		Protocol:       state.CurrentProtocol,
		Schema:         state.CurrentSchema,
		ManifestSHA256: state.CurrentManifestSHA256,
	}
	if current.Protocol == 0 {
		current.Protocol = record.Protocol
	}
	if current.Schema == 0 {
		current.Schema = record.Schema
	}
	return current, true, nil
}

func listInstalledVersions(root string) ([]InstalledVersion, []string) {
	dir := filepath.Join(root, "versions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{err.Error()}
	}
	var versions []InstalledVersion
	var issues []string
	for _, entry := range entries {
		release, ok := releaseFromDirectoryName(entry.Name())
		if !ok {
			continue
		}
		if entry.IsDir() == false {
			issues = append(issues, fmt.Sprintf("versions/%s is not a directory", entry.Name()))
			continue
		}
		record, found, err := readInstalled(filepath.Join(dir, entry.Name()))
		if err != nil {
			issues = append(issues, fmt.Sprintf("versions/%s: %v", entry.Name(), err))
			continue
		}
		if !found {
			issues = append(issues, fmt.Sprintf("versions/%s has no installation metadata", entry.Name()))
			continue
		}
		if record.Release != release {
			issues = append(issues, fmt.Sprintf("versions/%s contains release %s", entry.Name(), record.Release))
			continue
		}
		versions = append(versions, InstalledVersion{
			InstalledRelease: record,
			Path:             filepath.Join(dir, entry.Name()),
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		a, _ := canonicalRelease(versions[i].Release)
		b, _ := canonicalRelease(versions[j].Release)
		return a.Compare(b) > 0
	})
	return versions, issues
}

func runtimeIsWindows() bool {
	return os.PathSeparator == '\\'
}
