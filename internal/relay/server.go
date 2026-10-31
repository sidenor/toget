package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/identity"
	nodenet "github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/relayproto"
)

const (
	maxRegisterBody  = 4 << 20
	maxRoutesBody    = 16 << 20
	maxResultBody    = relayproto.MaxResultWire
	registerTokenTTL = 15 * time.Minute
	targetTTL        = 7 * 24 * time.Hour
	maxTargets       = 200000
)

// Options configures a relay.
type Options struct {
	Listen          string
	Advertise       string
	Paths           config.Paths
	Parent          string
	Directory       http.Handler
	Logger          *slog.Logger
	QueueTTL        time.Duration
	ResponseTTL     time.Duration
	WaitTimeout     time.Duration
	PollWait        time.Duration
	SessionTTL      time.Duration
	MaxQueued       int
	MaxQueuedBytes  int64
	MaxPending      int
	MaxPendingBytes int64
}

// Service is a Toget relay. It forwards opaque signed requests to registered
// nodes, queues them while a node is offline, and can chain to a parent relay.
type Service struct {
	opts      Options
	logger    *slog.Logger
	paths     config.Paths
	advertise string
	parentURL string

	key     ed25519.PrivateKey
	relayID string

	mu          sync.RWMutex
	url         string
	endpoint    string
	fingerprint string
	server      *http.Server
	listener    net.Listener
	started     bool
	closeOnce   sync.Once
	cancel      context.CancelFunc

	nonces     *nodenet.NonceCache
	limit      *limiter
	sessionTTL time.Duration

	sessions     map[string]*session
	routes       map[string]*routeEntry
	targets      map[string]*targetEntry
	pending      map[string]*pendingRequest
	queued       int
	queuedBytes  int64
	pendingBytes int64

	parent       *parentLink
	dir          http.Handler
	announceWake chan struct{}
}

type session struct {
	id       string
	kind     string
	token    string
	frames   chan relayproto.Envelope
	lastSeen time.Time
	expires  time.Time
	pollMu   sync.Mutex
	routes   map[string]relayproto.RouteAnnouncement
}

type routeEntry struct {
	session *session
	kind    string
	hops    int
	via     []string
	expires time.Time
}

// targetEntry remembers a directly registered node after its session ends,
// so the relay can queue requests for it during a temporary outage. It holds
// only the public server ID and timestamps.
type targetEntry struct {
	kind         string
	registeredAt time.Time
	expires      time.Time
}

type pendingRequest struct {
	id        string
	target    string
	sessionID string
	envelope  relayproto.Envelope
	ch        chan relayproto.Result
	result    *relayproto.Result
	expires   time.Time
	resultTTL time.Time
	offline   bool
	delivered bool
	bodyBytes int64
}

