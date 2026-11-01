package site

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DownloadPrefix is where release archives are published on the official
// origin. The install scripts fetch their version pointer and archives here.
const DownloadPrefix = "/downloads/"

const downloadImmutableCache = "public, max-age=31536000, immutable"

// WithDownloads mounts a read-only release directory at /downloads/ in front of
// next. Only flat, visible file names are served: no subdirectories, no hidden
// files, and no symlinks that escape the directory.
func WithDownloads(next http.Handler, dir string) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("download handler requires a fallback handler")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("download directory is empty")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("download directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("download directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("download path is not a directory: %s", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("download directory: %w", err)
	}
	root = resolved
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, DownloadPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		serveDownload(w, r, root)
	}), nil
}

func serveDownload(w http.ResponseWriter, r *http.Request, root string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, DownloadPrefix)
	if !validDownloadName(name) {
		http.NotFound(w, r)
		return
	}
	target := filepath.Join(root, name)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "download unavailable", http.StatusInternalServerError)
		return
	}
	if resolved != filepath.Join(root, name) || !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "download unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	contentType := downloadContentType(name)
	w.Header().Set("Content-Type", contentType)
	if name == "latest" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", downloadImmutableCache)
	}
	switch path.Ext(name) {
	case ".gz", ".zip":
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func validDownloadName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func downloadContentType(name string) string {
	switch {
	case name == "latest" || strings.HasSuffix(name, ".sha256"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".tar.gz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".zip"):
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}
