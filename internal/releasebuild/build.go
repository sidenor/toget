// Package releasebuild builds reproducible Toget release archives.
package releasebuild

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/update"
)

const (
	// DefaultPackage is the command built for a release.
	DefaultPackage = "./cmd/toget"
	// DefaultPlatforms is the cross-platform release matrix.
	DefaultPlatforms = "linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64"
	// ArchiveMode is the executable mode inside every archive.
	ArchiveMode = 0o755
)

// Options describes one release build.
type Options struct {
	RepoDir         string
	OutDir          string
	Release         string
	Platforms       []string
	RootPublicKey   string
	SourceDateEpoch int64
	GoBinary        string
	Env             []string
	Stdout          io.Writer
	Stderr          io.Writer
	Package         string
	Force           bool
	// Verify builds every archive twice and compares the bytes.
	Verify bool
}

// Artifact is one deterministic archive.
type Artifact struct {
	OS     string
	Arch   string
	Path   string
	SHA256 string
	Size   int64
}

// Build compiles and archives each platform. Archives use a fixed timestamp,
// trimpath, a disabled CGO toolchain, and stable compression so two builds
// from the same source and Go toolchain produce identical bytes.
func Build(ctx context.Context, opts Options) ([]Artifact, error) {
	parsed, err := update.ParseSemVer(opts.Release)
	if err != nil {
		return nil, fmt.Errorf("release: %w", err)
	}
	if len(parsed.Build) > 0 || parsed.String() != opts.Release {
		return nil, errors.New("release must be canonical SemVer without build metadata")
	}
	if strings.TrimSpace(opts.RepoDir) == "" {
		opts.RepoDir = "."
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		return nil, errors.New("output directory is required")
	}
	if opts.SourceDateEpoch < 0 {
		return nil, errors.New("source date epoch must not be negative")
	}
	platforms, err := parsePlatforms(opts.Platforms)
	if err != nil {
		return nil, err
	}
	if opts.Package == "" {
		opts.Package = DefaultPackage
	}
	if opts.GoBinary == "" {
		opts.GoBinary = "go"
	}
	rootKey := ""
	if strings.TrimSpace(opts.RootPublicKey) != "" {
		anchor, err := update.ParseTrustAnchor(opts.RootPublicKey)
		if err != nil {
			return nil, fmt.Errorf("root public key: %w", err)
		}
		rootKey = base64.RawURLEncoding.EncodeToString(anchor)
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	outDir, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	epoch := time.Unix(opts.SourceDateEpoch, 0).UTC()
	build := buildOptions{
		repoDir: opts.RepoDir, release: opts.Release, rootKey: rootKey,
		epoch: epoch, goBinary: opts.GoBinary, env: buildEnv(opts.Env, opts.SourceDateEpoch),
		pkg: opts.Package, stdout: opts.Stdout, stderr: opts.Stderr,
	}
	if !opts.Force {
		for _, platform := range platforms {
			path := artifactPath(outDir, opts.Release, platform)
			if _, err := os.Stat(path); err == nil {
				return nil, fmt.Errorf("%s already exists (use --force to overwrite)", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	artifacts, err := buildOnce(ctx, outDir, platforms, build)
	if err != nil {
		return nil, err
	}
	if opts.Verify {
		verifyDir, err := os.MkdirTemp("", "toget-reproduce-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(verifyDir)
		repeated, err := buildOnce(ctx, verifyDir, platforms, build)
		if err != nil {
			return nil, fmt.Errorf("reproducibility build: %w", err)
		}
		if err := compareArtifacts(artifacts, repeated); err != nil {
			return nil, err
		}
		fmt.Fprintf(opts.Stdout, "Reproducible: yes (%d platforms, identical SHA-256)\n", len(artifacts))
	}
	return artifacts, nil
}

type buildOptions struct {
	repoDir  string
	release  string
	rootKey  string
	epoch    time.Time
	goBinary string
	env      []string
	pkg      string
	stdout   io.Writer
	stderr   io.Writer
}

type platform struct {
	OS   string
	Arch string
}

func buildOnce(ctx context.Context, outDir string, platforms []platform, opts buildOptions) ([]Artifact, error) {
	staging, err := os.MkdirTemp("", "toget-build-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	artifacts := make([]Artifact, 0, len(platforms))
	for _, target := range platforms {
		executable := "toget"
		if target.OS == "windows" {
			executable += ".exe"
		}
		binaryPath := filepath.Join(staging, target.OS+"-"+target.Arch+"-"+executable)
		env := append([]string{}, opts.env...)
		env = append(env, "GOOS="+target.OS, "GOARCH="+target.Arch)
		ldflags := "-s -w -buildid= -X github.com/sidenor/toget/internal/version.App=" + opts.release
		if opts.rootKey != "" {
			ldflags += " -X github.com/sidenor/toget/internal/version.ReleaseRootKey=" + opts.rootKey
		}
		args := []string{
			"build", "-trimpath", "-buildvcs=false", "-mod=readonly",
			"-o", binaryPath, "-ldflags", ldflags, opts.pkg,
		}
		cmd := exec.CommandContext(ctx, opts.goBinary, args...)
		cmd.Dir = opts.repoDir
		cmd.Env = env
		var stderr bytes.Buffer
		cmd.Stdout = opts.stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			message := strings.TrimSpace(stderr.String())
			if message != "" {
				return nil, fmt.Errorf("build %s/%s: %w: %s", target.OS, target.Arch, err, message)
			}
			return nil, fmt.Errorf("build %s/%s: %w", target.OS, target.Arch, err)
		}
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("build %s/%s produced an empty binary", target.OS, target.Arch)
		}
		path := artifactPath(outDir, opts.release, target)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := writeArchive(path, executable, data, opts.epoch); err != nil {
			return nil, err
		}
		sum, size, err := hashFile(path)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{OS: target.OS, Arch: target.Arch, Path: path, SHA256: sum, Size: size})
		fmt.Fprintf(opts.stdout, "Built %s/%s: %s (%d bytes)\n", target.OS, target.Arch, path, size)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].OS != artifacts[j].OS {
			return artifacts[i].OS < artifacts[j].OS
		}
		return artifacts[i].Arch < artifacts[j].Arch
	})
	return artifacts, nil
}

func parsePlatforms(values []string) ([]platform, error) {
	if len(values) == 0 {
		values = strings.Split(DefaultPlatforms, ",")
	}
	seen := make(map[string]bool, len(values))
	platforms := make([]platform, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		osName, arch, ok := strings.Cut(value, "/")
		if !ok || !platformToken(osName) || !platformToken(arch) {
			return nil, fmt.Errorf("platform %q must be <os>/<arch> using lowercase letters and digits", value)
		}
		key := osName + "/" + arch
		if seen[key] {
			return nil, fmt.Errorf("platform %s is listed more than once", key)
		}
		seen[key] = true
		platforms = append(platforms, platform{OS: osName, Arch: arch})
	}
	if len(platforms) == 0 {
		return nil, errors.New("no platforms selected")
	}
	return platforms, nil
}

func platformToken(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func buildEnv(base []string, epoch int64) []string {
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+8)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch name {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GO111MODULE", "GOWORK", "SOURCE_DATE_EPOCH":
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"CGO_ENABLED=0",
		"GO111MODULE=on",
		"GOFLAGS=",
		"GOWORK=off",
		"SOURCE_DATE_EPOCH="+strconv.FormatInt(epoch, 10),
	)
}

func artifactPath(outDir, release string, target platform) string {
	extension := ".tar.gz"
	if target.OS == "windows" {
		extension = ".zip"
	}
	return filepath.Join(outDir, "daemon", target.OS+"-"+target.Arch, "v"+release,
		"toget-"+target.OS+"-"+target.Arch+extension)
}

func writeArchive(path, name string, data []byte, epoch time.Time) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".toget-release-*")
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
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(path, ".tar.gz"):
		if err := writeTarGz(tmp, name, data, epoch); err != nil {
			return err
		}
	case strings.HasSuffix(path, ".zip"):
		if err := writeZip(tmp, name, data, epoch); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported archive path %s", path)
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
	return nil
}