// New creates a relay. The relay's own key is loaded or created in Paths.Home.
func New(opts Options) (*Service, error) {
	if opts.Paths.Home == "" {
		return nil, errors.New("relay: a data directory is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	key, err := loadOrCreateRelayKey(filepath.Join(opts.Paths.Home, "relay-key.json"))
	if err != nil {
		return nil, err
	}
	if opts.QueueTTL <= 0 {
		opts.QueueTTL = relayproto.DefaultQueueTTL
	}
	if opts.ResponseTTL <= 0 {
		opts.ResponseTTL = relayproto.DefaultResponseTTL
	}
	if opts.WaitTimeout <= 0 {
		opts.WaitTimeout = relayproto.DefaultWaitTimeout
	}
	if opts.PollWait <= 0 || opts.PollWait > relayproto.DefaultPollWait {
		opts.PollWait = relayproto.DefaultPollWait
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = relayproto.DefaultSessionTTL
	}
	if opts.MaxQueued <= 0 {
		opts.MaxQueued = 128
	}
	if opts.MaxQueuedBytes <= 0 {
		opts.MaxQueuedBytes = 16 << 20
	}
	if opts.MaxPending <= 0 {
		opts.MaxPending = 512
	}
	if opts.MaxPendingBytes <= 0 {
		opts.MaxPendingBytes = 128 << 20
	}
	var parentBase string
	if strings.TrimSpace(opts.Parent) != "" && strings.TrimSpace(opts.Parent) != "off" {
		base, fingerprint, err := nodenet.ParsePinnedURL(opts.Parent)
		if err != nil {
			return nil, fmt.Errorf("relay parent: %w", err)
		}
		parentBase = nodenet.NodeURL(base, fingerprint)
	}
	return &Service{
		opts: opts, logger: logger, paths: opts.Paths,
		advertise: strings.TrimSpace(opts.Advertise),
		key:       key, relayID: relayproto.RelayID(key.Public().(ed25519.PublicKey)),
		sessionTTL:   opts.SessionTTL,
		announceWake: make(chan struct{}, 1),
		parentURL:    parentBase,
		nonces:       nodenet.NewNonceCache(),
		limit:        newLimiter(240, 60),
		sessions:     make(map[string]*session),
		routes:       make(map[string]*routeEntry),
		targets:      make(map[string]*targetEntry),
		pending:      make(map[string]*pendingRequest),
		dir:          opts.Directory,
	}, nil
}

// RelayID returns this relay's self-certifying ID.
func (s *Service) RelayID() string {
	if s == nil {
		return ""
	}
	return s.relayID
}

// URL returns the pinned relay URL.
func (s *Service) URL() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.url
}

// Start binds the relay listener and starts the parent link, if configured.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("relay: already started")
	}
	s.mu.Unlock()

	listen := strings.TrimSpace(s.opts.Listen)
	if listen == "" {
		listen = "0.0.0.0:7719"
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("relay: invalid listen address: %w", err)
	}
	hosts := []string{host, s.advertise, nodenet.OutboundIP()}
	material, err := nodenet.LoadOrCreateTLS(
		filepath.Join(s.paths.Home, "relay-tls-cert.pem"),
		filepath.Join(s.paths.Home, "relay-tls-key.pem"),
		hosts,
	)
	if err != nil {
		return err
	}
	rawListener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("relay: listen: %w", err)
	}
	hostPort, err := nodenet.AdvertiseHostPort(rawListener.Addr().String(), s.advertise)
	if err != nil {
		rawListener.Close()
		return err
	}
	endpoint := "https://" + hostPort
	tlsListener := tls.NewListener(rawListener, &tls.Config{
		Certificates: []tls.Certificate{material.Certificate},
		MinVersion:   tls.VersionTLS13,
	})
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      150 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	s.mu.Lock()
	s.listener = tlsListener
	s.server = server
	s.endpoint = endpoint
	s.fingerprint = material.Fingerprint
	s.url = nodenet.NodeURL(endpoint, material.Fingerprint)
	s.started = true
	s.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	parent, err := newParentLink(runCtx, s, s.parentURL)
	if err != nil {
		tlsListener.Close()
		return err
	}
	s.mu.Lock()
	s.parent = parent
	s.mu.Unlock()

	go func() {
		if err := server.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("relay listener stopped", "error", err)
		}
	}()
	go s.cleanupLoop(runCtx)
	if parent != nil {
		go s.runParent(runCtx)
	}
	s.logger.Info("relay listening", "url", s.url, "relay_id", s.relayID)
	return nil
}

// Close stops the relay listener.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		s.mu.RLock()
		cancel := s.cancel
		server := s.server
		s.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		if server != nil {
			ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			err = server.Shutdown(ctx)
		}
	})
	return err
}

// Handler returns the relay HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/v1/health", s.handleHealth)
	mux.HandleFunc("GET /relay/v1/info", s.handleInfo)
	mux.HandleFunc("GET /relay/v1/node/{target}", s.handleTargetInfo)
	mux.HandleFunc("POST /relay/v1/register", s.handleRegister)
	mux.HandleFunc("POST /relay/v1/poll", s.handlePoll)
	mux.HandleFunc("POST /relay/v1/results/{id}", s.handleResult)
	mux.HandleFunc("POST /relay/v1/routes", s.handleRoutes)
	mux.HandleFunc("POST /relay/v1/forward/{target}", s.handleForward)
	mux.HandleFunc("GET /relay/v1/responses/{ticket}", s.handleQueuedResponse)
	if s.dir != nil {
		mux.Handle("/directory/", s.dir)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown relay endpoint")
	})
	return s.secure(mux)
}

