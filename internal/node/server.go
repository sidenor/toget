package node

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/card"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/mdns"
	"github.com/sidenor/toget/internal/relayproto"
	"github.com/sidenor/toget/internal/room"
)

const (
	maxRedeemBody     = 2 << 20
	maxSyncBody       = 1 << 20
	maxEventBody      = 2 << 20
	maxAttachmentBody = room.MaxAttachmentSize*4/3 + 4096
	defaultSync       = 3 * time.Second
)

// Options configures the LAN node transport.
type Options struct {
	Accounts     *account.Service
	Rooms        *room.Service
	Paths        config.Paths
	Listen       string
	Advertise    string
	SyncInterval time.Duration
	// AccountSyncInterval controls how often paired devices exchange account
	// records. Zero uses the default.
	AccountSyncInterval time.Duration
	// Relay is a pinned relay URL, "auto" to discover one from the directory,
	// or empty/"off" to disable relay fallback.
	Relay string
	// Directory is a pinned directory URL. It is used to publish this node
	// and to resolve a default relay when Relay is "auto".
	Directory string
	// MDNS is "on" to advertise and browse Toget nodes on the local network.
	// Empty or "off" disables multicast discovery.
	MDNS   string
	Logger *slog.Logger
}

// Info is the local node's public transport identity.
type Info struct {
	ServerID       string `json:"server_id"`
	ServerCode     string `json:"server_code"`
	AccountID      string `json:"account_id"`
	DeviceID       string `json:"device_id"`
	Endpoint       string `json:"endpoint"`
	URL            string `json:"url"`
	DirectURL      string `json:"direct_url,omitempty"`
	RelayURL       string `json:"relay_url,omitempty"`
	RelayConnected bool   `json:"relay_connected"`
	DirectoryURL   string `json:"directory_url,omitempty"`
	MDNS           bool   `json:"mdns"`
	MDNSPeers      int    `json:"mdns_peers"`
	TLSFingerprint string `json:"tls_fingerprint"`
	Protocol       int    `json:"protocol"`
}

// Service serves the pinned HTTPS node API and synchronizes remote rooms.
type Service struct {
	accounts  *account.Service
	rooms     *room.Service
	paths     config.Paths
	cards     *card.Store
	listen    string
	advertise string
	logger    *slog.Logger
	syncEvery time.Duration

	mu          sync.RWMutex
	url         string
	endpoint    string
	fingerprint string
	server      *http.Server
	listener    net.Listener
	started     bool
	closeOnce   sync.Once
	cancel      context.CancelFunc

	nonces *NonceCache
	syncMu sync.Mutex

	accountSyncEvery   time.Duration
	accountSyncMu      sync.Mutex
	accountSyncTrigger chan struct{}
	accountSyncCursor  int

	relaySetting     string
	relayMu          sync.RWMutex
	relayBase        string
	relayFP          string
	directorySetting string
	directoryBase    string
	directoryFP      string
	directoryHTTP    *http.Client
	relayConnected   atomic.Bool

	mdnsSetting string
	mdnsMu      sync.RWMutex
	discovery   *mdns.Service

	pairMu    sync.Mutex
	pairings  map[string]*pairSession
	joinMu    sync.Mutex
	pairJoins map[string]*pairJoin
}

