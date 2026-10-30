package room_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func newService(t *testing.T) (*room.Service, *storage.DB) {
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
	return room.New(db, v), db
}

func actor(t *testing.T, accountID, deviceID, name string) room.Actor {
	t.Helper()
	_, priv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	accountPub, _, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptionPub, _, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	return room.Actor{
		AccountID: accountID, DeviceID: deviceID, DisplayName: name,
		SigningKey: priv, AccountPublicKey: accountPub, EncryptionPublicKey: encryptionPub,
	}
}

func TestRoomLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	a := actor(t, "12345678123456", "device-1", "Aiden")

	created, createdEvent, err := svc.CreateRoom(ctx, "General", "Daily work", a)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "General" || created.CreatedBy != a.AccountID || created.HomeNode != a.DeviceID {
		t.Fatalf("unexpected room: %#v", created)
	}
	if createdEvent.Seq != 1 || createdEvent.Type != room.EventRoomCreated {
		t.Fatalf("unexpected first event: %#v", createdEvent)
	}
	if createdEvent.Signature == "" {
		t.Fatal("first event is not signed")
	}

	first, err := svc.PostMessage(ctx, created.ID, a, room.MessagePostedPayload{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.PostMessage(ctx, created.ID, a, room.MessagePostedPayload{Text: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 2 || second.Seq != 3 {
		t.Fatalf("sequences = %d, %d; want 2, 3", first.Seq, second.Seq)
	}
	if first.PrevID != createdEvent.ID || second.PrevID != first.ID {
		t.Fatalf("hash chain links are wrong: %q, %q", first.PrevID, second.PrevID)
	}
	if first.PrevHash != createdEvent.Hash || second.PrevHash != first.Hash {
		t.Fatalf("hash links are wrong: %q, %q", first.PrevHash, second.PrevHash)
	}

	events, err := svc.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Type != room.EventRoomCreated || events[1].Type != room.EventMessagePosted {
		t.Fatalf("unexpected event types: %q, %q", events[0].Type, events[1].Type)
	}
	var payload room.MessagePostedPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text != "hello" || payload.Format != "plain" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	after, err := svc.ListEvents(ctx, created.ID, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Seq != 3 {
		t.Fatalf("after=2 returned %#v", after)
	}

	rooms, err := svc.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].ID != created.ID {
		t.Fatalf("unexpected rooms: %#v", rooms)
	}
	members, err := svc.ListMembers(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Role != "owner" {
		t.Fatalf("unexpected members: %#v", members)
	}
	devices, err := svc.ListDevices(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != a.DeviceID {
		t.Fatalf("unexpected devices: %#v", devices)
	}
}

func TestRoomValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	a := actor(t, "author", "device", "Author")

	if _, _, err := svc.CreateRoom(ctx, "  ", "", a); !errors.Is(err, room.ErrInvalidName) {
		t.Fatalf("blank name error = %v", err)
	}
	if _, _, err := svc.CreateRoom(ctx, strings.Repeat("x", room.MaxNameLength+1), "", a); !errors.Is(err, room.ErrInvalidName) {
		t.Fatalf("long name error = %v", err)
	}
	if _, _, err := svc.CreateRoom(ctx, "General", strings.Repeat("x", room.MaxTopicLength+1), a); !errors.Is(err, room.ErrInvalidTopic) {
		t.Fatalf("long topic error = %v", err)
	}
	created, _, err := svc.CreateRoom(ctx, "General", "", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, a, room.MessagePostedPayload{Text: "  "}); !errors.Is(err, room.ErrEmptyMessage) {
		t.Fatalf("empty message error = %v", err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, a, room.MessagePostedPayload{Text: strings.Repeat("x", room.MaxMessageLength+1)}); !errors.Is(err, room.ErrMessageTooLong) {
		t.Fatalf("long message error = %v", err)
	}
	if _, err := svc.PostMessage(ctx, "missing", a, room.MessagePostedPayload{Text: "hello"}); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("missing room error = %v", err)
	}
	if _, err := svc.ListEvents(ctx, "missing", 0, 0); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("missing room list error = %v", err)
	}
	other := actor(t, "other-account", "other-device", "Other")
	if _, err := svc.PostMessage(ctx, created.ID, other, room.MessagePostedPayload{Text: "hi"}); !errors.Is(err, room.ErrNotMember) {
		t.Fatalf("non-member post error = %v", err)
	}
}

func TestInviteRedeemAndLeave(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	owner := actor(t, "owner-account", "owner-device", "Owner")
	guest := actor(t, "guest-account", "guest-device", "Guest")

	created, _, err := svc.CreateRoom(ctx, "Shared", "", owner)
	if err != nil {
		t.Fatal(err)
	}
	invite, event, err := svc.CreateInvite(ctx, created.ID, owner, room.HistoryRecent, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if invite.Secret == "" || event.Type != room.EventMemberInvited {
		t.Fatalf("unexpected invite: %#v", invite)
	}
	if _, _, _, err := svc.RedeemInvite(ctx, invite.Secret, guest); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.RedeemInvite(ctx, invite.Secret, guest); !errors.Is(err, room.ErrInviteUsed) {
		t.Fatalf("second redemption error = %v", err)
	}
	members, err := svc.ListMembers(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	if _, err := svc.PostMessage(ctx, created.ID, guest, room.MessagePostedPayload{Text: "hello from guest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Leave(ctx, created.ID, guest); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, guest, room.MessagePostedPayload{Text: "after leave"}); !errors.Is(err, room.ErrNotMember) {
		t.Fatalf("post after leave error = %v", err)
	}
}

func TestEditMessage(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	a := actor(t, "account", "device", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "General", "", a)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := svc.PostMessage(ctx, created.ID, a, room.MessagePostedPayload{Text: "typo"})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := svc.EditMessage(ctx, created.ID, a, posted.ID, "fixed")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Type != room.EventMessageEdited || edited.Seq != 3 {
		t.Fatalf("unexpected edit event: %#v", edited)
	}
	if _, err := svc.EditMessage(ctx, created.ID, a, "missing", "fixed"); !errors.Is(err, room.ErrMessageNotFound) {
		t.Fatalf("missing edit error = %v", err)
	}
}

func TestAuditExportVerifiesAndDetectsTamper(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	a := actor(t, "account", "device", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "General", "", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, a, room.MessagePostedPayload{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	audit, err := svc.ExportAudit(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := room.VerifyAudit(audit); err != nil {
		t.Fatal(err)
	}
	audit.Events[1].Payload = json.RawMessage(`{"text":"tampered"}`)
	if err := room.VerifyAudit(audit); !errors.Is(err, room.ErrCorruptEvent) {
		t.Fatalf("tampered audit error = %v", err)
	}
}

func TestSearchAndReadCursor(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	a := actor(t, "account", "device", "Aiden")
	first, _, err := svc.CreateRoom(ctx, "General", "", a)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := svc.CreateRoom(ctx, "Random", "", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, first.ID, a, room.MessagePostedPayload{Text: "needle in a haystack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, second.ID, a, room.MessagePostedPayload{Text: "nothing here"}); err != nil {
		t.Fatal(err)
	}
	hits, err := svc.Search(ctx, "NEEDLE", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].RoomID != first.ID {
		t.Fatalf("unexpected search hits: %#v", hits)
	}
	if err := svc.SetReadCursor(ctx, first.ID, a.DeviceID, 2); err != nil {
		t.Fatal(err)
	}
	unread, err := svc.Unread(ctx, first.ID, a.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if unread != 0 {
		t.Fatalf("unread = %d, want 0", unread)
	}
}

type corruptingStore struct {
	room.Store
}

func (c corruptingStore) ListEvents(ctx context.Context, roomID string, after int64, limit int) ([]room.Event, error) {
	events, err := c.Store.ListEvents(ctx, roomID, after, limit)
	if err != nil || len(events) == 0 {
		return events, err
	}
	events[0].Payload = []byte("enc:v1:not-a-real-payload")
	return events, nil
}

func TestCorruptEventIsDetected(t *testing.T) {
	ctx := context.Background()
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

	svc := room.New(db, v)
	a := actor(t, "account", "device", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "General", "", a)
	if err != nil {
		t.Fatal(err)
	}

	corrupt := room.New(corruptingStore{Store: db}, v)
	if _, err := corrupt.ListEvents(ctx, created.ID, 0, 0); !errors.Is(err, room.ErrCorruptEvent) {
		t.Fatalf("error = %v, want ErrCorruptEvent", err)
	}
}
