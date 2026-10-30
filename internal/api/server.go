// Package api implements the loopback HTTP API and serves the local web UI.
package api

import (
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
	"strconv"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/version"
)

const (
	cookieName   = "toget_token"
	maxBodyBytes = 1 << 20
)

// Options configures the local API.
type Options struct {
	Accounts *account.Service
	Rooms    *room.Service
	Version  version.Info
	Token    string
	Listen   string
	UI       fs.FS
	Logger   *slog.Logger
}

// Server is the loopback HTTP server.
type Server struct {
	accounts *account.Service
	rooms    *room.Service
	version  version.Info
	token    string
	listen   string
	ui       fs.FS
	logger   *slog.Logger
	origins  map[string]bool
}

// New creates a local API server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		accounts: opts.Accounts,
		rooms:    opts.Rooms,
		version:  opts.Version,
		token:    opts.Token,
		listen:   opts.Listen,
		ui:       opts.UI,
		logger:   logger,
		origins:  allowedOrigins(opts.Listen),
	}
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/v1/status", s.protected(s.handleStatus))
	mux.HandleFunc("GET /api/v1/account", s.protected(s.handleGetAccount))
	mux.HandleFunc("POST /api/v1/account", s.protected(s.handleCreateAccount))
	mux.HandleFunc("GET /api/v1/rooms", s.protected(s.handleListRooms))
	mux.HandleFunc("POST /api/v1/rooms", s.protected(s.handleCreateRoom))
	mux.HandleFunc("GET /api/v1/rooms/{id}", s.protected(s.handleGetRoom))
	mux.HandleFunc("GET /api/v1/rooms/{id}/events", s.protected(s.handleListEvents))
	mux.HandleFunc("POST /api/v1/rooms/{id}/messages", s.protected(s.handlePostMessage))
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
	Version version.Info `json:"version"`
	Account *AccountView `json:"account"`
	Rooms   int          `json:"rooms"`
	Listen  string       `json:"listen"`
}

// AccountView is the public shape of an account.
type AccountView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ServerID   string    `json:"server_id"`
	ServerCode string    `json:"server_code"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		Version: s.version,
		Listen:  s.listen,
	}
	snapshot, err := s.accounts.Get(r.Context())
	if err == nil {
		resp.Account = accountView(snapshot.Account)
	} else if !errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "could not load the account")
		return
	}
	rooms, err := s.rooms.ListRooms(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load rooms")
		return
	}
	resp.Rooms = len(rooms)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountView(snapshot.Account))
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
	writeJSON(w, http.StatusCreated, accountView(snapshot.Account))
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.rooms.ListRooms(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load rooms")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

type createRoomRequest struct {
	Name string `json:"name"`
}

type createRoomResponse struct {
	Room  *room.Room  `json:"room"`
	Event *room.Event `json:"event"`
}

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	var req createRoomRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	created, event, err := s.rooms.CreateRoom(r.Context(), req.Name, snapshot.Account.ID, snapshot.Device.ID)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createRoomResponse{Room: created, Event: event})
}

func (s *Server) handleGetRoom(w http.ResponseWriter, r *http.Request) {
	got, err := s.rooms.GetRoom(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.rooms.ListEvents(r.Context(), r.PathValue("id"), after, limit)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

type postMessageRequest struct {
	Text string `json:"text"`
}

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	var req postMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	event, err := s.rooms.PostMessage(r.Context(), r.PathValue("id"), snapshot.Account.ID, req.Text)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

func accountView(a *account.Account) *AccountView {
	if a == nil {
		return nil
	}
	serverCode := ""
	if len(a.ID) >= 8 {
		serverCode = a.ID[:8]
	}
	return &AccountView{
		ID:         a.ID,
		Name:       a.Name,
		ServerID:   a.ServerID,
		ServerCode: serverCode,
		CreatedAt:  a.CreatedAt,
	}
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

	if seeker, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, time.Time{}, seeker)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "could not read web UI", http.StatusInternalServerError)
		return
	}
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	_, _ = w.Write(data)
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
	case errors.Is(err, room.ErrEmptyMessage):
		writeError(w, http.StatusBadRequest, "empty_message", "message text is required")
	case errors.Is(err, room.ErrMessageTooLong):
		writeError(w, http.StatusRequestEntityTooLarge, "message_too_long", "message is too long")
	case errors.Is(err, room.ErrCorruptEvent):
		writeError(w, http.StatusInternalServerError, "corrupt_event", "an event failed its integrity check")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not complete the room request")
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
