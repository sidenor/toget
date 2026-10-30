// Package storage implements the local SQLite store.
package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/room"
)

//go:embed schema.sql
var schemaSQL string

const schemaVersion = 1

// DB is the SQLite-backed local store.
type DB struct {
	db *sql.DB
}

// Open opens and migrates a database.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("storage: empty database path")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("storage: create directory: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db := &DB{db: sqlDB}
	if err := db.migrate(context.Background()); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (d *DB) Close() error {
	return d.db.Close()
}

// Ping checks the database connection.
func (d *DB) Ping(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

// SchemaVersion returns the current schema version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("storage: read schema version: %w", err)
	}
	return v, nil
}

func (d *DB) migrate(ctx context.Context) error {
	version, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	switch {
	case version == 0:
		if _, err := d.db.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("storage: create schema: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("storage: set schema version: %w", err)
		}
	case version > schemaVersion:
		return fmt.Errorf("storage: database schema %d is newer than this binary (%d)", version, schemaVersion)
	}
	return nil
}

// GetServer returns the local server identity.
func (d *DB) GetServer(ctx context.Context) (*account.Server, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, code, public_key, private_key, nonce, manifest, created_at
		FROM servers ORDER BY created_at LIMIT 1`)
	var s account.Server
	var created int64
	if err := row.Scan(&s.ID, &s.Code, &s.PublicKey, &s.PrivateKey, &s.Nonce, &s.Manifest, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, account.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get server: %w", err)
	}
	s.CreatedAt = fromMillis(created)
	return &s, nil
}

// GetAccount returns the local account.
func (d *DB) GetAccount(ctx context.Context) (*account.Account, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, server_id, code, name, public_key, private_key, password_hash, created_at
		FROM accounts ORDER BY created_at LIMIT 1`)
	var a account.Account
	var created int64
	if err := row.Scan(&a.ID, &a.ServerID, &a.Code, &a.Name, &a.PublicKey, &a.PrivateKey, &a.PasswordHash, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, account.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get account: %w", err)
	}
	a.CreatedAt = fromMillis(created)
	return &a, nil
}

// GetDevice returns the first device for an account.
func (d *DB) GetDevice(ctx context.Context, accountID string) (*account.Device, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, account_id, name, public_key, private_key, certificate, created_at
		FROM devices WHERE account_id = ? ORDER BY created_at LIMIT 1`, accountID)
	var dev account.Device
	var created int64
	if err := row.Scan(&dev.ID, &dev.AccountID, &dev.Name, &dev.PublicKey, &dev.PrivateKey, &dev.Certificate, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, account.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get device: %w", err)
	}
	dev.CreatedAt = fromMillis(created)
	return &dev, nil
}

// CreateAccount inserts a server (if new), an account, and its first device.
func (d *DB) CreateAccount(ctx context.Context, server *account.Server, acct *account.Account, dev *account.Device) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin account: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO servers (id, code, public_key, private_key, nonce, manifest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		server.ID, server.Code, server.PublicKey, server.PrivateKey, server.Nonce, server.Manifest, toMillis(server.CreatedAt)); err != nil {
		return fmt.Errorf("storage: insert server: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO accounts (id, server_id, code, name, public_key, private_key, password_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		acct.ID, acct.ServerID, acct.Code, acct.Name, acct.PublicKey, acct.PrivateKey, acct.PasswordHash, toMillis(acct.CreatedAt)); err != nil {
		if isUniqueViolation(err) {
			return account.ErrAlreadyExists
		}
		return fmt.Errorf("storage: insert account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO devices (id, account_id, name, public_key, private_key, certificate, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		dev.ID, dev.AccountID, dev.Name, dev.PublicKey, dev.PrivateKey, dev.Certificate, toMillis(dev.CreatedAt)); err != nil {
		return fmt.Errorf("storage: insert device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return account.ErrAlreadyExists
		}
		return fmt.Errorf("storage: commit account: %w", err)
	}
	return nil
}

// AccountCodeExists reports whether a 6-digit code is used on a server.
func (d *DB) AccountCodeExists(ctx context.Context, serverID, code string) (bool, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM accounts WHERE server_id = ? AND code = ?`, serverID, code).Scan(&n); err != nil {
		return false, fmt.Errorf("storage: check account code: %w", err)
	}
	return n > 0, nil
}

// CreateRoom inserts a room and its first event in one transaction.
func (d *DB) CreateRoom(ctx context.Context, r *room.Room, event *room.Event) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin room: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rooms (id, name, home_node, created_by, epoch, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.HomeNode, r.CreatedBy, r.Epoch, toMillis(r.CreatedAt)); err != nil {
		return fmt.Errorf("storage: insert room: %w", err)
	}
	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit room: %w", err)
	}
	return nil
}

// ListRooms returns rooms newest first.
func (d *DB) ListRooms(ctx context.Context) ([]room.Room, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, name, home_node, created_by, epoch, created_at
		FROM rooms ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list rooms: %w", err)
	}
	defer rows.Close()

	var out []room.Room
	for rows.Next() {
		var r room.Room
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.HomeNode, &r.CreatedBy, &r.Epoch, &created); err != nil {
			return nil, fmt.Errorf("storage: scan room: %w", err)
		}
		r.CreatedAt = fromMillis(created)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list rooms: %w", err)
	}
	return out, nil
}

// GetRoom returns one room.
func (d *DB) GetRoom(ctx context.Context, id string) (*room.Room, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, name, home_node, created_by, epoch, created_at
		FROM rooms WHERE id = ?`, id)
	var r room.Room
	var created int64
	if err := row.Scan(&r.ID, &r.Name, &r.HomeNode, &r.CreatedBy, &r.Epoch, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get room: %w", err)
	}
	r.CreatedAt = fromMillis(created)
	return &r, nil
}

// LastEvent returns the highest-sequence event in a room.
func (d *DB) LastEvent(ctx context.Context, roomID string) (*room.Event, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, seq, type, author_id, payload, created_at, prev_id, hash
		FROM events WHERE room_id = ? ORDER BY seq DESC LIMIT 1`, roomID)
	event, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: last event: %w", err)
	}
	return event, nil
}

// AppendEvent inserts an event. The caller assigns the sequence.
func (d *DB) AppendEvent(ctx context.Context, event *room.Event) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin event: %w", err)
	}
	defer tx.Rollback()
	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit event: %w", err)
	}
	return nil
}

// ListEvents returns events after a sequence, oldest first.
func (d *DB) ListEvents(ctx context.Context, roomID string, after int64, limit int) ([]room.Event, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, room_id, seq, type, author_id, payload, created_at, prev_id, hash
		FROM events WHERE room_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		roomID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list events: %w", err)
	}
	defer rows.Close()

	var out []room.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan event: %w", err)
		}
		out = append(out, *event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list events: %w", err)
	}
	return out, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, event *room.Event) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, room_id, seq, type, author_id, payload, created_at, prev_id, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.RoomID, event.Seq, event.Type, event.AuthorID, event.Payload,
		toMillis(event.CreatedAt), event.PrevID, event.Hash); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("storage: event sequence conflict: %w", err)
		}
		return fmt.Errorf("storage: insert event: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(row rowScanner) (*room.Event, error) {
	var event room.Event
	var created int64
	if err := row.Scan(&event.ID, &event.RoomID, &event.Seq, &event.Type, &event.AuthorID,
		&event.Payload, &created, &event.PrevID, &event.Hash); err != nil {
		return nil, err
	}
	event.CreatedAt = fromMillis(created)
	return &event, nil
}

func toMillis(t time.Time) int64 {
	return t.UTC().UnixMilli()
}

func fromMillis(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
