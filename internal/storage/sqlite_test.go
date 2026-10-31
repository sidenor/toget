package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/identity"
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
	pub, _, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	member := &room.Member{
		RoomID: r.ID, AccountID: "account-1", DeviceID: "device-1",
		DisplayName: "Aiden", Role: "owner", Status: "active", JoinedAt: now,
	}
	device := &room.RoomDevice{
		RoomID: r.ID, DeviceID: "device-1", AccountID: "account-1",
		PublicKey: pub, JoinedAt: now,
	}
	epoch := &room.RoomEpoch{RoomID: r.ID, Epoch: 1, KeySealed: "sealed-room-key", CreatedAt: now}
	keys := []room.RoomMemberKey{{
		RoomID: r.ID, Epoch: 1, DeviceID: "device-1", Wrapped: []byte("wrapped-key"), CreatedAt: now,
	}}
	if err := db.CreateRoom(context.Background(), r, event, member, device, epoch, keys); err != nil {
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

func TestListEventsByTypes(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedRoom(t, db)
	now := time.Now().UTC()
	for i, eventType := range []string{room.EventAgentJoined, room.EventMessagePosted, room.EventAgentStatus} {
		if err := db.AppendEvent(ctx, &room.Event{
			ID: "event-" + string(rune('a'+i)), RoomID: "room-1", Seq: int64(i + 2),
			Type: eventType, AuthorID: "account-1", Payload: []byte(`{}`),
			CreatedAt: now.Add(time.Duration(i) * time.Second), Hash: "hash-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := db.ListEventsByTypes(ctx, "room-1", []string{room.EventAgentJoined, room.EventAgentStatus})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != room.EventAgentJoined || events[1].Type != room.EventAgentStatus {
		t.Fatalf("filtered events = %#v", events)
	}
	if events, err := db.ListEventsByTypes(ctx, "room-1", nil); err != nil || events != nil {
		t.Fatalf("empty type list = %#v, %v", events, err)
	}
	if events, err := db.ListEventsByTypes(ctx, "missing", []string{room.EventAgentJoined}); err != nil || len(events) != 0 {
		t.Fatalf("missing room = %#v, %v", events, err)
	}
}

func TestMigrateV5AddsDeviceNodeURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toget.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			server_id TEXT NOT NULL,
			code TEXT NOT NULL,
			name TEXT NOT NULL,
			public_key BLOB NOT NULL,
			private_key BLOB NOT NULL,
			password_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		INSERT INTO accounts (id, server_id, code, name, public_key, private_key, password_hash, created_at)
			VALUES ('account', 'server', '123456', 'Aiden', x'01', x'02', 'hash', 0);
		CREATE TABLE devices (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			name TEXT NOT NULL,
			public_key BLOB NOT NULL,
			private_key BLOB NOT NULL,
			encryption_public_key BLOB NOT NULL DEFAULT x'',
			encryption_private_key BLOB NOT NULL DEFAULT x'',
			certificate TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'node',
			revoked_at INTEGER,
			last_seen_at INTEGER,
			created_at INTEGER NOT NULL,
			local INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE events (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			epoch INTEGER NOT NULL DEFAULT 1,
			type TEXT NOT NULL,
			author_id TEXT NOT NULL,
			author_device TEXT NOT NULL DEFAULT '',
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			prev_id TEXT NOT NULL DEFAULT '',
			prev_hash TEXT NOT NULL DEFAULT '',
			hash TEXT NOT NULL,
			signature TEXT NOT NULL DEFAULT '',
			UNIQUE(room_id, seq)
		);
		PRAGMA user_version = 5;`)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	version, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	rows, err := db.db.QueryContext(context.Background(), `PRAGMA table_info(devices)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "node_url" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("node_url column was not added by the v5 migration")
	}
	var origin string
	if err := db.db.QueryRowContext(context.Background(),
		`SELECT origin_server_id FROM accounts WHERE id = 'account'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "server" {
		t.Fatalf("origin_server_id = %q, want the local server as backfill", origin)
	}
}
