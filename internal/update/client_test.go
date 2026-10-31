package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/version"
)

type releaseServer struct {
	server   *httptest.Server
	dir      string
	release  testRelease
	root     *SigningKey
	key      *SigningKey
	cert     ReleaseKey
	verifier *Verifier
}

func newReleaseServer(t *testing.T, version string, binary []byte) *releaseServer {
	t.Helper()
	dir := t.TempDir()
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	root, key, cert, verifier := testFundingKeys(t)
	now := time.Now().UTC()
	verifier.Now = func() time.Time { return now }
	release := buildTestRelease(t, dir, version, server.URL, root, key, cert, binary)
	return &releaseServer{server: server, dir: dir, release: release, root: root, key: key, cert: cert, verifier: verifier}
}

func (r *releaseServer) client() *Client {
	return &Client{
		Verifier:   r.verifier,
		ChannelURL: r.server.URL + "/channels/stable.json",
		Channel:    "stable",
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Now:        time.Now,
	}
}

func TestClientCheckAvailable(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	result, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "device-rollout-id-1234", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Reason != ReasonAvailable {
		t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
	}
	if result.Artifact == nil || result.Artifact.OS != runtime.GOOS || result.Artifact.Arch != runtime.GOARCH {
		t.Fatalf("artifact = %+v", result.Artifact)
	}
	if result.Manifest.Release != "1.1.0" || result.ManifestSHA256 == "" {
		t.Fatal("manifest metadata missing")
	}
	if result.ManifestURL != rs.server.URL+"/manifests/v1.1.0.json" {
		t.Fatalf("manifest URL = %q", result.ManifestURL)
	}
	if result.RolloutPercent != 100 {
		t.Fatalf("rollout = %d", result.RolloutPercent)
	}
}

