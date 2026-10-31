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

const schemaVersion = 7

// CurrentSchemaVersion is the newest database schema this build understands.
// Backup/restore uses it to reject archives from newer builds.
const CurrentSchemaVersion = schemaVersion

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
func (d *DB) Close() error { return d.db.Close() }

// Ping checks the database connection.
func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

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
	if version > schemaVersion {
		return fmt.Errorf("storage: database schema %d is newer than this binary (%d)", version, schemaVersion)
	}
	if version == 0 {
		if _, err := d.db.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("storage: create schema: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("storage: set schema version: %w", err)
		}
		return nil
	}
	migrations := []func(context.Context) error{
		d.migrateV1, d.migrateV2, d.migrateV3, d.migrateV4, d.migrateV5, d.migrateV6,
	}
	for from := version; from < schemaVersion; from++ {
		if from < 1 || from > len(migrations) {
			return fmt.Errorf("storage: no migration path from schema %d", from)
		}
		if err := migrations[from-1](ctx); err != nil {
			return err
		}
	}
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("storage: set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV1(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`ALTER TABLE devices ADD COLUMN encryption_public_key BLOB NOT NULL DEFAULT x''`,
		`ALTER TABLE devices ADD COLUMN encryption_private_key BLOB NOT NULL DEFAULT x''`,
		`ALTER TABLE devices ADD COLUMN role TEXT NOT NULL DEFAULT 'node'`,
		`ALTER TABLE devices ADD COLUMN revoked_at INTEGER`,
		`ALTER TABLE devices ADD COLUMN last_seen_at INTEGER`,
		`ALTER TABLE rooms ADD COLUMN topic TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rooms ADD COLUMN home_server_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rooms ADD COLUMN home_address TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rooms ADD COLUMN archived_at INTEGER`,
		`ALTER TABLE events ADD COLUMN author_device TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE events ADD COLUMN prev_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE events ADD COLUMN signature TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE events ADD COLUMN epoch INTEGER NOT NULL DEFAULT 1`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("storage: migrate v1: %w", err)
		}
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS room_members (
			room_id TEXT NOT NULL REFERENCES rooms(id), account_id TEXT NOT NULL,
			device_id TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'member', status TEXT NOT NULL DEFAULT 'active',
			account_public_key BLOB NOT NULL DEFAULT x'', encryption_public_key BLOB NOT NULL DEFAULT x'',
			joined_at INTEGER NOT NULL, left_at INTEGER, PRIMARY KEY (room_id, account_id))`,
		`CREATE INDEX IF NOT EXISTS room_members_account ON room_members(account_id)`,
		`CREATE TABLE IF NOT EXISTS room_devices (
			room_id TEXT NOT NULL REFERENCES rooms(id), device_id TEXT NOT NULL,
			account_id TEXT NOT NULL, public_key BLOB NOT NULL,
			encryption_public_key BLOB NOT NULL DEFAULT x'', joined_at INTEGER NOT NULL,
			revoked_at INTEGER, PRIMARY KEY (room_id, device_id))`,
		`CREATE TABLE IF NOT EXISTS room_epochs (
			room_id TEXT NOT NULL REFERENCES rooms(id), epoch INTEGER NOT NULL,
			key_sealed TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (room_id, epoch))`,
		`CREATE TABLE IF NOT EXISTS room_member_keys (
			room_id TEXT NOT NULL REFERENCES rooms(id), epoch INTEGER NOT NULL,
			device_id TEXT NOT NULL, wrapped_key BLOB NOT NULL, created_at INTEGER NOT NULL,
			PRIMARY KEY (room_id, epoch, device_id))`,
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY, account_id TEXT NOT NULL, name TEXT NOT NULL,
			runtime TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'idle',
			permissions TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL,
			last_seen_at INTEGER, revoked_at INTEGER)`,
		`CREATE INDEX IF NOT EXISTS agents_account ON agents(account_id)`,
		`CREATE TABLE IF NOT EXISTS agent_rooms (
			agent_id TEXT NOT NULL REFERENCES agents(id), room_id TEXT NOT NULL REFERENCES rooms(id),
			joined_at INTEGER NOT NULL, PRIMARY KEY (agent_id, room_id))`,
		`CREATE TABLE IF NOT EXISTS approvals (
			id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id), agent_id TEXT NOT NULL,
			requested_by TEXT NOT NULL, action TEXT NOT NULL, action_digest TEXT NOT NULL,
			description TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', expires_at INTEGER NOT NULL,
			decided_by TEXT, decided_at INTEGER, consumed_at INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS approvals_room_status ON approvals(room_id, status)`,
		`CREATE TABLE IF NOT EXISTS tool_receipts (
			id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id), agent_id TEXT NOT NULL,
			approval_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, tool TEXT NOT NULL,
			input TEXT NOT NULL DEFAULT '{}', output TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS tool_receipts_room ON tool_receipts(room_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id),
			event_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, mime TEXT NOT NULL,
			size INTEGER NOT NULL, sha256 TEXT NOT NULL, path TEXT NOT NULL,
			key_sealed TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS read_cursors (
			room_id TEXT NOT NULL REFERENCES rooms(id), device_id TEXT NOT NULL,
			last_seq INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL,
			PRIMARY KEY (room_id, device_id))`,
		`CREATE TABLE IF NOT EXISTS outbox (
			id TEXT PRIMARY KEY, target TEXT NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS peers (
			server_id TEXT PRIMARY KEY, code TEXT NOT NULL DEFAULT '', address TEXT NOT NULL,
			public_key BLOB NOT NULL DEFAULT x'', last_seen INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS invites (
			id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id),
			secret_hash TEXT NOT NULL UNIQUE, created_by TEXT NOT NULL,
			history TEXT NOT NULL DEFAULT 'none', expires_at INTEGER NOT NULL,
			used_at INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS policies (
			id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id),
			agent_id TEXT NOT NULL DEFAULT '', rules TEXT NOT NULL,
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("storage: migrate v1 schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit migration: %w", err)
	}
	return nil
}

func (d *DB) migrateV2(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin v2 migration: %w", err)
	}
	defer tx.Rollback()
	// A fresh v2 database already has the epoch column; ignore duplicate-column
	// errors so this migration is idempotent.
	if _, err := tx.ExecContext(ctx, `ALTER TABLE events ADD COLUMN epoch INTEGER NOT NULL DEFAULT 1`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("storage: migrate v2: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit v2 migration: %w", err)
	}
	return nil
}

// migrateV3 adds the local-device marker. A pre-multi-device database has a
// single device, which is by definition the device running this node.
func (d *DB) migrateV3(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin v3 migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE devices ADD COLUMN local INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("storage: migrate v3: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE devices SET local = 1 WHERE id = (
			SELECT id FROM devices ORDER BY created_at, id LIMIT 1
		) AND NOT EXISTS (SELECT 1 FROM devices WHERE local = 1)`); err != nil {
		return fmt.Errorf("storage: mark local device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit v3 migration: %w", err)
	}
	return nil
}

