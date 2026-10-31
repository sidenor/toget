package update

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testArtifact() Artifact {
	return Artifact{
		OS:         "linux",
		Arch:       "amd64",
		URL:        "https://releases.example/daemon/linux-amd64/v1.2.3/toget.tar.gz",
		SHA256:     strings.Repeat("a", 64),
		Size:       1024,
		Format:     FormatTarGz,
		Executable: "toget",
	}
}

func baseManifest(cert ReleaseKey) Manifest {
	return Manifest{
		Format:      FormatManifest,
		Version:     CurrentVersion,
		Release:     "1.2.3",
		Protocol:    1,
		Schema:      9,
		MinApp:      "1.0.0",
		MinProtocol: 1,
		Level:       LevelRecommended,
		Changelog:   "changes",
		PublishedAt: formatTime(time.Now().UTC().Add(-time.Minute)),
		Artifacts:   []Artifact{testArtifact()},
		ReleaseKey:  cert,
	}
}

func signManifestRaw(t *testing.T, key *SigningKey, manifest Manifest) []byte {
	t.Helper()
	manifest.Signature = ""
	if err := SignManifest(key, &manifest); err != nil {
		t.Fatal(err)
	}
	return mustJSON(t, manifest)
}

func TestVerifyManifestValidAndSignature(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	manifest := baseManifest(cert)
	raw := signManifestRaw(t, key, manifest)
	parsed, err := verifier.VerifyManifest(raw, "1.2.3")
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if parsed.Release != "1.2.3" || len(parsed.Artifacts) != 1 {
		t.Fatal("unexpected parsed manifest")
	}
	if _, err := verifier.VerifyManifest(raw, "1.2.4"); err == nil {
		t.Fatal("manifest accepted for the wrong release")
	}
	tampered := manifest
	tampered.Changelog = "different"
	if _, err := verifier.VerifyManifest(signManifestRaw(t, key, tampered), "1.2.3"); err != nil {
		t.Fatalf("re-signed manifest rejected: %v", err)
	}
	badSignature := parsed
	signature, _ := decodeBase64(badSignature.Signature)
	signature[0] ^= 0xff
	badSignature.Signature = encodeBase64(signature)
	if _, err := verifier.VerifyManifest(mustJSON(t, badSignature), "1.2.3"); err == nil {
		t.Fatal("tampered signature was accepted")
	}
}

