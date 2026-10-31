package api_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/update"
)

func writeTestTarGz(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: "toget", Mode: 0o755, Size: int64(len(body))}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newSignedReleaseServer serves a minimal signed channel for API tests.
func newSignedReleaseServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)

	root, err := update.GenerateSigningKey(update.KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	releaseKey, err := update.GenerateSigningKey(update.KindRelease)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cert, err := update.CertifyRelease(root, releaseKey.Public, now.Add(-time.Hour), now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte(fmt.Sprintf("#!/bin/sh\necho '{\"app\":\"1.1.0\",\"protocol\":1,\"schema\":9,\"os\":%q,\"arch\":%q}'\n",
		runtime.GOOS, runtime.GOARCH))
	archive := filepath.Join(dir, "daemon", runtime.GOOS+"-"+runtime.GOARCH, "v1.1.0", "toget.tar.gz")
	writeTestTarGz(t, archive, binary)

	manifest, err := update.BuildManifest(update.BuildManifestOptions{
		Dir:         dir,
		Release:     "1.1.0",
		BaseURL:     server.URL,
		Executable:  "toget",
		Protocol:    1,
		Schema:      9,
		MinApp:      "0.0.1",
		MinProtocol: 1,
		Level:       update.LevelRecommended,
		Changelog:   "API test release",
		PublishedAt: now.Add(-time.Minute),
		Key:         releaseKey,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dir, "manifests", "v1.1.0.json"), manifest)
	channel, err := update.BuildChannel(update.BuildChannelOptions{
		Channel:     "stable",
		Release:     "1.1.0",
		ManifestRef: server.URL + "/manifests/v1.1.0.json",
		Rollout:     100,
		PublishedAt: now.Add(-time.Minute),
		Key:         releaseKey,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dir, "channels", "stable.json"), channel)

	rootKey := base64.RawURLEncoding.EncodeToString(ed25519.PublicKey(root.Public))
	return server, rootKey
}

func newUpdateTestNode(t *testing.T) (*testNode, *update.Monitor) {
	t.Helper()
	releaseServer, rootKey := newSignedReleaseServer(t)
	home := t.TempDir()
	managedRoot := filepath.Join(t.TempDir(), "managed")
	monitor, err := update.NewMonitor(update.MonitorOptions{
		Home:       home,
		StatePath:  filepath.Join(home, "update-check.json"),
		Root:       managedRoot,
		Channel:    "stable",
		ChannelURL: releaseServer.URL + "/channels/stable.json",
		RootKey:    rootKey,
		Mode:       "notify",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	node := newTestNodeWithOptions(t, nil, func(opts *api.Options) {
		opts.Updates = monitor
	})
	return node, monitor
}

func TestUpdateAPIAvailableForLockedNode(t *testing.T) {
	node, _ := newUpdateTestNode(t)

	resp, _ := node.request(t, http.MethodGet, "/api/v1/update", "", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token update status = %d, want 401", resp.StatusCode)
	}

	resp, body := node.request(t, http.MethodGet, "/api/v1/update", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d %s", resp.StatusCode, body)
	}
	view := decode[api.UpdateView](t, body)
	if !view.Enabled || view.Mode != "notify" || view.Current.App == "" {
		t.Fatalf("update status = %s", body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/update/check", testToken, `{"force":true}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update check = %d %s", resp.StatusCode, body)
	}
	checked := decode[api.UpdateCheckView](t, body)
	if checked.Result == nil || !checked.Result.Available || checked.Result.Manifest.Release != "1.1.0" {
		t.Fatalf("update check = %s", body)
	}
	if checked.Update.Result == nil || checked.Update.LastCheck == nil {
		t.Fatalf("check did not update state: %s", body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/update/dismiss", testToken,
		`{"release":"1.1.0","hours":1}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss = %d %s", resp.StatusCode, body)
	}
	if view := decode[api.UpdateView](t, body); !view.Dismissed {
		t.Fatalf("dismiss view = %s", body)
	}
	resp, body = node.request(t, http.MethodDelete, "/api/v1/update/dismiss", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear dismissal = %d %s", resp.StatusCode, body)
	}
	if view := decode[api.UpdateView](t, body); view.Dismissed {
		t.Fatalf("clear dismissal view = %s", body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/update/apply", testToken,
		`{"release":"9.9.9"}`, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("apply unknown release = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/update/restart", testToken, "", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("restart without command = %d %s", resp.StatusCode, body)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/update/check", testToken, `{"nope":true}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown check field = %d, want 400", resp.StatusCode)
	}
}