// migrateV4 adds per-(Agent, Room) runtime sessions and their catch-up
// cursors. Runtime state is local; it is never written to the room log.
func (d *DB) migrateV4(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin v4 migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS agent_sessions (
			agent_id        TEXT NOT NULL REFERENCES agents(id),
			room_id         TEXT NOT NULL REFERENCES rooms(id),
			last_seen_seq   INTEGER NOT NULL DEFAULT 0,
			last_run_seq    INTEGER NOT NULL DEFAULT 0,
			active_event_id TEXT NOT NULL DEFAULT '',
			active_seq      INTEGER NOT NULL DEFAULT 0,
			runtime_session TEXT NOT NULL DEFAULT '',
			run_count       INTEGER NOT NULL DEFAULT 0,
			status          TEXT NOT NULL DEFAULT 'idle',
			last_error      TEXT NOT NULL DEFAULT '',
			updated_at      INTEGER NOT NULL,
			PRIMARY KEY (agent_id, room_id))`); err != nil {
		return fmt.Errorf("storage: migrate v4: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS agent_sessions_room ON agent_sessions(room_id)`); err != nil {
		return fmt.Errorf("storage: migrate v4 index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit v4 migration: %w", err)
	}
	return nil
}

// migrateV5 adds the last known pinned node address for each device. Older
// rows start empty and learn the address when devices sync again.
func (d *DB) migrateV5(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin v5 migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE devices ADD COLUMN node_url TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("storage: migrate v5: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit v5 migration: %w", err)
	}
	return nil
}