func (s *Service) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("relay handler panic", "error", recovered)
				writeError(w, http.StatusInternalServerError, "internal", "internal relay error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Service) handleInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	info := relayproto.Info{
		Protocol:       relayproto.Protocol,
		RelayID:        s.relayID,
		URL:            s.url,
		TLSFingerprint: s.fingerprint,
		Parent:         s.parent != nil,
		Sessions:       s.liveSessionCountLocked(),
		Routes:         s.liveRouteCountLocked(),
		Queued:         s.queued,
		ServerTime:     time.Now().UTC(),
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, info)
}

func (s *Service) handleTargetInfo(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("target")
	if target == "" || len(target) > relayproto.MaxTarget {
		writeError(w, http.StatusBadRequest, "invalid_target", "target is invalid")
		return
	}
	s.mu.RLock()
	online := s.targetOnlineLocked(target)
	url := s.url
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, relayproto.TargetInfo{
		Protocol: relayproto.Protocol, Target: target, Online: online,
		RelayID: s.relayID, RelayURL: url, ServerTime: time.Now().UTC(),
	})
}

func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.limit.allow("register:"+clientIP(r), 30, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many registrations")
		return
	}
	body, err := readBody(w, r, maxRegisterBody)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req relayproto.RegisterRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid registration body")
		return
	}
	now := time.Now().UTC()
	var id, kind string
	switch req.Kind {
	case "node":
		im, err := identity.VerifyManifestJSON(req.IdentityManifest)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_manifest", "server manifest is invalid")
			return
		}
		serverPub, err := identity.DecodePublicKey(im.RootPubKey)
		if err != nil || im.ServerID != req.ServerID || im.ServerCode != req.ServerCode {
			writeError(w, http.StatusBadRequest, "invalid_manifest", "server identity does not match the registration")
			return
		}
		var nm nodenet.Manifest
		if err := json.Unmarshal(req.NodeManifest, &nm); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_manifest", "node manifest is invalid")
			return
		}
		if err := nodenet.VerifyManifest(&nm, serverPub); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_manifest", "node manifest signature is invalid")
			return
		}
		if nm.ServerID != im.ServerID || nm.ServerCode != im.ServerCode {
			writeError(w, http.StatusBadRequest, "invalid_manifest", "node manifest identity does not match")
			return
		}
		if err := relayproto.VerifyRegistration(req, serverPub, now, 5*time.Minute); err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		id, kind = req.ServerID, "node"
	case "relay":
		pub, err := base64.RawStdEncoding.DecodeString(req.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			writeError(w, http.StatusBadRequest, "invalid_key", "relay public key is invalid")
			return
		}
		publicKey := ed25519.PublicKey(pub)
		if relayproto.RelayID(publicKey) != req.RelayID || req.RelayID == "" {
			writeError(w, http.StatusBadRequest, "invalid_key", "relay ID does not match its key")
			return
		}
		if err := relayproto.VerifyRegistration(req, publicKey, now, 5*time.Minute); err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		id, kind = req.RelayID, "relay"
	default:
		writeError(w, http.StatusBadRequest, "invalid_kind", "registration kind is invalid")
		return
	}
	if err := s.nonces.Check(req.Nonce, now); err != nil {
		writeError(w, http.StatusConflict, "replayed_registration", "registration was already used")
		return
	}
	token := relayproto.NewToken()
	expires := now.Add(registerTokenTTL)
	sess := &session{
		id: id, kind: kind, token: token,
		frames:   make(chan relayproto.Envelope, 64),
		lastSeen: now, expires: expires,
		routes: make(map[string]relayproto.RouteAnnouncement),
	}
	s.mu.Lock()
	if kind == "node" {
		if existing := s.targets[id]; existing == nil && len(s.targets) >= maxTargets {
			for key, entry := range s.targets {
				if now.After(entry.expires) {
					delete(s.targets, key)
				}
			}
			if len(s.targets) >= maxTargets {
				s.mu.Unlock()
				writeError(w, http.StatusServiceUnavailable, "relay_full", "the relay has too many node registrations")
				return
			}
		}
		s.targets[id] = &targetEntry{kind: "node", registeredAt: now, expires: now.Add(targetTTL)}
	}
	if old := s.sessions[id]; old != nil {
		// A new registration supersedes the old token but keeps queued
		// requests; they are addressed to the identity, not the session.
		old.expires = now
	}
	s.sessions[id] = sess
	s.mu.Unlock()
	if kind == "node" && s.parent != nil {
		select {
		case s.announceWake <- struct{}{}:
		default:
		}
	}
	writeJSON(w, http.StatusOK, relayproto.RegisterResponse{
		Protocol: relayproto.Protocol, Token: token, ExpiresAt: expires,
		RelayID: s.relayID, RelayURL: s.URL(), Parent: s.parent != nil,
	})
}

