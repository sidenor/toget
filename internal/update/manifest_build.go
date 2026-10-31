package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BuildManifestOptions describes a release artifact tree.
type BuildManifestOptions struct {
	// Dir contains daemon/<os>-<arch>/v<release>/<archive>. The archive name
	// is not significant; the platform directory and extension are.
	Dir string
	// Release is the canonical SemVer release being published.
	Release string
	// BaseURL optionally prefixes artifact URLs. It must be HTTPS (or a
	// loopback HTTP URL during development).
	BaseURL string
	// Executable is the path of the binary inside every archive. It defaults
	// to toget, or toget.exe for Windows.
	Executable string

	Protocol    int
	Schema      int
	MinApp      string
	MinProtocol int
	Level       string
	Changelog   string
	PublishedAt time.Time
	// SBOMPath is an optional local file included as an SBOM reference.
	SBOMPath string

	Key  *SigningKey
	Cert ReleaseKey
}

// BuildManifest scans a release directory, hashes its archives, signs the
// resulting manifest, and returns it.
func BuildManifest(opts BuildManifestOptions) (*Manifest, error) {
	release, err := canonicalRelease(opts.Release)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	if opts.Dir == "" {
		return nil, errors.New("artifact directory is required")
	}
	if opts.Level == "" {
		opts.Level = LevelRecommended
	}
	if !channelLevelValid(opts.Level) {
		return nil, fmt.Errorf("invalid release level %q", opts.Level)
	}
	if opts.Protocol < 1 || opts.Protocol > 1000 {
		return nil, fmt.Errorf("protocol %d is out of range", opts.Protocol)
	}
	if opts.Schema < 1 || opts.Schema > 100000 {
		return nil, fmt.Errorf("schema %d is out of range", opts.Schema)
	}
	if opts.MinProtocol < 0 || opts.MinProtocol > opts.Protocol {
		return nil, fmt.Errorf("min protocol %d is out of range", opts.MinProtocol)
	}
	if opts.MinApp != "" {
		minApp, err := canonicalRelease(opts.MinApp)
		if err != nil {
			return nil, fmt.Errorf("min app: %w", err)
		}
		if minApp.Compare(release) > 0 {
			return nil, errors.New("min app is newer than the release")
		}
	}
	if opts.Executable != "" {
		if err := validateExecutablePath(opts.Executable); err != nil {
			return nil, err
		}
	}
	if len(opts.Changelog) > 8192 {
		return nil, errors.New("changelog is too long")
	}
	if opts.PublishedAt.IsZero() {
		opts.PublishedAt = time.Now().UTC()
	}
	base := strings.TrimSuffix(strings.TrimSpace(opts.BaseURL), "/")
	if base != "" {
		if err := validateReference(base + "/daemon"); err != nil {
			return nil, fmt.Errorf("base URL: %w", err)
		}
	}

	root, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("artifact directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("artifact directory is not a directory")
	}

	versionDir := "v" + release.String()
	artifacts := make([]Artifact, 0, 8)
	seen := map[string]bool{}
	err = filepath.WalkDir(filepath.Join(root, "daemon"), func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if entry.IsDir() {
			// A release tree keeps every published version side by side.
			// Hash only the directory of the release being signed.
			if len(parts) == 3 && parts[0] == "daemon" && parts[2] != versionDir {
				return filepath.SkipDir
			}
			return nil
		}
		if len(parts) != 4 || parts[0] != "daemon" || parts[2] != versionDir {
			return fmt.Errorf("artifact %s is not in daemon/<os>-<arch>/%s/", filepath.ToSlash(rel), versionDir)
		}
		osName, arch, ok := strings.Cut(parts[1], "-")
		if !ok || !validPlatformToken(osName) || !validPlatformToken(arch) {
			return fmt.Errorf("artifact %s has an invalid platform directory", filepath.ToSlash(rel))
		}
		format, ok := archiveFormat(parts[3])
		if !ok {
			return fmt.Errorf("artifact %s must end in .tar.gz or .zip", filepath.ToSlash(rel))
		}
		key := osName + "/" + arch
		if seen[key] {
			return fmt.Errorf("multiple archives for %s", key)
		}
		seen[key] = true
		sum, size, err := hashFile(current)
		if err != nil {
			return err
		}
		if size <= 0 || size > MaxArtifactBytes {
			return fmt.Errorf("artifact %s has an invalid size", filepath.ToSlash(rel))
		}
		executable := opts.Executable
		if executable == "" {
			executable = "toget"
			if osName == "windows" {
				executable = "toget.exe"
			}
		}
		url := filepath.ToSlash(rel)
		if base != "" {
			url = base + "/" + url
		}
		artifacts = append(artifacts, Artifact{
			OS: osName, Arch: arch, URL: url, SHA256: sum,
			Size: size, Format: format, Executable: executable,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("no release archives under %s/daemon", root)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].OS != artifacts[j].OS {
			return artifacts[i].OS < artifacts[j].OS
		}
		return artifacts[i].Arch < artifacts[j].Arch
	})

	var sbom *FileRef
	if opts.SBOMPath != "" {
		sum, size, err := hashFile(opts.SBOMPath)
		if err != nil {
			return nil, fmt.Errorf("SBOM: %w", err)
		}
		if size <= 0 || size > MaxSBOMBytes {
			return nil, errors.New("SBOM has an invalid size")
		}
		url := filepath.ToSlash(filepath.Base(opts.SBOMPath))
		if base != "" {
			url = base + "/sbom/" + url
		} else {
			url = "sbom/" + url
		}
		sbom = &FileRef{URL: url, SHA256: sum, Size: size}
	}

	manifest := &Manifest{
		Format:      FormatManifest,
		Version:     CurrentVersion,
		Release:     release.String(),
		Protocol:    opts.Protocol,
		Schema:      opts.Schema,
		MinApp:      opts.MinApp,
		MinProtocol: opts.MinProtocol,
		Level:       opts.Level,
		Changelog:   opts.Changelog,
		PublishedAt: formatTime(opts.PublishedAt),
		Artifacts:   artifacts,
		SBOM:        sbom,
		ReleaseKey:  opts.Cert,
	}
	if err := SignManifest(opts.Key, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// BuildChannelOptions describes a signed channel pointer.
type BuildChannelOptions struct {
	Channel     string
	Release     string
	ManifestRef string
	Rollout     int
	PublishedAt time.Time
	Key         *SigningKey
	Cert        ReleaseKey
}

// BuildChannel validates and signs a channel pointer.
func BuildChannel(opts BuildChannelOptions) (*Channel, error) {
	if !channelNamePattern.MatchString(opts.Channel) {
		return nil, fmt.Errorf("invalid channel name %q", opts.Channel)
	}
	release, err := canonicalRelease(opts.Release)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	if err := validateReference(opts.ManifestRef); err != nil {
		return nil, fmt.Errorf("manifest reference: %w", err)
	}
	if opts.Rollout < 1 || opts.Rollout > 100 {
		return nil, errors.New("rollout must be between 1 and 100")
	}
	if opts.PublishedAt.IsZero() {
		opts.PublishedAt = time.Now().UTC()
	}
	channel := &Channel{
		Format:      FormatChannel,
		Version:     CurrentVersion,
		Channel:     opts.Channel,
		Release:     release.String(),
		Manifest:    opts.ManifestRef,
		Rollout:     opts.Rollout,
		PublishedAt: formatTime(opts.PublishedAt),
		ReleaseKey:  opts.Cert,
	}
	if err := SignChannel(opts.Key, channel); err != nil {
		return nil, err
	}
	return channel, nil
}

func channelLevelValid(value string) bool {
	switch value {
	case LevelRecommended, LevelSecurity, LevelForced:
		return true
	default:
		return false
	}
}

func archiveFormat(name string) (string, bool) {
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		return FormatTarGz, true
	case strings.HasSuffix(name, ".zip"):
		return FormatZip, true
	default:
		return "", false
	}
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", path)
	}
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(file, MaxArtifactBytes+1))
	if err != nil {
		return "", 0, err
	}
	if n > MaxArtifactBytes {
		return "", 0, fmt.Errorf("%s is too large", path)
	}
	return hex.EncodeToString(hasher.Sum(nil)), n, nil
}