// New creates a node service. A listen address of "off" disables it and
// returns a nil service.
func New(opts Options) (*Service, error) {
	listen := strings.TrimSpace(opts.Listen)
	if listen == "" {
		listen = config.DefaultNodeListen
	}
	if listen == config.NodeListenOff {
		return nil, nil
	}
	if opts.Accounts == nil || opts.Rooms == nil {
		return nil, errors.New("node: account and room services are required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := opts.SyncInterval
	if interval <= 0 {
		interval = defaultSync
	}
	accountSyncInterval := opts.AccountSyncInterval
	if accountSyncInterval <= 0 {
		accountSyncInterval = defaultAccountSyncEvery
	}
	relaySetting := strings.TrimSpace(opts.Relay)
	if relaySetting == "off" {
		relaySetting = ""
	}
	var relayBase, relayFP string
	if relaySetting != "" && relaySetting != "auto" {
		base, fingerprint, err := ParsePinnedURL(relaySetting)
		if err != nil {
			return nil, fmt.Errorf("node relay: %w", err)
		}
		relayBase, relayFP = base, fingerprint
	}
	directorySetting := strings.TrimSpace(opts.Directory)
	if directorySetting == "off" {
		directorySetting = ""
	}
	var directoryBase, directoryFP string
	var directoryHTTP *http.Client
	if directorySetting != "" {
		base, fingerprint, err := ParsePinnedURL(directorySetting)
		if err != nil {
			return nil, fmt.Errorf("node directory: %w", err)
		}
		directoryBase, directoryFP = base, fingerprint
		config, err := PinnedTLSConfig(fingerprint)
		if err != nil {
			return nil, err
		}
		directoryHTTP = pinnedHTTPClient(config, 30*time.Second, 30*time.Second)
	}
	if relaySetting == "auto" && directoryBase == "" {
		return nil, errors.New("node relay: auto discovery needs a directory")
	}
	mdnsSetting := strings.TrimSpace(opts.MDNS)
	if mdnsSetting == "off" {
		mdnsSetting = ""
	}
	if mdnsSetting != "" && mdnsSetting != "on" {
		return nil, errors.New(`node mdns: expected "on" or "off"`)
	}
	cards, err := card.Open(opts.Paths.Cards)
	if err != nil {
		logger.Warn("saved cards could not be loaded", "error", err)
	}
	return &Service{
		accounts: opts.Accounts, rooms: opts.Rooms, paths: opts.Paths, cards: cards,
		listen: listen, advertise: strings.TrimSpace(opts.Advertise),
		logger: logger, syncEvery: interval, accountSyncEvery: accountSyncInterval,
		accountSyncTrigger: make(chan struct{}, 1),
		nonces:             NewNonceCache(),
		relaySetting:       relaySetting, relayBase: relayBase, relayFP: relayFP,
		directorySetting: directorySetting, directoryBase: directoryBase,
		directoryFP: directoryFP, directoryHTTP: directoryHTTP,
		mdnsSetting: mdnsSetting,
	}, nil
}

// Start binds the node listener and begins serving and polling.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("node: already started")
	}
	s.mu.Unlock()

	host, _, err := net.SplitHostPort(s.listen)
	if err != nil {
		return fmt.Errorf("node: invalid listen address: %w", err)
	}
	hosts := []string{host, s.advertise, OutboundIP()}
	material, err := LoadOrCreateTLS(
		filepath.Join(s.paths.Home, "node-tls-cert.pem"),
		filepath.Join(s.paths.Home, "node-tls-key.pem"),
		hosts,
	)
	if err != nil {
		return err
	}
	rawListener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return fmt.Errorf("node: listen: %w", err)
	}
	hostPort, err := AdvertiseHostPort(rawListener.Addr().String(), s.advertise)
	if err != nil {
		rawListener.Close()
		return err
	}
	endpoint := "https://" + hostPort

	s.mu.Lock()
	s.listener = tls.NewListener(rawListener, &tls.Config{
		Certificates: []tls.Certificate{material.Certificate},
		MinVersion:   tls.VersionTLS13,
	})
	s.fingerprint = material.Fingerprint
	s.endpoint = endpoint
	s.url = NodeURL(endpoint, material.Fingerprint)
	s.server = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	listener := s.listener
	server := s.server
	s.started = true
	s.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("node listener stopped", "error", err)
		}
	}()
	go s.syncLoop(runCtx)
	go s.accountSyncLoop(runCtx)
	if s.relaySetting != "" {
		go s.relayLoop(runCtx)
	}
	if s.directorySetting != "" {
		go s.directoryLoop(runCtx)
	}
	if s.mdnsSetting != "" {
		go s.mdnsLoop(runCtx)
	}
	go func() {
		<-runCtx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := s.EnsureAllRooms(ctx); err != nil {
			s.logger.Warn("could not attach room home addresses", "error", err)
		}
	}()
	s.logger.Info("node transport listening", "url", s.URL())
	return nil
}

// Close stops the node transport.
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

// URL returns the pinned home URL shown in invitations.
func (s *Service) URL() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.url
}

// Fingerprint returns the TLS certificate pin.
func (s *Service) Fingerprint() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fingerprint
}

// ServerID returns this node's stable account server ID.
func (s *Service) ServerID(ctx context.Context) (string, error) {
	if s == nil {
		return "", errors.New("node transport is disabled")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return "", err
	}
	if snapshot.Server != nil {
		return snapshot.Server.ID, nil
	}
	return snapshot.Account.ServerID, nil
}

