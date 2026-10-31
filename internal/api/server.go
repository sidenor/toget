// Package api implements the loopback HTTP API and serves the local web UI.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/session"
	"github.com/sidenor/toget/internal/tool"
	"github.com/sidenor/toget/internal/version"
)

const (
	cookieName   = "toget_token"
	maxBodyBytes = 2 << 20
)

// AgentRunController controls in-flight Agent runs. The runner implements it.
type AgentRunController interface {
	Stop(agentID string) bool
}

// Options configures the local API.
type Options struct {
	Accounts  *account.Service
	Rooms     *room.Service
	Agents    *agent.Service
	Approvals *approval.Service
	Tools     *tool.Service
	Node      *node.Service
	Session   *session.Manager
	AgentRuns AgentRunController
	Version   version.Info
	Token     string
	Listen    string
	UI        fs.FS
	Logger    *slog.Logger
}

// Server is the loopback HTTP server.
type Server struct {
	accounts  *account.Service
	rooms     *room.Service
	agents    *agent.Service
	approvals *approval.Service
	tools     *tool.Service
	node      *node.Service
	session   *session.Manager
	agentRuns AgentRunController
	version   version.Info
	token     string
	listen    string
	ui        fs.FS
	logger    *slog.Logger
	origins   map[string]bool
}

