//go:build !js

// The account host's storage and HTTP service are native-only. The reset
// page compiles this package for GOOS=js and only needs the client.
package hosted

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
)

const (
	hostSchemaVersion = 4

	// SessionTTL bounds an authenticated host session.
	SessionTTL = 12 * time.Hour
	// MaxSessionsPerAccount bounds live sessions per account.
	MaxSessionsPerAccount = 32
	// MaxPendingRegistrationsPerHost bounds half-finished signups.
	MaxPendingRegistrationsPerHost = 4096
	// MaxEmailAccounts bounds how many accounts one email address may recover.
	MaxEmailAccounts = 8

	metaHostPrivateKey = "host_private_key"
	metaHostNonce      = "host_nonce"
	metaHostName       = "host_name"
	metaOPAQUEConfig   = "opaque_config"
	metaOPAQUEMaterial = "opaque_material"
	metaEmailKey       = "email_key"
	metaCreatedAt      = "created_at"

	emailTokenPurposeVerify = "verify"
	emailTokenPurposeReset  = "reset"
)

var (
	// ErrAccountNotFound means no such account exists on this host.
	ErrAccountNotFound = errors.New("hosted account not found")
	// ErrPendingRegistration means a signup is missing or expired.
	ErrPendingRegistration = errors.New("pending registration not found")
	// ErrEmailTokenInvalid means a token is unknown, expired, or already used.
	ErrEmailTokenInvalid = errors.New("invalid email token")
	// ErrKeyMaterial means a key update is malformed or would rewrite history.
	ErrKeyMaterial = errors.New("invalid account key material")
	// ErrTooManyPending means the host is at its signup limit.
	ErrTooManyPending = errors.New("too many pending registrations")
)

var hostSchema = []string{
	`CREATE TABLE IF NOT EXISTS host_meta (
		key TEXT PRIMARY KEY,
		value BLOB NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS hosted_accounts (
		id TEXT PRIMARY KEY,
		code TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		public_key BLOB NOT NULL,
		opaque_record BLOB NOT NULL,
		key_generation INTEGER NOT NULL DEFAULT 1,
		key_rotations TEXT NOT NULL DEFAULT '[]',
		export_wrap TEXT NOT NULL,
		recovery_wrap TEXT NOT NULL,
		key_bundle TEXT NOT NULL,
		email_cipher BLOB NOT NULL DEFAULT x'',
		email_hash BLOB NOT NULL DEFAULT x'',
		email_verified INTEGER NOT NULL DEFAULT 0,
		email_verified_at INTEGER,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_accounts_email ON hosted_accounts(email_hash)`,
	`CREATE TABLE IF NOT EXISTS hosted_devices (
		id TEXT PRIMARY KEY,
		account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		record TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_devices_account ON hosted_devices(account_id)`,
	`CREATE TABLE IF NOT EXISTS pending_registrations (
		account_id TEXT PRIMARY KEY,
		account_code TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		email_cipher BLOB NOT NULL DEFAULT x'',
		email_hash BLOB NOT NULL DEFAULT x'',
		account_public_key BLOB NOT NULL,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS email_tokens (
		token_hash BLOB PRIMARY KEY,
		account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		purpose TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		used_at INTEGER
	)`,
	`CREATE INDEX IF NOT EXISTS email_tokens_account ON email_tokens(account_id, purpose)`,
	`CREATE TABLE IF NOT EXISTS hosted_sessions (
		token_hash BLOB PRIMARY KEY,
		account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_sessions_account ON hosted_sessions(account_id)`,
	`CREATE TABLE IF NOT EXISTS hosted_session_devices (
		token_hash BLOB PRIMARY KEY REFERENCES hosted_sessions(token_hash) ON DELETE CASCADE,
		account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		device_id TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_session_devices_device ON hosted_session_devices(account_id, device_id)`,
	`CREATE TABLE IF NOT EXISTS hosted_vaults (
		account_id TEXT PRIMARY KEY REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		blob TEXT NOT NULL,
		version INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS hosted_rooms (
		id TEXT PRIMARY KEY,
		owner_account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		epoch INTEGER NOT NULL,
		head INTEGER NOT NULL DEFAULT 0,
		head_envelope_hash TEXT NOT NULL DEFAULT '',
		rotation_required INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS hosted_room_members (
		room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
		account_id TEXT NOT NULL REFERENCES hosted_accounts(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		status TEXT NOT NULL,
		history TEXT NOT NULL DEFAULT 'none',
		joined_at INTEGER NOT NULL,
		left_at INTEGER,
		PRIMARY KEY (room_id, account_id)
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_room_members_account ON hosted_room_members(account_id, status)`,
	`CREATE TABLE IF NOT EXISTS hosted_room_devices (
		room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
		device_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		signing_public_key BLOB NOT NULL,
		encryption_public_key BLOB NOT NULL,
		history TEXT NOT NULL DEFAULT 'none',
		from_seq INTEGER NOT NULL DEFAULT 1,
		joined_at INTEGER NOT NULL,
		revoked_at INTEGER,
		PRIMARY KEY (room_id, device_id)
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_room_devices_account ON hosted_room_devices(room_id, account_id)`,
	`CREATE TABLE IF NOT EXISTS hosted_room_events (
		room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		event_id TEXT NOT NULL,
		type TEXT NOT NULL,
		epoch INTEGER NOT NULL,
		author_id TEXT NOT NULL,
		author_device TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		payload TEXT NOT NULL,
		prev_id TEXT NOT NULL DEFAULT '',
		prev_hash TEXT NOT NULL DEFAULT '',
		hash TEXT NOT NULL,
		signature TEXT NOT NULL DEFAULT '',
		envelope_hash TEXT NOT NULL,
		envelope_prev_hash TEXT NOT NULL DEFAULT '',
		envelope_signature TEXT NOT NULL,
		PRIMARY KEY (room_id, seq),
		UNIQUE (room_id, event_id)
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_room_events_epoch ON hosted_room_events(room_id, epoch, seq)`,
	`CREATE TABLE IF NOT EXISTS hosted_room_keys (
		room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
		epoch INTEGER NOT NULL,
		device_id TEXT NOT NULL,
		wrapped BLOB NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (room_id, epoch, device_id)
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_room_keys_device ON hosted_room_keys(room_id, device_id, epoch)`,
	`CREATE TABLE IF NOT EXISTS hosted_room_invites (
		room_id TEXT NOT NULL REFERENCES hosted_rooms(id) ON DELETE CASCADE,
		invite_id TEXT NOT NULL,
		secret_hash BLOB NOT NULL UNIQUE,
		history TEXT NOT NULL,
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		used_at INTEGER,
		used_by TEXT,
		revoked_at INTEGER,
		PRIMARY KEY (room_id, invite_id)
	)`,
	`CREATE INDEX IF NOT EXISTS hosted_room_invites_room ON hosted_room_invites(room_id, expires_at)`,
	`CREATE TABLE IF NOT EXISTS hosted_room_invite_keys (
		room_id TEXT NOT NULL,
		invite_id TEXT NOT NULL,
		epoch INTEGER NOT NULL,
		wrapped BLOB NOT NULL,
		PRIMARY KEY (room_id, invite_id, epoch),
		FOREIGN KEY (room_id, invite_id) REFERENCES hosted_room_invites(room_id, invite_id) ON DELETE CASCADE
	)`,
}