// Info returns the public node identity.
func (s *Service) Info(ctx context.Context) (*Info, error) {
	if s == nil {
		return nil, errors.New("node transport is disabled")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	endpoint, direct, fingerprint := s.endpoint, s.url, s.fingerprint
	s.mu.RUnlock()
	relayRoute, _ := s.RelayRoute(ctx)
	address := direct
	if relayRoute != "" {
		address = relayproto.JoinAddress(direct, relayRoute)
	}
	peers := 0
	if discovery := s.discoveryService(); discovery != nil {
		peers = len(discovery.Peers())
	}
	return &Info{
		ServerID: snapshot.Server.ID, ServerCode: snapshot.Server.Code,
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		Endpoint: endpoint, URL: address, DirectURL: direct, RelayURL: relayRoute,
		RelayConnected: s.RelayConnected(), DirectoryURL: s.directorySetting,
		MDNS: s.mdnsSetting != "", MDNSPeers: peers,
		TLSFingerprint: fingerprint, Protocol: Protocol,
	}, nil
}

// Address returns the direct node URL with an optional relay fallback encoded
// in the fragment. Invitation, pairing, and room home addresses use it.
func (s *Service) Address(ctx context.Context) string {
	direct := s.URL()
	route, err := s.RelayRoute(ctx)
	if err != nil || route == "" {
		return direct
	}
	return relayproto.JoinAddress(direct, route)
}

// RelayRoute returns the pinned relay route for this node, if one is
// configured and the account identity is available.
func (s *Service) RelayRoute(ctx context.Context) (string, error) {
	if s == nil {
		return "", nil
	}
	base, fingerprint := s.relayConfig()
	if base == "" {
		return "", nil
	}
	serverID, err := s.ServerID(ctx)
	if err != nil {
		return "", err
	}
	return relayproto.NodeRouteURL(base, fingerprint, serverID)
}

// RelayConnected reports whether the node has polled its relay recently.
func (s *Service) RelayConnected() bool {
	if s == nil {
		return false
	}
	return s.relayConnected.Load()
}

func (s *Service) relayConfig() (string, string) {
	s.relayMu.RLock()
	defer s.relayMu.RUnlock()
	return s.relayBase, s.relayFP
}

func (s *Service) setRelayConfig(base, fingerprint string) {
	s.relayMu.Lock()
	s.relayBase, s.relayFP = base, fingerprint
	s.relayMu.Unlock()
}

func (s *Service) manifest(ctx context.Context) (*Manifest, error) {
	if s == nil {
		return nil, errors.New("node transport is disabled")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	serverKey, err := s.accounts.UnsealServerKey(snapshot.Server)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	endpoint, fingerprint := s.endpoint, s.fingerprint
	s.mu.RUnlock()
	relayRoute, _ := s.RelayRoute(ctx)
	now := time.Now().UTC().Truncate(time.Second)
	manifest := &Manifest{
		ServerID: snapshot.Server.ID, ServerCode: snapshot.Server.Code,
		AccountID: snapshot.Account.ID, Endpoint: endpoint, TLSFingerprint: fingerprint,
		RelayURL: relayRoute, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := signManifest(manifest, serverKey); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Handler returns the node HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /toget/v1/health", s.handleHealth)
	mux.HandleFunc("GET /toget/v1/info", s.handleInfo)
	mux.HandleFunc("GET /toget/v1/manifest", s.handleInfo)
	mux.HandleFunc("GET /toget/v1/identity", s.handleIdentity)
	mux.HandleFunc("POST /toget/v1/invites/redeem", s.handleRedeem)
	mux.HandleFunc("POST /toget/v1/rooms/sync", s.handleSync)
	mux.HandleFunc("POST /toget/v1/rooms/events", s.handleEvent)
	mux.HandleFunc("POST /toget/v1/rooms/leave", s.handleLeave)
	mux.HandleFunc("POST /toget/v1/rooms/attachments", s.handleAttachmentUpload)
	mux.HandleFunc("GET /toget/v1/attachments/{id}", s.handleAttachmentDownload)
	mux.HandleFunc("POST /toget/v1/pair/request", s.handlePairRequest)
	mux.HandleFunc("POST /toget/v1/pair/complete", s.handlePairComplete)
	mux.HandleFunc("POST /toget/v1/account/sync", s.handleAccountSync)
	mux.HandleFunc("POST /toget/v1/rooms/devices", s.handleRegisterDevice)
	mux.HandleFunc("POST /toget/v1/rooms/devices/revoke", s.handleRevokeDevice)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeNodeError(w, http.StatusNotFound, "not_found", "unknown node endpoint")
	})
	return s.secure(mux)
}

func (s *Service) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("node handler panic", "error", recovered)
				writeNodeError(w, http.StatusInternalServerError, "internal", "internal node error")
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
	manifest, err := s.manifest(r.Context())
	if err != nil {
		writeNodeError(w, http.StatusServiceUnavailable, "not_ready", "node identity is not ready")
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

// handleIdentity serves the account's signed identity manifest. Directory
// lookups already expose it; serving it here lets a LAN peer that found this
// node over mDNS verify the node manifest without contacting a directory.
func (s *Service) handleIdentity(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil || snapshot.Server == nil || snapshot.Server.Manifest == "" {
		writeNodeError(w, http.StatusServiceUnavailable, "not_ready", "node identity is not ready")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(snapshot.Server.Manifest))
}

func (s *Service) handleRedeem(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxRedeemBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req RedeemRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if err := VerifyRequest(r, body, ed25519.PublicKey(req.Joiner.SigningPublicKey), s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "invite request signature is invalid")
		return
	}
	actor, err := s.localActor(r.Context())
	if err != nil {
		writeNodeError(w, http.StatusServiceUnavailable, "not_ready", "home node identity is not ready")
		return
	}
	joinedRoom, _, _, invite, err := s.rooms.RedeemInviteFor(r.Context(), req.Secret, req.Joiner, actor)
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	if _, err := s.EnsureRoomHome(r.Context(), joinedRoom.ID); err != nil {
		s.logger.Warn("could not record room home address", "room", joinedRoom.ID, "error", err)
	}
	bundle, err := s.rooms.ExportJoinBundle(r.Context(), joinedRoom.ID, req.Joiner.DeviceID, invite.History)
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	bundle.InviteID = invite.ID
	bundle.History = invite.History
	writeJSON(w, http.StatusOK, bundle)
}

func (s *Service) handleSync(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxSyncBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req syncRequest
	if err := json.Unmarshal(body, &req); err != nil || req.RoomID == "" || req.After < 0 {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room_id and after are required")
		return
	}
	device, err := s.requestDevice(r, req.RoomID)
	if err != nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderAccount), device.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "account does not match the device")
		return
	}
	if err := VerifyRequest(r, body, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "sync request signature is invalid")
		return
	}
	bundle, err := s.rooms.ExportSync(r.Context(), req.RoomID, device.DeviceID, req.After, 0)
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}