// migrateV6 records the server that created the account. Accounts imported
// before this column existed did not retain their origin server; the local
// server is the best known anchor and still gives newly paired devices a
// reachable bootstrap peer.
func (d *DB) migrateV6(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin v6 migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE accounts ADD COLUMN origin_server_id TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("storage: migrate v6: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET origin_server_id = server_id WHERE origin_server_id = ''`); err != nil {
		return fmt.Errorf("storage: migrate v6 backfill: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit v6 migration: %w", err)
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
		SELECT id, server_id, origin_server_id, code, name, public_key, private_key, password_hash, created_at
		FROM accounts ORDER BY created_at LIMIT 1`)
	var a account.Account
	var created int64
	if err := row.Scan(&a.ID, &a.ServerID, &a.OriginServerID, &a.Code, &a.Name, &a.PublicKey, &a.PrivateKey, &a.PasswordHash, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, account.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get account: %w", err)
	}
	a.CreatedAt = fromMillis(created)
	return &a, nil
}

// GetDevice returns the device running this node.
func (d *DB) GetDevice(ctx context.Context, accountID string) (*account.Device, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, account_id, name, public_key, private_key, encryption_public_key,
		       encryption_private_key, certificate, node_url, role, revoked_at, last_seen_at, created_at, local
		FROM devices WHERE account_id = ? ORDER BY local DESC, created_at LIMIT 1`, accountID)
	return scanDevice(row)
}

// GetDeviceByID returns one device.
func (d *DB) GetDeviceByID(ctx context.Context, deviceID string) (*account.Device, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, account_id, name, public_key, private_key, encryption_public_key,
		       encryption_private_key, certificate, node_url, role, revoked_at, last_seen_at, created_at, local
		FROM devices WHERE id = ?`, deviceID)
	return scanDevice(row)
}

// ListDevices returns all devices for an account, oldest first.
func (d *DB) ListDevices(ctx context.Context, accountID string) ([]account.Device, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, account_id, name, public_key, private_key, encryption_public_key,
		       encryption_private_key, certificate, node_url, role, revoked_at, last_seen_at, created_at, local
		FROM devices WHERE account_id = ? ORDER BY local DESC, created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("storage: list devices: %w", err)
	}
	defer rows.Close()
	var out []account.Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan device: %w", err)
		}
		out = append(out, *device)
	}
	return out, rows.Err()
}

// AddDevice inserts a device certificate.
func (d *DB) AddDevice(ctx context.Context, dev *account.Device) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO devices (id, account_id, name, public_key, private_key, encryption_public_key,
			encryption_private_key, certificate, node_url, role, revoked_at, last_seen_at, created_at, local)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dev.ID, dev.AccountID, dev.Name, dev.PublicKey, nonNilBytes(dev.PrivateKey), dev.EncryptionPublicKey,
		nonNilBytes(dev.EncryptionPrivateKey), dev.Certificate, dev.NodeURL, dev.Role, nullableMillis(dev.RevokedAt),
		nullableMillis(dev.LastSeenAt), toMillis(dev.CreatedAt), boolInt(dev.Local))
	if err != nil {
		if isUniqueViolation(err) {
			return account.ErrAlreadyExists
		}
		return fmt.Errorf("storage: add device: %w", err)
	}
	return nil
}

// UpdateDevice updates mutable device fields, including a renewed
// certificate.
func (d *DB) UpdateDevice(ctx context.Context, dev *account.Device) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE devices SET name = ?, role = ?, certificate = ?, node_url = ?, revoked_at = ?, last_seen_at = ? WHERE id = ?`,
		dev.Name, dev.Role, dev.Certificate, dev.NodeURL, nullableMillis(dev.RevokedAt), nullableMillis(dev.LastSeenAt), dev.ID)
	if err != nil {
		return fmt.Errorf("storage: update device: %w", err)
	}
	return nil
}

// CreateAccount inserts a server (if new), an account, and its first device.
func (d *DB) CreateAccount(ctx context.Context, server *account.Server, acct *account.Account, dev *account.Device) error {
	return d.insertAccount(ctx, server, acct, []account.Device{*dev})
}

// ImportAccount inserts a recovered account and every known device in one
// transaction. Only the device running this node carries private keys.
func (d *DB) ImportAccount(ctx context.Context, server *account.Server, acct *account.Account, devices []account.Device) error {
	return d.insertAccount(ctx, server, acct, devices)
}