func (s *Service) handlePoll(w http.ResponseWriter, r *http.Request) {
	sess := s.authorizedSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "relay session is invalid or expired")
		return
	}
	sess.pollMu.Lock()
	defer sess.pollMu.Unlock()

	now := time.Now().UTC()
	s.mu.Lock()
	sess.lastSeen = now
	var batch []relayproto.Envelope
	if sess.kind == "node" {
		batch = s.takeQueuedLocked(sess.id, 16)
	}
	s.mu.Unlock()
	if len(batch) > 0 {
		writeJSON(w, http.StatusOK, relayproto.PollResponse{
			Protocol: relayproto.Protocol, Frames: batch, ServerTime: now,
		})
		return
	}

	timer := time.NewTimer(s.opts.PollWait)
	defer timer.Stop()
	for len(batch) < 16 {
		select {
		case frame := <-sess.frames:
			batch = append(batch, frame)
			// Drain anything already waiting without another timer cycle.
			for len(batch) < 16 {
				select {
				case frame := <-sess.frames:
					batch = append(batch, frame)
				default:
					goto drained
				}
			}
		drained:
			if len(batch) > 0 {
				writeJSON(w, http.StatusOK, relayproto.PollResponse{
					Protocol: relayproto.Protocol, Frames: batch, ServerTime: time.Now().UTC(),
				})
				return
			}
		case <-timer.C:
			writeJSON(w, http.StatusOK, relayproto.PollResponse{
				Protocol: relayproto.Protocol, Frames: nil, ServerTime: time.Now().UTC(),
			})
			return
		case <-r.Context().Done():
			return
		}
	}
	writeJSON(w, http.StatusOK, relayproto.PollResponse{
		Protocol: relayproto.Protocol, Frames: batch, ServerTime: time.Now().UTC(),
	})
}

func (s *Service) handleResult(w http.ResponseWriter, r *http.Request) {
	sess := s.authorizedSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "relay session is invalid")
		return
	}
	body, err := readBody(w, r, maxResultBody)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var result relayproto.Result
	if err := json.Unmarshal(body, &result); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid result body")
		return
	}
	if result.Status < 100 || result.Status > 599 {
		writeError(w, http.StatusBadRequest, "invalid_result", "result status is invalid")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request id is required")
		return
	}
	if !s.finishPending(id, sess.id, result) {
		writeError(w, http.StatusNotFound, "request_not_found", "no request is waiting for this result")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleRoutes(w http.ResponseWriter, r *http.Request) {
	sess := s.authorizedSession(r)
	if sess == nil || sess.kind != "relay" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "relay session is invalid")
		return
	}
	body, err := readBody(w, r, maxRoutesBody)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req relayproto.RoutesRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Protocol != relayproto.Protocol {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid route announcement")
		return
	}
	if len(req.Routes) > 10000 {
		writeError(w, http.StatusBadRequest, "invalid_routes", "too many routes")
		return
	}
	now := time.Now().UTC()
	keep := make(map[string]bool)
	s.mu.Lock()
	for _, ann := range req.Routes {
		if err := s.validateAnnouncementLocked(ann, sess); err != nil {
			continue
		}
		s.routes[ann.Target] = &routeEntry{
			session: sess, kind: ann.Kind, hops: len(ann.Via),
			via: append([]string(nil), ann.Via...), expires: now.Add(2 * s.sessionTTL),
		}
		keep[ann.Target] = true
	}
	for target, entry := range s.routes {
		if entry.session == sess && !keep[target] {
			delete(s.routes, target)
		}
	}
	count := len(req.Routes)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "routes": count})
}