func (s *Service) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxEventBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req struct {
		Event room.WireEvent `json:"event"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Event.RoomID == "" || req.Event.ID == "" {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "event is required")
		return
	}
	event := req.Event.ToEvent()
	if event.AuthorDevice != r.Header.Get(HeaderDevice) || event.AuthorID != r.Header.Get(HeaderAccount) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "event author does not match the request identity")
		return
	}
	device, err := s.requestDevice(r, event.RoomID)
	if err != nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if !ConstantTimeEqual(device.AccountID, event.AuthorID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "event author is not the request device")
		return
	}
	if err := VerifyRequest(r, body, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "event request signature is invalid")
		return
	}
	if err := s.rooms.AcceptRemoteEvent(r.Context(), &event); err != nil {
		head, _ := s.rooms.LastSeq(r.Context(), event.RoomID)
		if errors.Is(err, room.ErrInvalidEvent) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{"code": "conflict", "message": "event does not continue the room log"},
				"head":  head,
			})
			return
		}
		s.writeRoomError(w, err, head)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": event.ID, "seq": event.Seq})
}

func (s *Service) handleLeave(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxEventBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req struct {
		Event room.WireEvent `json:"event"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Event.RoomID == "" || req.Event.ID == "" {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "event is required")
		return
	}
	event := req.Event.ToEvent()
	if event.AuthorDevice != r.Header.Get(HeaderDevice) || event.AuthorID != r.Header.Get(HeaderAccount) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "event author does not match the request identity")
		return
	}
	device, err := s.requestDevice(r, event.RoomID)
	if err != nil || device.AccountID != event.AuthorID {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if err := VerifyRequest(r, body, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "leave request signature is invalid")
		return
	}
	if err := s.rooms.AcceptRemoteLeave(r.Context(), &event); err != nil {
		head, _ := s.rooms.LastSeq(r.Context(), event.RoomID)
		if errors.Is(err, room.ErrInvalidEvent) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{"code": "conflict", "message": "leave does not continue the room log"},
				"head":  head,
			})
			return
		}
		s.writeRoomError(w, err, head)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": event.ID, "seq": event.Seq})
}

func (s *Service) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	roomID := query.Get("room_id")
	if roomID == "" {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room_id is required")
		return
	}
	body, err := readNodeBody(w, r, maxAttachmentBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	device, err := s.requestDevice(r, roomID)
	if err != nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderAccount), device.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "account does not match the device")
		return
	}
	if err := VerifyRequest(r, body, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "attachment request signature is invalid")
		return
	}
	size, err := strconv.ParseInt(query.Get("size"), 10, 64)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "size is invalid")
		return
	}
	epoch, err := strconv.Atoi(query.Get("epoch"))
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "epoch is invalid")
		return
	}
	attachment := &room.Attachment{
		ID: query.Get("id"), RoomID: roomID, Name: query.Get("name"),
		MIME: query.Get("mime"), Size: size, SHA256: query.Get("sha256"),
		KeySealed: strconv.Itoa(epoch), CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.rooms.ImportAttachment(r.Context(), roomID, device.AccountID, device.DeviceID, attachment, body); err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	writeJSON(w, http.StatusCreated, attachment.ToWire())
}