func (d *DB) insertAccount(ctx context.Context, server *account.Server, acct *account.Account, devices []account.Device) error {
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
	originServerID := acct.OriginServerID
	if originServerID == "" {
		originServerID = acct.ServerID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO accounts (id, server_id, origin_server_id, code, name, public_key, private_key, password_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		acct.ID, acct.ServerID, originServerID, acct.Code, acct.Name, acct.PublicKey, acct.PrivateKey, acct.PasswordHash, toMillis(acct.CreatedAt)); err != nil {
		if isUniqueViolation(err) {
			return account.ErrAlreadyExists
		}
		return fmt.Errorf("storage: insert account: %w", err)
	}
	for i := range devices {
		dev := &devices[i]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO devices (id, account_id, name, public_key, private_key, encryption_public_key,
				encryption_private_key, certificate, node_url, role, revoked_at, last_seen_at, created_at, local)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dev.ID, dev.AccountID, dev.Name, dev.PublicKey, nonNilBytes(dev.PrivateKey), dev.EncryptionPublicKey,
			nonNilBytes(dev.EncryptionPrivateKey), dev.Certificate, dev.NodeURL, dev.Role, nullableMillis(dev.RevokedAt),
			nullableMillis(dev.LastSeenAt), toMillis(dev.CreatedAt), boolInt(dev.Local)); err != nil {
			if isUniqueViolation(err) {
				return account.ErrAlreadyExists
			}
			return fmt.Errorf("storage: insert device: %w", err)
		}
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

// CreateRoom inserts a room, its first event, and the creator's membership in
// one transaction.
func (d *DB) CreateRoom(ctx context.Context, r *room.Room, event *room.Event, member *room.Member, device *room.RoomDevice, epoch *room.RoomEpoch, keys []room.RoomMemberKey) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin room: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rooms (id, name, topic, home_node, home_server_id, home_address, created_by,
			epoch, created_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Topic, r.HomeNode, r.HomeServerID, r.HomeAddress, r.CreatedBy,
		r.Epoch, toMillis(r.CreatedAt), nullableMillis(r.ArchivedAt)); err != nil {
		return fmt.Errorf("storage: insert room: %w", err)
	}
	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := insertMember(ctx, tx, member); err != nil {
		return err
	}
	if err := insertRoomDevice(ctx, tx, device); err != nil {
		return err
	}
	if epoch != nil {
		if err := insertRoomEpoch(ctx, tx, epoch); err != nil {
			return err
		}
	}
	for i := range keys {
		if err := insertRoomMemberKey(ctx, tx, &keys[i]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit room: %w", err)
	}
	return nil
}

// ImportRoom stores a complete room snapshot received from its home node.
// All rows are written in one transaction so a failed join cannot leave a
// partially imported room behind.
func (d *DB) ImportRoom(ctx context.Context, r *room.Room, members []room.Member, devices []room.RoomDevice, epochs []room.RoomEpoch, keys []room.RoomMemberKey, events []room.Event) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin import room: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rooms (id, name, topic, home_node, home_server_id, home_address, created_by,
			epoch, created_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Topic, r.HomeNode, r.HomeServerID, r.HomeAddress, r.CreatedBy,
		r.Epoch, toMillis(r.CreatedAt), nullableMillis(r.ArchivedAt)); err != nil {
		return fmt.Errorf("storage: import room: %w", err)
	}
	for i := range events {
		if err := insertEvent(ctx, tx, &events[i]); err != nil {
			return err
		}
	}
	for i := range members {
		if err := insertMember(ctx, tx, &members[i]); err != nil {
			return err
		}
	}
	for i := range devices {
		if err := insertRoomDevice(ctx, tx, &devices[i]); err != nil {
			return err
		}
	}
	for i := range epochs {
		if err := insertRoomEpoch(ctx, tx, &epochs[i]); err != nil {
			return err
		}
	}
	for i := range keys {
		if err := insertRoomMemberKey(ctx, tx, &keys[i]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit import room: %w", err)
	}
	return nil
}

// ImportEvents appends a batch of already-verified or deliberately opaque
// remote events in one transaction.
func (d *DB) ImportEvents(ctx context.Context, events []room.Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin import events: %w", err)
	}
	defer tx.Rollback()
	for i := range events {
		if err := insertEvent(ctx, tx, &events[i]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit import events: %w", err)
	}
	return nil
}

// ListRooms returns rooms newest first.
func (d *DB) ListRooms(ctx context.Context) ([]room.Room, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, name, topic, home_node, home_server_id, home_address, created_by, epoch,
		       created_at, archived_at
		FROM rooms ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list rooms: %w", err)
	}
	defer rows.Close()
	var out []room.Room
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan room: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list rooms: %w", err)
	}
	return out, nil
}

// GetRoom returns one room.
func (d *DB) GetRoom(ctx context.Context, id string) (*room.Room, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, name, topic, home_node, home_server_id, home_address, created_by, epoch,
		       created_at, archived_at
		FROM rooms WHERE id = ?`, id)
	r, err := scanRoom(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get room: %w", err)
	}
	return r, nil
}

// UpdateRoom updates mutable room settings.
func (d *DB) UpdateRoom(ctx context.Context, r *room.Room) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE rooms SET name = ?, topic = ?, epoch = ?, home_server_id = ?, home_address = ?,
			archived_at = ? WHERE id = ?`,
		r.Name, r.Topic, r.Epoch, r.HomeServerID, r.HomeAddress, nullableMillis(r.ArchivedAt), r.ID)
	if err != nil {
		return fmt.Errorf("storage: update room: %w", err)
	}
	return nil
}

