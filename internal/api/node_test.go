package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/qr"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/session"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/tool"
	"github.com/sidenor/toget/internal/vault"
	"github.com/sidenor/toget/internal/version"
)

func newTestNodeWithLAN(t *testing.T) *testNode {
	t.Helper()
	dir := t.TempDir()
	paths, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	v, err := vault.LoadOrCreate(paths.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	accounts := account.New(db, v)
	var lan *node.Service
	var rooms *room.Service
	rooms = room.New(db, v, room.WithDataDir(dir), room.WithWriteGuard(
		func(ctx context.Context, roomID, _ string) error {
			record, err := rooms.GetRoom(ctx, roomID)
			if err != nil {
				return err
			}
			snapshot, err := accounts.Get(ctx)
			if err != nil {
				return nil
			}
			if record.HomeServerID != "" && record.HomeServerID != snapshot.Account.ServerID {
				return room.ErrRemoteReadOnly
			}
			return nil
		},
	))
	agents := agent.New(db, rooms)
	approvals := approval.New(db, rooms, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lan, err = node.New(node.Options{
		Accounts: accounts, Rooms: rooms, Paths: paths,
		Listen: "127.0.0.1:0", SyncInterval: time.Hour, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := lan.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		lan.Close()
		cancel()
	})

	handler := api.New(api.Options{
		Accounts:  accounts,
		Rooms:     rooms,
		Agents:    agents,
		Approvals: approvals,
		Tools:     tool.New(db, rooms, approvals, nil),
		Node:      lan,
		Session:   session.New(session.DefaultTTL),
		Version:   version.Current(),
		Token:     testToken,
		Listen:    "127.0.0.1:7718",
		Logger:    logger,
	}).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &testNode{url: server.URL, token: testToken}
}

func TestNodeInfoAndPortableInvite(t *testing.T) {
	server := newTestNodeWithLAN(t)

	resp, body := server.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Host","password":"testpass123","device_name":"host"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}

	resp, body = server.request(t, http.MethodGet, "/api/v1/node", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node info = %d %s", resp.StatusCode, body)
	}
	info := decode[struct {
		Enabled bool      `json:"enabled"`
		Info    node.Info `json:"info"`
	}](t, body)
	if !info.Enabled || info.Info.URL == "" || len(info.Info.TLSFingerprint) != 64 {
		t.Fatalf("node info = %s", body)
	}

	resp, body = server.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"LAN room"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}
	created := decode[struct {
		Room room.Room `json:"room"`
	}](t, body)
	if created.Room.HomeServerID == "" || created.Room.HomeAddress != info.Info.URL {
		t.Fatalf("room home = %#v", created.Room)
	}

	resp, body = server.request(t, http.MethodPost, "/api/v1/rooms/"+created.Room.ID+"/invites", testToken,
		`{"history":"none"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite = %d %s", resp.StatusCode, body)
	}
	invite := decode[struct {
		Invite room.Invite `json:"invite"`
	}](t, body)
	if invite.Invite.Code == "" {
		t.Fatalf("invite has no portable code: %s", body)
	}
	code, err := node.DecodeInvite(invite.Invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if code.URL != info.Info.URL || code.RoomID != created.Room.ID || code.Secret != invite.Invite.Secret {
		t.Fatalf("decoded invite = %#v", code)
	}
	if _, err := qr.Text(invite.Invite.Code); err != nil {
		t.Fatalf("invite does not fit in a QR code: %v", err)
	}
	if _, err := qr.PNG(invite.Invite.Code, 2); err != nil {
		t.Fatalf("render invite QR: %v", err)
	}
}

func TestNodeLookupRequiresDirectory(t *testing.T) {
	server := newTestNodeWithLAN(t)

	resp, body := server.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Host","password":"testpass123","device_name":"host"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	resp, body = server.request(t, http.MethodGet, "/api/v1/node/lookup?code=1234567", testToken, "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short code lookup = %d %s", resp.StatusCode, body)
	}
	resp, body = server.request(t, http.MethodGet, "/api/v1/node/lookup?code=12345678", testToken, "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("lookup without directory = %d %s", resp.StatusCode, body)
	}

	resp, body = server.request(t, http.MethodGet, "/api/v1/node/discover", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discover = %d %s", resp.StatusCode, body)
	}
	var discovery struct {
		Enabled bool  `json:"enabled"`
		Peers   []any `json:"peers"`
	}
	if err := json.Unmarshal(body, &discovery); err != nil {
		t.Fatal(err)
	}
	if discovery.Enabled || len(discovery.Peers) != 0 {
		t.Fatalf("unexpected discovery response: %s", body)
	}
}

func TestSetNodeRelayAPI(t *testing.T) {
	server := newTestNodeWithLAN(t)
	resp, body := server.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Host","password":"testpass123","device_name":"host"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}

	resp, body = server.request(t, http.MethodGet, "/api/v1/node", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node info = %d %s", resp.StatusCode, body)
	}
	initial := decode[struct {
		Info node.Info `json:"info"`
	}](t, body)
	if initial.Info.RelayMode != "off" || initial.Info.RelaySetting != "off" {
		t.Fatalf("initial relay = %#v", initial.Info)
	}

	resp, body = server.request(t, http.MethodPost, "/api/v1/node/relay", testToken, `{"relay":"auto"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("automatic relay without directory = %d %s", resp.StatusCode, body)
	}
	resp, body = server.request(t, http.MethodPost, "/api/v1/node/relay", testToken,
		`{"relay":"http://relay.example.com#fp=`+strings.Repeat("ab", 32)+`"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid relay URL = %d %s", resp.StatusCode, body)
	}
	resp, body = server.request(t, http.MethodPost, "/api/v1/node/relay", testToken, `{"relay":"auto","extra":true}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", resp.StatusCode, body)
	}

	resp, body = server.request(t, http.MethodPost, "/api/v1/node/relay", testToken, `{"relay":"off"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable relay = %d %s", resp.StatusCode, body)
	}
	updated := decode[struct {
		Enabled bool      `json:"enabled"`
		Info    node.Info `json:"info"`
	}](t, body)
	if !updated.Enabled || updated.Info.RelayMode != "off" || updated.Info.RelaySetting != "off" {
		t.Fatalf("updated relay = %s", body)
	}
}

func TestRotateAccountKeyAPI(t *testing.T) {
	server := newTestNodeWithLAN(t)
	resp, body := server.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Host","password":"testpass123","device_name":"host"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	view := decode[struct {
		KeyGeneration int `json:"key_generation"`
	}](t, body)
	if view.KeyGeneration != 1 {
		t.Fatalf("initial key generation = %d", view.KeyGeneration)
	}
	resp, body = server.request(t, http.MethodPost, "/api/v1/account/rotate-key", testToken, `{}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate key = %d %s", resp.StatusCode, body)
	}
	result := decode[struct {
		Generation int      `json:"generation"`
		Devices    int      `json:"devices"`
		Warnings   []string `json:"warnings"`
	}](t, body)
	if result.Generation != 2 || result.Devices != 1 || len(result.Warnings) != 0 {
		t.Fatalf("rotate result = %#v", result)
	}
	resp, body = server.request(t, http.MethodGet, "/api/v1/account", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get account = %d %s", resp.StatusCode, body)
	}
	view = decode[struct {
		KeyGeneration int `json:"key_generation"`
	}](t, body)
	if view.KeyGeneration != 2 {
		t.Fatalf("account key generation = %d, want 2", view.KeyGeneration)
	}
}
