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

func TestRemoteAttachmentRoundTrip(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest1 := newNode(t, "GuestOne")
	guest2 := newNode(t, "GuestTwo")
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roomRecord, _, err := host.rooms.CreateRoom(ctx, "Files", "", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, roomRecord.ID); err != nil {
		t.Fatal(err)
	}
	join := func(n *testNode) *room.Room {
		t.Helper()
		invite, _, err := host.rooms.CreateInvite(ctx, roomRecord.ID, hostActor, room.HistoryNone, 0)
		if err != nil {
			t.Fatal(err)
		}
		code, err := host.node.InviteCode(ctx, roomRecord.ID, invite.Secret, roomRecord.Name)
		if err != nil {
			t.Fatal(err)
		}
		joined, err := n.node.Join(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return joined
	}
	joined1 := join(guest1)
	joined2 := join(guest2)
	guest1Actor, err := guest1.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("remote attachment payload")
	attachment, err := guest1.node.UploadAttachment(ctx, joined1.ID, guest1Actor, "note.txt", "text/plain", payload)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.ID == "" || attachment.Path == "" {
		t.Fatalf("sender attachment = %#v", attachment)
	}
	if _, err := guest1.node.PostRemoteMessage(ctx, joined1.ID, guest1Actor, room.MessagePostedPayload{
		Text: "with a file", Attachments: []string{attachment.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := guest2.node.SyncRoom(ctx, joined2.ID); err != nil {
		t.Fatal(err)
	}
	guest2Actor, err := guest2.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fetchedMeta, err := guest2.rooms.GetAttachment(ctx, attachment.ID, guest2Actor)
	if err != nil {
		t.Fatal(err)
	}
	if fetchedMeta.EventID == "" || fetchedMeta.Path != "" {
		t.Fatalf("synced attachment = %#v", fetchedMeta)
	}
	if _, _, err := guest2.rooms.OpenAttachment(ctx, attachment.ID, guest2Actor); !errors.Is(err, room.ErrAttachmentNotFound) {
		t.Fatalf("uncached open = %v, want ErrAttachmentNotFound", err)
	}
	if err := guest2.node.FetchAttachment(ctx, attachment.ID, guest2Actor); err != nil {
		t.Fatal(err)
	}
	_, got, err := guest2.rooms.OpenAttachment(ctx, attachment.ID, guest2Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload = %q", got)
	}

	hostActor, err = host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, hostBytes, err := host.rooms.OpenAttachment(ctx, attachment.ID, hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hostBytes, payload) {
		t.Fatalf("home payload = %q", hostBytes)
	}

	// A remote device cannot read an attachment from a room it never joined.
	if _, _, err := host.rooms.ReadAttachmentCiphertext(ctx, roomRecord.ID, attachment.ID, guest2.snapshot.Account.ID, "not-a-device"); err == nil {
		t.Fatal("unknown device read home attachment")
	}
}

func TestRemoteLeaveRotatesAndRevokes(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	other := newNode(t, "Other")
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roomRecord, _, err := host.rooms.CreateRoom(ctx, "Leave", "", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, roomRecord.ID); err != nil {
		t.Fatal(err)
	}
	join := func(n *testNode) *room.Room {
		t.Helper()
		invite, _, err := host.rooms.CreateInvite(ctx, roomRecord.ID, hostActor, room.HistoryNone, 0)
		if err != nil {
			t.Fatal(err)
		}
		code, err := host.node.InviteCode(ctx, roomRecord.ID, invite.Secret, roomRecord.Name)
		if err != nil {
			t.Fatal(err)
		}
		joined, err := n.node.Join(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return joined
	}
	joined := join(guest)
	otherJoined := join(other)
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeEpoch := joined.Epoch
	left, err := guest.node.LeaveRoom(ctx, joined.ID, guestActor)
	if err != nil {
		t.Fatal(err)
	}
	if left.Type != room.EventMemberLeft {
		t.Fatalf("leave event = %#v", left)
	}
	hostActor, err = host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.rooms.PostMessage(ctx, roomRecord.ID, hostActor, room.MessagePostedPayload{Text: "after leave"}); err != nil {
		t.Fatal(err)
	}
	hostRoom, err := host.rooms.GetRoom(ctx, roomRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hostRoom.Epoch <= beforeEpoch {
		t.Fatalf("home epoch did not rotate: %d -> %d", beforeEpoch, hostRoom.Epoch)
	}
	members, err := host.rooms.ListMembers(ctx, roomRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.AccountID == guest.snapshot.Account.ID && member.Status != "left" {
			t.Fatalf("guest member status = %q", member.Status)
		}
	}
	if _, err := guest.node.PostRemoteMessage(ctx, joined.ID, guestActor, room.MessagePostedPayload{Text: "should fail"}); err == nil {
		t.Fatal("left member could still post")
	}
	if _, err := guest.node.SyncRoom(ctx, joined.ID); err == nil {
		t.Fatal("left member could still sync")
	}
	leftRoom, err := guest.rooms.GetRoom(ctx, joined.ID)
	if err != nil {
		t.Fatal(err)
	}
	if leftRoom.ArchivedAt == nil {
		t.Fatal("left remote room was not archived on the leaving device")
	}
	otherActor, err := other.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.node.SyncRoom(ctx, otherJoined.ID); err != nil {
		t.Fatal(err)
	}
	events, err := other.rooms.ListEvents(ctx, otherJoined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundLeave, foundAfter := false, false
	for _, event := range events {
		if event.Type == room.EventMemberLeft && event.AuthorID == guest.snapshot.Account.ID {
			foundLeave = true
		}
		if event.Type == room.EventMessagePosted && strings.Contains(string(event.Payload), "after leave") {
			foundAfter = true
		}
	}
	if !foundLeave || !foundAfter {
		t.Fatalf("other member events leave=%v after=%v", foundLeave, foundAfter)
	}
	if _, err := other.node.PostRemoteMessage(ctx, otherJoined.ID, otherActor, room.MessagePostedPayload{Text: "still here"}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteArchiveStaysLocalAndPropagates(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joinRoom := func(name string) *room.Room {
		t.Helper()
		record, _, err := host.rooms.CreateRoom(ctx, name, "", hostActor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := host.node.EnsureRoomHome(ctx, record.ID); err != nil {
			t.Fatal(err)
		}
		invite, _, err := host.rooms.CreateInvite(ctx, record.ID, hostActor, room.HistoryNone, 0)
		if err != nil {
			t.Fatal(err)
		}
		code, err := host.node.InviteCode(ctx, record.ID, invite.Secret, record.Name)
		if err != nil {
			t.Fatal(err)
		}
		joined, err := guest.node.Join(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return joined
	}
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	local := joinRoom("Local archive")
	if _, _, err := guest.node.ArchiveRoom(ctx, local.ID, guestActor); err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.SyncRoom(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	guestRoom, err := guest.rooms.GetRoom(ctx, local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guestRoom.ArchivedAt == nil {
		t.Fatal("local archive was cleared by sync")
	}
	hostRecord, err := host.rooms.GetRoom(ctx, local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hostRecord.ArchivedAt != nil {
		t.Fatal("guest archive changed the home room")
	}
	if _, err := guest.node.SyncRoom(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	guestRoom, err = guest.rooms.GetRoom(ctx, local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guestRoom.ArchivedAt == nil {
		t.Fatal("local archive did not survive a second sync")
	}

	shared := joinRoom("Shared archive")
	if _, _, err := host.node.ArchiveRoom(ctx, shared.ID, hostActor); err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.SyncRoom(ctx, shared.ID); err != nil {
		t.Fatal(err)
	}
	guestRoom, err = guest.rooms.GetRoom(ctx, shared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guestRoom.ArchivedAt == nil {
		t.Fatal("home archive did not propagate to the guest")
	}
	events, err := guest.rooms.ListEvents(ctx, shared.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == room.EventRoomArchived {
			found = true
		}
	}
	if !found {
		t.Fatal("guest did not import room.archived")
	}
}
