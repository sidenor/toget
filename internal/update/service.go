package update

import (
	"errors"
	"net/http"
	"os"
	"path"
	"strings"
)

// Service serves a signed release tree over HTTP. It never lists directories
// and only exposes the documented release prefixes.
type Service struct {
	root *os.Root
}

// NewService opens dir as the root of a static release service.
func NewService(dir string) (*Service, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("release directory is empty")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("release directory is not a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Service{root: root}, nil
}

// Close releases the directory handle.
func (s *Service) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

// Handler returns the static release handler.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

var releasePrefixes = []string{"channels/", "manifests/", "daemon/", "sbom/"}

func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative, ok := releasePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	info, err := s.root.Lstat(relative)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	file, err := s.root.Open(relative)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(relative))
	w.Header().Set("Cache-Control", cacheControl(relative))
	http.ServeContent(w, r, path.Base(relative), opened.ModTime(), file)
}

func releasePath(requestPath string) (string, bool) {
	if !strings.HasPrefix(requestPath, "/") {
		return "", false
	}
	relative := strings.TrimPrefix(requestPath, "/")
	if relative == "" || len(relative) > MaxArchivePath {
		return "", false
	}
	if strings.ContainsAny(relative, "\x00\\") {
		return "", false
	}
	if strings.HasSuffix(relative, "/") {
		return "", false
	}
	cleaned := path.Clean(relative)
	if cleaned != relative {
		return "", false
	}
	allowed := false
	for _, prefix := range releasePrefixes {
		if strings.HasPrefix(cleaned, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
		if strings.HasPrefix(part, ".") {
			return "", false
		}
	}
	return cleaned, true
}

func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".sig"):
		return "application/json"
	case strings.HasSuffix(name, ".tar.gz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".zip"):
		return "application/zip"
	case strings.HasSuffix(name, ".txt"), strings.HasSuffix(name, ".sha256"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func cacheControl(name string) string {
	if strings.HasPrefix(name, "channels/") ||
		name == "manifests/index.json" || name == "manifests/index.json.sig" {
		return "public, max-age=60, must-revalidate"
	}
	return "public, max-age=31536000, immutable"
}