func (s *Service) handleAttachmentDownload(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room_id")
	attachmentID := r.PathValue("id")
	if roomID == "" || attachmentID == "" {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room_id and attachment id are required")
		return
	}
	device, err := s.requestDevice(r, roomID)
	if err != nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderAccount), device.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "account does not match the device")
		return
	}
	if err := VerifyRequest(r, nil, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "attachment request signature is invalid")
		return
	}
	_, sealed, err := s.rooms.ReadAttachmentCiphertext(r.Context(), roomID, attachmentID, device.AccountID, device.DeviceID)
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(sealed)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sealed)
}

func (s *Service) requestDevice(r *http.Request, roomID string) (*room.RoomDevice, error) {
	deviceID := r.Header.Get(HeaderDevice)
	if deviceID == "" {
		return nil, errors.New("missing device header")
	}
	return s.rooms.GetDevice(r.Context(), roomID, deviceID)
}

func (s *Service) writeRoomError(w http.ResponseWriter, err error, head int64) {
	switch {
	case errors.Is(err, room.ErrInviteInvalid), errors.Is(err, room.ErrNotFound):
		writeNodeError(w, http.StatusNotFound, "not_found", "room or invite was not found")
	case errors.Is(err, room.ErrInviteExpired):
		writeNodeError(w, http.StatusGone, "invite_expired", "invite has expired")
	case errors.Is(err, room.ErrInviteUsed):
		writeNodeError(w, http.StatusConflict, "invite_used", "invite has already been used")
	case errors.Is(err, room.ErrMemberExists):
		writeNodeError(w, http.StatusConflict, "already_member", "account is already a room member")
	case errors.Is(err, room.ErrNotMember), errors.Is(err, room.ErrForbidden):
		writeNodeError(w, http.StatusForbidden, "forbidden", "room action is not allowed")
	case errors.Is(err, room.ErrInviteSecret), errors.Is(err, room.ErrInvalidEvent):
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room request is invalid")
	case errors.Is(err, room.ErrInvalidAttachment):
		writeNodeError(w, http.StatusBadRequest, "invalid_attachment", "attachment is invalid")
	case errors.Is(err, room.ErrAttachmentTooLarge):
		writeNodeError(w, http.StatusRequestEntityTooLarge, "attachment_too_large", "attachment exceeds the size limit")
	case errors.Is(err, room.ErrAttachmentNotFound):
		writeNodeError(w, http.StatusNotFound, "attachment_not_found", "attachment was not found")
	case errors.Is(err, room.ErrAttachmentsDisabled):
		writeNodeError(w, http.StatusServiceUnavailable, "attachments_disabled", "attachment storage is not configured")
	case errors.Is(err, room.ErrRemoteReadOnly):
		writeNodeError(w, http.StatusConflict, "remote_room", "room is hosted by another node")
	default:
		s.logger.Warn("node request failed", "error", err)
		writeNodeError(w, http.StatusInternalServerError, "internal", "room operation failed")
	}
	_ = head
}

func readNodeBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"internal","message":"could not encode response"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeNodeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// localActor builds the home node's own signing identity.
func (s *Service) localActor(ctx context.Context) (room.Actor, error) {
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return room.Actor{}, err
	}
	if snapshot.Device.RevokedAt != nil {
		return room.Actor{}, errors.New("local device is revoked")
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(snapshot.Device.Certificate), snapshot.Account.PublicKey)
	if err != nil {
		return room.Actor{}, fmt.Errorf("device certificate: %w", err)
	}
	if cert.DeviceID != snapshot.Device.ID || cert.AccountID != snapshot.Account.ID {
		return room.Actor{}, errors.New("device certificate does not match the local device")
	}
	key, err := s.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return room.Actor{}, err
	}
	return room.Actor{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		DisplayName: snapshot.Account.Name, SigningKey: key,
		AccountPublicKey:    snapshot.Account.PublicKey,
		EncryptionPublicKey: snapshot.Device.EncryptionPublicKey,
	}, nil
}

func (s *Service) localJoiner(ctx context.Context) (room.RemoteJoiner, *account.Snapshot, error) {
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return room.RemoteJoiner{}, nil, err
	}
	return room.RemoteJoiner{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		DisplayName:         snapshot.Account.Name,
		AccountPublicKey:    snapshot.Account.PublicKey,
		SigningPublicKey:    snapshot.Device.PublicKey,
		EncryptionPublicKey: snapshot.Device.EncryptionPublicKey,
		DeviceCertificate:   []byte(snapshot.Device.Certificate),
	}, snapshot, nil
}

