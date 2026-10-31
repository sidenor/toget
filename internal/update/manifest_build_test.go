package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildManifest(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "daemon", "linux-amd64", "v1.2.3", "toget.tar.gz")
	writeTarGz(t, archive, []testArchiveEntry{{Name: "toget", Body: []byte("binary"), Mode: 0o755}})
	sbom := filepath.Join(dir, "sbom", "toget.spdx.json")
	writeFile(t, sbom, []byte(`{"spdxVersion":"SPDX-2.3"}`))
	_, key, cert, verifier := testFundingKeys(t)
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir:         dir,
		Release:     "1.2.3",
		BaseURL:     "https://releases.example",
		Executable:  "toget",
		Protocol:    1,
		Schema:      9,
		MinApp:      "1.0.0",
		MinProtocol: 1,
		Level:       LevelSecurity,
		Changelog:   "security fixes",
		PublishedAt: time.Now().UTC().Add(-time.Minute),
		SBOMPath:    sbom,
		Key:         key,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Release != "1.2.3" || manifest.Level != LevelSecurity {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if len(manifest.Artifacts) != 1 {
		t.Fatalf("artifacts = %d", len(manifest.Artifacts))
	}
	artifact := manifest.Artifacts[0]
	if artifact.OS != "linux" || artifact.Arch != "amd64" || artifact.Format != FormatTarGz {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	if artifact.URL != "https://releases.example/daemon/linux-amd64/v1.2.3/toget.tar.gz" {
		t.Fatalf("artifact url = %q", artifact.URL)
	}
	if artifact.Size <= 0 || len(artifact.SHA256) != 64 {
		t.Fatal("artifact hash or size missing")
	}
	if manifest.SBOM == nil || manifest.SBOM.URL != "https://releases.example/sbom/toget.spdx.json" {
		t.Fatalf("sbom = %+v", manifest.SBOM)
	}
	verified, err := verifier.VerifyManifest(mustJSON(t, manifest), "1.2.3")
	if err != nil {
		t.Fatalf("built manifest did not verify: %v", err)
	}
	if verified.Artifacts[0].SHA256 != artifact.SHA256 {
		t.Fatal("manifest changed after signing")
	}
}

func TestBuildManifestMultiplePlatformsSorted(t *testing.T) {
	dir := t.TempDir()
	for _, platform := range []string{"windows-amd64", "darwin-arm64", "linux-amd64"} {
		path := filepath.Join(dir, "daemon", platform, "v1.0.0", "toget.tar.gz")
		writeTarGz(t, path, []testArchiveEntry{{Name: "toget", Body: []byte(platform)}})
	}
	_, key, cert, _ := testFundingKeys(t)
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir: dir, Release: "1.0.0", Protocol: 1, Schema: 1,
		Level: LevelRecommended, Key: key, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sortedArtifactKeys(manifest), ",")
	want := "darwin/arm64,linux/amd64,windows/amd64"
	if got != want {
		t.Fatalf("artifact order = %q, want %q", got, want)
	}
	if manifest.Artifacts[2].Executable != "toget.exe" {
		t.Fatalf("windows executable = %q", manifest.Artifacts[2].Executable)
	}
}

func TestBuildManifestIgnoresOtherVersions(t *testing.T) {
	dir := t.TempDir()
	writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget-old.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("old")}})
	writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.1.0", "toget-new.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("new")}})
	writeTarGz(t, filepath.Join(dir, "daemon", "darwin-arm64", "v1.0.0", "toget-old.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("old")}})
	_, key, cert, _ := testFundingKeys(t)
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir: dir, Release: "1.1.0", Protocol: 1, Schema: 1,
		Level: LevelRecommended, Key: key, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v", manifest.Artifacts)
	}
	if manifest.Artifacts[0].URL != "daemon/linux-amd64/v1.1.0/toget-new.tar.gz" {
		t.Fatalf("artifact = %+v", manifest.Artifacts[0])
	}
}

