package update

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "channels", "stable.json"), []byte(`{"format":"toget-release-channel"}`))
	writeFile(t, filepath.Join(dir, "manifests", "v1.2.3.json"), []byte(`{"format":"toget-release-manifest"}`))
	writeFile(t, filepath.Join(dir, "daemon", "linux-amd64", "v1.2.3", "toget.tar.gz"), []byte("archive-bytes"))
	writeFile(t, filepath.Join(dir, "sbom", "toget.spdx.json"), []byte(`{"spdxVersion":"SPDX-2.3"}`))
	writeFile(t, filepath.Join(dir, "state.json"), []byte("secret"))
	writeFile(t, filepath.Join(dir, "channels", ".hidden.json"), []byte("hidden"))
	service, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	return service, dir
}

func TestServiceServesReleaseFiles(t *testing.T) {
	service, _ := newTestService(t)
	handler := service.Handler()
	cases := []struct {
		path        string
		contentType string
		body        string
	}{
		{"/channels/stable.json", "application/json", `{"format":"toget-release-channel"}`},
		{"/manifests/v1.2.3.json", "application/json", `{"format":"toget-release-manifest"}`},
		{"/daemon/linux-amd64/v1.2.3/toget.tar.gz", "application/gzip", "archive-bytes"},
		{"/sbom/toget.spdx.json", "application/json", `{"spdxVersion":"SPDX-2.3"}`},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.path, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != tc.contentType {
			t.Errorf("%s: content type %q", tc.path, got)
		}
		if recorder.Body.String() != tc.body {
			t.Errorf("%s: body %q", tc.path, recorder.Body.String())
		}
		if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", tc.path)
		}
	}
}

func TestServiceCacheHeaders(t *testing.T) {
	service, _ := newTestService(t)
	handler := service.Handler()
	request := httptest.NewRequest(http.MethodGet, "/channels/stable.json", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=60") {
		t.Fatalf("channel cache control = %q", got)
	}
	request = httptest.NewRequest(http.MethodGet, "/manifests/v1.2.3.json", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("manifest cache control = %q", got)
	}
}

func TestServiceHEADAndRange(t *testing.T) {
	service, _ := newTestService(t)
	handler := service.Handler()
	request := httptest.NewRequest(http.MethodHead, "/daemon/linux-amd64/v1.2.3/toget.tar.gz", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("HEAD status %d, body %q", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/daemon/linux-amd64/v1.2.3/toget.tar.gz", nil)
	request.Header.Set("Range", "bytes=0-6")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d", recorder.Code)
	}
	if recorder.Body.String() != "archive" {
		t.Fatalf("range body = %q", recorder.Body.String())
	}
}

func TestServiceRejectsUnsafePaths(t *testing.T) {
	service, dir := newTestService(t)
	handler := service.Handler()
	paths := []string{
		"/",
		"/state.json",
		"/channels/",
		"/channels",
		"/channels/../state.json",
		"/channels/%2e%2e/state.json",
		"/channels/./stable.json",
		"/channels//stable.json",
		"/channels/.hidden.json",
		"/channels/.hidden.json/",
		"/other/stable.json",
		"/daemon",
		"/daemon/",
		"/daemon/linux-amd64/v1.2.3/",
		"/channels/stable.json/extra",
	}
	for _, path := range paths {
		request := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, recorder.Code)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "state.json"), filepath.Join(dir, "channels", "link.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/channels/link.json", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("symlink status = %d", recorder.Code)
	}
}

func TestServiceMethods(t *testing.T) {
	service, _ := newTestService(t)
	handler := service.Handler()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		request := httptest.NewRequest(method, "/channels/stable.json", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d", method, recorder.Code)
		}
		if recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", method, recorder.Header().Get("Allow"))
		}
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(""); err == nil {
		t.Fatal("empty directory accepted")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file, []byte("x"))
	if _, err := NewService(file); err == nil {
		t.Fatal("regular file accepted as a release directory")
	}
	if _, err := NewService(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestReleasePath(t *testing.T) {
	valid := map[string]string{
		"/channels/stable.json":                   "channels/stable.json",
		"/manifests/v1.2.3.json":                  "manifests/v1.2.3.json",
		"/daemon/linux-amd64/v1.2.3/toget.tar.gz": "daemon/linux-amd64/v1.2.3/toget.tar.gz",
		"/sbom/toget.spdx.json":                   "sbom/toget.spdx.json",
	}
	for raw, want := range valid {
		got, ok := releasePath(raw)
		if !ok || got != want {
			t.Errorf("releasePath(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	invalid := []string{
		"", "channels/stable.json", "/", "//channels/stable.json", "/channels//stable.json",
		"/channels/../stable.json", "/channels/./stable.json", "/channels/stable.json/",
		"/channels/stable.json\x00", `/channels/stable\\.json`, "/state.json", "/channels/.hidden",
	}
	for _, raw := range invalid {
		if _, ok := releasePath(raw); ok {
			t.Errorf("releasePath(%q) unexpectedly succeeded", raw)
		}
	}
}
