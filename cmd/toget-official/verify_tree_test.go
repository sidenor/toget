package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseVerifyTree(t *testing.T) {
	dir, rootKey, releaseKey, certPath := setupReleaseKeys(t)
	releaseDir := filepath.Join(dir, "releases")
	archivePath := writeReleaseArchive(t, releaseDir, "1.0.0")
	sbomPath := filepath.Join(releaseDir, "sbom", "toget-1.0.0.spdx.json")
	mustRunOfficial(t, "release", "sbom",
		"--dir", releaseDir, "--release", "1.0.0", "--out", sbomPath, "--module-dir", "../..",
	)
	mustRunOfficial(t, "release", "manifest",
		"--dir", releaseDir, "--release", "1.0.0",
		"--base-url", "https://releases.example",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
		"--sbom", sbomPath,
	)
	mustRunOfficial(t, "release", "channel",
		"--dir", releaseDir, "--channel", "stable", "--release", "1.0.0",
		"--manifest-ref", "https://releases.example/manifests/v1.0.0.json",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	mustRunOfficial(t, "release", "index",
		"--dir", releaseDir,
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	mustRunOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey)

	// Metadata checks still work when large archives are intentionally skipped.
	originalArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted a tampered archive")
	}
	mustRunOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey, "--skip-artifacts")

	// Restore the archive and damage the SBOM instead.
	if err := os.WriteFile(archivePath, originalArchive, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sbomPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted a tampered SBOM")
	}
}

func TestReleaseVerifyTreeRejectsUnreferencedFiles(t *testing.T) {
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
	mustRunOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey)

	extra := filepath.Join(releaseDir, "daemon", "linux-amd64", "v1.0.0", "extra.txt")
	if err := os.WriteFile(extra, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted an unreferenced daemon file")
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "root.key"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted a top-level private file")
	}
	if err := os.Remove(filepath.Join(releaseDir, "root.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(archivePathForRelease(releaseDir, "1.0.0"), filepath.Join(releaseDir, "daemon", "linux-amd64", "v1.0.0", "link.tar.gz")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted a symlink")
	}
}

func TestReleaseVerifyTreeRejectsOrphanChannelSidecar(t *testing.T) {
	dir, rootKey, releaseKey, certPath := setupReleaseKeys(t)
	releaseDir := filepath.Join(dir, "releases")
	writeReleaseArchive(t, releaseDir, "1.0.0")
	mustRunOfficial(t, "release", "manifest",
		"--dir", releaseDir, "--release", "1.0.0",
		"--base-url", "https://releases.example",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey, "--no-sbom",
	)
	mustRunOfficial(t, "release", "channel",
		"--dir", releaseDir, "--channel", "stable", "--release", "1.0.0",
		"--manifest-ref", "https://releases.example/manifests/v1.0.0.json",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	mustRunOfficial(t, "release", "index",
		"--dir", releaseDir,
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	orphan := filepath.Join(releaseDir, "channels", "orphan.json.sig")
	if err := os.WriteFile(orphan, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted an orphan channel sidecar")
	}
	if err := os.Remove(orphan); err != nil {
		t.Fatal(err)
	}
	mustRunOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey)
}

func archivePathForRelease(dir, release string) string {
	return filepath.Join(dir, "daemon", "linux-amd64", "v"+release, "toget-linux-amd64.tar.gz")
}

func TestReleaseVerifyTreeRequiresIndex(t *testing.T) {
	dir := t.TempDir()
	rootKey := filepath.Join(t.TempDir(), "root.key")
	mustRunOfficial(t, "release", "keygen", "--kind", "root", "--out", rootKey)
	err := runOfficial(t, "release", "verify-tree", "--dir", dir, "--root-key", rootKey)
	if err == nil || !strings.Contains(err.Error(), "index is missing") {
		t.Fatalf("verify-tree error = %v, want missing index", err)
	}
}

func TestReleaseVerifyTreeRejectsYankedChannel(t *testing.T) {
	dir, rootKey, releaseKey, certPath := setupReleaseKeys(t)
	releaseDir := filepath.Join(dir, "releases")
	for _, release := range []string{"1.0.0", "1.1.0"} {
		writeReleaseArchive(t, releaseDir, release)
		mustRunOfficial(t, "release", "manifest",
			"--dir", releaseDir, "--release", release,
			"--base-url", "https://releases.example",
			"--key", releaseKey, "--cert", certPath, "--root-key", rootKey, "--no-sbom",
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
	// Yank without repointing by deleting the channel only for this check:
	// rewriting the channel afterwards would hide the inconsistent state.
	mustRunOfficial(t, "release", "yank",
		"--dir", releaseDir, "--release", "1.1.0", "--reason", "regression",
		"--replace-with", "1.0.0",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey,
	)
	mustRunOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey)
	// A channel must never point at a yanked release, even if it is signed.
	mustRunOfficial(t, "release", "channel", "--dir", releaseDir, "--channel", "stable", "--release", "1.1.0",
		"--manifest-ref", "https://releases.example/manifests/v1.1.0.json",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey, "--force")
	if err := runOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey); err == nil {
		t.Fatal("verify-tree accepted a channel pointing at a yanked release")
	}
	mustRunOfficial(t, "release", "channel", "--dir", releaseDir, "--channel", "stable", "--release", "1.0.0",
		"--manifest-ref", "https://releases.example/manifests/v1.0.0.json",
		"--key", releaseKey, "--cert", certPath, "--root-key", rootKey, "--force")
	mustRunOfficial(t, "release", "verify-tree", "--dir", releaseDir, "--root-key", rootKey)
}
