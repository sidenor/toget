//go:build !js

package hosted

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testResetAssets() http.Handler {
	return newResetAssets(fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html><title>reset</title>")},
		"main.js":          &fstest.MapFile{Data: []byte("console.log('reset')")},
		"toget-reset.wasm": &fstest.MapFile{Data: bytes.Repeat([]byte("wasm"), 2048)},
		".hidden":          &fstest.MapFile{Data: []byte("secret")},
		"nested/.keep":     &fstest.MapFile{Data: []byte("hidden")},
	})
}

func serveReset(t *testing.T, handler http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestResetAssets(t *testing.T) {
	handler := testResetAssets()

	index := serveReset(t, handler, http.MethodGet, "/", nil)
	if index.Code != http.StatusOK || index.Body.String() != "<!doctype html><title>reset</title>" {
		t.Fatalf("index = %d %q", index.Code, index.Body.String())
	}
	if got := index.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("index content type = %q", got)
	}
	if got := index.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index cache control = %q", got)
	}
	explicit := serveReset(t, handler, http.MethodGet, "/index.html", nil)
	if explicit.Code != http.StatusOK {
		t.Fatalf("explicit index = %d", explicit.Code)
	}

	plain := serveReset(t, handler, http.MethodGet, "/toget-reset.wasm", nil)
	if plain.Code != http.StatusOK || plain.Body.Len() != 8192 {
		t.Fatalf("plain wasm = %d %d bytes", plain.Code, plain.Body.Len())
	}
	if got := plain.Header().Get("Content-Type"); got != "application/wasm" {
		t.Fatalf("wasm content type = %q", got)
	}
	if got := plain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("plain wasm content encoding = %q", got)
	}

	compressed := serveReset(t, handler, http.MethodGet, "/toget-reset.wasm", map[string]string{
		"Accept-Encoding": "br, gzip;q=0.8",
	})
	if compressed.Code != http.StatusOK {
		t.Fatalf("compressed wasm = %d", compressed.Code)
	}
	if got := compressed.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("compressed content encoding = %q", got)
	}
	if got := compressed.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("vary = %q", got)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, bytes.Repeat([]byte("wasm"), 2048)) {
		t.Fatal("gzip body does not match the WASM file")
	}

	rejected := serveReset(t, handler, http.MethodGet, "/toget-reset.wasm", map[string]string{
		"Accept-Encoding": "gzip;q=0",
	})
	if got := rejected.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("gzip;q=0 content encoding = %q", got)
	}

	head := serveReset(t, handler, http.MethodHead, "/toget-reset.wasm", map[string]string{
		"Accept-Encoding": "gzip",
	})
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d %d bytes", head.Code, head.Body.Len())
	}
	if head.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD is missing Content-Length")
	}
	if got := head.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("HEAD content encoding = %q", got)
	}

	for _, target := range []string{"/.hidden", "/nested/.keep", "/missing.js", "/nested", "/../etc/passwd"} {
		response := serveReset(t, handler, http.MethodGet, target, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", target, response.Code)
		}
	}

	post := serveReset(t, handler, http.MethodPost, "/", nil)
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", post.Code)
	}
}
