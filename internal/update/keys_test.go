package update

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSigningKeySaveLoad(t *testing.T) {
	root, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "root.key")
	if err := root.Save(path, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 0600", info.Mode().Perm())
	}
	loaded, err := LoadSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Kind != KindRoot || loaded.KeyID != root.KeyID || !loaded.Public.Equal(root.Public) {
		t.Fatal("loaded key does not match")
	}
	if err := root.Save(path, false); err == nil {
		t.Fatal("overwrote an existing key without force")
	}
	if err := root.Save(path, true); err != nil {
		t.Fatalf("force save: %v", err)
	}
}

func TestLoadSigningKeyRejectsPermissions(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("permission check is Unix-only")
	}
	root, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "root.key")
	if err := root.Save(path, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("expected mode error, got %v", err)
	}
}

func TestLoadSigningKeyRejectsTampering(t *testing.T) {
	release, err := GenerateSigningKey(KindRelease)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release.key")
	if err := release.Save(path, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file KeyFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	file.PrivateKey = encodeBase64(make([]byte, ed25519.SeedSize))
	tampered := mustJSON(t, file)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(path); err == nil {
		t.Fatal("tampered key was accepted")
	}

	file.PrivateKey = encodeBase64(release.Private.Seed())
	file.KeyID = RootKeyPrefix + ":AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := os.WriteFile(path, mustJSON(t, file), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKey(path); err == nil {
		t.Fatal("key with mismatched id was accepted")
	}
}

func TestParseTrustAnchor(t *testing.T) {
	root, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	release, err := GenerateSigningKey(KindRelease)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "root.key")
	if err := root.Save(keyPath, false); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"raw":      encodeBase64(root.Public),
		"path":     keyPath,
		"key file": string(mustJSON(t, mustKeyFile(t, root))),
	} {
		public, err := ParseTrustAnchor(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !public.Equal(root.Public) {
			t.Fatalf("%s: wrong key", name)
		}
	}
	releasePath := filepath.Join(dir, "release.key")
	if err := release.Save(releasePath, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustAnchor(releasePath); err == nil {
		t.Fatal("release key file was accepted as a root key")
	}
}

func mustKeyFile(t *testing.T, key *SigningKey) KeyFile {
	t.Helper()
	file, err := key.KeyFile()
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCertifyAndVerifyReleaseKey(t *testing.T) {
	root, release, cert, verifier := testFundingKeys(t)
	public, err := verifier.VerifyReleaseKey(cert)
	if err != nil {
		t.Fatal(err)
	}
	if !public.Equal(release.Public) {
		t.Fatal("certified key does not match")
	}
	if cert.Root != root.KeyID {
		t.Fatal("certificate root does not match")
	}

	otherRoot, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewVerifier(otherRoot.Public).VerifyReleaseKey(cert); err == nil {
		t.Fatal("certificate verified under the wrong root")
	}

	now := time.Now().UTC().Truncate(time.Second)
	expired, err := CertifyRelease(root, release.Public, now.Add(-48*time.Hour), now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyReleaseKey(expired); err == nil {
		t.Fatal("expired certificate was accepted")
	}
	future, err := CertifyRelease(root, release.Public, now.Add(10*time.Minute), now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyReleaseKey(future); err == nil {
		t.Fatal("not-yet-valid certificate was accepted")
	}

	tampered := cert
	signature, _ := decodeBase64(cert.Signature)
	signature[0] ^= 0xff
	tampered.Signature = encodeBase64(signature)
	if _, err := verifier.VerifyReleaseKey(tampered); err == nil {
		t.Fatal("tampered certificate signature was accepted")
	}

	badID := cert
	badID.KeyID = RootKeyPrefix + ":AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := verifier.VerifyReleaseKey(badID); err == nil {
		t.Fatal("certificate with a mismatched key id was accepted")
	}
}

func TestSigningKeysMustMatchCertificates(t *testing.T) {
	_, release, cert, _ := testFundingKeys(t)
	other, err := GenerateSigningKey(KindRelease)
	if err != nil {
		t.Fatal(err)
	}
	channel := &Channel{
		Format: FormatChannel, Version: CurrentVersion, Channel: "stable",
		Release: "1.0.0", Manifest: "https://example.com/manifest.json",
		Rollout: 100, PublishedAt: formatTime(time.Now()), ReleaseKey: cert,
	}
	if err := SignChannel(other, channel); err == nil {
		t.Fatal("signed with a key that does not match the certificate")
	}
	if err := SignChannel(release, channel); err != nil {
		t.Fatal(err)
	}
}

func TestSignatureCoversCertificate(t *testing.T) {
	root, release, cert, verifier := testFundingKeys(t)
	channel, err := BuildChannel(BuildChannelOptions{
		Channel: "stable", Release: "1.0.0",
		ManifestRef: "https://example.com/manifests/v1.0.0.json",
		Rollout:     100, PublishedAt: time.Now().UTC(), Key: release, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, channel)
	if _, err := verifier.VerifyChannel(raw, "stable"); err != nil {
		t.Fatalf("valid channel rejected: %v", err)
	}
	var parsed Channel
	if err := decodeStrict(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.ReleaseKey.PublicKey = encodeBase64(root.Public)
	if _, err := verifier.VerifyChannel(mustJSON(t, parsed), "stable"); err == nil {
		t.Fatal("modified embedded certificate was accepted")
	}
}
