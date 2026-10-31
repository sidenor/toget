package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/session"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/tool"
	"github.com/sidenor/toget/internal/vault"
	"github.com/sidenor/toget/internal/version"
)

const testToken = "test-token-0123456789-abcdefghijklmnop"

type testNode struct {
	url   string
	token string
}

func newTestNode(t *testing.T, ui fs.FS) *testNode {
	t.Helper()
	return newTestNodeWithOptions(t, ui, nil)
}

func newTestNodeWithOptions(t *testing.T, ui fs.FS, configure func(*api.Options)) *testNode {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	accounts := account.New(db, v)
	rooms := room.New(db, v, room.WithDataDir(dir))
	agents := agent.New(db, rooms)
	approvals := approval.New(db, rooms)
	opts := api.Options{
		Accounts:  accounts,
		Rooms:     rooms,
		Agents:    agents,
		Approvals: approvals,
		Tools:     tool.New(db, rooms, approvals),
		Session:   session.New(session.DefaultTTL),
		Version:   version.Current(),
		Token:     testToken,
		Listen:    "127.0.0.1:7718",
		UI:        ui,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if configure != nil {
		configure(&opts)
	}
	handler := api.New(opts).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &testNode{url: server.URL, token: testToken}
}

func (n *testNode) request(t *testing.T, method, path, token, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, n.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return value
}

func TestNodeDisabledAPI(t *testing.T) {
	node := newTestNode(t, nil)
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/node", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node info = %d %s", resp.StatusCode, body)
	}
	info := decode[struct {
		Enabled bool `json:"enabled"`
	}](t, body)
	if info.Enabled {
		t.Fatalf("node info = %s, want disabled", body)
	}

	resp, _ = node.request(t, http.MethodPost, "/api/v1/node/sync", testToken, "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("node sync = %d, want 503", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/account/sync", testToken, "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("account sync = %d, want 503", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/rooms/anything/sync", testToken, "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("room sync = %d, want 503", resp.StatusCode)
	}
}