// New creates a local API server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	sessions := opts.Session
	if sessions == nil {
		sessions = session.New(session.DefaultTTL)
	}
	return &Server{
		accounts:  opts.Accounts,
		rooms:     opts.Rooms,
		agents:    opts.Agents,
		approvals: opts.Approvals,
		tools:     opts.Tools,
		node:      opts.Node,
		session:   sessions,
		agentRuns: opts.AgentRuns,
		version:   opts.Version,
		token:     opts.Token,
		listen:    opts.Listen,
		ui:        opts.UI,
		logger:    logger,
		origins:   allowedOrigins(opts.Listen),
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// These stay available while locked so the client can discover state and
	// present the login screen.
	mux.HandleFunc("GET /api/v1/session", s.protected(s.handleGetSession))
	mux.HandleFunc("POST /api/v1/session", s.protected(s.handleUnlock))
	mux.HandleFunc("DELETE /api/v1/session", s.protected(s.handleLock))
	mux.HandleFunc("GET /api/v1/status", s.protected(s.handleStatus))
	mux.HandleFunc("GET /api/v1/account", s.protected(s.handleGetAccount))
	mux.HandleFunc("POST /api/v1/account", s.protected(s.handleCreateAccount))

	// Account and device management.
	mux.HandleFunc("GET /api/v1/devices", s.data(s.handleListDevices))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.data(s.handleRevokeDevice))
	mux.HandleFunc("GET /api/v1/pairings", s.data(s.handleListPairings))
	mux.HandleFunc("POST /api/v1/pairings", s.data(s.handleCreatePairing))
	mux.HandleFunc("POST /api/v1/pairings/{id}/approve", s.data(s.handleApprovePairing))
	mux.HandleFunc("DELETE /api/v1/pairings/{id}", s.data(s.handleCancelPairing))

	// A device without an account recovers one from a pairing code. These
	// routes cannot require the account session yet.
	mux.HandleFunc("POST /api/v1/pairing/join", s.protected(s.handleStartPairJoin))
	mux.HandleFunc("GET /api/v1/pairing/join/{id}", s.protected(s.handlePairJoinStatus))
	mux.HandleFunc("DELETE /api/v1/pairing/join/{id}", s.protected(s.handleCancelPairJoin))

	// Rooms, membership, messages, search, and audit.
	mux.HandleFunc("GET /api/v1/node", s.data(s.handleNodeInfo))
	mux.HandleFunc("GET /api/v1/node/discover", s.data(s.handleNodeDiscover))
	mux.HandleFunc("GET /api/v1/node/lookup", s.data(s.handleNodeLookup))
	mux.HandleFunc("POST /api/v1/node/sync", s.data(s.handleNodeSync))
	mux.HandleFunc("POST /api/v1/rooms/{id}/sync", s.data(s.handleRoomSync))
	mux.HandleFunc("GET /api/v1/rooms", s.data(s.handleListRooms))
	mux.HandleFunc("POST /api/v1/rooms", s.data(s.handleCreateRoom))
	mux.HandleFunc("GET /api/v1/rooms/{id}", s.data(s.handleGetRoom))
	mux.HandleFunc("PATCH /api/v1/rooms/{id}", s.data(s.handleUpdateRoom))
	mux.HandleFunc("POST /api/v1/rooms/{id}/archive", s.data(s.handleArchiveRoom))
	mux.HandleFunc("GET /api/v1/rooms/{id}/events", s.data(s.handleListEvents))
	mux.HandleFunc("POST /api/v1/rooms/{id}/messages", s.data(s.handlePostMessage))
	mux.HandleFunc("PATCH /api/v1/rooms/{id}/messages/{eventID}", s.data(s.handleEditMessage))
	mux.HandleFunc("GET /api/v1/rooms/{id}/members", s.data(s.handleListMembers))
	mux.HandleFunc("GET /api/v1/rooms/{id}/devices", s.data(s.handleListRoomDevices))
	mux.HandleFunc("POST /api/v1/rooms/{id}/invites", s.data(s.handleCreateInvite))
	mux.HandleFunc("POST /api/v1/invites/redeem", s.data(s.handleRedeemInvite))
	mux.HandleFunc("POST /api/v1/rooms/{id}/leave", s.data(s.handleLeaveRoom))
	mux.HandleFunc("GET /api/v1/rooms/{id}/audit", s.data(s.handleExportAudit))
	mux.HandleFunc("POST /api/v1/audit/verify", s.data(s.handleVerifyAudit))
	mux.HandleFunc("GET /api/v1/rooms/{id}/search", s.data(s.handleSearchRoom))
	mux.HandleFunc("GET /api/v1/search", s.data(s.handleSearch))
	mux.HandleFunc("GET /api/v1/rooms/{id}/read", s.data(s.handleGetReadCursor))
	mux.HandleFunc("POST /api/v1/rooms/{id}/read", s.data(s.handleSetReadCursor))
	mux.HandleFunc("GET /api/v1/rooms/{id}/unread", s.data(s.handleUnread))
	mux.HandleFunc("GET /api/v1/rooms/{id}/attachments", s.data(s.handleListAttachments))
	mux.HandleFunc("POST /api/v1/rooms/{id}/attachments", s.data(s.handleUploadAttachment))
	mux.HandleFunc("GET /api/v1/attachments/{id}", s.data(s.handleGetAttachment))
	mux.HandleFunc("GET /api/v1/attachments/{id}/content", s.data(s.handleAttachmentContent))

	// Agents, approvals, and tool receipts.
	mux.HandleFunc("GET /api/v1/agents", s.data(s.handleListAgents))
	mux.HandleFunc("POST /api/v1/agents", s.data(s.handleCreateAgent))
	mux.HandleFunc("GET /api/v1/agents/{id}", s.data(s.handleGetAgent))
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.data(s.handleUpdateAgent))
	mux.HandleFunc("POST /api/v1/agents/{id}/revoke", s.data(s.handleRevokeAgent))
	mux.HandleFunc("GET /api/v1/agents/{id}/rooms", s.data(s.handleAgentRooms))
	mux.HandleFunc("GET /api/v1/agents/{id}/sessions", s.data(s.handleListAgentSessions))
	mux.HandleFunc("DELETE /api/v1/agents/{id}/sessions/{roomID}", s.data(s.handleResetAgentSession))
	mux.HandleFunc("POST /api/v1/agents/{id}/stop", s.data(s.handleStopAgent))
	mux.HandleFunc("GET /api/v1/rooms/{id}/agents", s.data(s.handleListRoomAgents))
	mux.HandleFunc("POST /api/v1/rooms/{id}/agents", s.data(s.handleJoinAgent))
	mux.HandleFunc("DELETE /api/v1/rooms/{id}/agents/{agentID}", s.data(s.handleLeaveAgent))

	mux.HandleFunc("GET /api/v1/rooms/{id}/approvals", s.data(s.handleListApprovals))
	mux.HandleFunc("POST /api/v1/rooms/{id}/approvals", s.data(s.handleRequestApproval))
	mux.HandleFunc("GET /api/v1/approvals/{id}", s.data(s.handleGetApproval))
	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", s.data(s.handleDecideApproval))
	mux.HandleFunc("POST /api/v1/approvals/{id}/consume", s.data(s.handleConsumeApproval))

	mux.HandleFunc("GET /api/v1/rooms/{id}/tools", s.data(s.handleListTools))
	mux.HandleFunc("POST /api/v1/rooms/{id}/tools", s.data(s.handleRequestTool))
	mux.HandleFunc("GET /api/v1/tools/{id}", s.data(s.handleGetTool))
	mux.HandleFunc("PATCH /api/v1/tools/{id}", s.data(s.handleCompleteTool))

	mux.HandleFunc("GET /", s.handleUI)
	return s.recoverer(s.logRequests(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

// StatusResponse is the payload of GET /api/v1/status.
type StatusResponse struct {
	Version         version.Info `json:"version"`
	Account         *AccountView `json:"account"`
	Rooms           int          `json:"rooms"`
	Agents          int          `json:"agents"`
	PendingApproval int          `json:"pending_approvals"`
	Listen          string       `json:"listen"`
	Locked          bool         `json:"locked"`
}

// AccountView is the public shape of an account.
type AccountView struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	ServerID   string      `json:"server_id"`
	ServerCode string      `json:"server_code"`
	CreatedAt  time.Time   `json:"created_at"`
	Device     *DeviceView `json:"device,omitempty"`
}

// DeviceView is the public shape of a device, without private key material.
type DeviceView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Fingerprint string     `json:"fingerprint"`
	Current     bool       `json:"current"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		Version: s.version,
		Listen:  s.listen,
	}
	snapshot, err := s.accounts.Get(r.Context())
	if err == nil {
		resp.Account = accountView(snapshot.Account, snapshot.Device)
		unlocked, _ := s.session.State()
		resp.Locked = !unlocked
		if unlocked {
			rooms, err := s.rooms.ListRooms(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "could not load rooms")
				return
			}
			resp.Rooms = len(rooms)
			agents, err := s.agents.List(r.Context(), snapshot.Account.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "could not load agents")
				return
			}
			resp.Agents = len(agents)
			pending, err := s.approvals.List(r.Context(), "", approval.StatusPending)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "could not load approvals")
				return
			}
			resp.PendingApproval = len(pending)
		}
	} else if !errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "could not load the account")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountView(snapshot.Account, snapshot.Device))
}

type createAccountRequest struct {
	Name       string `json:"name"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	snapshot, err := s.accounts.Create(r.Context(), req.Name, req.Password, req.DeviceName)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	s.session.Unlock()
	if s.node != nil {
		s.node.EnsureAllRooms(r.Context())
	}
	writeJSON(w, http.StatusCreated, accountView(snapshot.Account, snapshot.Device))
}