func writeTarGz(output io.Writer, name string, data []byte, epoch time.Time) error {
	gz, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return err
	}
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	gz.Header.Name = ""
	gz.Header.Comment = ""
	tw := tar.NewWriter(gz)
	modTime := epoch
	if modTime.Before(time.Unix(0, 0)) {
		modTime = time.Unix(0, 0)
	}
	header := &tar.Header{
		Name: name, Mode: ArchiveMode, Size: int64(len(data)),
		Typeflag: tar.TypeReg, ModTime: modTime.UTC(),
		Uid: 0, Gid: 0, Uname: "", Gname: "",
		Format: tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(output io.Writer, name string, data []byte, epoch time.Time) error {
	zw := zip.NewWriter(output)
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(ArchiveMode)
	modTime := epoch
	if modTime.Before(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
		modTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	header.SetModTime(modTime.UTC())
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return zw.Close()
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func compareArtifacts(first, second []Artifact) error {
	if len(first) != len(second) {
		return fmt.Errorf("reproducibility build produced %d artifacts, want %d", len(second), len(first))
	}
	for i := range first {
		if first[i].OS != second[i].OS || first[i].Arch != second[i].Arch {
			return fmt.Errorf("reproducibility build artifact order differs at %d", i)
		}
		if first[i].SHA256 != second[i].SHA256 {
			return fmt.Errorf("%s/%s is not reproducible: %s != %s", first[i].OS, first[i].Arch, first[i].SHA256, second[i].SHA256)
		}
	}
	return nil
}