func (s *Service) validateAnnouncementLocked(ann relayproto.RouteAnnouncement, sess *session) error {
	if ann.Target == "" || len(ann.Target) > relayproto.MaxTarget {
		return errors.New("route target is invalid")
	}
	if ann.Target == s.relayID || ann.Target == sess.id {
		return errors.New("route points at a relay")
	}
	if ann.Kind != "node" && ann.Kind != "relay" {
		return errors.New("route kind is invalid")
	}
	if len(ann.Via) == 0 || len(ann.Via) > relayproto.MaxHops {
		return errors.New("route path is invalid")
	}
	if ann.Via[0] != sess.id {
		return errors.New("route path does not start at the announcing relay")
	}
	seen := map[string]bool{}
	for _, hop := range ann.Via {
		if hop == "" || seen[hop] {
			return errors.New("route path is duplicated")
		}
		if hop == s.relayID {
			return errors.New("route path loops through this relay")
		}
		seen[hop] = true
	}
	if s.sessions[ann.Target] != nil {
		// A direct registration always wins over a remote announcement.
		return errors.New("route target is directly registered")
	}
	return nil
}

func (s *Service) handleForward(w http.ResponseWriter, r *http.Request) {
	if !s.limit.allow("forward:"+clientIP(r), 600, 200) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many relayed requests")
		return
	}
	target := r.PathValue("target")
	if target == "" || len(target) > relayproto.MaxTarget {
		writeError(w, http.StatusBadRequest, "invalid_target", "target is invalid")
		return
	}
	body, err := readBody(w, r, relayproto.MaxEnvelopeWire)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var env relayproto.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid relay envelope")
		return
	}
	if env.Target != target {
		writeError(w, http.StatusBadRequest, "invalid_target", "target does not match the envelope")
		return
	}
	if err := relayproto.ValidateEnvelope(&env); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_envelope", err.Error())
		return
	}
	now := time.Now().UTC()
	env.Protocol = relayproto.Protocol
	env.RequestID = relayproto.NewRequestID()
	env.CreatedAt = now
	if env.ExpiresAt.IsZero() || env.ExpiresAt.After(now.Add(s.opts.QueueTTL)) {
		env.ExpiresAt = now.Add(s.opts.QueueTTL)
	}

	direct := s.lookupDirect(target)
	if direct != nil && !s.sessionLive(direct, now) {
		s.queueOrError(w, &env, direct.id)
		return
	}
	if direct == nil {
		route, _ := s.lookupRoute(target, now)
		if route == nil {
			if s.knownNode(target, now) {
				s.queueOrError(w, &env, target)
				return
			}
			if s.parent != nil {
				result := s.forwardViaParent(r.Context(), &env)
				s.writeResult(w, result)
				return
			}
			writeError(w, http.StatusNotFound, "route_not_found", "no relay route to this node")
			return
		}
		if !s.sessionLive(route.session, now) {
			writeError(w, http.StatusServiceUnavailable, "relay_offline", "the next relay is offline")
			return
		}
	}
	result := s.forwardLocal(r.Context(), &env, true)
	s.writeResult(w, result)
}

func (s *Service) handleQueuedResponse(w http.ResponseWriter, r *http.Request) {
	ticket := r.PathValue("ticket")
	if ticket == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "ticket is required")
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	p := s.pending[ticket]
	if p == nil || !p.offline {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "ticket_not_found", "relay ticket was not found")
		return
	}
	if p.result != nil {
		result := *p.result
		s.mu.Unlock()
		writeResult(w, result, s.relayID)
		return
	}
	if now.After(p.expires) {
		delete(s.pending, ticket)
		s.queued--
		s.queuedBytes -= p.bodyBytes
		s.mu.Unlock()
		writeError(w, http.StatusGatewayTimeout, "queue_expired", "the node did not accept the queued request in time")
		return
	}
	queued := relayproto.Queued{Queued: true, Ticket: ticket, ExpiresAt: p.expires}
	expires := p.expires
	s.mu.Unlock()
	w.Header().Set(relayproto.HeaderRelayQueued, "true")
	w.Header().Set(relayproto.HeaderRelayTicket, ticket)
	w.Header().Set("Retry-After", "1")
	_ = expires
	writeJSON(w, http.StatusAccepted, queued)
}

