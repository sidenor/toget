package site

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><html><body><div id=\"app-root\">Toget</div></body></html>"),
		},
		"assets/app-abc123.js": &fstest.MapFile{
			Data: []byte("console.log('toget')"),
		},
		"assets/app-def456.css": &fstest.MapFile{
			Data: []byte("body{background:#fff}"),
		},
		"favicon.svg": &fstest.MapFile{
			Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"),
		},
		"manifest.webmanifest": &fstest.MapFile{
			Data: []byte(`{"name":"Toget"}`),
		},
		"secret.txt": &fstest.MapFile{
			Data: []byte("do not serve this by traversal"),
		},
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := NewHandler(testAssets())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func request(handler http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestIndexAndClientRoutes(t *testing.T) {
	handler := newTestHandler(t)
	for _, target := range []string{"/", "/chats/42", "/chats/42?tab=files", "/index.html"} {
		recorder := request(handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", target, recorder.Code)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `id="app-root"`) {
			t.Fatalf("%s: body does not contain app shell: %q", target, body)
		}
		if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("%s: Content-Type = %q", target, got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache", target, got)
		}
		assertPageHeaders(t, recorder)
	}
}

func TestIndexETag(t *testing.T) {
	handler := newTestHandler(t)
	first := request(handler, http.MethodGet, "/", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("index response has no ETag")
	}
	second := request(handler, http.MethodGet, "/", map[string]string{"If-None-Match": etag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("conditional index status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", second.Body.String())
	}
	assertPageHeaders(t, second)
}

func TestHashedAssets(t *testing.T) {
	handler := newTestHandler(t)
	cases := []struct {
		target      string
		contentType string
		cache       string
		body        string
	}{
		{"/assets/app-abc123.js", "text/javascript; charset=utf-8", assetCache, "console.log('toget')"},
		{"/assets/app-def456.css", "text/css; charset=utf-8", assetCache, "body{background:#fff}"},
		{"/favicon.svg", "image/svg+xml", publicCache, "<svg"},
		{"/manifest.webmanifest", "application/manifest+json", publicCache, `"Toget"`},
	}
	for _, test := range cases {
		recorder := request(handler, http.MethodGet, test.target, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", test.target, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != test.contentType {
			t.Fatalf("%s: Content-Type = %q, want %q", test.target, got, test.contentType)
		}
		if got := recorder.Header().Get("Cache-Control"); got != test.cache {
			t.Fatalf("%s: Cache-Control = %q, want %q", test.target, got, test.cache)
		}
		if !strings.Contains(recorder.Body.String(), test.body) {
			t.Fatalf("%s: body = %q, want substring %q", test.target, recorder.Body.String(), test.body)
		}
		assertPageHeaders(t, recorder)
	}
}

func TestHead(t *testing.T) {
	handler := newTestHandler(t)
	for _, target := range []string{"/", "/assets/app-abc123.js"} {
		recorder := request(handler, http.MethodHead, target, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", target, recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("%s: HEAD body = %q, want empty", target, recorder.Body.String())
		}
		if recorder.Header().Get("Content-Length") == "" {
			t.Fatalf("%s: HEAD has no Content-Length", target)
		}
	}
}

func TestMissingFiles(t *testing.T) {
	handler := newTestHandler(t)
	for _, target := range []string{
		"/assets/missing.js",
		"/assets/nested/missing.css",
		"/missing.png",
		"/assets",
		"/.env",
		"/.well-known/security.txt",
		"/assets/../secret.txt",
		"/%2e%2e/secret.txt",
		"/assets/%2e%2e/secret.txt",
		"/assets%5c..%5csecret.txt",
	} {
		recorder := request(handler, http.MethodGet, target, nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "do not serve this by traversal") {
			t.Fatalf("%s: traversal exposed secret.txt", target)
		}
		assertPageHeaders(t, recorder)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	handler := newTestHandler(t)
	recorder := request(handler, http.MethodPost, "/", nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want %q", got, "GET, HEAD")
	}
	assertPageHeaders(t, recorder)
}

func TestRouterReservesHostAndReset(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-API-Path", r.URL.Path)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "api")
	})
	router, err := NewRouter(api, testAssets())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	for _, target := range []string{"/host", "/host/", "/host/v1/health", "/reset", "/reset/", "/reset/some/asset.js"} {
		recorder := request(router, http.MethodGet, target, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 from API", target, recorder.Code)
		}
		if recorder.Body.String() != "api" {
			t.Fatalf("%s: body = %q, want api", target, recorder.Body.String())
		}
		if got := recorder.Header().Get("X-API-Path"); got != target {
			t.Fatalf("%s: API path = %q", target, got)
		}
		if got := recorder.Header().Get("Content-Security-Policy"); got != "" {
			t.Fatalf("%s: site CSP leaked onto API response: %q", target, got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: API Cache-Control = %q", target, got)
		}
	}

	recorder := request(router, http.MethodGet, "/resetpage", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("/resetpage: status = %d, want site fallback", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `id="app-root"`) {
		t.Fatalf("/resetpage: body = %q, want site shell", recorder.Body.String())
	}
	assertPageHeaders(t, recorder)
}

func TestNewHandlerRequiresBuild(t *testing.T) {
	if _, err := NewHandler(nil); err == nil {
		t.Fatal("NewHandler(nil) succeeded")
	}
	if _, err := NewHandler(fstest.MapFS{}); err == nil {
		t.Fatal("NewHandler without index.html succeeded")
	}
	if _, err := NewHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: nil}}); err == nil {
		t.Fatal("NewHandler with empty index.html succeeded")
	}
	assets := fstest.MapFS{
		"index.html":            &fstest.MapFile{Data: []byte("<!doctype html>")},
		"assets/only-this.js":   &fstest.MapFile{Data: []byte("x")},
		"manifest.webmanifest":  &fstest.MapFile{Data: []byte(`{}`)},
		"unused/favicon.svg":    &fstest.MapFile{Data: []byte("<svg/>")},
		"unused/readme.txt":     &fstest.MapFile{Data: []byte("notes")},
		"unused/subfolder.json": &fstest.MapFile{Data: []byte(`{}`)},
	}
	if _, err := NewHandler(assets); err != nil {
		t.Fatalf("NewHandler valid assets: %v", err)
	}
}

