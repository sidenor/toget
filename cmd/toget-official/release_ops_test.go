package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/sbom"
	"github.com/sidenor/toget/internal/update"
)

func setupReleaseKeys(t *testing.T) (dir, rootKey, releaseKey, certPath string) {
	t.Helper()
	dir = t.TempDir()
	keys := filepath.Join(dir, "keys")
	rootKey = filepath.Join(keys, "root.key")
	releaseKey = filepath.Join(keys, "release.key")
	certPath = filepath.Join(keys, "release.cert.json")
	mustRunOfficial(t, "release", "keygen", "--kind", "root", "--out", rootKey)
	mustRunOfficial(t, "release", "keygen", "--kind", "release", "--out", releaseKey)
	mustRunOfficial(t, "release", "certify", "--root-key", rootKey, "--release-key", releaseKey, "--out", certPath)
	return dir, rootKey, releaseKey, certPath
}

func readVerifiedSidecar(t *testing.T, path string, data []byte, cert update.ReleaseKey) {
	t.Helper()
	sidecarData, err := os.ReadFile(path + ".sig")
	if err != nil {
		t.Fatalf("read sidecar for %s: %v", path, err)
	}
	var signature update.DetachedSignature
	if err := json.Unmarshal(sidecarData, &signature); err != nil {
		t.Fatal(err)
	}
	if err := update.VerifyDetachedSignature("", data, cert, signature); err != nil {
		t.Fatalf("sidecar for %s: %v", path, err)
	}
}

func TestReleaseIndexLatestAndYank(t *testing.T) {
	dir, rootKey, releaseKey, certPath := setupReleaseKeys(t)
	releaseDir := filepath.Join(dir, "releases")
	for _, release := range []string{"1.0.0", "1.1.0"} {
		writeReleaseArchive(t, releaseDir, release)
		mustRunOfficial(t, "release", "manifest",
			"--dir", releaseDir, "--release", release,
			"--base-url", "https://releases.example",
			"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
			"--no-sbom",
		)
	}
	mustRunOfficial(t, "release", "channel",
		"--dir", releaseDir, "--channel", "stable", "--release", "1.1.0",
		"--manifest-ref", "https://releases.example/manifests/v1.1.0.json",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	mustRunOfficial(t, "release", "index",
		"--dir", releaseDir,
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)

	root, err := update.LoadSigningKey(rootKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier := update.NewVerifier(root.Public)
	indexPath := filepath.Join(releaseDir, "manifests", "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := verifier.VerifyReleaseIndex(indexData)
	if err != nil {
		t.Fatal(err)
	}
	if index.Latest != "1.1.0" || len(index.Releases) != 2 {
		t.Fatalf("index = %+v", index)
	}
	readVerifiedSidecar(t, indexPath, indexData, index.ReleaseKey)
	manifestData, err := os.ReadFile(filepath.Join(releaseDir, "manifests", "v1.1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verifier.VerifyManifest(manifestData, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	readVerifiedSidecar(t, filepath.Join(releaseDir, "manifests", "v1.1.0.json"), manifestData, manifest.ReleaseKey)
	channelPath := filepath.Join(releaseDir, "channels", "stable.json")
	channelData, err := os.ReadFile(channelPath)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := verifier.VerifyChannel(channelData, "stable")
	if err != nil {
		t.Fatal(err)
	}
	readVerifiedSidecar(t, channelPath, channelData, channel.ReleaseKey)

	mustRunOfficial(t, "release", "latest", "--dir", releaseDir, "--root-key", rootKey)

	if err := runOfficial(t, "release", "yank",
		"--dir", releaseDir, "--release", "1.1.0", "--reason", "regression",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	); err == nil {
		t.Fatal("yank left a channel on the yanked release without --replace-with")
	}
	mustRunOfficial(t, "release", "yank",
		"--dir", releaseDir, "--release", "1.1.0", "--reason", "regression",
		"--replace-with", "1.0.0",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)

	indexData, err = os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err = verifier.VerifyReleaseIndex(indexData)
	if err != nil {
		t.Fatal(err)
	}
	if index.Latest != "1.0.0" {
		t.Fatalf("latest after yank = %q", index.Latest)
	}
	if !index.Releases[0].Yanked || index.Releases[0].YankReason != "regression" {
		t.Fatalf("yank entry = %+v", index.Releases[0])
	}
	readVerifiedSidecar(t, indexPath, indexData, index.ReleaseKey)

	channelData, err = os.ReadFile(channelPath)
	if err != nil {
		t.Fatal(err)
	}
	channel, err = verifier.VerifyChannel(channelData, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if channel.Release != "1.0.0" || !strings.HasSuffix(channel.Manifest, "/manifests/v1.0.0.json") {
		t.Fatalf("channel after yank = %+v", channel)
	}
	readVerifiedSidecar(t, channelPath, channelData, channel.ReleaseKey)

	// Rebuilding the index must preserve a yank recorded in the signed index.
	mustRunOfficial(t, "release", "index",
		"--dir", releaseDir, "--force",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	indexData, err = os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err = verifier.VerifyReleaseIndex(indexData)
	if err != nil {
		t.Fatal(err)
	}
	if index.Latest != "1.0.0" || !index.Releases[0].Yanked {
		t.Fatalf("yank was not preserved: %+v", index)
	}
}

func TestReleaseIndexRejectsTamperedManifestAndSidecar(t *testing.T) {
	dir, rootKey, releaseKey, certPath := setupReleaseKeys(t)
	releaseDir := filepath.Join(dir, "releases")
	writeReleaseArchive(t, releaseDir, "1.0.0")
	mustRunOfficial(t, "release", "manifest",
		"--dir", releaseDir, "--release", "1.0.0",
		"--base-url", "https://releases.example",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey, "--no-sbom",
	)
	mustRunOfficial(t, "release", "index",
		"--dir", releaseDir,
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	manifestPath := filepath.Join(releaseDir, "manifests", "v1.0.0.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'x'
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "index",
		"--dir", releaseDir, "--force",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	); err == nil {
		t.Fatal("index accepted a tampered manifest")
	}

	// Restore a valid manifest and index, then break only the sidecar.
	if err := os.Remove(filepath.Join(releaseDir, "manifests", "index.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(releaseDir, "manifests", "index.json.sig")); err != nil {
		t.Fatal(err)
	}
	mustRunOfficial(t, "release", "manifest",
		"--dir", releaseDir, "--release", "1.0.0",
		"--base-url", "https://releases.example",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey, "--no-sbom", "--force",
	)
	mustRunOfficial(t, "release", "index",
		"--dir", releaseDir, "--force",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	sidecarPath := filepath.Join(releaseDir, "manifests", "index.json.sig")
	sidecarData, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	sidecarData[0] = 'x'
	if err := os.WriteFile(sidecarPath, sidecarData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "latest", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("latest accepted a tampered index sidecar")
	}
}

func TestReleaseSBOMCommand(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "sbom", "toget-1.0.0.spdx.json")
	mustRunOfficial(t, "release", "sbom",
		"--dir", dir, "--release", "1.0.0", "--out", out, "--module-dir", "../..",
	)
	document, err := sbom.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Packages) < 2 {
		t.Fatalf("packages = %d", len(document.Packages))
	}
	if document.Packages[0].VersionInfo != "1.0.0" {
		t.Fatalf("main package = %+v", document.Packages[0])
	}
	if err := runOfficial(t, "release", "sbom",
		"--dir", dir, "--release", "1.0.0", "--out", out, "--module-dir", "../..",
	); err == nil {
		t.Fatal("SBOM overwrote an existing file without --force")
	}
}
