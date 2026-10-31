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

type pairAPINode struct {
	server   *testNode
	accounts *account.Service
	rooms    *room.Service
}

func newPairAPINode(t *testing.T, createAccount bool) *pairAPINode {
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
		Listen: "127.0.0.1:0", SyncInterval: time.Hour, AccountSyncInterval: time.Hour, Logger: logger,
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
		Accounts: accounts, Rooms: rooms, Agents: agents, Approvals: approvals,
		Tools: tool.New(db, rooms, approvals, nil), Node: lan,
		Session: session.New(session.DefaultTTL), Version: version.Current(),
		Token: testToken, Listen: "127.0.0.1:7718", Logger: logger,
	}).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	out := &pairAPINode{server: &testNode{url: server.URL, token: testToken}, accounts: accounts, rooms: rooms}
	if createAccount {
		if _, err := accounts.Create(ctx, "Host", "testpass123", "host"); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestPairingAPIEndToEnd(t *testing.T) {
	host := newPairAPINode(t, true)
	guest := newPairAPINode(t, false)
	ctx := context.Background()

	// The account was created directly, so activate the API session with the
	// account password first.
	resp, body := host.server.request(t, http.MethodPost, "/api/v1/session", testToken,
		`{"password":"testpass123"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock host = %d %s", resp.StatusCode, body)
	}
	resp, body = host.server.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"Pair API room"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}
	created := decode[struct {
		Room room.Room `json:"room"`
	}](t, body)
	resp, body = host.server.request(t, http.MethodPost, "/api/v1/rooms/"+created.Room.ID+"/messages", testToken,
		`{"text":"hello from host"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post message = %d %s", resp.StatusCode, body)
	}

	resp, body = host.server.request(t, http.MethodPost, "/api/v1/pairings", testToken, `{}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pairing = %d %s", resp.StatusCode, body)
	}
	pairing := decode[node.PairingView](t, body)
	if pairing.Code == "" || pairing.Status != "waiting" {
		t.Fatalf("pairing = %s", body)
	}
	if _, err := qr.Text(pairing.Code); err != nil {
		t.Fatalf("pairing code does not fit in a QR code: %v", err)
	}
	if _, err := qr.PNG(pairing.Code, 2); err != nil {
		t.Fatalf("render pairing QR: %v", err)
	}
	resp, body = guest.server.request(t, http.MethodPost, "/api/v1/pairing/join", testToken,
		`{"code":`+quote(pairing.Code)+`,"device_name":"Laptop"}`, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start join = %d %s", resp.StatusCode, body)
	}
	join := decode[node.PairJoinView](t, body)

	waitUntil(t, 10*time.Second, func() bool {
		resp, body := host.server.request(t, http.MethodGet, "/api/v1/pairings", testToken, "", nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		list := decode[struct {
			Pairings []node.PairingView `json:"pairings"`
		}](t, body)
		for _, item := range list.Pairings {
			if item.ID == pairing.ID {
				return item.Status == "requested" && item.DeviceName == "Laptop"
			}
		}
		return false
	})
	resp, body = host.server.request(t, http.MethodPost, "/api/v1/pairings/"+pairing.ID+"/approve", testToken, `{}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, body)
	}
	approved := decode[node.PairingView](t, body)
	if approved.Status != "approved" {
		t.Fatalf("approved = %s", body)
	}

	waitUntil(t, 15*time.Second, func() bool {
		resp, body := guest.server.request(t, http.MethodGet, "/api/v1/pairing/join/"+join.ID, testToken, "", nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		view := decode[node.PairJoinView](t, body)
		if view.Status == "failed" || view.Status == "canceled" || view.Status == "expired" {
			t.Fatalf("join failed: %s", body)
		}
		return view.Status == "ready"
	})

	resp, body = guest.server.request(t, http.MethodGet, "/api/v1/status", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("guest status = %d %s", resp.StatusCode, body)
	}
	status := decode[api.StatusResponse](t, body)
	hostSnapshot, err := host.accounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Account == nil || status.Account.ID != hostSnapshot.Account.ID {
		t.Fatalf("guest account = %+v", status.Account)
	}
	if !status.Locked {
		t.Fatal("guest should require the account password after pairing")
	}
	resp, body = guest.server.request(t, http.MethodPost, "/api/v1/session", testToken,
		`{"password":"testpass123"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock guest = %d %s", resp.StatusCode, body)
	}
	resp, body = guest.server.request(t, http.MethodGet, "/api/v1/rooms", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("guest rooms = %d %s", resp.StatusCode, body)
	}
	rooms := decode[struct {
		Rooms []room.Room `json:"rooms"`
	}](t, body)
	if len(rooms.Rooms) != 1 || rooms.Rooms[0].ID != created.Room.ID {
		t.Fatalf("guest rooms = %s", body)
	}
	resp, body = guest.server.request(t, http.MethodGet, "/api/v1/rooms/"+created.Room.ID+"/events", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("guest events = %d %s", resp.StatusCode, body)
	}
	events := decode[struct {
		Events []room.Event `json:"events"`
	}](t, body)
	found := false
	for _, event := range events.Events {
		if event.Type == room.EventMessagePosted && contains(string(event.Payload), "hello from host") {
			found = true
		}
	}
	if !found {
		t.Fatalf("guest events = %s", body)
	}

	guestSnapshot, err := guest.accounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = host.server.request(t, http.MethodPost, "/api/v1/account/sync", testToken, "{}", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("account sync = %d %s", resp.StatusCode, body)
	}
	syncResult := decode[api.AccountSyncResult](t, body)
	if syncResult.Peers < 1 {
		t.Fatalf("account sync result = %s, want at least one paired peer", body)
	}
	resp, body = host.server.request(t, http.MethodDelete, "/api/v1/devices/"+guestSnapshot.Device.ID, testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke paired device = %d %s", resp.StatusCode, body)
	}
	roomDevices, err := host.rooms.ListDevices(ctx, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range roomDevices {
		if device.DeviceID == guestSnapshot.Device.ID && device.RevokedAt == nil {
			t.Fatal("paired device is still active in the room")
		}
	}
}

func waitUntil(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for API condition")
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
