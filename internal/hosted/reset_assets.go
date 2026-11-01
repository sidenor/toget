//go:build !js

package hosted

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// resetAssets serves the static reset page. The WASM bundle is large, so it is
// gzip-compressed once on first use (and cached) for browsers that support
// it. Directory listings are disabled: only files that exist can be fetched.
type resetAssets struct {
	assets fs.FS

	mu        sync.Mutex
	gzipKey   string
	gzipBytes []byte
	gzipErr   error
}

func newResetAssets(assets fs.FS) http.Handler {
	return &resetAssets{assets: assets}
}

func (a *resetAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path.Clean("/" + r.URL.Path)
	if name == "/" {
		name = "/index.html"
	}
	if strings.Contains(name, "/.") || strings.HasPrefix(name, "/.") {
		http.NotFound(w, r)
		return
	}
	file, err := a.assets.Open(strings.TrimPrefix(name, "/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if name == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}

	if name == "/toget-reset.wasm" && acceptsGzip(r) {
		data, err := a.gzipWasm(info)
		if err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			if r.Method == http.MethodHead {
				return
			}
			_, _ = w.Write(data)
			return
		}
	}

	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
			return
		}
		_, _ = io.Copy(w, file)
		return
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), seeker)
}

// gzipWasm compresses the WASM bundle at the fastest level; the first reset
// page load pays a few hundred milliseconds, later loads get 4 MB instead of
// 14 MB. The cache key follows the file so a replaced --host-reset-dir asset is
// picked up.
func (a *resetAssets) gzipWasm(info fs.FileInfo) ([]byte, error) {
	key := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gzipKey == key {
		return a.gzipBytes, a.gzipErr
	}
	a.gzipKey = key
	a.gzipBytes = nil
	a.gzipErr = nil
	data, err := fs.ReadFile(a.assets, "toget-reset.wasm")
	if err != nil {
		a.gzipErr = err
		return nil, err
	}
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	if err != nil {
		a.gzipErr = err
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		a.gzipErr = err
		return nil, err
	}
	if err := writer.Close(); err != nil {
		a.gzipErr = err
		return nil, err
	}
	a.gzipBytes = buffer.Bytes()
	return a.gzipBytes, nil
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(part, ";")
		if len(fields) == 0 || !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		quality := 1.0
		for _, parameter := range fields[1:] {
			parameter = strings.TrimSpace(parameter)
			if !strings.HasPrefix(parameter, "q=") {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(parameter, "q=")), 64); err == nil {
				quality = parsed
			}
		}
		if quality > 0 {
			return true
		}
	}
	return false
}