func TestVerifyManifestRejectsUnknownAndTrailingData(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	raw := signManifestRaw(t, key, baseManifest(cert))
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	generic["extra"] = true
	if _, err := verifier.VerifyManifest(mustJSON(t, generic), "1.2.3"); err == nil {
		t.Fatal("unknown field was accepted")
	}
	trailing := append(append([]byte{}, raw...), []byte("\n{}\n")...)
	if _, err := verifier.VerifyManifest(trailing, "1.2.3"); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestVerifyManifestValidation(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"future published_at", func(m *Manifest) { m.PublishedAt = formatTime(time.Now().Add(time.Hour)) }},
		{"bad release", func(m *Manifest) { m.Release = "v1.2.3" }},
		{"build metadata", func(m *Manifest) { m.Release = "1.2.3+build" }},
		{"bad protocol", func(m *Manifest) { m.Protocol = 0 }},
		{"bad schema", func(m *Manifest) { m.Schema = 0 }},
		{"min protocol", func(m *Manifest) { m.MinProtocol = 2 }},
		{"bad min app", func(m *Manifest) { m.MinApp = "2.0.0" }},
		{"bad level", func(m *Manifest) { m.Level = "sometimes" }},
		{"long changelog", func(m *Manifest) { m.Changelog = strings.Repeat("x", 8193) }},
		{"no artifacts", func(m *Manifest) { m.Artifacts = nil }},
		{"duplicate artifacts", func(m *Manifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) }},
		{"bad artifact os", func(m *Manifest) { m.Artifacts[0].OS = "../linux" }},
		{"bad artifact url", func(m *Manifest) { m.Artifacts[0].URL = "http://example.com/a.tar.gz" }},
		{"bad artifact sha", func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("A", 64) }},
		{"bad artifact size", func(m *Manifest) { m.Artifacts[0].Size = 0 }},
		{"bad artifact format", func(m *Manifest) { m.Artifacts[0].Format = "rar" }},
		{"bad executable", func(m *Manifest) { m.Artifacts[0].Executable = "../toget" }},
		{"empty executable", func(m *Manifest) { m.Artifacts[0].Executable = "" }},
		{"bad sbom", func(m *Manifest) {
			m.SBOM = &FileRef{URL: "sbom/a.json", SHA256: strings.Repeat("b", 64), Size: 10}
			m.SBOM.Size = MaxSBOMBytes + 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := baseManifest(cert)
			tc.mutate(&manifest)
			raw := signManifestRaw(t, key, manifest)
			if _, err := verifier.VerifyManifest(raw, ""); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func TestVerifyManifestRequiresCertifiedKey(t *testing.T) {
	_, key, cert, _ := testFundingKeys(t)
	otherRoot, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewVerifier(otherRoot.Public).VerifyManifest(signManifestRaw(t, key, baseManifest(cert)), "1.2.3"); err == nil {
		t.Fatal("manifest verified under an unrelated root")
	}
}

func TestArchivedManifestUsesPublicationTimeCertificateWindow(t *testing.T) {
	root, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	release, err := GenerateSigningKey(KindRelease)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	cert, err := CertifyRelease(root, release.Public, now.Add(-72*time.Hour), now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget.tar.gz"), []testArchiveEntry{
		{Name: "toget", Body: []byte("binary"), Mode: 0o755},
	})
	published := now.Add(-60 * time.Hour)
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir: dir, Release: "1.0.0", Executable: "toget",
		Protocol: 1, Schema: 9, Level: LevelRecommended,
		PublishedAt: published, Key: release, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(root.Public)
	if _, err := verifier.VerifyManifest(mustJSON(t, manifest), "1.0.0"); err != nil {
		t.Fatalf("archived manifest with an expired certificate was rejected: %v", err)
	}
	channel, err := BuildChannel(BuildChannelOptions{
		Channel: "stable", Release: "1.0.0", ManifestRef: "v1.0.0.json",
		Rollout: 100, PublishedAt: published, Key: release, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyChannel(mustJSON(t, channel), "stable"); err == nil {
		t.Fatal("channel signed by an expired certificate was accepted")
	}
	if _, err := BuildManifest(BuildManifestOptions{
		Dir: dir, Release: "1.0.0", Executable: "toget",
		Protocol: 1, Schema: 9, Level: LevelRecommended,
		PublishedAt: now, Key: release, Cert: cert,
	}); err == nil {
		t.Fatal("manifest was signed outside the certificate window")
	}
}

func TestVerifyChannel(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	channel, err := BuildChannel(BuildChannelOptions{
		Channel:     "beta",
		Release:     "1.2.3-beta.1",
		ManifestRef: "https://releases.example/manifests/v1.2.3-beta.1.json",
		Rollout:     25,
		PublishedAt: time.Now().UTC().Add(-time.Minute),
		Key:         key,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, channel)
	parsed, err := verifier.VerifyChannel(raw, "beta")
	if err != nil {
		t.Fatalf("valid channel rejected: %v", err)
	}
	if parsed.Rollout != 25 {
		t.Fatal("rollout not preserved")
	}
	if _, err := verifier.VerifyChannel(raw, "stable"); err == nil {
		t.Fatal("channel accepted under the wrong name")
	}
	for name, mutate := range map[string]func(*Channel){
		"bad name":       func(c *Channel) { c.Channel = "Beta" },
		"bad release":    func(c *Channel) { c.Release = "1.2.3+meta" },
		"bad manifest":   func(c *Channel) { c.Manifest = "../manifest.json" },
		"bad rollout":    func(c *Channel) { c.Rollout = 0 },
		"future publish": func(c *Channel) { c.PublishedAt = formatTime(time.Now().Add(time.Hour)) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *channel
			mutate(&changed)
			changed.Signature = ""
			if err := SignChannel(key, &changed); err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.VerifyChannel(mustJSON(t, changed), ""); err == nil {
				t.Fatal("invalid channel was accepted")
			}
		})
	}
}

func TestChannelSignatureTampering(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	channel, err := BuildChannel(BuildChannelOptions{
		Channel: "stable", Release: "1.2.3",
		ManifestRef: "https://releases.example/manifests/v1.2.3.json",
		Rollout:     100, PublishedAt: time.Now().UTC(), Key: key, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel.Rollout = 50
	if _, err := verifier.VerifyChannel(mustJSON(t, channel), "stable"); err == nil {
		t.Fatal("modified channel was accepted")
	}
}