// LocalActor returns the local account's room signing identity.
func (s *Service) LocalActor(ctx context.Context) (room.Actor, error) {
	return s.localActor(ctx)
}

// InviteCode wraps a room invite secret for a peer.
func (s *Service) InviteCode(ctx context.Context, roomID, secret, roomName string) (string, error) {
	if s == nil || s.URL() == "" {
		return "", errors.New("node transport is disabled")
	}
	serverID, err := s.ServerID(ctx)
	if err != nil {
		return "", err
	}
	return EncodeInvite(InviteCode{
		Protocol: Protocol, Secret: secret, URL: s.Address(ctx),
		ServerID: serverID, RoomID: roomID, RoomName: roomName,
	})
}

// IsRemoteRoom reports whether a room is hosted by another node.
func (s *Service) IsRemoteRoom(ctx context.Context, roomID string) (bool, error) {
	if s == nil {
		return false, nil
	}
	record, err := s.rooms.GetRoom(ctx, roomID)
	if err != nil {
		return false, err
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return false, nil
	}
	return record.HomeServerID != "" && record.HomeServerID != snapshot.Account.ServerID, nil
}

// EnsureRoomHome attaches this node's address to a locally hosted room.
func (s *Service) EnsureRoomHome(ctx context.Context, roomID string) (*room.Room, error) {
	if s == nil || s.URL() == "" {
		return nil, errors.New("node transport is disabled")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.rooms.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if record.HomeServerID != "" && record.HomeServerID != snapshot.Account.ServerID {
		return record, nil
	}
	if record.HomeNode != snapshot.Device.ID && record.HomeServerID == "" {
		return record, nil
	}
	address := s.Address(ctx)
	if record.HomeServerID == snapshot.Account.ServerID && record.HomeAddress == address {
		return record, nil
	}
	return s.rooms.SetHomeInfo(ctx, roomID, snapshot.Account.ServerID, address)
}

// EnsureAllRooms backfills home addresses for locally created rooms.
func (s *Service) EnsureAllRooms(ctx context.Context) error {
	if s == nil || s.URL() == "" {
		return nil
	}
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		return err
	}
	for i := range rooms {
		if _, err := s.EnsureRoomHome(ctx, rooms[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// Join redeems a portable invite code on this node.
func (s *Service) Join(ctx context.Context, rawCode string) (*room.Room, error) {
	code, err := DecodeInvite(rawCode)
	if err != nil {
		return nil, err
	}
	joiner, snapshot, err := s.localJoiner(ctx)
	if err != nil {
		return nil, err
	}
	deviceKey, err := s.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return nil, err
	}
	client, err := NewClientForURL(code.URL, joiner.AccountID, joiner.DeviceID, deviceKey)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	bundle, err := client.Redeem(ctx, code.Secret, joiner)
	if err != nil {
		return nil, err
	}
	if bundle.Room.ID != code.RoomID || bundle.Room.HomeServerID != code.ServerID {
		return nil, errors.New("invite response does not match the invite code")
	}
	encryptionKey, err := s.accounts.UnsealDeviceEncryptionKey(snapshot.Device)
	if err != nil {
		return nil, err
	}
	joined, err := s.rooms.ImportJoinBundle(ctx, bundle, snapshot.Device.ID, encryptionKey)
	if err != nil {
		return nil, err
	}
	s.TriggerAccountSync()
	return joined, nil
}

// SyncRoom pulls incremental changes for one remote room.
func (s *Service) SyncRoom(ctx context.Context, roomID string) (int, error) {
	if s == nil {
		return 0, nil
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	remote, err := s.IsRemoteRoom(ctx, roomID)
	if err != nil || !remote {
		return 0, err
	}
	record, err := s.rooms.GetRoom(ctx, roomID)
	if err != nil {
		return 0, err
	}
	if record.HomeAddress == "" {
		return 0, errors.New("remote room has no home address")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return 0, err
	}
	key, err := s.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return 0, err
	}
	client, err := NewClientForURL(record.HomeAddress, snapshot.Account.ID, snapshot.Device.ID, key)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	last, err := s.rooms.LastSeq(ctx, roomID)
	if err != nil {
		return 0, err
	}
	registered := false
	total := 0
	for batch := 0; batch < 100; batch++ {
		bundle, err := client.Sync(ctx, roomID, last)
		if err != nil {
			var remoteErr *RemoteError
			if !registered && errors.As(err, &remoteErr) && remoteErr.Status == http.StatusForbidden {
				if regErr := s.registerDeviceAtHome(ctx, client, roomID, snapshot); regErr != nil {
					return total, err
				}
				registered = true
				continue
			}
			return total, err
		}
		if bundle.Room.ID != roomID || bundle.Room.HomeServerID != record.HomeServerID {
			return total, errors.New("sync response does not match the room")
		}
		if bundle.After != last {
			return total, errors.New("sync response starts at the wrong cursor")
		}
		encryptionKey, err := s.accounts.UnsealDeviceEncryptionKey(snapshot.Device)
		if err != nil {
			return total, err
		}
		n, err := s.rooms.ImportSync(ctx, bundle, snapshot.Device.ID, encryptionKey)
		if err != nil {
			return total, err
		}
		total += n
		last, err = s.rooms.LastSeq(ctx, roomID)
		if err != nil {
			return total, err
		}
		if !bundle.HasMore {
			return total, nil
		}
	}
	return total, errors.New("room has too many events to sync in one pass")
}

// registerDeviceAtHome lazily registers a paired device with a remote room so
// the home node can verify its requests and wrap epoch keys to it.
func (s *Service) registerDeviceAtHome(ctx context.Context, client *Client, roomID string, snapshot *account.Snapshot) error {
	device, err := s.accounts.GetDeviceByID(ctx, snapshot.Device.ID)
	if err != nil {
		return err
	}
	if device.Certificate == "" {
		if err := s.accounts.RefreshCertificates(ctx); err != nil {
			return err
		}
		device, err = s.accounts.GetDeviceByID(ctx, snapshot.Device.ID)
		if err != nil {
			return err
		}
	}
	if _, err := client.RegisterDevice(ctx, roomID, []byte(device.Certificate)); err != nil {
		var remoteErr *RemoteError
		if errors.As(err, &remoteErr) && remoteErr.Status == http.StatusBadRequest {
			// The certificate may have expired since the last renewal.
			if refreshErr := s.accounts.RefreshCertificates(ctx); refreshErr == nil {
				device, getErr := s.accounts.GetDeviceByID(ctx, snapshot.Device.ID)
				if getErr == nil {
					_, retryErr := client.RegisterDevice(ctx, roomID, []byte(device.Certificate))
					return retryErr
				}
			}
		}
		return err
	}
	return nil
}

// SyncAll pulls every remote room.
func (s *Service) SyncAll(ctx context.Context) {
	if s == nil {
		return
	}
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		s.logger.Warn("sync: list rooms", "error", err)
		return
	}
	for i := range rooms {
		remote, err := s.IsRemoteRoom(ctx, rooms[i].ID)
		if err != nil || !remote {
			continue
		}
		if _, err := s.SyncRoom(ctx, rooms[i].ID); err != nil {
			s.logger.Debug("sync: room failed", "room", rooms[i].ID, "error", err)
		}
	}
}

func (s *Service) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(s.syncEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SyncAll(ctx)
		}
	}
}

