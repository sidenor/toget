package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/bridge"
)

const (
	bridgePrefix     = "/api/v1/bridge/"
	bridgeMaxEntries = 500
)

// BridgeMachine is the machine summary shown by the website. It deliberately
// omits the data directory and any key material.
type BridgeMachine struct {
	Name    string      `json:"name"`
	OS      string      `json:"os"`
	Arch    string      `json:"arch"`
	Version string      `json:"version"`
	Listen  string      `json:"listen"`
	Local   string      `json:"local_url"`
	Home    string      `json:"home,omitempty"`
	Locked  bool        `json:"locked"`
	Account *BridgeUser `json:"account,omitempty"`
	Agents  int         `json:"agents"`
}

// BridgeUser is the subset of the local account that is safe to show on the
// official website.
type BridgeUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BridgeAgent is an Agent plus the room names it belongs to.
type BridgeAgent struct {
	agent.Agent
	Rooms []BridgeAgentRoom `json:"rooms,omitempty"`
}

// BridgeAgentRoom names one room an Agent has joined.
type BridgeAgentRoom struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BridgeDirectoryEntry is one folder in the read-only directory browser.
type BridgeDirectoryEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Modified string `json:"modified,omitempty"`
}

// BridgeDirectory is one folder listing. Only directories are listed; file
// names and contents never leave the machine.
type BridgeDirectory struct {
	Path      string                    `json:"path"`
	Relative  string                    `json:"relative"`
	Parent    string                    `json:"parent,omitempty"`
	Home      string                    `json:"home"`
	Roots     []BridgeDirectoryShortcut `json:"roots"`
	Entries   []BridgeDirectoryEntry    `json:"entries"`
	Truncated bool                      `json:"truncated,omitempty"`
}

// BridgeDirectoryShortcut is a conventional folder shown as a shortcut.
type BridgeDirectoryShortcut struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type bridgePairRequest struct {
	Code string `json:"code"`
}

type bridgePairResponse struct {
	Token   string        `json:"token"`
	Bridge  bridge.Public `json:"bridge"`
	Machine BridgeMachine `json:"machine"`
}

type bridgeCreateCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type bridgeBrowserList struct {
	Browsers []bridge.Public `json:"browsers"`
}

type bridgeAgentSettingsRequest struct {
	Name        string          `json:"name"`
	Runtime     string          `json:"runtime"`
	Permissions json.RawMessage `json:"permissions"`
}

func (s *Server) registerBridgeRoutes(mux *http.ServeMux) {
	// Local UI management. These use the normal local token and same-origin
	// cookie, not the website bearer token.
	mux.HandleFunc("POST /api/v1/bridge/code", s.protected(s.handleBridgeCreateCode))
	mux.HandleFunc("GET /api/v1/bridge/browsers", s.data(s.handleBridgeBrowsers))
	mux.HandleFunc("DELETE /api/v1/bridge/browsers/{id}", s.data(s.handleBridgeRevokeBrowser))

	// Website endpoints. The token is issued by POST /pair.
	mux.HandleFunc("GET /api/v1/bridge/info", s.handleBridgeInfo)
	mux.HandleFunc("POST /api/v1/bridge/pair", s.handleBridgePair)
	mux.HandleFunc("POST /api/v1/bridge/unpair", s.bridgeAuth(false, s.handleBridgeUnpair))
	mux.HandleFunc("GET /api/v1/bridge/session", s.bridgeAuth(false, s.handleBridgeSession))
	mux.HandleFunc("GET /api/v1/bridge/machine", s.bridgeAuth(true, s.handleBridgeMachine))
	mux.HandleFunc("GET /api/v1/bridge/directory", s.bridgeAuth(true, s.handleBridgeDirectory))
	mux.HandleFunc("GET /api/v1/bridge/agents", s.bridgeAuth(true, s.handleBridgeAgents))
	mux.HandleFunc("POST /api/v1/bridge/agents", s.bridgeAuth(true, s.handleBridgeCreateAgent))
	mux.HandleFunc("PATCH /api/v1/bridge/agents/{id}", s.bridgeAuth(true, s.handleBridgeUpdateAgent))
	mux.HandleFunc("POST /api/v1/bridge/agents/{id}/revoke", s.bridgeAuth(true, s.handleBridgeRevokeAgent))
}

func originSet(origins []string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range origins {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			continue
		}
		out[u.Scheme+"://"+u.Host] = true
	}
	return out
}

