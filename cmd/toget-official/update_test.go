package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/update"
)

func runOfficial(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newRootCommand()
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(context.Background())
}

func mustRunOfficial(t *testing.T, args ...string) {
	t.Helper()
	if err := runOfficial(t, args...); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
}

func writeReleaseArchive(t *testing.T, dir, release string) string {
	t.Helper()
	platformDir := filepath.Join(dir, "daemon", "linux-amd64", "v"+release)
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(platformDir, "toget-linux-amd64.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	body := []byte("#!/bin/sh\necho toget\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "toget", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReleaseCommandWorkflow(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	rootKey := filepath.Join(keys, "root.key")
	releaseKey := filepath.Join(keys, "release.key")
	certPath := filepath.Join(keys, "release.cert.json")

	mustRunOfficial(t, "release", "keygen", "--kind", "root", "--out", rootKey)
	mustRunOfficial(t, "release", "keygen", "--kind", "release", "--out", releaseKey)
	mustRunOfficial(t, "release", "certify", "--root-key", rootKey, "--release-key", releaseKey, "--out", certPath)

	root, err := update.LoadSigningKey(rootKey)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := update.ParseTrustAnchor(rootKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier := update.NewVerifier(anchor)
	certData, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	var cert update.ReleaseKey
	if err := json.Unmarshal(certData, &cert); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyReleaseKey(cert); err != nil {
		t.Fatalf("certificate: %v", err)
	}

	releaseDir := filepath.Join(dir, "releases")
	writeReleaseArchive(t, releaseDir, "1.1.0")
	sbomPath := filepath.Join(releaseDir, "sbom", "toget.spdx.json")
	if err := os.MkdirAll(filepath.Dir(sbomPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sbomPath, []byte(`{"spdxVersion":"SPDX-2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mustRunOfficial(t, "release", "manifest",
		"--dir", releaseDir,
		"--release", "1.1.0",
		"--base-url", "https://releases.example",
		"--key", releaseKey,
		"--cert", certPath,
		"--root-key", rootKey,
		"--min-app", "1.0.0",
		"--changelog", "Test release",
		"--sbom", sbomPath,
	)
	manifestPath := filepath.Join(releaseDir, "manifests", "v1.1.0.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := verifier.VerifyManifest(manifestData, "1.1.0")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.Artifacts) != 1 || manifest.Artifacts[0].URL != "https://releases.example/daemon/linux-amd64/v1.1.0/toget-linux-amd64.tar.gz" {
		t.Fatalf("artifacts = %+v", manifest.Artifacts)
	}
	if manifest.SBOM == nil || manifest.SBOM.URL != "https://releases.example/sbom/toget.spdx.json" {
		t.Fatalf("SBOM = %+v", manifest.SBOM)
	}

	if err := runOfficial(t, "release", "manifest", "--dir", releaseDir, "--release", "1.1.0", "--base-url", "https://releases.example", "--key", releaseKey, "--cert", certPath, "--root-key", rootKey); err == nil {
		t.Fatal("manifest overwrote an existing immutable release without --force")
	}

	shutdown, address, err := startUpdateService(releaseDir, "127.0.0.1:0", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})

	mustRunOfficial(t, "release", "channel",
		"--dir", releaseDir,
		"--channel", "stable",
		"--release", "1.1.0",
		"--manifest-ref", address+"/manifests/v1.1.0.json",
		"--key", releaseKey,
		"--cert", certPath,
		"--root-key", rootKey,
	)
	channelPath := filepath.Join(releaseDir, "channels", "stable.json")
	channelData, err := os.ReadFile(channelPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyChannel(channelData, "stable"); err != nil {
		t.Fatalf("channel: %v", err)
	}

	mustRunOfficial(t, "release", "verify",
		"--channel-url", address+"/channels/stable.json",
		"--root-key", rootKey,
		"--channel", "stable",
		"--current", "1.0.0",
		"--os", "linux",
		"--arch", "amd64",
	)

	client := &update.Client{
		Verifier:   verifier,
		ChannelURL: address + "/channels/stable.json",
		Channel:    "stable",
		OS:         "linux",
		Arch:       "amd64",
	}
	result, err := client.Check(context.Background(), update.Current{App: "1.0.0", Protocol: 1, Schema: 9}, "test", update.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Manifest.Release != "1.1.0" {
		t.Fatalf("check = %+v", result)
	}
	if result.Artifact.URL != "https://releases.example/daemon/linux-amd64/v1.1.0/toget-linux-amd64.tar.gz" {
		t.Fatalf("artifact URL = %q", result.Artifact.URL)
	}

	public := base64.RawURLEncoding.EncodeToString(root.Public)
	if _, err := update.ParseTrustAnchor(public); err != nil {
		t.Fatalf("printed root public key: %v", err)
	}
}

func TestReleaseCommandsRejectInvalidKeysAndInputs(t *testing.T) {
	dir := t.TempDir()
	rootKey := filepath.Join(dir, "root.key")
	releaseKey := filepath.Join(dir, "release.key")
	mustRunOfficial(t, "release", "keygen", "--kind", "root", "--out", rootKey)
	mustRunOfficial(t, "release", "keygen", "--kind", "release", "--out", releaseKey)

	if err := runOfficial(t, "release", "keygen", "--out", filepath.Join(dir, "missing-kind.key")); err == nil {
		t.Fatal("keygen accepted a missing kind")
	}
	if err := runOfficial(t, "release", "certify", "--root-key", releaseKey, "--release-key", releaseKey, "--out", filepath.Join(dir, "bad.json")); err == nil {
		t.Fatal("certify accepted a release key as root")
	}
	if err := runOfficial(t, "release", "certify", "--root-key", rootKey, "--release-key", rootKey, "--out", filepath.Join(dir, "bad.json")); err == nil {
		t.Fatal("certify accepted a root key as release")
	}
	if err := runOfficial(t, "release", "certify", "--root-key", rootKey, "--release-key", releaseKey, "--out", filepath.Join(dir, "bad.json"), "--valid-for", "0s"); err == nil {
		t.Fatal("certify accepted a non-positive lifetime")
	}
	if err := runOfficial(t, "release", "manifest", "--dir", dir); err == nil {
		t.Fatal("manifest accepted missing flags")
	}
	if err := runOfficial(t, "release", "channel", "--release", "1.0.0"); err == nil {
		t.Fatal("channel accepted missing flags")
	}
	if err := runOfficial(t, "release", "verify", "--channel-url", "http://127.0.0.1:1/channels/stable.json"); err == nil {
		t.Fatal("verify accepted a missing root key")
	}

	certPath := filepath.Join(dir, "cert.json")
	mustRunOfficial(t, "release", "certify", "--root-key", rootKey, "--release-key", releaseKey, "--out", certPath)
	if err := os.WriteFile(certPath, []byte(`{"format":"x"} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReleaseCertificate(certPath); err == nil {
		t.Fatal("certificate with trailing data was accepted")
	}
	if err := os.WriteFile(certPath, []byte(`{"unknown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReleaseCertificate(certPath); err == nil {
		t.Fatal("certificate with unknown fields was accepted")
	}

	document := filepath.Join(dir, "document.json")
	if err := writeReleaseDocument(document, map[string]string{"a": "b"}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeReleaseDocument(document, map[string]string{"a": "c"}, false); err == nil {
		t.Fatal("document overwrite was allowed without --force")
	}
	if err := writeReleaseDocument(document, map[string]string{"a": "c"}, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"c"`)) || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("document = %q", data)
	}
}

func TestUpdateServiceSafety(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := startUpdateService(dir, "0.0.0.0:0", "", "", false); err == nil {
		t.Fatal("non-loopback update listener started without TLS")
	}
	if _, _, err := startUpdateService(dir, "127.0.0.1:0", filepath.Join(dir, "cert.pem"), "", false); err == nil {
		t.Fatal("update listener accepted TLS certificate without key")
	}
	badCert := filepath.Join(dir, "bad-cert.pem")
	badKey := filepath.Join(dir, "bad-key.pem")
	if err := os.WriteFile(badCert, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badKey, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := startUpdateService(dir, "127.0.0.1:0", badCert, badKey, false); err == nil {
		t.Fatal("update listener accepted an invalid TLS certificate")
	}
	if _, _, err := startUpdateService(filepath.Join(dir, "missing"), "127.0.0.1:0", "", "", false); err == nil {
		t.Fatal("update listener accepted a missing release directory")
	}
	shutdown, address, err := startUpdateService(dir, "127.0.0.1:0", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(address, "http://127.0.0.1:") {
		t.Fatalf("address = %q", address)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:7721": true,
		"localhost:7721": true,
		"[::1]:7721":     true,
		"0.0.0.0:7721":   false,
		":7721":          false,
		"example.com:80": false,
		"not-an-address": false,
	}
	for value, want := range cases {
		if got := isLoopbackListen(value); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", value, got, want)
		}
	}
}