func (s *Service) queueOrError(w http.ResponseWriter, env *relayproto.Envelope, sessionID string) {
	now := time.Now().UTC()
	size := int64(len(env.Body))
	s.mu.Lock()
	if s.queued >= s.opts.MaxQueued || s.queuedBytes+size > s.opts.MaxQueuedBytes {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "queue_full", "the relay queue is full")
		return
	}
	perTarget, perBytes := 0, int64(0)
	for _, p := range s.pending {
		if p.offline && p.target == env.Target {
			perTarget++
			perBytes += p.bodyBytes
		}
	}
	if perTarget >= 16 || perBytes+size > 4<<20 {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "queue_full", "the node queue is full")
		return
	}
	expires := now.Add(s.opts.QueueTTL)
	if !env.ExpiresAt.IsZero() && env.ExpiresAt.Before(expires) {
		expires = env.ExpiresAt
	}
	p := &pendingRequest{
		id: env.RequestID, target: env.Target, sessionID: sessionID,
		envelope: *env, expires: expires, offline: true, bodyBytes: size,
	}
	s.pending[p.id] = p
	s.queued++
	s.queuedBytes += size
	s.mu.Unlock()
	w.Header().Set(relayproto.HeaderRelayQueued, "true")
	w.Header().Set(relayproto.HeaderRelayTicket, p.id)
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusAccepted, relayproto.Queued{Queued: true, Ticket: p.id, ExpiresAt: expires})
}

func (s *Service) forwardLocal(ctx context.Context, env *relayproto.Envelope, allowParent bool) relayproto.Result {
	if env == nil {
		return errorResult(http.StatusBadRequest, "invalid_envelope", "envelope is required")
	}
	if relayproto.HasHop(env, s.relayID) || len(env.Hops) >= relayproto.MaxHops {
		return errorResult(http.StatusLoopDetected, "loop_detected", "relay path loops")
	}
	now := time.Now().UTC()
	target := env.Target
	if target == s.relayID {
		return errorResult(http.StatusLoopDetected, "loop_detected", "a relay cannot target itself")
	}
	sess := s.lookupDirect(target)
	var viaSession *session
	if sess == nil {
		route, _ := s.lookupRoute(target, now)
		if route != nil {
			sess = route.session
			viaSession = route.session
		}
	}
	if sess != nil {
		if !s.sessionLive(sess, now) {
			return errorResult(http.StatusServiceUnavailable, "node_offline", "the target node is offline")
		}
		if viaSession != nil {
			// Re-check the chain before adding another hop.
			if len(env.Hops)+1 > relayproto.MaxHops {
				return errorResult(http.StatusLoopDetected, "loop_detected", "relay hop limit exceeded")
			}
		}
		size := int64(len(env.Body))
		pending := &pendingRequest{
			id: env.RequestID, target: target, sessionID: sess.id,
			ch: make(chan relayproto.Result, 1), envelope: *env,
			expires: now.Add(s.opts.WaitTimeout), bodyBytes: size,
		}
		s.mu.Lock()
		if len(s.pending) >= s.opts.MaxPending || s.pendingBytes+size > s.opts.MaxPendingBytes {
			s.mu.Unlock()
			return errorResult(http.StatusServiceUnavailable, "relay_busy", "the relay has too many requests in flight")
		}
		s.pending[pending.id] = pending
		s.pendingBytes += size
		s.mu.Unlock()
		out := *env
		out.RequestID = pending.id
		out.Hops = append(append([]string(nil), env.Hops...), s.relayID)
		select {
		case sess.frames <- out:
		default:
			s.mu.Lock()
			delete(s.pending, pending.id)
			s.pendingBytes -= pending.bodyBytes
			s.mu.Unlock()
			return errorResult(http.StatusServiceUnavailable, "relay_busy", "the target node is not keeping up")
		}
		timer := time.NewTimer(s.opts.WaitTimeout)
		defer timer.Stop()
		select {
		case result := <-pending.ch:
			return result
		case <-timer.C:
			s.mu.Lock()
			delete(s.pending, pending.id)
			s.pendingBytes -= pending.bodyBytes
			s.mu.Unlock()
			return errorResult(http.StatusGatewayTimeout, "node_timeout", "the target node did not answer in time")
		case <-ctx.Done():
			s.mu.Lock()
			delete(s.pending, pending.id)
			s.pendingBytes -= pending.bodyBytes
			s.mu.Unlock()
			return errorResult(http.StatusGatewayTimeout, "request_canceled", "the relayed request was canceled")
		}
	}
	if allowParent && s.parent != nil {
		return s.forwardViaParent(ctx, env)
	}
	return errorResult(http.StatusNotFound, "route_not_found", "no relay route to this node")
}