// bridgeCORS adds CORS and Private Network Access headers for the website
// bridge only. The local UI is same-origin and needs none of this.
func (s *Server) bridgeCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, bridgePrefix) {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		allowed := origin != "" && s.bridgeOrigins[origin]
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			if strings.EqualFold(r.Header.Get("Access-Control-Request-Private-Network"), "true") {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
		}
		if r.Method == http.MethodOptions {
			if !allowed {
				writeError(w, http.StatusForbidden, "origin_denied", "request origin is not allowed")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) bridgeOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin != "" && s.bridgeOrigins[origin]
}

func bearer(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

// bridgeAuth validates a website token. When requireUnlock is true, the local
// vault must also be unlocked: listing and changing Agents needs account
// authority, not just a browser token.
func (s *Server) bridgeAuth(requireUnlock bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.bridge == nil {
			writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "the local bridge is not available")
			return
		}
		if !s.bridgeOriginAllowed(r) {
			writeError(w, http.StatusForbidden, "origin_denied", "request origin is not allowed")
			return
		}
		token, err := s.bridge.Validate(bearer(r), r.Header.Get("Origin"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "pair this browser with the local node again")
			return
		}
		if requireUnlock {
			if err := s.session.Require(); err != nil {
				s.accounts.LockVault()
				writeError(w, http.StatusLocked, "locked", "unlock the local node at localhost:7718")
				return
			}
			s.session.Touch()
		}
		snapshot, err := s.accounts.Get(r.Context())
		if err != nil {
			if errors.Is(err, account.ErrNotFound) {
				writeError(w, http.StatusConflict, "no_account", "create an account on the local node first")
				return
			}
			writeAccountError(w, err)
			return
		}
		if token.AccountID != "" && token.AccountID != snapshot.Account.ID {
			writeError(w, http.StatusForbidden, "account_changed", "the local node is signed in to a different account; pair again")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleBridgeInfo(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "the local bridge is not available")
		return
	}
	if !s.bridgeOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "origin_denied", "request origin is not allowed")
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"app": "toget", "bridge": true, "version": s.version.App,
	})
}

func (s *Server) handleBridgeCreateCode(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "the local bridge is not available")
		return
	}
	if _, err := s.accounts.Get(r.Context()); err != nil {
		if errors.Is(err, account.ErrNotFound) {
			writeError(w, http.StatusConflict, "no_account", "create or sign in to an account first")
			return
		}
		writeAccountError(w, err)
		return
	}
	code, expires, err := s.bridge.CreateCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create a pairing code")
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, bridgeCreateCodeResponse{Code: code, ExpiresAt: expires})
}

func (s *Server) handleBridgeBrowsers(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		writeJSON(w, http.StatusOK, bridgeBrowserList{Browsers: []bridge.Public{}})
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, bridgeBrowserList{Browsers: s.bridge.List()})
}

func (s *Server) handleBridgeRevokeBrowser(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "the local bridge is not available")
		return
	}
	if err := s.bridge.Revoke(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "paired browser not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBridgePair(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "the local bridge is not available")
		return
	}
	if !s.bridgeOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "origin_denied", "request origin is not allowed")
		return
	}
	var req bridgePairRequest
	if err := decodeJSONLimit(w, r, &req, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if len(req.Code) != 6 {
		writeError(w, http.StatusBadRequest, "invalid_code", "enter the six-digit code shown in the local Toget window")
		return
	}
	accountID := ""
	if snapshot, err := s.accounts.Get(r.Context()); err == nil {
		accountID = snapshot.Account.ID
	}
	token, record, err := s.bridge.Pair(req.Code, r.Header.Get("Origin"), accountID)
	if err != nil {
		switch {
		case errors.Is(err, bridge.ErrTooManyTokens):
			writeError(w, http.StatusConflict, "too_many_browsers", "too many paired browsers; revoke one in local settings")
		default:
			writeError(w, http.StatusUnauthorized, "invalid_code", "the pairing code is invalid or expired")
		}
		return
	}
	machine, _ := s.bridgeMachineView(r, false)
	noStore(w)
	writeJSON(w, http.StatusOK, bridgePairResponse{Token: token, Bridge: record, Machine: machine})
}

func (s *Server) handleBridgeUnpair(w http.ResponseWriter, r *http.Request) {
	record, err := s.bridge.Validate(bearer(r), r.Header.Get("Origin"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "this browser is not paired")
		return
	}
	if err := s.bridge.Revoke(record.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not disconnect this browser")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBridgeSession(w http.ResponseWriter, r *http.Request) {
	machine, err := s.bridgeMachineView(r, false)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"machine": machine})
}

func (s *Server) handleBridgeMachine(w http.ResponseWriter, r *http.Request) {
	machine, err := s.bridgeMachineView(r, true)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, machine)
}

func (s *Server) bridgeMachineView(r *http.Request, withAgents bool) (BridgeMachine, error) {
	name, _ := os.Hostname()
	if strings.TrimSpace(name) == "" {
		name = "This computer"
	}
	machine := BridgeMachine{
		Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Version: s.version.App, Listen: s.listen, Local: localDisplayURL(s.listen),
	}
	if home, err := os.UserHomeDir(); err == nil {
		machine.Home = home
	}
	unlocked, _ := s.session.State()
	machine.Locked = !unlocked
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return machine, nil
		}
		return machine, err
	}
	machine.Account = &BridgeUser{ID: snapshot.Account.ID, Name: snapshot.Account.Name}
	if withAgents {
		agents, err := s.agents.List(r.Context(), snapshot.Account.ID)
		if err != nil {
			return machine, err
		}
		machine.Agents = len(agents)
	}
	return machine, nil
}

func localDisplayURL(listen string) string {
	_, port, err := splitListen(listen)
	if err != nil || port == "" {
		return ""
	}
	return "http://toget.localhost:" + port
}