func TestClientCheckUpToDate(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	result, err := client.Check(context.Background(), Current{App: "1.1.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Reason != ReasonUpToDate {
		t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
	}
	if _, err := client.Check(context.Background(), Current{App: "1.1.0", Protocol: 1, Schema: 9, ManifestSHA256: strings.Repeat("f", 64)}, "rollout-id-123456", CheckOptions{}); err == nil {
		t.Fatal("republished release was accepted")
	}
}

func TestClientCheckDowngrade(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	result, err := client.Check(context.Background(), Current{App: "2.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Reason != ReasonNewerInstalled {
		t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
	}
	result, err = client.Check(context.Background(), Current{App: "2.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || !result.Downgrade || result.Reason != "downgrade" {
		t.Fatalf("downgrade result = %+v", result)
	}
	result, err = client.Check(context.Background(), Current{App: "2.0.0", Protocol: 1, Schema: version.Schema + 1}, "rollout-id-123456", CheckOptions{AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Reason != ReasonSchemaDowngrade {
		t.Fatalf("schema downgrade result = %+v", result)
	}
	result, err = client.Check(context.Background(), Current{App: "2.0.0", Protocol: 2, Schema: 9}, "rollout-id-123456", CheckOptions{AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Reason != ReasonProtocolDowngrade {
		t.Fatalf("protocol downgrade result = %+v", result)
	}
}

func (r *releaseServer) publishManifest(t *testing.T, release string, protocol, schema, minProtocol int, minApp string) {
	t.Helper()
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir:         r.dir,
		Release:     release,
		BaseURL:     r.server.URL,
		Executable:  "toget",
		Protocol:    protocol,
		Schema:      schema,
		MinApp:      minApp,
		MinProtocol: minProtocol,
		Level:       LevelRecommended,
		Changelog:   "test release " + release,
		PublishedAt: time.Now().UTC().Add(-time.Minute),
		Key:         r.key,
		Cert:        r.cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.dir, "manifests", "v"+release+".json")
	writeFile(t, path, append(mustJSON(t, manifest), '\n'))
}

func TestClientCheckCompatibility(t *testing.T) {
	check := func(t *testing.T, rs *releaseServer, current Current, opts CheckOptions) *CheckResult {
		t.Helper()
		result, err := rs.client().Check(context.Background(), current, "rollout-id-123456", opts)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	t.Run("minimum app", func(t *testing.T) {
		rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
		rs.publishManifest(t, "1.1.0", 1, 9, 1, "1.0.0")
		result := check(t, rs, Current{App: "0.9.0", Protocol: 1, Schema: 9}, CheckOptions{})
		if result.Available || result.Reason != ReasonAppTooOld {
			t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
		}
	})

	t.Run("minimum protocol", func(t *testing.T) {
		rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
		rs.publishManifest(t, "1.1.0", 2, 9, 2, "0.1.0")
		result := check(t, rs, Current{App: "1.0.0", Protocol: 1, Schema: 9}, CheckOptions{})
		if result.Available || result.Reason != ReasonProtocolTooOld {
			t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
		}
	})

	t.Run("forward protocol", func(t *testing.T) {
		rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
		rs.publishManifest(t, "1.1.0", 2, 9, 1, "0.1.0")
		result := check(t, rs, Current{App: "1.0.0", Protocol: 1, Schema: 9}, CheckOptions{})
		if !result.Available || result.Reason != ReasonAvailable {
			t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
		}
	})

	t.Run("schema downgrade", func(t *testing.T) {
		rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
		rs.publishManifest(t, "1.1.0", 1, 8, 1, "0.1.0")
		result := check(t, rs, Current{App: "1.0.0", Protocol: 1, Schema: 9}, CheckOptions{})
		if result.Available || result.Reason != ReasonSchemaDowngrade {
			t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
		}
	})

	t.Run("protocol downgrade on upgrade", func(t *testing.T) {
		rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
		rs.publishManifest(t, "1.1.0", 1, 9, 1, "0.1.0")
		result := check(t, rs, Current{App: "1.0.0", Protocol: 2, Schema: 9}, CheckOptions{})
		if result.Available || result.Reason != ReasonProtocolDowngrade {
			t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
		}
	})

	t.Run("schema upgrade warning", func(t *testing.T) {
		rs := newReleaseServer(t, "1.1.0", fakeBinaryWithSchema("1.1.0", 10))
		rs.publishManifest(t, "1.1.0", 1, 10, 1, "0.1.0")
		result := check(t, rs, Current{App: "1.0.0", Protocol: 1, Schema: 9}, CheckOptions{})
		if !result.Available || result.Reason != ReasonAvailable {
			t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
		}
		if !strings.Contains(result.Warning, "database schema") {
			t.Fatalf("warning = %q, want a schema-upgrade warning", result.Warning)
		}
	})
}

func TestClientCheckStagedRollout(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	channel, err := BuildChannel(BuildChannelOptions{
		Channel:     "stable",
		Release:     "1.1.0",
		ManifestRef: rs.server.URL + "/manifests/v1.1.0.json",
		Rollout:     1,
		PublishedAt: time.Now().UTC(),
		Key:         rs.key,
		Cert:        rs.cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(rs.dir, "channels", "stable.json"), mustJSON(t, channel))
	client := rs.client()
	excluded := ""
	for i := 0; i < 10000; i++ {
		candidate := fmt.Sprintf("rollout-id-%d", i)
		if rolloutBucket(candidate, "stable", "1.1.0") >= 1 {
			excluded = candidate
			break
		}
	}
	if excluded == "" {
		t.Fatal("could not find an excluded rollout identity")
	}
	result, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, excluded, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Reason != ReasonRollout {
		t.Fatalf("excluded result = available %v, reason %q", result.Available, result.Reason)
	}
	result, err = client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, excluded, CheckOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available {
		t.Fatal("forced rollout was not available")
	}
	if _, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "", CheckOptions{}); err == nil {
		t.Fatal("staged rollout without an identity was accepted")
	}
}

func TestClientCheckNoArtifact(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	client.OS = "plan9"
	result, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Reason != ReasonNoArtifact {
		t.Fatalf("result = available %v, reason %q", result.Available, result.Reason)
	}
}

func TestClientCheckRejectsInvalidChannel(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	client.Channel = "beta"
	if _, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{}); err == nil {
		t.Fatal("channel name mismatch was accepted")
	}
	client = rs.client()
	client.ChannelURL = "http://example.com/channels/stable.json"
	if _, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{}); err == nil {
		t.Fatal("non-loopback HTTP channel URL was accepted")
	}
	client = rs.client()
	client.ChannelURL = rs.server.URL + "/channels/missing.json"
	if _, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{}); err == nil {
		t.Fatal("missing channel was accepted")
	}
}

func TestClientCheckTamperedManifest(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	path := filepath.Join(rs.dir, "manifests", "v1.1.0.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := decodeStrict(data, &generic); err != nil {
		t.Fatal(err)
	}
	generic["release"] = "9.9.9"
	writeFile(t, path, mustJSON(t, generic))
	if _, err := rs.client().Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{}); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
}

func TestClientDownloadArtifact(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	result, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "toget.tar.gz")
	if _, err := client.DownloadArtifact(context.Background(), result, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != result.Artifact.Size || hashHex(data) != result.Artifact.SHA256 {
		t.Fatal("downloaded artifact does not match")
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("download mode = %o", info.Mode().Perm())
	}

	badDest := filepath.Join(t.TempDir(), "bad.tar.gz")
	result.Artifact.Size++
	if _, err := client.DownloadArtifact(context.Background(), result, badDest); err == nil {
		t.Fatal("size mismatch was accepted")
	}
	if _, err := os.Stat(badDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial download was left behind")
	}
}

func TestClientDownloadDetectsHashMismatch(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	client := rs.client()
	result, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(rs.dir, filepath.FromSlash(result.Artifact.URL[len(rs.server.URL)+1:]))
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	writeFile(t, artifactPath, data)
	dest := filepath.Join(t.TempDir(), "bad.tar.gz")
	if _, err := client.DownloadArtifact(context.Background(), result, dest); err == nil {
		t.Fatal("hash mismatch was accepted")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial download was left behind")
	}
}

func TestClientDownloadSBOM(t *testing.T) {
	dir := t.TempDir()
	sbom := []byte(`{"spdxVersion":"SPDX-2.3"}`)
	writeFile(t, filepath.Join(dir, "sbom", "toget.spdx.json"), sbom)
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer server.Close()
	client := &Client{}
	result := &CheckResult{
		ManifestURL: server.URL + "/manifests/v1.0.0.json",
		SBOM: &FileRef{
			URL:    server.URL + "/sbom/toget.spdx.json",
			SHA256: hashHex(sbom),
			Size:   int64(len(sbom)),
		},
	}
	dest := filepath.Join(t.TempDir(), "toget.spdx.json")
	ref, err := client.DownloadSBOM(context.Background(), result, dest)
	if err != nil {
		t.Fatal(err)
	}
	if ref == nil || ref.SHA256 != result.SBOM.SHA256 {
		t.Fatalf("SBOM reference = %+v", ref)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !sameBytes(data, sbom) {
		t.Fatal("downloaded SBOM does not match")
	}
	if ref, err := client.DownloadSBOM(context.Background(), &CheckResult{}, dest); err != nil || ref != nil {
		t.Fatalf("missing SBOM = %+v, err %v", ref, err)
	}
	result.SBOM.SHA256 = strings.Repeat("0", 64)
	if _, err := client.DownloadSBOM(context.Background(), result, filepath.Join(t.TempDir(), "bad.json")); err == nil {
		t.Fatal("SBOM hash mismatch was accepted")
	}
}

func TestClientRedirectPolicy(t *testing.T) {
	rs := newReleaseServer(t, "1.1.0", fakeBinary("1.1.0"))
	target := rs.server.URL + "/channels/stable.json"
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer redirector.Close()
	client := rs.client()
	client.ChannelURL = redirector.URL + "/channels/stable.json"
	if _, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{}); err != nil {
		t.Fatalf("loopback redirect rejected: %v", err)
	}

	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/channels/stable.json", http.StatusFound)
	}))
	defer external.Close()
	client = rs.client()
	client.ChannelURL = external.URL + "/channels/stable.json"
	if _, err := client.Check(context.Background(), Current{App: "1.0.0", Protocol: 1, Schema: 9}, "rollout-id-123456", CheckOptions{}); err == nil {
		t.Fatal("external redirect was followed")
	}
}
