package node_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type testNode struct {
	node     *node.Service
	accounts *account.Service
	rooms    *room.Service
	snapshot *account.Snapshot
	db       *storage.DB
}

func newNode(t *testing.T, name string) *testNode {
	t.Helper()
	home := t.TempDir()
	paths, err := config.Resolve(home)
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
	accounts := account.New(db, v)
	var rooms *room.Service
	rooms = room.New(db, v, room.WithDataDir(paths.Home), room.WithWriteGuard(
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
	service, err := node.New(node.Options{
		Accounts: accounts, Rooms: rooms, Paths: paths,
		Listen: "127.0.0.1:0", SyncInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := accounts.Create(ctx, name, "password-12345", name+" device")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		cancel()
		db.Close()
	})
	return &testNode{node: service, accounts: accounts, rooms: rooms, snapshot: snapshot, db: db}
}

func TestTwoNodeJoinSyncAndRemoteMessage(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roomRecord, _, err := host.rooms.CreateRoom(ctx, "LAN room", "two nodes", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, roomRecord.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.rooms.PostMessage(ctx, roomRecord.ID, hostActor, room.MessagePostedPayload{Text: "before join"}); err != nil {
		t.Fatal(err)
	}
	invite, _, err := host.rooms.CreateInvite(ctx, roomRecord.ID, hostActor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, roomRecord.ID, invite.Secret, roomRecord.Name)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := guest.node.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if joined.ID != roomRecord.ID || joined.HomeServerID != host.snapshot.Account.ServerID || joined.HomeAddress == "" {
		t.Fatalf("unexpected joined room: %#v", joined)
	}
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := guest.rooms.CreateInvite(ctx, joined.ID, guestActor, room.HistoryNone, 0); !errors.Is(err, room.ErrRemoteReadOnly) {
		t.Fatalf("guest invite on a remote room = %v, want ErrRemoteReadOnly", err)
	}
	events, err := guest.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != room.EventMemberJoined {
		t.Fatalf("history none exposed %#v", events)
	}
	if _, err := host.rooms.PostMessage(ctx, roomRecord.ID, hostActor, room.MessagePostedPayload{Text: "after join"}); err != nil {
		t.Fatal(err)
	}
	if n, err := guest.node.SyncRoom(ctx, joined.ID); err != nil || n != 1 {
		t.Fatalf("sync = %d, %v", n, err)
	}
	events, err = guest.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !strings.Contains(string(events[1].Payload), "after join") {
		t.Fatalf("guest events = %#v", events)
	}

	posted, err := guest.node.PostRemoteMessage(ctx, joined.ID, guestActor, room.MessagePostedPayload{Text: "from guest"})
	if err != nil {
		t.Fatal(err)
	}
	if posted.AuthorID != guest.snapshot.Account.ID || posted.AuthorDevice != guest.snapshot.Device.ID {
		t.Fatalf("posted event = %#v", posted)
	}
	hostEvents, err := host.rooms.ListEvents(ctx, roomRecord.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostEvents) != 6 || hostEvents[5].AuthorID != guest.snapshot.Account.ID {
		t.Fatalf("host events = %#v", hostEvents)
	}

	// Replaying the last sync returns nothing and is idempotent.
	bundle, err := host.rooms.ExportSync(ctx, roomRecord.ID, guest.snapshot.Device.ID, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Events) != 0 {
		t.Fatalf("expected an empty sync, got %d events", len(bundle.Events))
	}
}

func TestNodeManifestAndPinnedTLS(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roomRecord, _, err := host.rooms.CreateRoom(ctx, "Pinned", "", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, roomRecord.ID); err != nil {
		t.Fatal(err)
	}
	invite, _, err := host.rooms.CreateInvite(ctx, roomRecord.ID, hostActor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, roomRecord.ID, invite.Secret, roomRecord.Name)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := node.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	deviceKey, err := guest.accounts.UnsealDeviceKey(guest.snapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	client, err := node.NewClientForURL(decoded.URL, guest.snapshot.Account.ID, guest.snapshot.Device.ID, deviceKey)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := client.Info(ctx)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := node.VerifyManifest(manifest, host.snapshot.Server.PublicKey); err != nil {
		t.Fatal(err)
	}
	if manifest.ServerID != host.snapshot.Account.ServerID || manifest.AccountID != host.snapshot.Account.ID {
		t.Fatalf("manifest identity = %#v", manifest)
	}

	endpoint, _, err := node.ParsePinnedURL(decoded.URL)
	if err != nil {
		t.Fatal(err)
	}
	wrong := node.NodeURL(endpoint, strings.Repeat("00", 32))
	badClient, err := node.NewClientForURL(wrong, guest.snapshot.Account.ID, guest.snapshot.Device.ID, deviceKey)
	if err != nil {
		t.Fatal(err)
	}
	defer badClient.Close()
	if _, err := badClient.Info(ctx); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("wrong pin error = %v", err)
	}
}

func TestRequestReplayAndClockSkew(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	body := []byte(`{"room_id":"r","after":0}`)
	req := httptest.NewRequest("POST", "https://node/toget/v1/rooms/sync", bytes.NewReader(body))
	if err := node.SignRequest(req, body, priv, now); err != nil {
		t.Fatal(err)
	}
	cache := node.NewNonceCache()
	if err := node.VerifyRequest(req, body, pub, cache, now); err != nil {
		t.Fatal(err)
	}
	if err := node.VerifyRequest(req, body, pub, cache, now); !errors.Is(err, node.ErrRequestReplay) {
		t.Fatalf("replay error = %v", err)
	}

	stale := httptest.NewRequest("POST", "https://node/toget/v1/rooms/sync", bytes.NewReader(body))
	if err := node.SignRequest(stale, body, priv, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := node.VerifyRequest(stale, body, pub, node.NewNonceCache(), now); !errors.Is(err, node.ErrRequestUnauthorized) {
		t.Fatalf("stale request error = %v", err)
	}
	tampered := []byte(`{"room_id":"r","after":1}`)
	if err := node.VerifyRequest(req, tampered, pub, node.NewNonceCache(), now); !errors.Is(err, node.ErrRequestUnauthorized) {
		t.Fatalf("tampered request error = %v", err)
	}
}