func splitListen(listen string) (string, string, error) {
	if strings.HasPrefix(listen, ":") {
		return "", strings.TrimPrefix(listen, ":"), nil
	}
	u, err := url.Parse("//" + listen)
	if err != nil {
		return "", "", err
	}
	return u.Hostname(), u.Port(), nil
}

func (s *Server) handleBridgeAgents(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	agents, err := s.agents.List(r.Context(), snapshot.Account.ID)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	out := make([]BridgeAgent, 0, len(agents))
	for i := range agents {
		item := BridgeAgent{Agent: agents[i]}
		refs, err := s.agents.Rooms(r.Context(), agents[i].ID)
		if err == nil {
			for _, ref := range refs {
				room := BridgeAgentRoom{ID: ref.RoomID}
				if got, err := s.rooms.GetRoom(r.Context(), ref.RoomID); err == nil {
					room.Name = got.Name
				}
				item.Rooms = append(item.Rooms, room)
			}
		}
		out = append(out, item)
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (s *Server) handleBridgeCreateAgent(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	var req bridgeAgentSettingsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	created, err := s.agents.Register(r.Context(), snapshot.Account.ID, req.Name, req.Runtime, req.Permissions)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusCreated, BridgeAgent{Agent: *created})
}

func (s *Server) handleBridgeUpdateAgent(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	var req bridgeAgentSettingsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updated, err := s.agents.UpdateSettings(r.Context(), r.PathValue("id"), snapshot.Account.ID, req.Name, req.Runtime, req.Permissions)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, BridgeAgent{Agent: *updated})
}

func (s *Server) handleBridgeRevokeAgent(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	id := r.PathValue("id")
	if _, err := s.ownedAgent(r, id); err != nil {
		writeAgentError(w, err)
		return
	}
	if err := s.agents.Revoke(r.Context(), id, actor); err != nil {
		writeAgentError(w, err)
		return
	}
	if s.agentRuns != nil {
		s.agentRuns.Stop(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBridgeDirectory(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		writeError(w, http.StatusServiceUnavailable, "no_home", "the local home directory is not available")
		return
	}
	home, err = filepath.Abs(home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve the home directory")
		return
	}
	home, _ = filepath.EvalSymlinks(home)
	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	if requested == "" {
		requested = home
	}
	directory, err := listBridgeDirectory(home, requested)
	if err != nil {
		if errors.Is(err, errDirectoryDenied) {
			writeError(w, http.StatusForbidden, "directory_denied", "this folder is outside the allowed home directory")
			return
		}
		writeError(w, http.StatusNotFound, "directory_unavailable", "this folder cannot be opened")
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, directory)
}

var errDirectoryDenied = errors.New("directory denied")

func listBridgeDirectory(home, requested string) (BridgeDirectory, error) {
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(home, requested)
	}
	clean := filepath.Clean(requested)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return BridgeDirectory{}, err
	}
	resolved = filepath.Clean(resolved)
	if !withinHome(home, resolved) {
		return BridgeDirectory{}, errDirectoryDenied
	}
	relative, err := filepath.Rel(home, resolved)
	if err != nil {
		return BridgeDirectory{}, errDirectoryDenied
	}
	if relative == "." {
		relative = ""
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part != "" && strings.HasPrefix(part, ".") {
			return BridgeDirectory{}, errDirectoryDenied
		}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return BridgeDirectory{}, errors.New("not a directory")
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return BridgeDirectory{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	out := BridgeDirectory{
		Path: resolved, Relative: relative, Home: home,
		Roots:   bridgeShortcuts(home),
		Entries: []BridgeDirectoryEntry{},
	}
	if resolved != home {
		out.Parent = filepath.Dir(resolved)
	}
	for _, entry := range entries {
		if len(out.Entries) >= bridgeMaxEntries {
			out.Truncated = true
			break
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !entry.IsDir() {
			continue
		}
		path := filepath.Join(resolved, name)
		// A symlink must not escape the home directory.
		if target, err := filepath.EvalSymlinks(path); err != nil || !withinHome(home, target) {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, BridgeDirectoryEntry{
			Name: name, Path: path, Modified: entryInfo.ModTime().UTC().Format(time.RFC3339),
		})
	}
	if out.Entries == nil {
		out.Entries = []BridgeDirectoryEntry{}
	}
	return out, nil
}

func withinHome(home, path string) bool {
	home = filepath.Clean(home)
	path = filepath.Clean(path)
	if path == home {
		return true
	}
	return strings.HasPrefix(path, home+string(filepath.Separator))
}

func bridgeShortcuts(home string) []BridgeDirectoryShortcut {
	names := []string{"Desktop", "Documents", "Downloads", "Projects", "Code", "workspace"}
	out := make([]BridgeDirectoryShortcut, 0, len(names))
	for _, name := range names {
		path := filepath.Join(home, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			out = append(out, BridgeDirectoryShortcut{Name: name, Path: path})
		}
	}
	return out
}

func (s *Server) logBridgeError(err error) {
	if err != nil && s.logger != nil {
		s.logger.Warn("bridge request failed", "error", err)
	}
}