// LastEvent returns the highest-sequence event in a room.
func (d *DB) LastEvent(ctx context.Context, roomID string) (*room.Event, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, seq, epoch, type, author_id, author_device, payload, created_at,
		       prev_id, prev_hash, hash, signature
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

// GetEvent returns one event by ID.
func (d *DB) GetEvent(ctx context.Context, eventID string) (*room.Event, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, seq, epoch, type, author_id, author_device, payload, created_at,
		       prev_id, prev_hash, hash, signature
		FROM events WHERE id = ?`, eventID)
	event, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrMessageNotFound
		}
		return nil, fmt.Errorf("storage: get event: %w", err)
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

// FirstEventSeqForEpoch returns the first event sequence sealed with an epoch.
func (d *DB) FirstEventSeqForEpoch(ctx context.Context, roomID string, epoch int) (int64, error) {
	var seq int64
	err := d.db.QueryRowContext(ctx, `
		SELECT seq FROM events WHERE room_id = ? AND epoch = ? ORDER BY seq ASC LIMIT 1`,
		roomID, epoch).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, room.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("storage: first event for epoch: %w", err)
	}
	return seq, nil
}

// ListEvents returns events after a sequence, oldest first.
func (d *DB) ListEvents(ctx context.Context, roomID string, after int64, limit int) ([]room.Event, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, room_id, seq, epoch, type, author_id, author_device, payload, created_at,
		       prev_id, prev_hash, hash, signature
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

// GetMember returns a room membership.
func (d *DB) GetMember(ctx context.Context, roomID, accountID string) (*room.Member, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT room_id, account_id, device_id, display_name, role, status,
		       account_public_key, encryption_public_key, joined_at, left_at
		FROM room_members WHERE room_id = ? AND account_id = ?`, roomID, accountID)
	return scanMember(row)
}

// ListMembers returns room members.
func (d *DB) ListMembers(ctx context.Context, roomID string) ([]room.Member, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT room_id, account_id, device_id, display_name, role, status,
		       account_public_key, encryption_public_key, joined_at, left_at
		FROM room_members WHERE room_id = ? ORDER BY joined_at`, roomID)
	if err != nil {
		return nil, fmt.Errorf("storage: list members: %w", err)
	}
	defer rows.Close()
	var out []room.Member
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan member: %w", err)
		}
		out = append(out, *member)
	}
	return out, rows.Err()
}

// AddMember inserts a room member.
func (d *DB) AddMember(ctx context.Context, member *room.Member) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin member: %w", err)
	}
	defer tx.Rollback()
	if err := insertMember(ctx, tx, member); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit member: %w", err)
	}
	return nil
}

// UpdateMember updates membership state.
func (d *DB) UpdateMember(ctx context.Context, member *room.Member) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE room_members SET device_id = ?, display_name = ?, role = ?, status = ?,
			account_public_key = ?, encryption_public_key = ?, left_at = ?
		WHERE room_id = ? AND account_id = ?`,
		member.DeviceID, member.DisplayName, member.Role, member.Status,
		member.AccountPublicKey, member.EncryptionPublicKey, nullableMillis(member.LeftAt),
		member.RoomID, member.AccountID)
	if err != nil {
		return fmt.Errorf("storage: update member: %w", err)
	}
	return nil
}

// UpsertMember inserts a membership or refreshes an existing one. A member
// who left and rejoins is reactivated without losing their row.
func (d *DB) UpsertMember(ctx context.Context, member *room.Member) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin member: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_members (room_id, account_id, device_id, display_name, role, status,
			account_public_key, encryption_public_key, joined_at, left_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id, account_id) DO UPDATE SET
			device_id = excluded.device_id, display_name = excluded.display_name,
			role = excluded.role, status = excluded.status,
			account_public_key = excluded.account_public_key,
			encryption_public_key = excluded.encryption_public_key,
			joined_at = excluded.joined_at, left_at = excluded.left_at`,
		member.RoomID, member.AccountID, member.DeviceID, member.DisplayName, member.Role, member.Status,
		nonNilBytes(member.AccountPublicKey), nonNilBytes(member.EncryptionPublicKey),
		toMillis(member.JoinedAt), nullableMillis(member.LeftAt)); err != nil {
		return fmt.Errorf("storage: upsert member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit member: %w", err)
	}
	return nil
}

// GetRoomDevice returns a trusted device for a room.
func (d *DB) GetRoomDevice(ctx context.Context, roomID, deviceID string) (*room.RoomDevice, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT room_id, device_id, account_id, public_key, encryption_public_key, joined_at, revoked_at
		FROM room_devices WHERE room_id = ? AND device_id = ?`, roomID, deviceID)
	return scanRoomDevice(row)
}