// HostState is the persistent identity of an account host.
type HostState struct {
	Server         *identity.Server
	Name           string
	OPAQUEConfig   []byte
	OPAQUEMaterial []byte
	EmailKey       []byte
	ManifestJSON   []byte
	CreatedAt      time.Time
}

// AccountRecord is one hosted account. Every secret is ciphertext.
type AccountRecord struct {
	ID            string
	Code          string
	Name          string
	PublicKey     []byte
	OPAQUERecord  []byte
	KeyGeneration int
	KeyRotations  []string
	ExportWrap    SealedBlob
	RecoveryWrap  SealedBlob
	KeyBundle     SealedBlob
	EmailCipher   []byte
	EmailHash     []byte
	EmailVerified bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AccountRef is the public identity of one account.
type AccountRef struct {
	ID   string
	Name string
}

// PendingRegistration is an account ID reserved while the client finishes
// registration.
type PendingRegistration struct {
	AccountID        string
	AccountCode      string
	Name             string
	EmailCipher      []byte
	EmailHash        []byte
	AccountPublicKey []byte
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

// EmailToken is one outstanding verification or reset token. The raw token is
// never stored, only its SHA-256 hash.
type EmailToken struct {
	TokenHash []byte
	AccountID string
	Purpose   string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// RegistrationFinish is the durable half of account creation.
type RegistrationFinish struct {
	AccountID    string
	OPAQUERecord []byte
	ExportWrap   SealedBlob
	RecoveryWrap SealedBlob
	KeyBundle    SealedBlob
	Device       account.DeviceRecord
}

// SyncSummary describes what a device sync changed.
type SyncSummary struct {
	Devices  []account.DeviceRecord
	Added    int
	Updated  int
	Revoked  int
	Skipped  int
	Warnings []string
}

// Store is the account host's SQLite database.
type Store struct {
	db *sql.DB

	hostMu   sync.Mutex
	host     *HostState
	emailKey []byte
}

// OpenStore opens and migrates an account-host database.
func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("hosted: empty database path")
	}
	if path != ":memory:" {
		if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("hosted: open store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	return store, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("hosted: read schema version: %w", err)
	}
	if version > hostSchemaVersion {
		return fmt.Errorf("hosted: database schema %d is newer than this binary (%d)", version, hostSchemaVersion)
	}
	if version < hostSchemaVersion {
		// Every statement is idempotent, so upgrading from an older version
		// simply adds whatever tables and indexes are missing.
		for _, statement := range hostSchema {
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("hosted: create schema: %w", err)
			}
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", hostSchemaVersion)); err != nil {
			return fmt.Errorf("hosted: set schema version: %w", err)
		}
	}
	return nil
}

// EnsureHost loads or creates the host identity. name and endpoints are the
// operator's current configuration; the identity keys are never regenerated.
func (s *Store) EnsureHost(ctx context.Context, name string, endpoints []identity.Endpoint) (*HostState, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameLength {
		return nil, errors.New("host name is invalid")
	}
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	if s.host != nil && s.host.Name == name {
		return s.host, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin host setup: %w", err)
	}
	defer tx.Rollback()

	values, err := readMeta(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	var server *identity.Server
	var opaqueConfig, opaqueMaterial, emailKey []byte
	var createdAt time.Time

	if len(values[metaHostPrivateKey]) == 0 {
		server, err = identity.NewServer(endpoints)
		if err != nil {
			return nil, err
		}
		conf, configBytes, err := NewOPAQUEConfig()
		if err != nil {
			return nil, err
		}
		opaqueMaterial, err = NewOPAQUEMaterial(conf, server.ID)
		if err != nil {
			return nil, err
		}
		opaqueConfig = configBytes
		emailKey = make([]byte, 32)
		if _, err := rand.Read(emailKey); err != nil {
			return nil, fmt.Errorf("hosted: generate email key: %w", err)
		}
		if err := writeMeta(ctx, tx, map[string][]byte{
			metaHostPrivateKey: server.PrivateKey,
			metaHostNonce:      server.Nonce,
			metaHostName:       []byte(name),
			metaOPAQUEConfig:   opaqueConfig,
			metaOPAQUEMaterial: opaqueMaterial,
			metaEmailKey:       emailKey,
			metaCreatedAt:      []byte(now.Format(time.RFC3339Nano)),
		}); err != nil {
			return nil, err
		}
		createdAt = now
	} else {
		privateKey := ed25519.PrivateKey(append([]byte(nil), values[metaHostPrivateKey]...))
		nonce := append([]byte(nil), values[metaHostNonce]...)
		server, err = identity.NewServerFromKey(privateKey, nonce, endpoints)
		if err != nil {
			return nil, err
		}
		opaqueConfig = append([]byte(nil), values[metaOPAQUEConfig]...)
		opaqueMaterial = append([]byte(nil), values[metaOPAQUEMaterial]...)
		emailKey = append([]byte(nil), values[metaEmailKey]...)
		if len(emailKey) != 32 || len(opaqueConfig) == 0 || len(opaqueMaterial) == 0 {
			return nil, errors.New("hosted: host key material is incomplete")
		}
		createdAt, err = time.Parse(time.RFC3339Nano, string(values[metaCreatedAt]))
		if err != nil {
			return nil, fmt.Errorf("hosted: host creation time: %w", err)
		}
		if string(values[metaHostName]) != name {
			if err := writeMeta(ctx, tx, map[string][]byte{metaHostName: []byte(name)}); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: save host identity: %w", err)
	}
	state := &HostState{
		Server: server, Name: name,
		OPAQUEConfig: opaqueConfig, OPAQUEMaterial: opaqueMaterial,
		EmailKey: emailKey, ManifestJSON: server.ManifestJSON, CreatedAt: createdAt,
	}
	s.host = state
	s.emailKey = emailKey
	return state, nil
}

// Host returns the loaded host identity, if EnsureHost has run.
func (s *Store) Host() *HostState {
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	return s.host
}

func readMeta(ctx context.Context, tx *sql.Tx) (map[string][]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM host_meta`)
	if err != nil {
		return nil, fmt.Errorf("hosted: read host metadata: %w", err)
	}
	defer rows.Close()
	values := make(map[string][]byte)
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = append([]byte(nil), value...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func writeMeta(ctx context.Context, tx *sql.Tx, values map[string][]byte) error {
	for key, value := range values {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO host_meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("hosted: write host metadata: %w", err)
		}
	}
	return nil
}

// CreatePendingRegistration reserves an account ID. Expired reservations and
// their identifiers are dropped first.
func (s *Store) CreatePendingRegistration(ctx context.Context, pending *PendingRegistration) error {
	if pending == nil || !digits(pending.AccountID, 14) || !digits(pending.AccountCode, 6) ||
		!strings.HasSuffix(pending.AccountID, pending.AccountCode) ||
		len(pending.AccountPublicKey) != ed25519.PublicKeySize ||
		pending.ExpiresAt.IsZero() {
		return errors.New("invalid pending registration")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pending_registrations WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_registrations`).Scan(&count); err != nil {
		return err
	}
	if count >= MaxPendingRegistrationsPerHost {
		return ErrTooManyPending
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_registrations(
			account_id, account_code, name, email_cipher, email_hash,
			account_public_key, expires_at, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		pending.AccountID, pending.AccountCode, pending.Name,
		blobOrEmpty(pending.EmailCipher), blobOrEmpty(pending.EmailHash), pending.AccountPublicKey,
		pending.ExpiresAt.UnixMilli(), now.UnixMilli())
	if err != nil {
		if isConstraint(err) {
			return errors.New("account id is already reserved")
		}
		return err
	}
	return nil
}

// GetPendingRegistration loads a live reservation.
func (s *Store) GetPendingRegistration(ctx context.Context, accountID string) (*PendingRegistration, error) {
	var pending PendingRegistration
	var expires, created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, account_code, name, email_cipher, email_hash,
		        account_public_key, expires_at, created_at
		   FROM pending_registrations WHERE account_id = ?`, accountID).
		Scan(&pending.AccountID, &pending.AccountCode, &pending.Name,
			&pending.EmailCipher, &pending.EmailHash, &pending.AccountPublicKey,
			&expires, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPendingRegistration
	}
	if err != nil {
		return nil, err
	}
	pending.ExpiresAt = time.UnixMilli(expires).UTC()
	pending.CreatedAt = time.UnixMilli(created).UTC()
	if !time.Now().UTC().Before(pending.ExpiresAt) {
		return nil, ErrPendingRegistration
	}
	return &pending, nil
}

// FinishRegistration atomically creates the account and its first device.
func (s *Store) FinishRegistration(ctx context.Context, finish *RegistrationFinish) (*AccountRecord, error) {
	if finish == nil || !digits(finish.AccountID, 14) {
		return nil, errors.New("invalid registration finish")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var pending PendingRegistration
	var expires, pendingCreated int64
	err = tx.QueryRowContext(ctx,
		`SELECT account_id, account_code, name, email_cipher, email_hash,
		        account_public_key, expires_at, created_at
		   FROM pending_registrations WHERE account_id = ?`, finish.AccountID).
		Scan(&pending.AccountID, &pending.AccountCode, &pending.Name,
			&pending.EmailCipher, &pending.EmailHash, &pending.AccountPublicKey,
			&expires, &pendingCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPendingRegistration
	}
	if err != nil {
		return nil, err
	}
	if !now.Before(time.UnixMilli(expires).UTC()) {
		return nil, ErrPendingRegistration
	}
	if finish.Device.AccountID != pending.AccountID || len(pending.AccountPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("registration does not match its reservation")
	}
	if err := validateBlob(finish.ExportWrap); err != nil {
		return nil, fmt.Errorf("export wrap: %w", err)
	}
	if err := validateBlob(finish.RecoveryWrap); err != nil {
		return nil, fmt.Errorf("recovery wrap: %w", err)
	}
	if err := validateBlob(finish.KeyBundle); err != nil {
		return nil, fmt.Errorf("key bundle: %w", err)
	}
	rotations, err := json.Marshal([]string{})
	if err != nil {
		return nil, err
	}
	exportJSON, err := json.Marshal(finish.ExportWrap)
	if err != nil {
		return nil, err
	}
	recoveryJSON, err := json.Marshal(finish.RecoveryWrap)
	if err != nil {
		return nil, err
	}
	bundleJSON, err := json.Marshal(finish.KeyBundle)
	if err != nil {
		return nil, err
	}
	recordJSON, err := json.Marshal(finish.Device)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO hosted_accounts(
			id, code, name, public_key, opaque_record, key_generation, key_rotations,
			export_wrap, recovery_wrap, key_bundle, email_cipher, email_hash,
			email_verified, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		pending.AccountID, pending.AccountCode, pending.Name, pending.AccountPublicKey,
		finish.OPAQUERecord, rotations, exportJSON, recoveryJSON, bundleJSON,
		blobOrEmpty(pending.EmailCipher), blobOrEmpty(pending.EmailHash), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		if isConstraint(err) {
			return nil, errors.New("account already exists")
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hosted_devices(id, account_id, record, updated_at) VALUES(?, ?, ?, ?)`,
		finish.Device.ID, pending.AccountID, recordJSON, now.UnixMilli()); err != nil {
		if isConstraint(err) {
			return nil, errors.New("device already exists")
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pending_registrations WHERE account_id = ?`, pending.AccountID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, pending.AccountID)
}

// GetAccount loads one account.
func (s *Store) GetAccount(ctx context.Context, accountID string) (*AccountRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, code, name, public_key, opaque_record, key_generation, key_rotations,
		        export_wrap, recovery_wrap, key_bundle, email_cipher, email_hash,
		        email_verified, created_at, updated_at
		   FROM hosted_accounts WHERE id = ?`, accountID)
	return scanAccount(row)
}

// GetAccountRef loads only the public identity of an account.
func (s *Store) GetAccountRef(ctx context.Context, accountID string) (*AccountRef, error) {
	var ref AccountRef
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name FROM hosted_accounts WHERE id = ?`, accountID).Scan(&ref.ID, &ref.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

// ListAccountsByEmailHash returns accounts sharing one blind email index. The
// caller bounds how many recovery mails it sends.
func (s *Store) ListAccountsByEmailHash(ctx context.Context, hash []byte) ([]AccountRef, error) {
	if len(hash) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM hosted_accounts WHERE email_hash = ? ORDER BY created_at LIMIT ?`,
		hash, MaxEmailAccounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []AccountRef
	for rows.Next() {
		var ref AccountRef
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// UpdatePassword replaces the OPAQUE record and export wrap. The caller must
// have authenticated the session or consumed a reset token.
func (s *Store) UpdatePassword(ctx context.Context, accountID string, opaqueRecord []byte, exportWrap SealedBlob) error {
	if err := validateBlob(exportWrap); err != nil {
		return fmt.Errorf("export wrap: %w", err)
	}
	encoded, err := json.Marshal(exportWrap)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE hosted_accounts SET opaque_record = ?, export_wrap = ?, updated_at = ?
		  WHERE id = ?`, opaqueRecord, encoded, time.Now().UTC().UnixMilli(), accountID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// UpdateRecoveryWrap replaces the recovery wrap after a recovery credential
// rotation.
func (s *Store) UpdateRecoveryWrap(ctx context.Context, accountID string, recoveryWrap SealedBlob) error {
	if err := validateBlob(recoveryWrap); err != nil {
		return fmt.Errorf("recovery wrap: %w", err)
	}
	encoded, err := json.Marshal(recoveryWrap)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE hosted_accounts SET recovery_wrap = ?, updated_at = ? WHERE id = ?`,
		encoded, time.Now().UTC().UnixMilli(), accountID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// FinishRecovery atomically installs a reset password and permanently spends
// the reset token. All live sessions are revoked.
func (s *Store) FinishRecovery(ctx context.Context, accountID string, tokenHash []byte, opaqueRecord []byte, exportWrap, recoveryWrap SealedBlob) error {
	if len(tokenHash) != sha256.Size {
		return ErrEmailTokenInvalid
	}
	if err := validateBlob(exportWrap); err != nil {
		return fmt.Errorf("export wrap: %w", err)
	}
	if err := validateBlob(recoveryWrap); err != nil {
		return fmt.Errorf("recovery wrap: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	exportJSON, err := json.Marshal(exportWrap)
	if err != nil {
		return err
	}
	recoveryJSON, err := json.Marshal(recoveryWrap)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var tokenAccount, purpose string
	var expires int64
	var used sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT account_id, purpose, expires_at, used_at FROM email_tokens WHERE token_hash = ?`,
		tokenHash).Scan(&tokenAccount, &purpose, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEmailTokenInvalid
	}
	if err != nil {
		return err
	}
	if tokenAccount != accountID || purpose != emailTokenPurposeReset || used.Valid || !now.Before(time.UnixMilli(expires).UTC()) {
		return ErrEmailTokenInvalid
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE hosted_accounts
		    SET opaque_record = ?, export_wrap = ?, recovery_wrap = ?, updated_at = ?
		  WHERE id = ?`,
		opaqueRecord, exportJSON, recoveryJSON, now.UnixMilli(), accountID)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE email_tokens SET used_at = ? WHERE account_id = ? AND purpose = ? AND used_at IS NULL`,
		now.UnixMilli(), accountID, emailTokenPurposeReset); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hosted_sessions WHERE account_id = ?`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateKeyMaterial records a verified account-key rotation and the matching
// key bundle. It cannot move an account to a different key lineage.
func (s *Store) UpdateKeyMaterial(ctx context.Context, accountID string, generation int, publicKey []byte, rotations []string, keyBundle *SealedBlob) error {
	account, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if generation < 1 || generation < account.KeyGeneration || len(publicKey) != ed25519.PublicKeySize {
		return ErrKeyMaterial
	}
	if generation == account.KeyGeneration {
		if !hmac.Equal(publicKey, account.PublicKey) {
			return ErrKeyMaterial
		}
		if keyBundle == nil {
			// Same generation with unchanged keys: nothing to record. In
			// particular, never clear the stored rotation history.
			return nil
		}
		if err := validateBlob(*keyBundle); err != nil {
			return fmt.Errorf("key bundle: %w", err)
		}
		encodedBundle, err := json.Marshal(*keyBundle)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE hosted_accounts SET key_bundle = ?, updated_at = ? WHERE id = ?`,
			encodedBundle, time.Now().UTC().UnixMilli(), accountID)
		return err
	} else {
		if generation > 64+1 {
			return ErrKeyMaterial
		}
		originalChain := account.KeyRotations
		var original ed25519.PublicKey
		if len(originalChain) == 0 {
			original = account.PublicKey
		} else {
			original, err = identity.AccountPublicKeyAtGeneration(originalChain, accountID, 1)
			if err != nil {
				return ErrKeyMaterial
			}
		}
		first, err := identity.AccountPublicKeyAtGeneration(rotations, accountID, 1)
		if err != nil || !first.Equal(original) {
			return ErrKeyMaterial
		}
		current, err := identity.AccountPublicKeyAtGeneration(rotations, accountID, generation)
		if err != nil || !current.Equal(ed25519.PublicKey(publicKey)) {
			return ErrKeyMaterial
		}
	}
	encodedRotations, err := json.Marshal(rotations)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if keyBundle != nil {
		if err := validateBlob(*keyBundle); err != nil {
			return fmt.Errorf("key bundle: %w", err)
		}
		encodedBundle, err := json.Marshal(*keyBundle)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE hosted_accounts
			    SET key_generation = ?, public_key = ?, key_rotations = ?, key_bundle = ?, updated_at = ?
			  WHERE id = ?`,
			generation, publicKey, encodedRotations, encodedBundle, now, accountID)
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE hosted_accounts
		    SET key_generation = ?, public_key = ?, key_rotations = ?, updated_at = ?
		  WHERE id = ?`,
		generation, publicKey, encodedRotations, now, accountID)
	return err
}

// CreateEmailToken stores a hashed one-time token.
func (s *Store) CreateEmailToken(ctx context.Context, accountID, purpose string, tokenHash []byte, expiresAt time.Time) error {
	if !digits(accountID, 14) || len(tokenHash) != sha256.Size ||
		(purpose != emailTokenPurposeVerify && purpose != emailTokenPurposeReset) {
		return errors.New("invalid email token request")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM email_tokens WHERE account_id = ? AND purpose = ? AND used_at IS NULL`,
		accountID, purpose); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO email_tokens(token_hash, account_id, purpose, expires_at, created_at)
		 VALUES(?, ?, ?, ?, ?)`,
		tokenHash, accountID, purpose, expiresAt.UnixMilli(), now.UnixMilli())
	return err
}

// LookupEmailToken validates a token without spending it.
func (s *Store) LookupEmailToken(ctx context.Context, tokenHash []byte, purpose string) (*EmailToken, error) {
	if len(tokenHash) != sha256.Size {
		return nil, ErrEmailTokenInvalid
	}
	var token EmailToken
	var expires, created int64
	var used sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, account_id, purpose, expires_at, created_at, used_at
		   FROM email_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&token.TokenHash, &token.AccountID, &token.Purpose, &expires, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEmailTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	token.ExpiresAt = time.UnixMilli(expires).UTC()
	token.CreatedAt = time.UnixMilli(created).UTC()
	if token.Purpose != purpose || used.Valid || !time.Now().UTC().Before(token.ExpiresAt) {
		return nil, ErrEmailTokenInvalid
	}
	return &token, nil
}

// ConsumeEmailToken spends a verification token.
func (s *Store) ConsumeEmailToken(ctx context.Context, tokenHash []byte, purpose string) (*EmailToken, error) {
	token, err := s.LookupEmailToken(ctx, tokenHash, purpose)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := s.db.ExecContext(ctx,
		`UPDATE email_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`,
		now.UnixMilli(), tokenHash)
	if err != nil {
		return nil, err
	}
	if err := requireAffected(result); err != nil {
		return nil, ErrEmailTokenInvalid
	}
	return token, nil
}

// MarkEmailVerified records a successful address verification.
func (s *Store) MarkEmailVerified(ctx context.Context, accountID string) error {
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx,
		`UPDATE hosted_accounts SET email_verified = 1, email_verified_at = ?, updated_at = ?
		  WHERE id = ?`, now, now, accountID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// CreateSession stores a hashed bearer session, evicting the oldest session
// when the per-account cap is reached.
func (s *Store) CreateSession(ctx context.Context, accountID string, tokenHash []byte, expiresAt time.Time) error {
	if !digits(accountID, 14) || len(tokenHash) != sha256.Size || expiresAt.IsZero() {
		return errors.New("invalid session")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_sessions WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_sessions
		  WHERE account_id = ? AND token_hash NOT IN (
		    SELECT token_hash FROM hosted_sessions WHERE account_id = ?
		    ORDER BY created_at DESC LIMIT ?)`,
		accountID, accountID, MaxSessionsPerAccount-1); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO hosted_sessions(token_hash, account_id, expires_at, created_at, last_used_at)
		 VALUES(?, ?, ?, ?, ?)`,
		tokenHash, accountID, expiresAt.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	return err
}

// BindSessionDevice associates a live bearer session with the device that
// owns it. Revoking the device can then end exactly that session without
// signing out every other browser on the account.
func (s *Store) BindSessionDevice(ctx context.Context, accountID string, tokenHash []byte, deviceID string) error {
	if !digits(accountID, 14) || len(tokenHash) != sha256.Size || deviceID == "" || len(deviceID) > 128 {
		return errors.New("invalid session device")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO hosted_session_devices(token_hash, account_id, device_id, created_at)
		 SELECT token_hash, account_id, ?, ? FROM hosted_sessions
		  WHERE token_hash = ? AND account_id = ?`,
		deviceID, now.UnixMilli(), tokenHash, accountID)
	return err
}

// DeleteDeviceSessions removes every live session bound to one device. It is
// used when that device is revoked; unrelated devices stay signed in.
func (s *Store) DeleteDeviceSessions(ctx context.Context, accountID, deviceID string) error {
	if !digits(accountID, 14) || deviceID == "" || len(deviceID) > 128 {
		return errors.New("invalid session device")
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_sessions WHERE account_id = ? AND token_hash IN (
			SELECT token_hash FROM hosted_session_devices
			 WHERE account_id = ? AND device_id = ?)`,
		accountID, accountID, deviceID)
	return err
}

// SessionAccount resolves a live session and refreshes its last-used time.
func (s *Store) SessionAccount(ctx context.Context, tokenHash []byte) (string, error) {
	if len(tokenHash) != sha256.Size {
		return "", ErrSessionInvalid
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	var accountID string
	var expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, expires_at FROM hosted_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&accountID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionInvalid
	}
	if err != nil {
		return "", err
	}
	if !now.Before(time.UnixMilli(expires).UTC()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM hosted_sessions WHERE token_hash = ?`, tokenHash)
		return "", ErrSessionInvalid
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE hosted_sessions SET last_used_at = ? WHERE token_hash = ?`, now.UnixMilli(), tokenHash); err != nil {
		return "", err
	}
	return accountID, nil
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM hosted_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteOtherSessions keeps only the current session after a password change.
func (s *Store) DeleteOtherSessions(ctx context.Context, accountID string, keepTokenHash []byte) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_sessions WHERE account_id = ? AND token_hash != ?`, accountID, keepTokenHash)
	return err
}

// ListDevices returns every stored public device record.
func (s *Store) ListDevices(ctx context.Context, accountID string) ([]account.DeviceRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT record FROM hosted_devices WHERE account_id = ? ORDER BY updated_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []account.DeviceRecord
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var record account.DeviceRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			return nil, fmt.Errorf("hosted: decode device record: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// GetDevice returns one stored public device record.
func (s *Store) GetDevice(ctx context.Context, accountID, deviceID string) (*account.DeviceRecord, error) {
	var encoded []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT record FROM hosted_devices WHERE account_id = ? AND id = ?`, accountID, deviceID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hosted: get device: %w", err)
	}
	var record account.DeviceRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return nil, fmt.Errorf("hosted: decode device record: %w", err)
	}
	return &record, nil
}

// SyncDevices validates and merges public device records. Revocation is
// monotonic, and malformed records are skipped instead of blocking the rest.
func (s *Store) SyncDevices(ctx context.Context, acct *AccountRecord, incoming []account.DeviceRecord) (*SyncSummary, error) {
	if acct == nil {
		return nil, ErrAccountNotFound
	}
	if len(incoming) > MaxDeviceRecords {
		return nil, account.ErrTooManyDeviceRecords
	}
	keys, err := accountKeys(acct)
	if err != nil {
		return nil, err
	}
	existing, err := s.ListDevices(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*account.DeviceRecord, len(existing))
	for i := range existing {
		byID[existing[i].ID] = &existing[i]
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	summary := &SyncSummary{Warnings: make([]string, 0)}
	seen := make(map[string]bool, len(incoming))
	for i := range incoming {
		record := incoming[i]
		if seen[record.ID] {
			summary.Skipped++
			summary.Warnings = append(summary.Warnings, "ignored a duplicate device record")
			continue
		}
		seen[record.ID] = true
		validated, revokedAt, err := account.ValidateDeviceRecord(record, acct.ID, keys, now)
		if err != nil {
			summary.Skipped++
			summary.Warnings = append(summary.Warnings, err.Error())
			continue
		}
		if current, ok := byID[validated.ID]; ok {
			device := deviceFromRecord(*current)
			changed, newlyRevoked, err := account.MergeDeviceRecord(device, validated, revokedAt)
			if err != nil {
				summary.Skipped++
				summary.Warnings = append(summary.Warnings, err.Error())
				continue
			}
			if !changed {
				continue
			}
			merged := recordFromDevice(device)
			if newlyRevoked {
				merged.Revocation = record.Revocation
				summary.Revoked++
			} else if revokedAt != nil && merged.RevokedAt != nil && merged.RevokedAt.Equal(*revokedAt) {
				// A signed, earlier revocation replaced the stored one.
				merged.Revocation = record.Revocation
			} else {
				merged.Revocation = current.Revocation
			}
			if err := s.upsertDevice(ctx, acct.ID, merged, now); err != nil {
				return nil, err
			}
			updated := merged
			byID[updated.ID] = &updated
			summary.Updated++
			continue
		}
		if len(byID) >= MaxDeviceRecords {
			summary.Skipped++
			summary.Warnings = append(summary.Warnings, "the account already has the maximum number of devices")
			continue
		}
		stored := recordFromDevice(validated)
		stored.Revocation = record.Revocation
		if err := s.upsertDevice(ctx, acct.ID, stored, now); err != nil {
			return nil, err
		}
		added := stored
		byID[added.ID] = &added
		summary.Added++
	}
	summary.Devices, err = s.ListDevices(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	return summary, nil
}

func (s *Store) upsertDevice(ctx context.Context, accountID string, record account.DeviceRecord, now time.Time) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO hosted_devices(id, account_id, record, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET record = excluded.record, updated_at = excluded.updated_at`,
		record.ID, accountID, encoded, now.UnixMilli())
	return err
}

func deviceFromRecord(record account.DeviceRecord) *account.Device {
	return &account.Device{
		ID: record.ID, AccountID: record.AccountID, Name: record.Name,
		PublicKey:           append([]byte(nil), record.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), record.EncryptionPublicKey...),
		Certificate:         record.Certificate, NodeURL: record.NodeURL,
		Role: record.Role, RevokedAt: record.RevokedAt, CreatedAt: record.CreatedAt,
	}
}

func recordFromDevice(device *account.Device) account.DeviceRecord {
	return account.DeviceRecord{
		ID: device.ID, AccountID: device.AccountID, Name: device.Name,
		PublicKey:           append([]byte(nil), device.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), device.EncryptionPublicKey...),
		Certificate:         device.Certificate, NodeURL: device.NodeURL,
		Role: device.Role, RevokedAt: device.RevokedAt, CreatedAt: device.CreatedAt,
	}
}

func accountKeys(record *AccountRecord) ([]ed25519.PublicKey, error) {
	acct := &account.Account{
		ID: record.ID, PublicKey: record.PublicKey,
		KeyGeneration: record.KeyGeneration, KeyRotations: record.KeyRotations,
	}
	return account.VerificationKeys(acct)
}

func scanAccount(row *sql.Row) (*AccountRecord, error) {
	var account AccountRecord
	var rotationsJSON, exportJSON, recoveryJSON, bundleJSON []byte
	var emailVerified int
	var created, updated int64
	err := row.Scan(&account.ID, &account.Code, &account.Name, &account.PublicKey,
		&account.OPAQUERecord, &account.KeyGeneration, &rotationsJSON,
		&exportJSON, &recoveryJSON, &bundleJSON, &account.EmailCipher, &account.EmailHash,
		&emailVerified, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rotationsJSON, &account.KeyRotations); err != nil {
		return nil, fmt.Errorf("hosted: decode key rotations: %w", err)
	}
	if err := json.Unmarshal(exportJSON, &account.ExportWrap); err != nil {
		return nil, fmt.Errorf("hosted: decode export wrap: %w", err)
	}
	if err := json.Unmarshal(recoveryJSON, &account.RecoveryWrap); err != nil {
		return nil, fmt.Errorf("hosted: decode recovery wrap: %w", err)
	}
	if err := json.Unmarshal(bundleJSON, &account.KeyBundle); err != nil {
		return nil, fmt.Errorf("hosted: decode key bundle: %w", err)
	}
	account.EmailVerified = emailVerified == 1
	account.CreatedAt = time.UnixMilli(created).UTC()
	account.UpdatedAt = time.UnixMilli(updated).UTC()
	return &account, nil
}

// SealEmail encrypts an address under the host's email key and computes its
// blind lookup index.
func (s *Store) SealEmail(accountID, email string) (cipher, index []byte, err error) {
	email = normalizeEmail(email)
	if email == "" || !digits(accountID, 14) {
		return nil, nil, errors.New("invalid email address")
	}
	key, err := s.derivedEmailKey()
	if err != nil {
		return nil, nil, err
	}
	blob, err := SealBlob(key, []byte(email), emailAAD(accountID))
	if err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(blob)
	if err != nil {
		return nil, nil, err
	}
	indexKey, err := s.emailIndexKey()
	if err != nil {
		return nil, nil, err
	}
	mac := hmac.New(sha256.New, indexKey)
	mac.Write([]byte(email))
	return encoded, mac.Sum(nil), nil
}

// OpenEmail decrypts a stored address.
func (s *Store) OpenEmail(accountID string, cipher []byte) (string, error) {
	if len(cipher) == 0 {
		return "", nil
	}
	key, err := s.derivedEmailKey()
	if err != nil {
		return "", err
	}
	var blob SealedBlob
	if err := json.Unmarshal(cipher, &blob); err != nil {
		return "", err
	}
	plaintext, err := OpenBlob(key, blob, emailAAD(accountID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// EmailIndex computes the blind index of an address for recovery lookup.
func (s *Store) EmailIndex(email string) ([]byte, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, errors.New("invalid email address")
	}
	key, err := s.emailIndexKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(email))
	return mac.Sum(nil), nil
}

// EmailHint returns a masked form safe to display on the reset page.
func EmailHint(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	local, domain := email[:at], email[at+1:]
	if domain == "" {
		return ""
	}
	if len(local) <= 1 {
		return "*@" + domain
	}
	return local[:1] + "***@" + domain
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func emailAAD(accountID string) string {
	return "toget:hosted-email:v1:" + accountID
}

func (s *Store) derivedEmailKey() ([]byte, error) {
	s.hostMu.Lock()
	emailKey := append([]byte(nil), s.emailKey...)
	s.hostMu.Unlock()
	if len(emailKey) == 0 {
		return nil, errors.New("host is not initialized")
	}
	return deriveKey(emailKey, "", "toget:hosted-email-encryption:v1")
}

func (s *Store) emailIndexKey() ([]byte, error) {
	s.hostMu.Lock()
	emailKey := append([]byte(nil), s.emailKey...)
	s.hostMu.Unlock()
	if len(emailKey) == 0 {
		return nil, errors.New("host is not initialized")
	}
	return deriveKey(emailKey, "", "toget:hosted-email-index:v1")
}

func validateBlob(blob SealedBlob) error {
	if blob.Version != BlobVersion || blob.Algorithm != BlobAlgorithmXChaCha20Poly1305 ||
		len(blob.Nonce) == 0 || len(blob.Ciphertext) == 0 || len(blob.Ciphertext) > MaxBlobLength {
		return ErrInvalidBlob
	}
	return nil
}

func requireAffected(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

func isConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint")
}

// blobOrEmpty keeps optional BLOB columns non-NULL. SQLite does not apply a
// column default when the INSERT names the column and binds a nil slice.
func blobOrEmpty(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func ensurePrivateDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hosted: create directory: %w", err)
	}
	return nil
}
