// Package directory implements the optional public Toget directory. It maps
// short server codes to signed node manifests. Every record is verified
// before it is stored and again before it is returned, so the directory never
// becomes an authority that can redirect clients to an unverified endpoint.
package directory

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/directoryproto"
	"github.com/sidenor/toget/internal/identity"
	nodenet "github.com/sidenor/toget/internal/node"
)

const (
	DefaultListen = "0.0.0.0:7720"
	defaultTTL    = directoryproto.DefaultRecordTTL
	maxRecords    = 100000
)

// Options configures the public directory.
type Options struct {
	Listen    string
	Advertise string
	Paths     config.Paths
	// RelayURL is the default relay advertised to nodes configured with
	// relay "auto". It must be a pinned HTTPS URL such as
	// https://relay.example:7719#fp=<hex>.
	RelayURL   string
	Logger     *slog.Logger
	RecordTTL  time.Duration
	MaxRecords int
}

// Service is a running Toget directory.
type Service struct {
	opts       Options
	paths      config.Paths
	logger     *slog.Logger
	relayURL   string
	recordTTL  time.Duration
	maxRecords int

	mu          sync.Mutex
	records     map[string]*storedRecord
	byCode      map[string]string
	started     bool
	server      *http.Server
	listener    net.Listener
	cancel      context.CancelFunc
	url         string
	fingerprint string
	limiter     *limiter
}