func (s *Service) forwardViaParent(ctx context.Context, env *relayproto.Envelope) relayproto.Result {
	parent := s.parent
	if parent == nil {
		return errorResult(http.StatusNotFound, "route_not_found", "no parent relay is configured")
	}
	out := *env
	if out.RequestID == "" {
		out.RequestID = relayproto.NewRequestID()
	}
	if relayproto.HasHop(&out, s.relayID) || len(out.Hops) >= relayproto.MaxHops {
		return errorResult(http.StatusLoopDetected, "loop_detected", "relay path loops")
	}
	out.Hops = append(append([]string(nil), out.Hops...), s.relayID)
	resp, data, err := parent.forward(ctx, &out)
	if err != nil {
		return errorResult(http.StatusBadGateway, "parent_unreachable", "the parent relay is unreachable")
	}
	if resp.StatusCode == http.StatusAccepted && resp.Header.Get(relayproto.HeaderRelayQueued) == "true" {
		var queued relayproto.Queued
		if err := json.Unmarshal(data, &queued); err == nil && queued.Ticket != "" {
			return parent.pollTicket(ctx, queued)
		}
	}
	return resultFromResponse(resp, data)
}

func (s *Service) finishPending(id, sessionID string, result relayproto.Result) bool {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil || p.sessionID != sessionID {
		return false
	}
	if p.offline {
		copy := result
		copy.RelayedBy = s.relayID
		p.result = &copy
		p.resultTTL = now.Add(s.opts.ResponseTTL)
		return true
	}
	select {
	case p.ch <- result:
	default:
	}
	delete(s.pending, id)
	s.pendingBytes -= p.bodyBytes
	return true
}

func (s *Service) writeResult(w http.ResponseWriter, result relayproto.Result) {
	writeResult(w, result, s.relayID)
}

// writeResult writes an inner response with relay metadata. It is a free
// function so queued responses can be replayed without a request context.
func writeResult(w http.ResponseWriter, result relayproto.Result, relayID string) {
	if result.Code != "" {
		writeError(w, result.Status, result.Code, result.Message)
		return
	}
	if result.Status < 100 || result.Status > 599 {
		result.Status = http.StatusBadGateway
	}
	body, err := relayproto.DecodeBody(result.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid_result", "the node returned an invalid response")
		return
	}
	for name, value := range result.Headers {
		if allowedResponseHeader(name) {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set(relayproto.HeaderRelayedBy, firstNonEmpty(result.RelayedBy, relayID))
	w.WriteHeader(result.Status)
	_, _ = w.Write(body)
}

func (s *Service) lookupDirect(id string) *session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

func (s *Service) lookupRoute(id string, now time.Time) (*routeEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.routes[id]
	if entry == nil {
		return nil, false
	}
	if !now.Before(entry.expires) || !s.sessionLiveLocked(entry.session, now) {
		delete(s.routes, id)
		return nil, false
	}
	return entry, true
}

func (s *Service) sessionLive(sess *session, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionLiveLocked(sess, now)
}

func (s *Service) sessionLiveLocked(sess *session, now time.Time) bool {
	if sess == nil {
		return false
	}
	return now.Sub(sess.lastSeen) <= s.sessionTTL
}

func (s *Service) targetOnlineLocked(target string) bool {
	now := time.Now().UTC()
	if sess := s.sessions[target]; sess != nil && s.sessionLiveLocked(sess, now) {
		return true
	}
	if entry := s.routes[target]; entry != nil && now.Before(entry.expires) && s.sessionLiveLocked(entry.session, now) {
		return true
	}
	return false
}

func (s *Service) knownNode(target string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry := s.targets[target]
	return entry != nil && entry.kind == "node" && now.Before(entry.expires)
}

func (s *Service) takeQueuedLocked(target string, limit int) []relayproto.Envelope {
	now := time.Now().UTC()
	var out []relayproto.Envelope
	for _, p := range s.pending {
		if len(out) >= limit {
			break
		}
		if !p.offline || p.delivered || p.target != target {
			continue
		}
		if now.After(p.expires) {
			delete(s.pending, p.id)
			s.queued--
			s.queuedBytes -= p.bodyBytes
			continue
		}
		p.delivered = true
		out = append(out, p.envelope)
	}
	return out
}

func (s *Service) authorizedSession(r *http.Request) *session {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		return nil
	}
	id := r.URL.Query().Get("id")
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id != "" {
		sess := s.sessions[id]
		if sess != nil && nodenet.ConstantTimeEqual(sess.token, token) && now.Before(sess.expires) {
			return sess
		}
		return nil
	}
	for _, sess := range s.sessions {
		if nodenet.ConstantTimeEqual(sess.token, token) && now.Before(sess.expires) {
			return sess
		}
	}
	return nil
}

func (s *Service) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UTC()
			s.mu.Lock()
			for id, p := range s.pending {
				if p.offline {
					if p.result != nil && !p.resultTTL.IsZero() && now.After(p.resultTTL) {
						delete(s.pending, id)
						s.queued--
						s.queuedBytes -= p.bodyBytes
						continue
					}
					if p.result == nil && now.After(p.expires) {
						delete(s.pending, id)
						s.queued--
						s.queuedBytes -= p.bodyBytes
					}
					continue
				}
				if now.After(p.expires.Add(time.Minute)) {
					delete(s.pending, id)
					s.pendingBytes -= p.bodyBytes
				}
			}
			for id, sess := range s.sessions {
				if now.After(sess.expires.Add(5 * time.Minute)) {
					delete(s.sessions, id)
				}
			}
			for target, entry := range s.routes {
				if now.After(entry.expires) {
					delete(s.routes, target)
				}
			}
			for id, entry := range s.targets {
				if now.After(entry.expires) {
					delete(s.targets, id)
				}
			}
			s.mu.Unlock()
			s.limit.cleanup(now)
		}
	}
}