// ListRoomDevices returns devices trusted in a room.
func (d *DB) ListRoomDevices(ctx context.Context, roomID string) ([]room.RoomDevice, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT room_id, device_id, account_id, public_key, encryption_public_key, joined_at, revoked_at
		FROM room_devices WHERE room_id = ? ORDER BY joined_at`, roomID)
	if err != nil {
		return nil, fmt.Errorf("storage: list room devices: %w", err)
	}
	defer rows.Close()
	var out []room.RoomDevice
	for rows.Next() {
		device, err := scanRoomDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan room device: %w", err)
		}
		out = append(out, *device)
	}
	return out, rows.Err()
}

// AddRoomDevice inserts a trusted room device.
func (d *DB) AddRoomDevice(ctx context.Context, device *room.RoomDevice) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin room device: %w", err)
	}
	defer tx.Rollback()
	if err := insertRoomDevice(ctx, tx, device); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit room device: %w", err)
	}
	return nil
}

// UpsertRoomDevice inserts a trusted room device or reactivates a revoked one.
func (d *DB) UpsertRoomDevice(ctx context.Context, device *room.RoomDevice) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin room device: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_devices (room_id, device_id, account_id, public_key, encryption_public_key,
			joined_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id, device_id) DO UPDATE SET
			account_id = excluded.account_id, public_key = excluded.public_key,
			encryption_public_key = excluded.encryption_public_key,
			joined_at = excluded.joined_at, revoked_at = excluded.revoked_at`,
		device.RoomID, device.DeviceID, device.AccountID, nonNilBytes(device.PublicKey),
		nonNilBytes(device.EncryptionPublicKey), toMillis(device.JoinedAt),
		nullableMillis(device.RevokedAt)); err != nil {
		return fmt.Errorf("storage: upsert room device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit room device: %w", err)
	}
	return nil
}

// UpdateRoomDevice refreshes a room device, including its revocation state.
func (d *DB) UpdateRoomDevice(ctx context.Context, device *room.RoomDevice) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE room_devices SET public_key = ?, encryption_public_key = ?, revoked_at = ?
		WHERE room_id = ? AND device_id = ?`,
		nonNilBytes(device.PublicKey), nonNilBytes(device.EncryptionPublicKey),
		nullableMillis(device.RevokedAt), device.RoomID, device.DeviceID)
	if err != nil {
		return fmt.Errorf("storage: update room device: %w", err)
	}
	return nil
}

// MergeRoomSnapshot installs or refreshes a room from a paired device. It
// carries no events: the new device fetches those from the room's home node.
// Existing local archive choices are preserved when the snapshot is not
// archived, and revoked devices stay revoked.
func (d *DB) MergeRoomSnapshot(ctx context.Context, r *room.Room, members []room.Member, devices []room.RoomDevice, epochs []room.RoomEpoch, keys []room.RoomMemberKey) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin room snapshot: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rooms (id, name, topic, home_node, home_server_id, home_address, created_by,
			epoch, created_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, topic = excluded.topic, home_node = excluded.home_node,
			home_server_id = excluded.home_server_id, home_address = excluded.home_address,
			epoch = MAX(rooms.epoch, excluded.epoch),
			archived_at = COALESCE(excluded.archived_at, rooms.archived_at)`,
		r.ID, r.Name, r.Topic, r.HomeNode, r.HomeServerID, r.HomeAddress, r.CreatedBy,
		r.Epoch, toMillis(r.CreatedAt), nullableMillis(r.ArchivedAt)); err != nil {
		return fmt.Errorf("storage: merge room: %w", err)
	}
	for i := range members {
		m := &members[i]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO room_members (room_id, account_id, device_id, display_name, role, status,
				account_public_key, encryption_public_key, joined_at, left_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room_id, account_id) DO UPDATE SET
				device_id = excluded.device_id, display_name = excluded.display_name,
				role = excluded.role,
				account_public_key = excluded.account_public_key,
				encryption_public_key = excluded.encryption_public_key,
				status = CASE WHEN room_members.status = 'left' THEN room_members.status ELSE excluded.status END,
				left_at = CASE WHEN room_members.status = 'left' THEN room_members.left_at ELSE excluded.left_at END`,
			m.RoomID, m.AccountID, m.DeviceID, m.DisplayName, m.Role, m.Status,
			nonNilBytes(m.AccountPublicKey), nonNilBytes(m.EncryptionPublicKey),
			toMillis(m.JoinedAt), nullableMillis(m.LeftAt)); err != nil {
			return fmt.Errorf("storage: merge member: %w", err)
		}
	}
	for i := range devices {
		dev := &devices[i]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO room_devices (room_id, device_id, account_id, public_key, encryption_public_key,
				joined_at, revoked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room_id, device_id) DO UPDATE SET
				account_id = excluded.account_id, public_key = excluded.public_key,
				encryption_public_key = excluded.encryption_public_key,
				revoked_at = COALESCE(room_devices.revoked_at, excluded.revoked_at)`,
			dev.RoomID, dev.DeviceID, dev.AccountID, nonNilBytes(dev.PublicKey),
			nonNilBytes(dev.EncryptionPublicKey), toMillis(dev.JoinedAt), nullableMillis(dev.RevokedAt)); err != nil {
			return fmt.Errorf("storage: merge room device: %w", err)
		}
	}
	for i := range epochs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO room_epochs (room_id, epoch, key_sealed, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(room_id, epoch) DO NOTHING`,
			epochs[i].RoomID, epochs[i].Epoch, epochs[i].KeySealed, toMillis(epochs[i].CreatedAt)); err != nil {
			return fmt.Errorf("storage: merge room epoch: %w", err)
		}
	}
	for i := range keys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO room_member_keys (room_id, epoch, device_id, wrapped_key, created_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(room_id, epoch, device_id) DO UPDATE SET
				wrapped_key = excluded.wrapped_key, created_at = excluded.created_at`,
			keys[i].RoomID, keys[i].Epoch, keys[i].DeviceID, keys[i].Wrapped, toMillis(keys[i].CreatedAt)); err != nil {
			return fmt.Errorf("storage: merge room member key: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit room snapshot: %w", err)
	}
	return nil
}