// SessionView reports the local unlock state.
type SessionView struct {
	HasAccount bool       `json:"has_account"`
	Locked     bool       `json:"locked"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	TTLSeconds int        `json:"ttl_seconds"`
}

func (s *Server) sessionView(r *http.Request) SessionView {
	view := SessionView{TTLSeconds: int(session.DefaultTTL.Seconds())}
	if _, err := s.accounts.Get(r.Context()); err != nil {
		return view
	}
	view.HasAccount = true
	unlocked, expires := s.session.State()
	view.Locked = !unlocked
	if unlocked {
		view.ExpiresAt = &expires
	}
	return view
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sessionView(r))
}

type unlockRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	key := clientKey(r)
	if err := s.session.Allow(key); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts; wait a few minutes")
		return
	}
	var req unlockRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := s.accounts.Login(r.Context(), req.Password); err != nil {
		if errors.Is(err, account.ErrInvalidCredentials) {
			s.session.Failed(key)
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "password is incorrect")
			return
		}
		writeAccountError(w, err)
		return
	}
	s.session.Succeeded(key)
	expires := s.session.Unlock()
	writeJSON(w, http.StatusOK, SessionView{
		HasAccount: true, Locked: false, ExpiresAt: &expires,
		TTLSeconds: int(session.DefaultTTL.Seconds()),
	})
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	s.session.Lock()
	w.WriteHeader(http.StatusNoContent)
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	devices, err := s.accounts.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load devices")
		return
	}
	out := make([]DeviceView, 0, len(devices))
	for i := range devices {
		out = append(out, deviceView(&devices[i], snapshot.Device.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	var warnings []string
	var err error
	if s.node != nil {
		warnings, err = s.node.RevokeAccountDevice(r.Context(), r.PathValue("id"))
	} else {
		err = s.accounts.RevokeDevice(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		switch {
		case errors.Is(err, account.ErrCurrentDevice):
			writeError(w, http.StatusConflict, "current_device", "the current device cannot revoke itself")
		case errors.Is(err, account.ErrNotFound):
			writeError(w, http.StatusNotFound, "device_not_found", "device not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal", "could not revoke the device")
		}
		return
	}
	if len(warnings) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warnings": warnings})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func accountView(a *account.Account, d *account.Device) *AccountView {
	if a == nil {
		return nil
	}
	serverCode := ""
	if len(a.ID) >= 8 {
		serverCode = a.ID[:8]
	}
	view := &AccountView{
		ID:         a.ID,
		Name:       a.Name,
		ServerID:   a.ServerID,
		ServerCode: serverCode,
		CreatedAt:  a.CreatedAt,
	}
	if d != nil {
		dv := deviceView(d, d.ID)
		view.Device = &dv
	}
	return view
}

func deviceView(d *account.Device, currentID string) DeviceView {
	return DeviceView{
		ID:          d.ID,
		Name:        d.Name,
		Role:        d.Role,
		Fingerprint: identity.Fingerprint(d.PublicKey),
		Current:     d.ID == currentID,
		LastSeenAt:  d.LastSeenAt,
		RevokedAt:   d.RevokedAt,
		CreatedAt:   d.CreatedAt,
	}
}

// actor builds the signing and encryption identity for the local account.
func (s *Server) actor(ctx context.Context) (room.Actor, *account.Snapshot, error) {
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return room.Actor{}, nil, err
	}
	if snapshot.Device.RevokedAt != nil {
		return room.Actor{}, nil, errors.New("this device has been revoked")
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(snapshot.Device.Certificate), snapshot.Account.PublicKey)
	if err != nil {
		return room.Actor{}, nil, fmt.Errorf("device certificate: %w", err)
	}
	if cert.DeviceID != snapshot.Device.ID || cert.AccountID != snapshot.Account.ID {
		return room.Actor{}, nil, errors.New("device certificate does not match the local device")
	}
	deviceKey, err := s.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return room.Actor{}, nil, err
	}
	return room.Actor{
		AccountID:           snapshot.Account.ID,
		DeviceID:            snapshot.Device.ID,
		DisplayName:         snapshot.Account.Name,
		SigningKey:          deviceKey,
		AccountPublicKey:    snapshot.Account.PublicKey,
		EncryptionPublicKey: snapshot.Device.EncryptionPublicKey,
	}, snapshot, nil
}

func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, "origin_denied", "request origin is not allowed")
			return
		}
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="toget"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "a local API token is required")
			return
		}
		next(w, r)
	}
}

func (s *Server) data(next http.HandlerFunc) http.HandlerFunc {
	return s.protected(s.requireUnlocked(next))
}

func (s *Server) requireUnlocked(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.session.Require(); err != nil {
			writeError(w, http.StatusLocked, "locked", "unlock the node with your password")
			return
		}
		s.session.Touch()
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	token := ""
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	if token == "" {
		if cookie, err := r.Cookie(cookieName); err == nil {
			token = cookie.Value
		}
	}
	if token == "" || len(token) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return s.origins[u.Host]
}

func allowedOrigins(listen string) map[string]bool {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = "7718"
	}
	if port == "" {
		port = "7718"
	}
	out := map[string]bool{
		net.JoinHostPort("127.0.0.1", port): true,
		net.JoinHostPort("localhost", port): true,
		net.JoinHostPort("::1", port):       true,
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		out[net.JoinHostPort(host, port)] = true
	}
	return out
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.Error(w, "web UI is not built", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self' blob:; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; worker-src 'self'; manifest-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}

	f, err := s.ui.Open(name)
	if err != nil {
		// Client-side routes fall back to the SPA shell; missing files do not.
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		f, err = s.ui.Open(name)
		if err != nil {
			http.Error(w, "web UI is not built", http.StatusServiceUnavailable)
			return
		}
	}
	defer f.Close()

	// Set the type before handing the file to ServeContent: the embedded FS
	// exposes io.ReadSeeker, and ServeContent would otherwise infer the type
	// itself (or sniff it) before our switch ran.
	if contentType := uiContentType(name); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if seeker, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, time.Time{}, seeker)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "could not read web UI", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func uiContentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	return ""
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrNotFound):
		writeError(w, http.StatusNotFound, "account_not_found", "create an account first")
	case errors.Is(err, account.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "account_exists", "this node already has an account")
	case errors.Is(err, account.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "invalid_name", "name must be 1–64 characters")
	case errors.Is(err, account.ErrInvalidPassword):
		writeError(w, http.StatusBadRequest, "invalid_password", "password must be at least 8 characters")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not complete the account request")
	}
}

func writeRoomError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, room.ErrNotFound):
		writeError(w, http.StatusNotFound, "room_not_found", "room not found")
	case errors.Is(err, room.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "invalid_room_name", "room name must be 1–80 characters")
	case errors.Is(err, room.ErrInvalidTopic):
		writeError(w, http.StatusBadRequest, "invalid_topic", "room topic is too long")
	case errors.Is(err, room.ErrEmptyMessage):
		writeError(w, http.StatusBadRequest, "empty_message", "message text is required")
	case errors.Is(err, room.ErrMessageTooLong):
		writeError(w, http.StatusRequestEntityTooLarge, "message_too_long", "message is too long")
	case errors.Is(err, room.ErrMessageNotFound):
		writeError(w, http.StatusNotFound, "message_not_found", "message not found")
	case errors.Is(err, room.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "you are not allowed to do this in this room")
	case errors.Is(err, room.ErrNotMember):
		writeError(w, http.StatusForbidden, "not_member", "you are not a member of this room")
	case errors.Is(err, room.ErrInviteExpired):
		writeError(w, http.StatusGone, "invite_expired", "invitation has expired")
	case errors.Is(err, room.ErrInviteUsed):
		writeError(w, http.StatusConflict, "invite_used", "invitation has already been used")
	case errors.Is(err, room.ErrInviteInvalid), errors.Is(err, room.ErrInviteSecret):
		writeError(w, http.StatusBadRequest, "invalid_invite", "invitation is invalid")
	case errors.Is(err, room.ErrInvalidInviteHistory):
		writeError(w, http.StatusBadRequest, "invalid_history", "history choice must be none, recent, or all")
	case errors.Is(err, room.ErrAttachmentNotFound):
		writeError(w, http.StatusNotFound, "attachment_not_found", "attachment not found")
	case errors.Is(err, room.ErrAttachmentTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "attachment_too_large", "attachment exceeds the 25 MiB limit")
	case errors.Is(err, room.ErrInvalidAttachment):
		writeError(w, http.StatusBadRequest, "invalid_attachment", "attachment is invalid")
	case errors.Is(err, room.ErrAttachmentUsed):
		writeError(w, http.StatusConflict, "attachment_used", "attachment is already attached to a message")
	case errors.Is(err, room.ErrAttachmentsDisabled):
		writeError(w, http.StatusServiceUnavailable, "attachments_disabled", "attachment storage is not configured")
	case errors.Is(err, room.ErrRemoteReadOnly):
		writeError(w, http.StatusConflict, "remote_room", "this room is hosted by another node; make that change there")
	case errors.Is(err, room.ErrKeyUnavailable):
		writeError(w, http.StatusForbidden, "key_unavailable", "this node does not have the key for that history")
	case errors.Is(err, room.ErrCorruptEvent):
		writeError(w, http.StatusInternalServerError, "corrupt_event", "an event failed its integrity check")
	default:
		if errors.Is(err, node.ErrUnreachable) {
			writeError(w, http.StatusBadGateway, "home_unreachable", "could not reach the room's home node")
			return
		}
		var remoteErr *node.RemoteError
		if errors.As(err, &remoteErr) {
			status := remoteErr.Status
			if status < 400 || status > 599 {
				status = http.StatusBadGateway
			}
			code := remoteErr.Code
			if code == "" {
				code = "home_node_error"
			}
			writeError(w, status, code, remoteErr.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not complete the room request")
	}
}

func writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
	case errors.Is(err, agent.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "invalid_agent_name", "agent name must be 1–64 characters")
	case errors.Is(err, agent.ErrInvalidRuntime):
		writeError(w, http.StatusBadRequest, "invalid_runtime", "agent runtime is required")
	case errors.Is(err, agent.ErrNotInRoom):
		writeError(w, http.StatusConflict, "agent_not_in_room", "agent is not in this room")
	case errors.Is(err, agent.ErrAlreadyInRoom):
		writeError(w, http.StatusConflict, "agent_already_in_room", "agent already joined this room")
	case errors.Is(err, agent.ErrRevoked):
		writeError(w, http.StatusConflict, "agent_revoked", "agent has been revoked")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not complete the agent request")
	}
}

func writeApprovalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, approval.ErrNotFound):
		writeError(w, http.StatusNotFound, "approval_not_found", "approval not found")
	case errors.Is(err, approval.ErrNotPending):
		writeError(w, http.StatusConflict, "not_pending", "approval is no longer pending")
	case errors.Is(err, approval.ErrExpired):
		writeError(w, http.StatusGone, "expired", "approval has expired")
	case errors.Is(err, approval.ErrNotGranted):
		writeError(w, http.StatusForbidden, "not_granted", "approval has not been granted")
	case errors.Is(err, approval.ErrConsumed):
		writeError(w, http.StatusConflict, "consumed", "approval has already been used")
	case errors.Is(err, approval.ErrDigestMismatch):
		writeError(w, http.StatusConflict, "digest_mismatch", "action does not match the approved action")
	case errors.Is(err, approval.ErrInvalidAction):
		writeError(w, http.StatusBadRequest, "invalid_action", "approval action is invalid")
	case errors.Is(err, approval.ErrInvalidDecision):
		writeError(w, http.StatusBadRequest, "invalid_decision", "decision must be granted or rejected")
	case errors.Is(err, approval.ErrNotAgentOwner):
		writeError(w, http.StatusForbidden, "not_agent_owner", "the agent belongs to another account")
	case errors.Is(err, approval.ErrAgentRevoked):
		writeError(w, http.StatusConflict, "agent_revoked", "agent has been revoked")
	case errors.Is(err, approval.ErrNotInRoom):
		writeError(w, http.StatusConflict, "agent_not_in_room", "agent is not in this room")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not complete the approval request")
	}
}

func writeToolError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tool.ErrNotFound):
		writeError(w, http.StatusNotFound, "tool_not_found", "tool receipt not found")
	case errors.Is(err, tool.ErrApprovalRequired):
		writeError(w, http.StatusForbidden, "approval_required", "this tool requires human approval")
	case errors.Is(err, tool.ErrNotActive):
		writeError(w, http.StatusConflict, "not_active", "tool receipt is already complete")
	case errors.Is(err, tool.ErrInvalidTool):
		writeError(w, http.StatusBadRequest, "invalid_tool", "tool name is required")
	case errors.Is(err, tool.ErrInvalidKind):
		writeError(w, http.StatusBadRequest, "invalid_tool_kind", "tool kind is invalid")
	case errors.Is(err, tool.ErrNotAgentOwner):
		writeError(w, http.StatusForbidden, "not_agent_owner", "the agent belongs to another account")
	case errors.Is(err, tool.ErrAgentRevoked):
		writeError(w, http.StatusConflict, "agent_revoked", "agent has been revoked")
	case errors.Is(err, tool.ErrNotInRoom):
		writeError(w, http.StatusConflict, "agent_not_in_room", "agent is not in this room")
	case errors.Is(err, approval.ErrExpired), errors.Is(err, approval.ErrConsumed),
		errors.Is(err, approval.ErrDigestMismatch), errors.Is(err, approval.ErrNotGranted):
		writeApprovalError(w, err)
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not complete the tool request")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic serving request", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}
