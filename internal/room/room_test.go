package room_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func newService(t *testing.T) *room.Service {
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
	return room.New(db, v)
}

func TestRoomLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	created, createdEvent, err := svc.CreateRoom(ctx, "General", "12345678123456", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "General" || created.CreatedBy != "12345678123456" || created.HomeNode != "device-1" {
		t.Fatalf("unexpected room: %#v", created)
	}
	if createdEvent.Seq != 1 || createdEvent.Type != room.EventRoomCreated {
		t.Fatalf("unexpected first event: %#v", createdEvent)
	}

	first, err := svc.PostMessage(ctx, created.ID, "12345678123456", "hello")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.PostMessage(ctx, created.ID, "12345678123456", "again")
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 2 || second.Seq != 3 {
		t.Fatalf("sequences = %d, %d; want 2, 3", first.Seq, second.Seq)
	}
	if first.PrevID != createdEvent.ID || second.PrevID != first.ID {
		t.Fatalf("hash chain links are wrong: %q, %q", first.PrevID, second.PrevID)
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
}

func TestRoomValidation(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	if _, _, err := svc.CreateRoom(ctx, "  ", "author", "device"); !errors.Is(err, room.ErrInvalidName) {
		t.Fatalf("blank name error = %v", err)
	}
	if _, _, err := svc.CreateRoom(ctx, strings.Repeat("x", room.MaxNameLength+1), "author", "device"); !errors.Is(err, room.ErrInvalidName) {
		t.Fatalf("long name error = %v", err)
	}
	created, _, err := svc.CreateRoom(ctx, "General", "author", "device")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, "author", "  "); !errors.Is(err, room.ErrEmptyMessage) {
		t.Fatalf("empty message error = %v", err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, "author", strings.Repeat("x", room.MaxMessageLength+1)); !errors.Is(err, room.ErrMessageTooLong) {
		t.Fatalf("long message error = %v", err)
	}
	if _, err := svc.PostMessage(ctx, "missing", "author", "hello"); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("missing room error = %v", err)
	}
	if _, err := svc.ListEvents(ctx, "missing", 0, 0); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("missing room list error = %v", err)
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
	created, _, err := svc.CreateRoom(ctx, "General", "author", "device")
	if err != nil {
		t.Fatal(err)
	}

	corrupt := room.New(corruptingStore{Store: db}, v)
	if _, err := corrupt.ListEvents(ctx, created.ID, 0, 0); !errors.Is(err, room.ErrCorruptEvent) {
		t.Fatalf("error = %v, want ErrCorruptEvent", err)
	}
}