// CreateInvite stores an invitation secret hash.
func (d *DB) CreateInvite(ctx context.Context, invite *room.Invite, secretHash string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO invites (id, room_id, secret_hash, created_by, history, expires_at, used_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		invite.ID, invite.RoomID, secretHash, invite.CreatedBy, invite.History,
		toMillis(invite.ExpiresAt), nullableMillis(invite.UsedAt), toMillis(invite.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: create invite: %w", err)
	}
	return nil
}

// GetInviteByHash returns an invitation by its secret hash.
func (d *DB) GetInviteByHash(ctx context.Context, secretHash string) (*room.Invite, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, created_by, history, expires_at, used_at, created_at
		FROM invites WHERE secret_hash = ?`, secretHash)
	var invite room.Invite
	var expires, created int64
	var used sql.NullInt64
	if err := row.Scan(&invite.ID, &invite.RoomID, &invite.CreatedBy, &invite.History,
		&expires, &used, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get invite: %w", err)
	}
	invite.ExpiresAt = fromMillis(expires)
	invite.CreatedAt = fromMillis(created)
	if used.Valid {
		t := fromMillis(used.Int64)
		invite.UsedAt = &t
	}
	return &invite, nil
}

// MarkInviteUsed marks an invitation as redeemed.
func (d *DB) MarkInviteUsed(ctx context.Context, id string, at time.Time) error {
	result, err := d.db.ExecContext(ctx, `UPDATE invites SET used_at = ? WHERE id = ? AND used_at IS NULL`, toMillis(at), id)
	if err != nil {
		return fmt.Errorf("storage: mark invite used: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return room.ErrInviteUsed
	}
	return nil
}

// SetReadCursor records a device's last-read sequence.
func (d *DB) SetReadCursor(ctx context.Context, roomID, deviceID string, seq int64) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO read_cursors (room_id, device_id, last_seq, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(room_id, device_id) DO UPDATE SET last_seq = excluded.last_seq, updated_at = excluded.updated_at`,
		roomID, deviceID, seq, time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("storage: set read cursor: %w", err)
	}
	return nil
}

// GetReadCursor returns a device's last-read sequence.
func (d *DB) GetReadCursor(ctx context.Context, roomID, deviceID string) (int64, error) {
	var seq int64
	err := d.db.QueryRowContext(ctx, `SELECT last_seq FROM read_cursors WHERE room_id = ? AND device_id = ?`,
		roomID, deviceID).Scan(&seq)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, room.ErrNotFound
		}
		return 0, fmt.Errorf("storage: get read cursor: %w", err)
	}
	return seq, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, event *room.Event) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, room_id, seq, epoch, type, author_id, author_device, payload, created_at,
			prev_id, prev_hash, hash, signature)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.RoomID, event.Seq, event.Epoch, event.Type, event.AuthorID, event.AuthorDevice,
		event.Payload, toMillis(event.CreatedAt), event.PrevID, event.PrevHash,
		event.Hash, event.Signature); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("storage: event sequence conflict: %w", err)
		}
		return fmt.Errorf("storage: insert event: %w", err)
	}
	return nil
}