func TestHealthAndAuth(t *testing.T) {
	node := newTestNode(t, nil)

	resp, body := node.request(t, http.MethodGet, "/healthz", "", "", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("health = %d %q", resp.StatusCode, body)
	}

	resp, _ = node.request(t, http.MethodGet, "/api/v1/status", "", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q", got)
	}

	resp, _ = node.request(t, http.MethodGet, "/api/v1/status", "wrong-token", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", resp.StatusCode)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/status", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	status := decode[api.StatusResponse](t, body)
	if status.Account != nil || status.Rooms != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.Version.App == "" || status.Listen != "127.0.0.1:7718" {
		t.Fatalf("unexpected version/listen: %#v", status)
	}

	resp, _ = node.request(t, http.MethodGet, "/api/v1/status", testToken, "", map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin status = %d, want 403", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodGet, "/api/v1/status", testToken, "", map[string]string{"Origin": "https://127.0.0.1:7718"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("https origin status = %d, want 403", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodGet, "/api/v1/status", testToken, "", map[string]string{"Origin": "http://127.0.0.1:7718"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed origin status = %d, want 200", resp.StatusCode)
	}
}

func TestAccountRoomMessageFlow(t *testing.T) {
	node := newTestNode(t, nil)

	resp, _ := node.request(t, http.MethodGet, "/api/v1/account", testToken, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("account before creation = %d, want 404", resp.StatusCode)
	}

	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken, `{"name":`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed account = %d %s", resp.StatusCode, body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/account", testToken, `{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	created := decode[api.AccountView](t, body)
	if len(created.ID) != 14 || created.ServerCode != created.ID[:8] {
		t.Fatalf("unexpected account view: %#v", created)
	}
	if !strings.HasPrefix(created.ServerID, "tog1:s:") {
		t.Fatalf("server ID = %q", created.ServerID)
	}

	resp, _ = node.request(t, http.MethodPost, "/api/v1/account", testToken, `{"name":"Aiden","password":"testpass123"}`, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate account = %d, want 409", resp.StatusCode)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/account", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get account = %d %s", resp.StatusCode, body)
	}
	if got := decode[api.AccountView](t, body); got.ID != created.ID {
		t.Fatalf("account ID = %q, want %q", got.ID, created.ID)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"General"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}
	var roomCreated struct {
		Room  room.Room  `json:"room"`
		Event room.Event `json:"event"`
	}
	if err := json.Unmarshal(body, &roomCreated); err != nil {
		t.Fatal(err)
	}
	if roomCreated.Room.ID == "" || roomCreated.Event.Seq != 1 || roomCreated.Event.Type != room.EventRoomCreated {
		t.Fatalf("unexpected room creation: %s", body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"General"} {"name":"Extra"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing JSON = %d %s", resp.StatusCode, body)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/rooms", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list rooms = %d %s", resp.StatusCode, body)
	}
	rooms := decode[struct {
		Rooms []room.Room `json:"rooms"`
	}](t, body)
	if len(rooms.Rooms) != 1 || rooms.Rooms[0].ID != roomCreated.Room.ID {
		t.Fatalf("unexpected rooms: %s", body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomCreated.Room.ID+"/messages", testToken, `{"text":"hello"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post message = %d %s", resp.StatusCode, body)
	}
	message := decode[room.Event](t, body)
	if message.Seq != 2 || message.AuthorID != created.ID {
		t.Fatalf("unexpected message: %s", body)
	}

	resp, _ = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomCreated.Room.ID+"/messages", testToken, `{"text":" "}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty message = %d, want 400", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/rooms/missing/messages", testToken, `{"text":"hi"}`, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing room message = %d, want 404", resp.StatusCode)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/rooms/"+roomCreated.Room.ID+"/events?after=0&limit=10", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list events = %d %s", resp.StatusCode, body)
	}
	events := decode[struct {
		Events []room.Event `json:"events"`
	}](t, body)
	if len(events.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(events.Events))
	}
	var payload room.MessagePostedPayload
	if err := json.Unmarshal(events.Events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text != "hello" {
		t.Fatalf("message payload = %#v", payload)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/status", testToken, "", nil)
	status := decode[api.StatusResponse](t, body)
	if resp.StatusCode != http.StatusOK || status.Account == nil || status.Account.ID != created.ID || status.Rooms != 1 {
		t.Fatalf("unexpected status after setup: %d %s", resp.StatusCode, body)
	}
}

func TestWebUISetsCookie(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<!doctype html><title>Toget</title>")},
		"assets/app.js":        &fstest.MapFile{Data: []byte("console.log('toget')")},
		"manifest.webmanifest": &fstest.MapFile{Data: []byte(`{"name":"Toget"}`)},
		"icons/icon-192.png":   &fstest.MapFile{Data: []byte("\x89PNG\r\n\x1a\n")},
	}
	node := newTestNode(t, ui)

	resp, body := node.request(t, http.MethodGet, "/", "", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Toget") {
		t.Fatalf("index = %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("index Cache-Control = %q", got)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "toget_token" || cookies[0].Value != testToken {
		t.Fatalf("cookies = %#v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags = %#v", cookies[0])
	}

	req, err := http.NewRequest(http.MethodGet, node.url+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookies[0])
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie status = %d, want 200", resp.StatusCode)
	}

	resp, body = node.request(t, http.MethodGet, "/assets/app.js", "", "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("toget")) {
		t.Fatalf("asset = %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("asset Cache-Control = %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Fatalf("asset Content-Type = %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q", got)
	}

	resp, _ = node.request(t, http.MethodGet, "/manifest.webmanifest", "", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/manifest+json") {
		t.Fatalf("manifest Content-Type = %q", got)
	}

	resp, _ = node.request(t, http.MethodGet, "/icons/icon-192.png", "", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("icon = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "image/png") {
		t.Fatalf("icon Content-Type = %q", got)
	}

	resp, _ = node.request(t, http.MethodGet, "/assets/missing.js", "", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodGet, "/favicon.ico", "", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file = %d, want 404", resp.StatusCode)
	}

	resp, body = node.request(t, http.MethodGet, "/rooms/anything", "", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Toget") {
		t.Fatalf("SPA fallback = %d %s", resp.StatusCode, body)
	}
}
