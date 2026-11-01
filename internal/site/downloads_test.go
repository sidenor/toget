package site

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDownloadHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"toget_0.1.0_linux_amd64.tar.gz":        "archive-bytes",
		"toget_0.1.0_linux_amd64.tar.gz.sha256": "abc123  toget_0.1.0_linux_amd64.tar.gz\n",
		"toget_0.1.0_windows_amd64.zip":         "zip-bytes",
		"latest":                                "0.1.0\n",
		".hidden":                               "secret",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "file"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("fell through"))
	})
	handler, err := WithDownloads(fallback, dir)
	if err != nil {
		t.Fatalf("WithDownloads: %v", err)
	}
	return handler, dir
}

func TestDownloadsServesArchivesAndPointer(t *testing.T) {
	handler, _ := newDownloadHandler(t)
	cases := []struct {
		target      string
		body        string
		contentType string
		cache       string
		disposition string
	}{
		{
			target:      "/downloads/toget_0.1.0_linux_amd64.tar.gz",
			body:        "archive-bytes",
			contentType: "application/gzip",
			cache:       downloadImmutableCache,
			disposition: `attachment; filename="toget_0.1.0_linux_amd64.tar.gz"`,
		},
		{
			target:      "/downloads/toget_0.1.0_windows_amd64.zip",
			body:        "zip-bytes",
			contentType: "application/zip",
			cache:       downloadImmutableCache,
			disposition: `attachment; filename="toget_0.1.0_windows_amd64.zip"`,
		},
		{
			target:      "/downloads/latest",
			body:        "0.1.0\n",
			contentType: "text/plain; charset=utf-8",
			cache:       "no-cache",
		},
		{
			target:      "/downloads/toget_0.1.0_linux_amd64.tar.gz.sha256",
			body:        "abc123  toget_0.1.0_linux_amd64.tar.gz\n",
			contentType: "text/plain; charset=utf-8",
			cache:       downloadImmutableCache,
		},
	}
	for _, tc := range cases {
		recorder := request(handler, http.MethodGet, tc.target, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.target, recorder.Code)
		}
		if got := recorder.Body.String(); got != tc.body {
			t.Fatalf("%s: body = %q, want %q", tc.target, got, tc.body)
		}
		if got := recorder.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("%s: Content-Type = %q, want %q", tc.target, got, tc.contentType)
		}
		if got := recorder.Header().Get("Cache-Control"); got != tc.cache {
			t.Fatalf("%s: Cache-Control = %q, want %q", tc.target, got, tc.cache)
		}
		if got := recorder.Header().Get("Content-Disposition"); got != tc.disposition {
			t.Fatalf("%s: Content-Disposition = %q, want %q", tc.target, got, tc.disposition)
		}
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q", tc.target, got)
		}
	}
}

func TestDownloadsRejectsUnsafeNames(t *testing.T) {
	handler, dir := newDownloadHandler(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.tar.gz")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	for _, target := range []string{
		"/downloads/",
		"/downloads/../latest",
		"/downloads/%2e%2e%2flatest",
		"/downloads/nested/file",
		"/downloads/.hidden",
		"/downloads/escape.tar.gz",
		"/downloads/name%20with%20spaces",
	} {
		recorder := request(handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "outside") {
			t.Fatalf("%s: served content outside the download root", target)
		}
	}
}

func TestDownloadsMethodAndFallthrough(t *testing.T) {
	handler, _ := newDownloadHandler(t)
	recorder := request(handler, http.MethodPost, "/downloads/latest", nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
	recorder = request(handler, http.MethodGet, "/agents", nil)
	if recorder.Code != http.StatusTeapot || recorder.Body.String() != "fell through" {
		t.Fatalf("fallback status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	recorder = request(handler, http.MethodHead, "/downloads/toget_0.1.0_linux_amd64.tar.gz", nil)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("HEAD status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestWithDownloadsValidatesDirectory(t *testing.T) {
	fallback := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if _, err := WithDownloads(nil, t.TempDir()); err == nil {
		t.Fatal("WithDownloads(nil) succeeded")
	}
	if _, err := WithDownloads(fallback, ""); err == nil {
		t.Fatal("WithDownloads with an empty directory succeeded")
	}
	if _, err := WithDownloads(fallback, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("WithDownloads with a missing directory succeeded")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := WithDownloads(fallback, file); err == nil {
		t.Fatal("WithDownloads with a file succeeded")
	}
}