func insertMember(ctx context.Context, tx *sql.Tx, member *room.Member) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO room_members (room_id, account_id, device_id, display_name, role, status,
			account_public_key, encryption_public_key, joined_at, left_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		member.RoomID, member.AccountID, member.DeviceID, member.DisplayName, member.Role, member.Status,
		nonNilBytes(member.AccountPublicKey), nonNilBytes(member.EncryptionPublicKey), toMillis(member.JoinedAt), nullableMillis(member.LeftAt))
	if err != nil {
		return fmt.Errorf("storage: insert member: %w", err)
	}
	return nil
}

func insertRoomDevice(ctx context.Context, tx *sql.Tx, device *room.RoomDevice) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO room_devices (room_id, device_id, account_id, public_key, encryption_public_key,
			joined_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		device.RoomID, device.DeviceID, device.AccountID, nonNilBytes(device.PublicKey),
		nonNilBytes(device.EncryptionPublicKey), toMillis(device.JoinedAt), nullableMillis(device.RevokedAt))
	if err != nil {
		return fmt.Errorf("storage: insert room device: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(row rowScanner) (*room.Event, error) {
	var event room.Event
	var created int64
	if err := row.Scan(&event.ID, &event.RoomID, &event.Seq, &event.Epoch, &event.Type, &event.AuthorID,
		&event.AuthorDevice, &event.Payload, &created, &event.PrevID, &event.PrevHash,
		&event.Hash, &event.Signature); err != nil {
		return nil, err
	}
	event.CreatedAt = fromMillis(created)
	return &event, nil
}

func scanRoom(row rowScanner) (*room.Room, error) {
	var r room.Room
	var created int64
	var archived sql.NullInt64
	if err := row.Scan(&r.ID, &r.Name, &r.Topic, &r.HomeNode, &r.HomeServerID, &r.HomeAddress,
		&r.CreatedBy, &r.Epoch, &created, &archived); err != nil {
		return nil, err
	}
	r.CreatedAt = fromMillis(created)
	if archived.Valid {
		t := fromMillis(archived.Int64)
		r.ArchivedAt = &t
	}
	return &r, nil
}

func scanMember(row rowScanner) (*room.Member, error) {
	var member room.Member
	var joined int64
	var left sql.NullInt64
	if err := row.Scan(&member.RoomID, &member.AccountID, &member.DeviceID, &member.DisplayName,
		&member.Role, &member.Status, &member.AccountPublicKey, &member.EncryptionPublicKey,
		&joined, &left); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, err
	}
	member.JoinedAt = fromMillis(joined)
	if left.Valid {
		t := fromMillis(left.Int64)
		member.LeftAt = &t
	}
	return &member, nil
}

func scanRoomDevice(row rowScanner) (*room.RoomDevice, error) {
	var device room.RoomDevice
	var joined int64
	var revoked sql.NullInt64
	if err := row.Scan(&device.RoomID, &device.DeviceID, &device.AccountID, &device.PublicKey,
		&device.EncryptionPublicKey, &joined, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, err
	}
	device.JoinedAt = fromMillis(joined)
	if revoked.Valid {
		t := fromMillis(revoked.Int64)
		device.RevokedAt = &t
	}
	return &device, nil
}

func scanDevice(row rowScanner) (*account.Device, error) {
	var dev account.Device
	var revoked, lastSeen sql.NullInt64
	var created int64
	var local int
	if err := row.Scan(&dev.ID, &dev.AccountID, &dev.Name, &dev.PublicKey, &dev.PrivateKey,
		&dev.EncryptionPublicKey, &dev.EncryptionPrivateKey, &dev.Certificate, &dev.NodeURL, &dev.Role,
		&revoked, &lastSeen, &created, &local); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, account.ErrNotFound
		}
		return nil, err
	}
	dev.CreatedAt = fromMillis(created)
	dev.Local = local != 0
	if revoked.Valid {
		t := fromMillis(revoked.Int64)
		dev.RevokedAt = &t
	}
	if lastSeen.Valid {
		t := fromMillis(lastSeen.Int64)
		dev.LastSeenAt = &t
	}
	return &dev, nil
}

func toMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func nullableMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return toMillis(*t)
}

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// Backup writes a consistent snapshot of the database to dest. dest must not
// exist.
func (d *DB) Backup(ctx context.Context, dest string) error {
	if dest == "" {
		return errors.New("storage: empty backup path")
	}
	if _, err := d.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("storage: backup: %w", err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return fmt.Errorf("storage: set backup permissions: %w", err)
	}
	return nil
}