func TestRouterRequiresAPI(t *testing.T) {
	if _, err := NewRouter(nil, testAssets()); err == nil {
		t.Fatal("NewRouter(nil API) succeeded")
	}
	if _, err := NewRouter(http.NotFoundHandler(), fstest.MapFS{}); err == nil {
		t.Fatal("NewRouter without site build succeeded")
	}
}

func TestContentTypeFallbacks(t *testing.T) {
	cases := map[string]string{
		"app.js":           "text/javascript; charset=utf-8",
		"app.css":          "text/css; charset=utf-8",
		"index.html":       "text/html; charset=utf-8",
		"icon.svg":         "image/svg+xml",
		"site.webmanifest": "application/manifest+json",
		"font.woff2":       "font/woff2",
		"data.json":        "application/json",
		"unknown.bin":      "application/octet-stream",
		"unknown":          "application/octet-stream",
	}
	for name, want := range cases {
		if got := contentType(name); got != want {
			t.Fatalf("contentType(%q) = %q, want %q", name, got, want)
		}
	}
}

func assertPageHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	header := recorder.Header()
	if got := header.Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Fatalf("Content-Security-Policy = %q, want %q", got, contentSecurityPolicy)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	if got := header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if got := header.Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Fatalf("Cross-Origin-Opener-Policy = %q", got)
	}
	if got := header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("Cross-Origin-Resource-Policy = %q", got)
	}
	if got := header.Get("Permissions-Policy"); got == "" {
		t.Fatal("Permissions-Policy is missing")
	}
}