func (s *Service) liveSessionCountLocked() int {
	now := time.Now().UTC()
	n := 0
	for _, sess := range s.sessions {
		if s.sessionLiveLocked(sess, now) {
			n++
		}
	}
	return n
}

func (s *Service) liveRouteCountLocked() int {
	now := time.Now().UTC()
	n := 0
	for _, entry := range s.routes {
		if now.Before(entry.expires) {
			n++
		}
	}
	return n
}

func resultFromResponse(resp *http.Response, data []byte) relayproto.Result {
	result := relayproto.Result{
		Protocol: relayproto.Protocol, Status: resp.StatusCode,
		Headers: map[string]string{}, Body: relayproto.EncodeBody(data),
	}
	for name, values := range resp.Header {
		if allowedResponseHeader(name) && len(values) > 0 {
			result.Headers[name] = values[0]
		}
	}
	return result
}

func allowedResponseHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Content-Type", "Cache-Control", "X-Toget-Relayed-By":
		return true
	}
	return false
}

func errorResult(status int, code, message string) relayproto.Result {
	return relayproto.Result{
		Protocol: relayproto.Protocol, Status: status, Code: code, Message: message,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"internal","message":"could not encode response"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("request body is required")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, errors.New("request body is too large or unreadable")
	}
	if len(data) == 0 {
		return nil, errors.New("request body is empty")
	}
	return data, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{buckets: make(map[string]*bucket), rate: rate, burst: burst}
}

func (l *limiter) allow(key string, rate, burst float64) bool {
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
	if rate > 0 {
		b.tokens += now.Sub(b.last).Seconds() * rate / 60
	}
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

func (l *limiter) cleanup(now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) < 4096 {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, key)
		}
	}
}

// SessionTTL mirrors the node's relay session liveness window. It is exposed
// for tests and for the parent link.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

func loadOrCreateRelayKey(path string) (ed25519.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		var stored struct {
			PrivateKey string `json:"private_key"`
		}
		if err := json.Unmarshal(data, &stored); err != nil {
			return nil, fmt.Errorf("relay: parse key file: %w", err)
		}
		raw, err := base64.RawStdEncoding.DecodeString(stored.PrivateKey)
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, errors.New("relay: key file is invalid")
		}
		return ed25519.PrivateKey(raw), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("relay: read key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(map[string]string{
		"private_key": base64.RawStdEncoding.EncodeToString(key),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("relay: write key: %w", err)
	}
	return key, nil
}