// PostRemoteMessage forwards a message to its home node and installs the
// accepted event locally. It retries once after a sequence conflict.
func (s *Service) PostRemoteMessage(ctx context.Context, roomID string, actor room.Actor, body room.MessagePostedPayload) (*room.Event, error) {
	remote, err := s.IsRemoteRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if !remote {
		return s.rooms.PostMessage(ctx, roomID, actor, body)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := s.SyncRoom(ctx, roomID); err != nil {
			return nil, fmt.Errorf("sync before send: %w", err)
		}
		event, err := s.rooms.PrepareMessage(ctx, roomID, actor, body)
		if err != nil {
			return nil, err
		}
		client, err := s.clientForRoom(ctx, roomID)
		if err != nil {
			return nil, err
		}
		err = client.PostEvent(ctx, event)
		client.Close()
		if err != nil {
			lastErr = err
			var remoteErr *RemoteError
			if errors.As(err, &remoteErr) && remoteErr.Status == http.StatusConflict {
				continue
			}
			return nil, err
		}
		if err := s.rooms.AppendRemote(ctx, event); err != nil {
			if _, syncErr := s.SyncRoom(ctx, roomID); syncErr == nil {
				if opened, getErr := s.rooms.GetOpenedEvent(ctx, roomID, event.ID); getErr == nil {
					return opened, nil
				}
			}
			return nil, err
		}
		return s.rooms.GetOpenedEvent(ctx, roomID, event.ID)
	}
	return nil, lastErr
}

func (s *Service) clientForRoom(ctx context.Context, roomID string) (*Client, error) {
	record, err := s.rooms.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if record.HomeAddress == "" {
		return nil, errors.New("room home address is missing")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return nil, err
	}
	return NewClientForURL(record.HomeAddress, snapshot.Account.ID, snapshot.Device.ID, key)
}

