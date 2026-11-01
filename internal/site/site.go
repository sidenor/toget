// Package site serves the public Toget web app: static assets with an SPA
// fallback. Account host API routes and the reset page stay on their own
// handler, with their own stricter headers.
package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	contentSecurityPolicy = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"manifest-src 'self'; base-uri 'none'; form-action 'none'; " +
		"frame-ancestors 'none'"
	assetCache  = "public, max-age=31536000, immutable"
	publicCache = "public, max-age=3600"
)

// Handler serves one single-page web app from an fs.FS.
type Handler struct {
	assets    fs.FS
	index     []byte
	indexETag string
}

// NewHandler reads index.html once and prepares a static handler. It fails if
// the build is incomplete rather than turning every page load into a 404.
func NewHandler(assets fs.FS) (*Handler, error) {
	if assets == nil {
		return nil, errors.New("site assets are required")
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read site index: %w", err)
	}
	if len(index) == 0 {
		return nil, errors.New("site index is empty")
	}
	sum := sha256.Sum256(index)
	return &Handler{
		assets:    assets,
		index:     index,
		indexETag: `"` + hex.EncodeToString(sum[:16]) + `"`,
	}, nil
}

// NewRouter sends account host routes to api and every other GET or HEAD to the
// embedded site. Keeping the two handlers apart preserves the API's locked-down
// CSP and no-store policy without weakening the page's asset policy.
func NewRouter(api http.Handler, assets fs.FS) (http.Handler, error) {
	if api == nil {
		return nil, errors.New("API handler is required")
	}
	page, err := NewHandler(assets)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHostedPath(r.URL.Path) {
			api.ServeHTTP(w, r)
			return
		}
		page.ServeHTTP(w, r)
	}), nil
}

func isHostedPath(requestPath string) bool {
	return requestPath == "/host" || strings.HasPrefix(requestPath, "/host/") ||
		requestPath == "/reset" || strings.HasPrefix(requestPath, "/reset/")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPageHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name, ok := cleanRequestPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	relative := strings.TrimPrefix(name, "/")
	if relative == "" {
		h.serveIndex(w, r)
		return
	}
	if hasHiddenSegment(relative) {
		http.NotFound(w, r)
		return
	}

	file, err := h.assets.Open(relative)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			h.serveMissing(w, r, name)
			return
		}
		http.Error(w, "site unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, "site unavailable", http.StatusInternalServerError)
		return
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", contentType(name))
	switch {
	case strings.HasPrefix(name, "/assets/"):
		w.Header().Set("Cache-Control", assetCache)
	case name == "/index.html" || strings.HasPrefix(name, "/wasm/"):
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", publicCache)
	}

	var content io.ReadSeeker
	if seeker, ok := file.(io.ReadSeeker); ok {
		content = seeker
	} else {
		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "site unavailable", http.StatusInternalServerError)
			return
		}
		content = bytes.NewReader(data)
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), content)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", h.indexETag)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func (h *Handler) serveMissing(w http.ResponseWriter, r *http.Request, name string) {
	// Client-side routes have no extension. A missing file-looking path or a
	// missing asset is a real 404, not the app shell.
	if path.Ext(name) != "" || name == "/assets" || strings.HasPrefix(name, "/assets/") {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
}

func cleanRequestPath(requestPath string) (string, bool) {
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == "." || segment == ".." ||
			strings.ContainsRune(segment, 0) || strings.ContainsRune(segment, '\\') {
			return "", false
		}
	}
	return path.Clean("/" + requestPath), true
}

func hasHiddenSegment(relative string) bool {
	for _, segment := range strings.Split(relative, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".webmanifest":
		return "application/manifest+json"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".sh":
		return "text/x-shellscript; charset=utf-8"
	case ".ps1":
		return "text/plain; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".woff2":
		return "font/woff2"
	case ".wasm":
		return "application/wasm"
	case ".ico":
		return "image/x-icon"
	}
	if value := mime.TypeByExtension(path.Ext(name)); value != "" {
		return value
	}
	return "application/octet-stream"
}

func setPageHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=()")
}
