package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/room"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedRoom(t *testing.T, db *DB) *room.Room {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := &room.Room{
		ID:        "room-1",
		Name:      "General",
		HomeNode:  "device-1",
		CreatedBy: "account-1",
		Epoch:     1,
		CreatedAt: now,
	}
	event := &room.Event{
		ID:        "event-1",
		RoomID:    r.ID,
		Seq:       1,
		Type:      room.EventRoomCreated,
		AuthorID:  "account-1",
		Payload:   []byte(`{"name":"General"}`),
		CreatedAt: now,
		Hash:      "hash-1",
	}
	if err := db.CreateRoom(context.Background(), r, event); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOpenMigratesAndReportsVersion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedRoom(t, db)

	if _, err := db.db.ExecContext(ctx, `UPDATE events SET author_id = 'mallory'`); err == nil {
		t.Fatal("UPDATE on events succeeded")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE error = %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM events`); err == nil {
		t.Fatal("DELETE on events succeeded")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE error = %v", err)
	}

	events, err := db.ListEvents(ctx, "room-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].AuthorID != "account-1" {
		t.Fatalf("events changed: %#v", events)
	}
}

func TestDuplicateSequenceIsRejected(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedRoom(t, db)

	now := time.Now().UTC()
	err := db.AppendEvent(ctx, &room.Event{
		ID: "event-2", RoomID: "room-1", Seq: 1, Type: room.EventMessagePosted,
		AuthorID: "account-1", Payload: []byte(`{}`), CreatedAt: now, Hash: "hash-2",
	})
	if err == nil {
		t.Fatal("duplicate sequence was accepted")
	}
	if !strings.Contains(err.Error(), "sequence conflict") {
		t.Fatalf("error = %v, want a sequence conflict", err)
	}
}

func TestUnknownRoomAndEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, err := db.GetRoom(ctx, "missing"); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("GetRoom error = %v", err)
	}
	if _, err := db.LastEvent(ctx, "missing"); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("LastEvent error = %v", err)
	}
	rooms, err := db.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 0 {
		t.Fatalf("new database has %d rooms", len(rooms))
	}
}
