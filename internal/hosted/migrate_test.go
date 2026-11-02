//go:build !js

package hosted

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateV5ToV6 rebuilds the two tables that used to require a local
// hosted_accounts row. Remote room members and federated friend requests must
// survive that account not existing on this host.
func TestMigrateV5ToV6(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range hostSchema {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			raw.Close()
			t.Fatalf("create schema: %v", err)
		}
	}
	// Replace the current tables with their version 5 shape: both carried
	// foreign keys into hosted_accounts, and friend requests had no cached
	// display names or host metadata.
	downgrade := []string{
		`DROP TABLE hosted_friend_requests`,
		`DROP TABLE hosted_room_members`,
		`CREATE TABLE hosted_room_members (
			room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
			account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			status TEXT NOT NULL,
			history TEXT NOT NULL DEFAULT 'none',
			joined_at INTEGER NOT NULL,
			left_at INTEGER,
			PRIMARY KEY (room_id, account_id)
		)`,
		`CREATE TABLE hosted_friend_requests (
			id TEXT PRIMARY KEY,
			from_account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
			to_account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
			room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
			sealed TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			UNIQUE (from_account_id, to_account_id, room_id)
		)`,
	}
	for _, statement := range downgrade {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			raw.Close()
			t.Fatalf("downgrade schema: %v", err)
		}
	}
	const (
		aliceID = "11111111000001"
		bobID   = "22222222000001"
		roomID  = "11111111-2222-3333-4444-555555555555"
	)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO hosted_accounts
		    (id, code, name, public_key, opaque_record, export_wrap, recovery_wrap, key_bundle, created_at, updated_at)
		  VALUES (?, '11111111', 'Alice', x'00', x'', '', '', '', 0, 0)`, []any{aliceID}},
		{`INSERT INTO hosted_accounts
		    (id, code, name, public_key, opaque_record, export_wrap, recovery_wrap, key_bundle, created_at, updated_at)
		  VALUES (?, '22222222', 'Bob', x'00', x'', '', '', '', 0, 0)`, []any{bobID}},
		{`INSERT INTO hosted_rooms (id, owner_account_id, epoch, created_at, updated_at)
		  VALUES (?, ?, 1, 0, 0)`, []any{roomID, bobID}},
		{`INSERT INTO hosted_room_members (room_id, account_id, role, status, history, joined_at)
		  VALUES (?, ?, 'member', 'active', 'all', 0)`, []any{roomID, aliceID}},
		{`INSERT INTO hosted_friend_requests
		    (id, from_account_id, to_account_id, room_id, sealed, created_at, expires_at)
		  VALUES ('req-1', ?, ?, ?, 'toget-sealed:v1:abc', 0, 9999999999999)`, []any{aliceID, bobID, roomID}},
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(ctx, statement.query, statement.args...); err != nil {
			raw.Close()
			t.Fatalf("seed schema: %v", err)
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open and migrate: %v", err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != hostSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, hostSchemaVersion)
	}
	if references, err := store.tableReferences(ctx, "hosted_room_members", "hosted_accounts"); err != nil || references {
		t.Fatalf("room members still reference accounts (err=%v)", err)
	}
	if references, err := store.tableReferences(ctx, "hosted_room_members", "hosted_rooms"); err != nil || !references {
		t.Fatalf("room members lost the room reference (err=%v)", err)
	}
	if references, err := store.tableReferences(ctx, "hosted_friend_requests", "hosted_accounts"); err != nil || references {
		t.Fatalf("friend requests still reference accounts (err=%v)", err)
	}
	var fromName, toName, fromHostID, toHostID string
	if err := store.db.QueryRowContext(ctx,
		`SELECT from_name, to_name, from_host_id, to_host_id
		   FROM hosted_friend_requests WHERE id = 'req-1'`).Scan(
		&fromName, &toName, &fromHostID, &toHostID); err != nil {
		t.Fatal(err)
	}
	if fromName != "Alice" || toName != "Bob" || fromHostID != "" || toHostID != "" {
		t.Fatalf("migrated request = %q/%q/%q/%q", fromName, toName, fromHostID, toHostID)
	}

	// Removing the sender's local account must not take the remote room
	// membership or the federated request with it.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM hosted_accounts WHERE id = ?`, aliceID); err != nil {
		t.Fatalf("delete sender account: %v", err)
	}
	var members, requests int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosted_room_members WHERE account_id = ?`, aliceID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosted_friend_requests WHERE id = 'req-1'`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if members != 1 || requests != 1 {
		t.Fatalf("after account delete: members=%d requests=%d", members, requests)
	}
}
