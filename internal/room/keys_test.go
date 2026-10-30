package room_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func newKeyedService(t *testing.T) (*room.Service, *storage.DB, string) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "toget.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return room.New(db, v), db, path
}

type keyedActor struct {
	actor room.Actor
	priv  []byte
}

func newKeyedActor(t *testing.T, accountID, deviceID, name string) keyedActor {
	t.Helper()
	_, signing, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	accountPub, _, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, encPriv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	return keyedActor{priv: encPriv, actor: room.Actor{
		AccountID: accountID, DeviceID: deviceID, DisplayName: name,
		SigningKey: signing, AccountPublicKey: accountPub, EncryptionPublicKey: encPub,
	}}
}

func TestRoomKeyWrapRoundTrip(t *testing.T) {
	pub, priv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	_, otherPriv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	wrapped, err := room.WrapRoomKey("room-1", 3, "device-1", pub, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := room.UnwrapRoomKey("room-1", 3, "device-1", priv, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("unwrapped key differs")
	}
	if _, err := room.UnwrapRoomKey("room-1", 3, "device-1", otherPriv, wrapped); err == nil {
		t.Fatal("wrapped key opened with the wrong device key")
	}
	if _, err := room.UnwrapRoomKey("room-1", 3, "device-2", priv, wrapped); err == nil {
		t.Fatal("wrapped key opened for the wrong device id")
	}
	if _, err := room.UnwrapRoomKey("room-1", 4, "device-1", priv, wrapped); err == nil {
		t.Fatal("wrapped key opened for the wrong epoch")
	}
	tampered := append([]byte(nil), wrapped...)
	tampered[len(tampered)-1] ^= 1
	if _, err := room.UnwrapRoomKey("room-1", 3, "device-1", priv, tampered); err == nil {
		t.Fatal("tampered wrapped key opened")
	}
}

func TestRoomPayloadsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	svc, _, path := newKeyedService(t)
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	a := newKeyedActor(t, "account-1", "device-1", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "Secret", "", a.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, a.actor, room.MessagePostedPayload{Text: "top secret text"}); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	var epoch int
	if err := raw.QueryRowContext(ctx, `SELECT payload, epoch FROM events WHERE room_id = ? AND type = ?`,
		created.ID, room.EventMessagePosted).Scan(&payload, &epoch); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(payload, []byte("rke1:")) {
		t.Fatalf("event payload is not sealed: %q", payload)
	}
	if bytes.Contains(payload, []byte("top secret text")) {
		t.Fatal("plaintext leaked into the stored payload")
	}
	if epoch != 1 {
		t.Fatalf("event epoch = %d, want 1", epoch)
	}
	events, err := svc.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !bytes.Contains(events[1].Payload, []byte("top secret text")) {
		t.Fatalf("decrypted events = %#v", events)
	}
}

func TestEpochRotationOnJoinAndLeave(t *testing.T) {
	ctx := context.Background()
	svc, db, _ := newKeyedService(t)
	owner := newKeyedActor(t, "account-1", "device-1", "Owner")
	guest := newKeyedActor(t, "account-2", "device-2", "Guest")
	created, _, err := svc.CreateRoom(ctx, "Shared", "", owner.actor)
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := svc.CreateInvite(ctx, created.ID, owner.actor, room.HistoryAll, 0)
	if err != nil {
		t.Fatal(err)
	}
	joined, _, joinEvent, err := svc.RedeemInvite(ctx, invite.Secret, guest.actor)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Epoch != 2 || joinEvent.Epoch != 2 {
		t.Fatalf("epochs after join = room %d, event %d; want 2", joined.Epoch, joinEvent.Epoch)
	}
	for epoch := 1; epoch <= 2; epoch++ {
		wrapped, err := db.GetRoomMemberKey(ctx, created.ID, epoch, guest.actor.DeviceID)
		if err != nil {
			t.Fatalf("guest key for epoch %d: %v", epoch, err)
		}
		if _, err := room.UnwrapRoomKey(created.ID, epoch, guest.actor.DeviceID, guest.priv, wrapped.Wrapped); err != nil {
			t.Fatalf("unwrap guest key for epoch %d: %v", epoch, err)
		}
	}
	if _, err := svc.Leave(ctx, created.ID, guest.actor); err != nil {
		t.Fatal(err)
	}
	latest, err := svc.GetRoom(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Epoch != 3 {
		t.Fatalf("epoch after leave = %d, want 3", latest.Epoch)
	}
	if _, err := db.GetRoomMemberKey(ctx, created.ID, 3, guest.actor.DeviceID); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("departed device received the new epoch key: %v", err)
	}
	if _, err := db.GetRoomMemberKey(ctx, created.ID, 3, owner.actor.DeviceID); err != nil {
		t.Fatalf("owner key after leave: %v", err)
	}
}

func TestInviteWithoutHistoryWithholdsOldKeys(t *testing.T) {
	ctx := context.Background()
	svc, db, _ := newKeyedService(t)
	owner := newKeyedActor(t, "account-1", "device-1", "Owner")
	guest := newKeyedActor(t, "account-2", "device-2", "Guest")
	created, _, err := svc.CreateRoom(ctx, "No history", "", owner.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, owner.actor, room.MessagePostedPayload{Text: "before you joined"}); err != nil {
		t.Fatal(err)
	}
	invite, _, err := svc.CreateInvite(ctx, created.ID, owner.actor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	joined, _, _, err := svc.RedeemInvite(ctx, invite.Secret, guest.actor)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Epoch != 2 {
		t.Fatalf("epoch = %d, want 2", joined.Epoch)
	}
	if _, err := db.GetRoomMemberKey(ctx, created.ID, 1, guest.actor.DeviceID); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("guest received an old epoch key: %v", err)
	}
	wrapped, err := db.GetRoomMemberKey(ctx, created.ID, 2, guest.actor.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := room.UnwrapRoomKey(created.ID, 2, guest.actor.DeviceID, guest.priv, wrapped.Wrapped); err != nil {
		t.Fatal(err)
	}
}