type storedRecord struct {
	Kind             string          `json:"kind,omitempty"`
	ServerID         string          `json:"server_id"`
	ServerCode       string          `json:"server_code"`
	IdentityManifest json.RawMessage `json:"identity_manifest"`
	NodeManifest     json.RawMessage `json:"node_manifest,omitempty"`
	HostManifest     json.RawMessage `json:"host_manifest,omitempty"`
	IssuedAt         time.Time       `json:"issued_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
}

type storeFile struct {
	Version int                      `json:"version"`
	Records map[string]*storedRecord `json:"records"`
}

// New creates a directory, loading persisted records.
func New(opts Options) (*Service, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	listen := strings.TrimSpace(opts.Listen)
	if listen == "" {
		listen = DefaultListen
	}
	ttl := opts.RecordTTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	max := opts.MaxRecords
	if max <= 0 {
		max = maxRecords
	}
	relayURL := strings.TrimSpace(opts.RelayURL)
	if relayURL != "" {
		canonical, err := canonicalRelayURL(relayURL)
		if err != nil {
			return nil, err
		}
		relayURL = canonical
	}
	s := &Service{
		opts: opts, paths: opts.Paths, logger: logger, relayURL: relayURL,
		recordTTL: ttl, maxRecords: max,
		records: make(map[string]*storedRecord), byCode: make(map[string]string),
		limiter: newLimiter(),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func canonicalRelayURL(raw string) (string, error) {
	base, fingerprint, err := nodenet.ParsePinnedURL(raw)
	if err != nil {
		return "", fmt.Errorf("directory relay: %w", err)
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("directory relay must be an HTTPS URL")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" {
		return "", errors.New("directory relay must not contain a path or query")
	}
	return nodenet.NodeURL(strings.TrimSuffix(base, "/"), fingerprint), nil
}

// URL returns the pinned directory URL after Start.
func (s *Service) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

// Start begins serving.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("directory: already started")
	}
	s.mu.Unlock()
	host, _, err := net.SplitHostPort(s.opts.Listen)
	if err != nil {
		return fmt.Errorf("directory: invalid listen address: %w", err)
	}
	if err := os.MkdirAll(s.paths.Home, 0o700); err != nil {
		return err
	}
	material, err := nodenet.LoadOrCreateTLS(
		filepath.Join(s.paths.Home, "directory-tls-cert.pem"),
		filepath.Join(s.paths.Home, "directory-tls-key.pem"),
		[]string{host, s.opts.Advertise, nodenet.OutboundIP()},
	)
	if err != nil {
		return err
	}
	rawListener, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		return fmt.Errorf("directory: listen: %w", err)
	}
	hostPort, err := nodenet.AdvertiseHostPort(rawListener.Addr().String(), s.opts.Advertise)
	if err != nil {
		rawListener.Close()
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	s.mu.Lock()
	s.started = true
	s.cancel = cancel
	s.server = server
	s.listener = tls.NewListener(rawListener, &tls.Config{
		Certificates: []tls.Certificate{material.Certificate},
		MinVersion:   tls.VersionTLS13,
	})
	s.fingerprint = material.Fingerprint
	s.url = nodenet.NodeURL("https://"+hostPort, material.Fingerprint)
	listener := s.listener
	s.mu.Unlock()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("directory listener stopped", "error", err)
		}
	}()
	go func() {
		<-runCtx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(shutdownCtx)
	}()
	s.logger.Info("directory listening", "url", s.URL())
	return nil
}

// Close stops the directory.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel, server := s.cancel, s.server
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if server != nil {
		ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		return server.Shutdown(ctx)
	}
	return nil
}

// Handler returns the directory HTTP API.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+directoryproto.HealthPath, s.handleHealth)
	mux.HandleFunc("POST "+directoryproto.PublishPath, s.handlePublish)
	mux.HandleFunc("GET "+directoryproto.LookupPrefix+"{code}", s.handleLookup)
	mux.HandleFunc("GET "+directoryproto.RelayPath, s.handleRelay)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown directory endpoint")
	})
	return securityHeaders(mux)
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Service) handleRelay(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("relay:"+clientIP(r), 120, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if s.relayURL == "" {
		writeError(w, http.StatusNotFound, "no_relay", "this directory does not advertise a relay")
		return
	}
	writeJSON(w, http.StatusOK, directoryproto.RelayResponse{
		Protocol: directoryproto.Protocol, RelayURL: s.relayURL,
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
}

func (s *Service) handlePublish(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("publish:"+clientIP(r), 20, 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many publishes")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, directoryproto.MaxPublishBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not read the request")
		return
	}
	if len(body) > directoryproto.MaxPublishBody {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "publish body is too large")
		return
	}
	var req directoryproto.PublishRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid publish body")
		return
	}
	record, err := verifyPublish(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_manifest", err.Error())
		return
	}
	record.UpdatedAt = time.Now().UTC()
	if err := s.storeRecord(record); err != nil {
		var conflict *conflictError
		if errors.As(err, &conflict) {
			writeError(w, http.StatusConflict, conflict.code, conflict.message)
			return
		}
		s.logger.Error("could not store directory record", "server_id", record.ServerID, "error", err)
		writeError(w, http.StatusInternalServerError, "storage_failed", "could not store the record")
		return
	}
	writeJSON(w, http.StatusOK, directoryproto.PublishResponse{
		Protocol: directoryproto.Protocol, Kind: record.Kind, ServerID: record.ServerID,
		ServerCode: record.ServerCode, ExpiresAt: record.ExpiresAt,
	})
}

func (s *Service) handleLookup(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("lookup:"+clientIP(r), 240, 60) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many lookups")
		return
	}
	code := r.PathValue("code")
	if !validServerCode(code) {
		writeError(w, http.StatusBadRequest, "invalid_code", "server code must be 8 digits")
		return
	}
	response, err := s.Lookup(code)
	if err != nil {
		var lookupErr *lookupError
		if errors.As(err, &lookupErr) {
			writeError(w, lookupErr.status, lookupErr.code, lookupErr.message)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not look up the server")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// Lookup returns one verified directory record. It is exported for an account
// host that embeds the directory and resolves records without an HTTP hop.
func (s *Service) Lookup(code string) (*directoryproto.LookupResponse, error) {
	if !validServerCode(code) {
		return nil, &lookupError{status: http.StatusBadRequest, code: "invalid_code", message: "server code must be 8 digits"}
	}
	s.mu.Lock()
	serverID := s.byCode[code]
	record := s.records[serverID]
	s.mu.Unlock()
	if record == nil {
		return nil, &lookupError{status: http.StatusNotFound, code: "not_found", message: "server is not in the directory"}
	}
	im, err := identity.VerifyManifestJSON(record.IdentityManifest)
	if err != nil {
		return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored server manifest is invalid"}
	}
	serverKey, err := identity.DecodePublicKey(im.RootPubKey)
	if err != nil {
		return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored server key is invalid"}
	}
	if im.ServerID != record.ServerID || im.ServerCode != code {
		return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored record does not match its code"}
	}
	if time.Now().UTC().After(record.ExpiresAt) {
		return nil, &lookupError{status: http.StatusNotFound, code: "expired", message: "the server record has expired"}
	}
	response := &directoryproto.LookupResponse{
		Protocol: directoryproto.Protocol, Kind: record.Kind,
		IdentityManifest: record.IdentityManifest, UpdatedAt: record.UpdatedAt,
	}
	switch record.Kind {
	case directoryproto.RecordKindHost:
		hm, err := identity.VerifyHostManifest(record.HostManifest, serverKey)
		if err != nil {
			return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored host manifest is invalid"}
		}
		if hm.ServerID != im.ServerID || hm.ServerCode != im.ServerCode {
			return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored host record does not match its server identity"}
		}
		response.HostManifest = record.HostManifest
	case directoryproto.RecordKindNode:
		var nm nodenet.Manifest
		if err := json.Unmarshal(record.NodeManifest, &nm); err != nil {
			return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored node manifest is invalid"}
		}
		if err := nodenet.VerifyManifest(&nm, serverKey); err != nil {
			return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored node manifest is invalid"}
		}
		if nm.ServerID != im.ServerID || nm.ServerCode != code {
			return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored record does not match its code"}
		}
		response.NodeManifest = record.NodeManifest
		response.RelayURL = nm.RelayURL
	default:
		return nil, &lookupError{status: http.StatusInternalServerError, code: "corrupt_record", message: "stored record has an unknown kind"}
	}
	return response, nil
}

type lookupError struct {
	status  int
	code    string
	message string
}

func (e *lookupError) Error() string { return e.message }

func validServerCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func verifyPublish(req directoryproto.PublishRequest) (*storedRecord, error) {
	if err := directoryproto.ValidatePublish(&req); err != nil {
		return nil, err
	}
	im, err := identity.VerifyManifestJSON(req.IdentityManifest)
	if err != nil {
		return nil, fmt.Errorf("server manifest is invalid: %w", err)
	}
	serverKey, err := identity.DecodePublicKey(im.RootPubKey)
	if err != nil {
		return nil, err
	}
	record := &storedRecord{
		Kind: req.Kind, ServerID: im.ServerID, ServerCode: im.ServerCode,
		IdentityManifest: append(json.RawMessage(nil), req.IdentityManifest...),
	}
	switch req.Kind {
	case directoryproto.RecordKindHost:
		hm, err := identity.VerifyHostManifest(req.HostManifest, serverKey)
		if err != nil {
			return nil, fmt.Errorf("host manifest is invalid: %w", err)
		}
		if hm.ServerID != im.ServerID || hm.ServerCode != im.ServerCode {
			return nil, errors.New("host manifest does not match the server identity")
		}
		record.HostManifest = append(json.RawMessage(nil), req.HostManifest...)
		record.IssuedAt = hm.IssuedAt
		record.ExpiresAt = hm.ExpiresAt
	case directoryproto.RecordKindNode:
		var nm nodenet.Manifest
		if err := json.Unmarshal(req.NodeManifest, &nm); err != nil {
			return nil, errors.New("node manifest is invalid")
		}
		if err := nodenet.VerifyManifest(&nm, serverKey); err != nil {
			return nil, fmt.Errorf("node manifest is invalid: %w", err)
		}
		if nm.ServerID != im.ServerID || nm.ServerCode != im.ServerCode {
			return nil, errors.New("node manifest does not match the server identity")
		}
		parsed, err := url.Parse(nm.Endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, errors.New("node endpoint must be an HTTPS URL")
		}
		record.NodeManifest = append(json.RawMessage(nil), req.NodeManifest...)
		record.IssuedAt = nm.IssuedAt
		record.ExpiresAt = nm.ExpiresAt
	default:
		return nil, errors.New("unsupported directory record kind")
	}
	return record, nil
}

type conflictError struct {
	code    string
	message string
}

func (e *conflictError) Error() string { return e.message }

func (s *Service) storeRecord(record *storedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.records[record.ServerID]
	if existing != nil && record.IssuedAt.Before(existing.IssuedAt) {
		return &conflictError{code: "stale_manifest", message: "a newer node manifest is already published"}
	}
	if owner, ok := s.byCode[record.ServerCode]; ok && owner != record.ServerID {
		return &conflictError{code: "code_taken", message: "that server code belongs to another server"}
	}
	if existing == nil && len(s.records) >= s.maxRecords {
		return &conflictError{code: "directory_full", message: "the directory is full"}
	}
	var previous *storedRecord
	if existing != nil {
		copy := *existing
		previous = &copy
	}
	oldCode := ""
	if existing != nil {
		oldCode = existing.ServerCode
	}
	s.records[record.ServerID] = record
	if oldCode != "" && oldCode != record.ServerCode {
		delete(s.byCode, oldCode)
	}
	s.byCode[record.ServerCode] = record.ServerID
	if err := s.saveLocked(); err != nil {
		if previous != nil {
			s.records[record.ServerID] = previous
			if oldCode != "" {
				s.byCode[oldCode] = record.ServerID
			}
		} else {
			delete(s.records, record.ServerID)
		}
		delete(s.byCode, record.ServerCode)
		return err
	}
	return nil
}

// PublishHost stores a host record produced by a directory operator. It runs
// the same verification as the HTTP publish endpoint, so a locally registered
// host cannot bypass manifest checks.
func (s *Service) PublishHost(identityManifest, hostManifest json.RawMessage) error {
	record, err := verifyPublish(directoryproto.PublishRequest{
		Protocol: directoryproto.Protocol, Kind: directoryproto.RecordKindHost,
		IdentityManifest: identityManifest, HostManifest: hostManifest,
	})
	if err != nil {
		return err
	}
	record.UpdatedAt = time.Now().UTC()
	return s.storeRecord(record)
}

func (s *Service) recordPath() string {
	return filepath.Join(s.paths.Home, "directory-records.json")
}

func (s *Service) load() error {
	data, err := os.ReadFile(s.recordPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("directory: read records: %w", err)
	}
	var store storeFile
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("directory: parse records: %w", err)
	}
	if store.Records == nil {
		return nil
	}
	for id, record := range store.Records {
		if record == nil || record.ServerID != id || record.ServerCode == "" {
			return errors.New("directory: records file is inconsistent")
		}
		if record.Kind == "" {
			record.Kind = directoryproto.RecordKindNode
		}
		if record.Kind != directoryproto.RecordKindNode && record.Kind != directoryproto.RecordKindHost {
			return errors.New("directory: records file has an unknown record kind")
		}
		if record.ExpiresAt.IsZero() && record.Kind == directoryproto.RecordKindNode {
			var nm nodenet.Manifest
			if err := json.Unmarshal(record.NodeManifest, &nm); err == nil {
				record.ExpiresAt = nm.ExpiresAt
			}
		}
		if owner, exists := s.byCode[record.ServerCode]; exists && owner != id {
			return errors.New("directory: records file has a duplicate server code")
		}
		s.records[id] = record
		s.byCode[record.ServerCode] = id
	}
	return nil
}

func (s *Service) saveLocked() error {
	data, err := json.Marshal(storeFile{Version: 1, Records: s.records})
	if err != nil {
		return err
	}
	path := s.recordPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if recover() != nil {
				writeError(w, http.StatusInternalServerError, "internal", "internal directory error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"internal","message":"could not encode response"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body directoryproto.ErrorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

// limiter is a tiny per-key token bucket.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter() *limiter { return &limiter{buckets: make(map[string]*bucket)} }

func (l *limiter) allow(key string, burst, perMinute float64) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * perMinute / 60
	b.last = now
	if b.tokens > burst {
		b.tokens = burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