// UploadAttachment stores a file in a local room or uploads its sealed
// ciphertext to the home node for a remote room, keeping a local copy either
// way.
func (s *Service) UploadAttachment(ctx context.Context, roomID string, actor room.Actor, name, mime string, data []byte) (*room.Attachment, error) {
	remote, err := s.IsRemoteRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if !remote {
		return s.rooms.UploadAttachment(ctx, roomID, actor, name, mime, data)
	}
	// Attachments are sealed with the room's epoch key. Pull the current
	// epoch first so a member who joined since the last sync can still open
	// the uploaded file.
	if _, err := s.SyncRoom(ctx, roomID); err != nil {
		return nil, fmt.Errorf("sync before upload: %w", err)
	}
	attachment, sealed, err := s.rooms.PrepareAttachment(ctx, roomID, actor, name, mime, data)
	if err != nil {
		return nil, err
	}
	client, err := s.clientForRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if _, err := client.UploadAttachment(ctx, roomID, attachment.ToWire(), sealed); err != nil {
		client.Close()
		return nil, err
	}
	client.Close()
	if err := s.rooms.StoreAttachment(ctx, attachment, sealed); err != nil {
		return nil, err
	}
	return attachment, nil
}

// FetchAttachment downloads and caches ciphertext for a remote attachment that
// is not on this device yet.
func (s *Service) FetchAttachment(ctx context.Context, attachmentID string, actor room.Actor) error {
	attachment, err := s.rooms.GetAttachment(ctx, attachmentID, actor)
	if err != nil {
		return err
	}
	if attachment.Path != "" {
		return nil
	}
	remote, err := s.IsRemoteRoom(ctx, attachment.RoomID)
	if err != nil {
		return err
	}
	if !remote {
		return room.ErrAttachmentNotFound
	}
	client, err := s.clientForRoom(ctx, attachment.RoomID)
	if err != nil {
		return err
	}
	sealed, err := client.FetchAttachment(ctx, attachment.RoomID, attachmentID)
	client.Close()
	if err != nil {
		return err
	}
	return s.rooms.CacheAttachment(ctx, attachment, sealed)
}

// LeaveRoom leaves a local room directly or asks the home node to record a
// remote leave event and rotate the room key.
func (s *Service) LeaveRoom(ctx context.Context, roomID string, actor room.Actor) (*room.Event, error) {
	remote, err := s.IsRemoteRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if !remote {
		return s.rooms.Leave(ctx, roomID, actor)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := s.SyncRoom(ctx, roomID); err != nil {
			return nil, fmt.Errorf("sync before leave: %w", err)
		}
		event, err := s.rooms.PrepareLeave(ctx, roomID, actor)
		if err != nil {
			return nil, err
		}
		client, err := s.clientForRoom(ctx, roomID)
		if err != nil {
			return nil, err
		}
		postErr := client.LeaveRoom(ctx, event)
		client.Close()
		if postErr != nil {
			lastErr = postErr
			var remoteErr *RemoteError
			if errors.As(postErr, &remoteErr) && remoteErr.Status == http.StatusConflict {
				// The home head moved; sync and prepare a new leave event.
				continue
			}
			return nil, postErr
		}
		if err := s.rooms.ApplyRemoteLeave(ctx, event); err != nil {
			// The leave was accepted remotely even if the local append raced
			// with another peer's event. Sync it in and finish locally.
			if _, syncErr := s.SyncRoom(ctx, roomID); syncErr != nil {
				return nil, err
			}
			opened, getErr := s.rooms.GetOpenedEvent(ctx, roomID, event.ID)
			if getErr != nil {
				return nil, err
			}
			s.archiveAfterLeave(ctx, roomID, actor)
			return opened, nil
		}
		// A left member can no longer read new events. Hide the room from this
		// device so it does not linger as an unusable conversation.
		s.archiveAfterLeave(ctx, roomID, actor)
		return s.rooms.GetOpenedEvent(ctx, roomID, event.ID)
	}
	return nil, lastErr
}

func (s *Service) archiveAfterLeave(ctx context.Context, roomID string, actor room.Actor) {
	if _, err := s.rooms.ArchiveLocal(ctx, roomID, actor); err != nil {
		s.logger.Warn("leave: archive remote room", "room", roomID, "error", err)
	}
}

// ArchiveRoom archives a room on its home node, or hides a remote room on this
// device without changing the shared log.
func (s *Service) ArchiveRoom(ctx context.Context, roomID string, actor room.Actor) (*room.Room, *room.Event, error) {
	remote, err := s.IsRemoteRoom(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	if !remote {
		return s.rooms.Archive(ctx, roomID, actor)
	}
	archived, err := s.rooms.ArchiveLocal(ctx, roomID, actor)
	if err != nil {
		return nil, nil, err
	}
	return archived, nil, nil
}