func TestBuildManifestErrors(t *testing.T) {
	_, key, cert, _ := testFundingKeys(t)
	base := func(dir string) BuildManifestOptions {
		return BuildManifestOptions{
			Dir: dir, Release: "1.0.0", Protocol: 1, Schema: 1,
			Level: LevelRecommended, Key: key, Cert: cert,
		}
	}
	t.Run("wrong layout", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "daemon", "toget.tar.gz"), []byte("x"))
		if _, err := BuildManifest(base(dir)); err == nil {
			t.Fatal("misplaced archive accepted")
		}
	})
	t.Run("duplicate platform", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"a.tar.gz", "b.tar.gz"} {
			writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", name), []testArchiveEntry{{Name: "toget", Body: []byte(name)}})
		}
		if _, err := BuildManifest(base(dir)); err == nil {
			t.Fatal("duplicate platform accepted")
		}
	})
	t.Run("no archives", func(t *testing.T) {
		if _, err := BuildManifest(base(t.TempDir())); err == nil {
			t.Fatal("empty release accepted")
		}
	})
	t.Run("invalid level", func(t *testing.T) {
		dir := t.TempDir()
		writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("x")}})
		opts := base(dir)
		opts.Level = "eventual"
		if _, err := BuildManifest(opts); err == nil {
			t.Fatal("invalid level accepted")
		}
	})
	t.Run("invalid release", func(t *testing.T) {
		dir := t.TempDir()
		writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("x")}})
		opts := base(dir)
		opts.Release = "1.0.0+build"
		if _, err := BuildManifest(opts); err == nil {
			t.Fatal("release with build metadata accepted")
		}
	})
	t.Run("invalid executable", func(t *testing.T) {
		dir := t.TempDir()
		writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("x")}})
		opts := base(dir)
		opts.Executable = "../toget"
		if _, err := BuildManifest(opts); err == nil {
			t.Fatal("unsafe executable accepted")
		}
	})
	t.Run("unsupported extension", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget.rar"), []byte("x"))
		if _, err := BuildManifest(base(dir)); err == nil {
			t.Fatal("unsupported archive accepted")
		}
	})
}

func TestBuildChannelRoundTrip(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	channel, err := BuildChannel(BuildChannelOptions{
		Channel:     "beta",
		Release:     "2.0.0-beta.2",
		ManifestRef: "https://releases.example/manifests/v2.0.0-beta.2.json",
		Rollout:     10,
		PublishedAt: time.Now().UTC(),
		Key:         key,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	if channel.Rollout != 10 || channel.Channel != "beta" {
		t.Fatalf("unexpected channel: %+v", channel)
	}
	if _, err := verifier.VerifyChannel(mustJSON(t, channel), "beta"); err != nil {
		t.Fatalf("built channel did not verify: %v", err)
	}
	for name, mutate := range map[string]func(*BuildChannelOptions){
		"bad channel":  func(o *BuildChannelOptions) { o.Channel = "Beta" },
		"bad release":  func(o *BuildChannelOptions) { o.Release = "2.0.0+meta" },
		"bad manifest": func(o *BuildChannelOptions) { o.ManifestRef = "../manifest.json" },
		"bad rollout":  func(o *BuildChannelOptions) { o.Rollout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			opts := BuildChannelOptions{
				Channel: "beta", Release: "2.0.0-beta.2",
				ManifestRef: "https://releases.example/manifests/v2.0.0-beta.2.json",
				Rollout:     10, Key: key, Cert: cert,
			}
			mutate(&opts)
			if _, err := BuildChannel(opts); err == nil {
				t.Fatal("invalid channel built")
			}
		})
	}
}

func TestBuildManifestDoesNotOverwriteSignatures(t *testing.T) {
	dir := t.TempDir()
	writeTarGz(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.0.0", "toget.tar.gz"), []testArchiveEntry{{Name: "toget", Body: []byte("x")}})
	_, key, cert, _ := testFundingKeys(t)
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir: dir, Release: "1.0.0", Protocol: 1, Schema: 1,
		Level: LevelRecommended, Key: key, Cert: cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := manifest.Signature
	manifest.PublishedAt = formatTime(time.Now().UTC().Add(-2 * time.Hour))
	if err := SignManifest(key, manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Signature == first {
		t.Fatal("re-signing did not change the signature")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
