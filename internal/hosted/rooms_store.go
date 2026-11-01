//go:build !js

// The account host's storage and HTTP service are native-only. The reset
// page compiles this package for GOOS=js and only needs the client.
package hosted

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/room"
)

// Hosted room-home errors. The HTTP layer maps these onto stable error codes;
// none of them reveal whether a room exists to a caller who cannot access it.
var (
	ErrHostedRoomNotFound       = errors.New("hosted room not found")
	ErrHostedRoomExists         = errors.New("hosted room already exists")
	ErrHostedRoomConflict       = errors.New("hosted room head has moved")
	ErrHostedRoomForbidden      = errors.New("not a room member")
	ErrHostedRoomDeviceRevoked  = errors.New("room device is revoked")
	ErrHostedRoomInviteInvalid  = errors.New("hosted room invite is invalid")
	ErrHostedRoomInviteUsed     = errors.New("hosted room invite has been used")
	ErrHostedRoomInviteExpired  = errors.New("hosted room invite has expired")
	ErrHostedRoomKeyCoverage    = errors.New("room epoch keys do not cover every active device")
	ErrHostedRoomRotationNeeded = errors.New("room epoch rotation is required")
	ErrHostedRoomMemberLimit    = errors.New("hosted room has reached its member limit")
	ErrHostedRoomDeviceLimit    = errors.New("hosted room has reached its device limit")
)

// HostedRoom is the host's blind view of one room. It never contains a name,
// topic, or key.
type HostedRoom struct {
	ID               string
	OwnerAccountID   string
	Epoch            int
	Head             int64
	HeadEnvelopeHash string
	RotationRequired bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// HostedRoomMember is one account's membership.
type HostedRoomMember struct {
	RoomID    string
	AccountID string
	Role      string
	Status    string
	History   string
	JoinedAt  time.Time
	LeftAt    *time.Time
}

// HostedRoomDevice is one trusted device and the history slice it may read.
type HostedRoomDevice struct {
	RoomID              string
	DeviceID            string
	AccountID           string
	DisplayName         string
	SigningPublicKey    []byte
	EncryptionPublicKey []byte
	History             string
	FromSeq             int64
	JoinedAt            time.Time
	RevokedAt           *time.Time
}

// HostedRoomInvite is a single-use invitation. Only a hash of the secret is
// stored.
type HostedRoomInvite struct {
	RoomID     string
	InviteID   string
	SecretHash []byte
	History    string
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     *time.Time
	UsedBy     string
	RevokedAt  *time.Time
}

// HostedRoomJoin is the identity and policy a redemption installs.
type HostedRoomJoin struct {
	RoomID              string
	AccountID           string
	DeviceID            string
	DisplayName         string
	SigningPublicKey    []byte
	EncryptionPublicKey []byte
	History             string
	Now                 time.Time
}

// HostedRoomRotation is an accepted epoch rotation.
type HostedRoomRotation struct {
	Epoch int
	Event room.Event
	Keys  []RoomKeyPackage
	Now   time.Time
}

// HostedRoomCreation is the transaction that creates a room and its first
// event.
type HostedRoomCreation struct {
	Room   HostedRoom
	Member HostedRoomMember
	Device HostedRoomDevice
	Event  room.Event
	Keys   []RoomKeyPackage
}

func millis(t time.Time) int64 { return t.UTC().UnixMilli() }

func timeFromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := timeFromMillis(value.Int64)
	return &t
}

// CreateHostedRoom stores a room, its owner, first device, first event, and
// the epoch key wrapped to that device in one transaction.
func (s *Store) CreateHostedRoom(ctx context.Context, creation HostedRoomCreation) (*HostedRoom, error) {
	r := creation.Room
	if r.ID == "" || r.OwnerAccountID == "" || r.Epoch < 1 || r.CreatedAt.IsZero() {
		return nil, ErrHostedRoomNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin room create: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hosted_rooms (id, owner_account_id, epoch, head, head_envelope_hash,
			rotation_required, created_at, updated_at)
		VALUES (?, ?, ?, 0, '', 0, ?, ?)`,
		r.ID, r.OwnerAccountID, r.Epoch, millis(r.CreatedAt), millis(r.UpdatedAt)); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrHostedRoomExists
		}
		return nil, fmt.Errorf("hosted: create room: %w", err)
	}
	if err := insertHostedRoomMember(ctx, tx, &creation.Member); err != nil {
		return nil, err
	}
	if err := insertHostedRoomDevice(ctx, tx, &creation.Device); err != nil {
		return nil, err
	}
	if creation.Event.RoomID != r.ID || creation.Event.Seq != 1 {
		return nil, ErrHostedRoomConflict
	}
	if err := insertHostedRoomEvent(ctx, tx, &creation.Event); err != nil {
		return nil, err
	}
	for i := range creation.Keys {
		if err := insertHostedRoomKey(ctx, tx, r.ID, &creation.Keys[i], true); err != nil {
			return nil, err
		}
	}
	if err := updateHostedRoomHead(ctx, tx, r.ID, creation.Event.Seq, creation.Event.EnvelopeHash, r.Epoch, false, r.UpdatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: commit room create: %w", err)
	}
	return s.GetHostedRoom(ctx, r.ID)
}

// GetHostedRoom loads one room.
func (s *Store) GetHostedRoom(ctx context.Context, roomID string) (*HostedRoom, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, owner_account_id, epoch, head, head_envelope_hash, rotation_required,
		       created_at, updated_at
		FROM hosted_rooms WHERE id = ?`, roomID)
	var r HostedRoom
	var created, updated int64
	var rotation int
	if err := row.Scan(&r.ID, &r.OwnerAccountID, &r.Epoch, &r.Head, &r.HeadEnvelopeHash,
		&rotation, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomNotFound
		}
		return nil, fmt.Errorf("hosted: get room: %w", err)
	}
	r.RotationRequired = rotation != 0
	r.CreatedAt = timeFromMillis(created)
	r.UpdatedAt = timeFromMillis(updated)
	return &r, nil
}

// HostedRoomAccess checks that an account device is an active member and
// returns the room, membership, and device.
func (s *Store) HostedRoomAccess(ctx context.Context, roomID, accountID, deviceID string) (*HostedRoom, *HostedRoomMember, *HostedRoomDevice, error) {
	r, err := s.GetHostedRoom(ctx, roomID)
	if err != nil {
		return nil, nil, nil, err
	}
	member, err := s.GetHostedRoomMember(ctx, roomID, accountID)
	if err != nil || member.Status != "active" {
		return nil, nil, nil, ErrHostedRoomForbidden
	}
	device, err := s.GetHostedRoomDevice(ctx, roomID, deviceID)
	if err != nil || device.AccountID != accountID {
		return nil, nil, nil, ErrHostedRoomForbidden
	}
	if device.RevokedAt != nil {
		return nil, nil, nil, ErrHostedRoomDeviceRevoked
	}
	return r, member, device, nil
}

// GetHostedRoomMember loads one membership.
func (s *Store) GetHostedRoomMember(ctx context.Context, roomID, accountID string) (*HostedRoomMember, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT room_id, account_id, role, status, history, joined_at, left_at
		FROM hosted_room_members WHERE room_id = ? AND account_id = ?`, roomID, accountID)
	var m HostedRoomMember
	var joined int64
	var left sql.NullInt64
	if err := row.Scan(&m.RoomID, &m.AccountID, &m.Role, &m.Status, &m.History, &joined, &left); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomForbidden
		}
		return nil, fmt.Errorf("hosted: get room member: %w", err)
	}
	m.JoinedAt = timeFromMillis(joined)
	m.LeftAt = nullableTime(left)
	return &m, nil
}

// GetHostedRoomDevice loads one device.
func (s *Store) GetHostedRoomDevice(ctx context.Context, roomID, deviceID string) (*HostedRoomDevice, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT room_id, device_id, account_id, display_name, signing_public_key,
		       encryption_public_key, history, from_seq, joined_at, revoked_at
		FROM hosted_room_devices WHERE room_id = ? AND device_id = ?`, roomID, deviceID)
	var d HostedRoomDevice
	var joined int64
	var revoked sql.NullInt64
	if err := row.Scan(&d.RoomID, &d.DeviceID, &d.AccountID, &d.DisplayName, &d.SigningPublicKey,
		&d.EncryptionPublicKey, &d.History, &d.FromSeq, &joined, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomForbidden
		}
		return nil, fmt.Errorf("hosted: get room device: %w", err)
	}
	d.JoinedAt = timeFromMillis(joined)
	d.RevokedAt = nullableTime(revoked)
	return &d, nil
}

// ListHostedRoomMembers returns every membership, including departed ones.
func (s *Store) ListHostedRoomMembers(ctx context.Context, roomID string) ([]HostedRoomMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT room_id, account_id, role, status, history, joined_at, left_at
		FROM hosted_room_members WHERE room_id = ? ORDER BY joined_at, account_id`, roomID)
	if err != nil {
		return nil, fmt.Errorf("hosted: list room members: %w", err)
	}
	defer rows.Close()
	var out []HostedRoomMember
	for rows.Next() {
		var m HostedRoomMember
		var joined int64
		var left sql.NullInt64
		if err := rows.Scan(&m.RoomID, &m.AccountID, &m.Role, &m.Status, &m.History, &joined, &left); err != nil {
			return nil, fmt.Errorf("hosted: scan room member: %w", err)
		}
		m.JoinedAt = timeFromMillis(joined)
		m.LeftAt = nullableTime(left)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListHostedRoomDevices returns every device, including revoked ones.
func (s *Store) ListHostedRoomDevices(ctx context.Context, roomID string) ([]HostedRoomDevice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT room_id, device_id, account_id, display_name, signing_public_key,
		       encryption_public_key, history, from_seq, joined_at, revoked_at
		FROM hosted_room_devices WHERE room_id = ? ORDER BY joined_at, device_id`, roomID)
	if err != nil {
		return nil, fmt.Errorf("hosted: list room devices: %w", err)
	}
	defer rows.Close()
	var out []HostedRoomDevice
	for rows.Next() {
		var d HostedRoomDevice
		var joined int64
		var revoked sql.NullInt64
		if err := rows.Scan(&d.RoomID, &d.DeviceID, &d.AccountID, &d.DisplayName, &d.SigningPublicKey,
			&d.EncryptionPublicKey, &d.History, &d.FromSeq, &joined, &revoked); err != nil {
			return nil, fmt.Errorf("hosted: scan room device: %w", err)
		}
		d.JoinedAt = timeFromMillis(joined)
		d.RevokedAt = nullableTime(revoked)
		out = append(out, d)
	}
	return out, rows.Err()
}

// RegisterHostedRoomDevice adds another device to an existing membership.
// history is inherited from the member, but a device added later never
// receives history it was not entitled to when it registered.
func (s *Store) RegisterHostedRoomDevice(ctx context.Context, roomID string, device HostedRoomDevice, maxDevices int) (*HostedRoom, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin device register: %w", err)
	}
	defer tx.Rollback()

	room, err := hostedRoomTx(ctx, tx, roomID)
	if err != nil {
		return nil, err
	}
	member, err := hostedRoomMemberTx(ctx, tx, roomID, device.AccountID)
	if err != nil || member.Status != "active" {
		return nil, ErrHostedRoomForbidden
	}
	existing, err := hostedRoomDeviceTx(ctx, tx, roomID, device.DeviceID)
	if err == nil {
		if existing.RevokedAt != nil {
			return nil, ErrHostedRoomDeviceRevoked
		}
		if existing.AccountID != device.AccountID {
			return nil, ErrHostedRoomForbidden
		}
		return room, nil
	}
	if !errors.Is(err, ErrHostedRoomForbidden) {
		return nil, err
	}
	devices, err := hostedRoomDeviceCountTx(ctx, tx, roomID)
	if err != nil {
		return nil, err
	}
	if devices >= maxDevices {
		return nil, ErrHostedRoomDeviceLimit
	}
	device.History = member.History
	from, err := historyStartTx(ctx, tx, room, member.History)
	if err != nil {
		return nil, err
	}
	device.FromSeq = from
	if err := insertHostedRoomDevice(ctx, tx, &device); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: commit device register: %w", err)
	}
	return s.GetHostedRoom(ctx, roomID)
}

// AppendHostedRoomEvent installs one already-verified event at the next
// sequence. A replay of an event the host already has is a no-op.
func (s *Store) AppendHostedRoomEvent(ctx context.Context, roomID string, event room.Event, rotationRequired bool) (*HostedRoom, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin event append: %w", err)
	}
	defer tx.Rollback()

	room, err := hostedRoomTx(ctx, tx, roomID)
	if err != nil {
		return nil, err
	}
	if rotationRequired && room.RotationRequired {
		return nil, ErrHostedRoomRotationNeeded
	}
	appended, err := appendHostedRoomEventTx(ctx, tx, roomID, room.Head, room.HeadEnvelopeHash, &event)
	if err != nil {
		return nil, err
	}
	if appended {
		if err := updateHostedRoomHead(ctx, tx, roomID, event.Seq, event.EnvelopeHash, room.Epoch, rotationRequired, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: commit event append: %w", err)
	}
	return s.GetHostedRoom(ctx, roomID)
}

// CreateHostedRoomInvite stores an invite event, the invite, and its
// invite-secret-wrapped epoch keys atomically.
func (s *Store) CreateHostedRoomInvite(ctx context.Context, invite HostedRoomInvite, event room.Event, keys []RoomKeyPackage) (*HostedRoom, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin invite create: %w", err)
	}
	defer tx.Rollback()

	room, err := hostedRoomTx(ctx, tx, invite.RoomID)
	if err != nil {
		return nil, err
	}
	if room.RotationRequired {
		return nil, ErrHostedRoomRotationNeeded
	}
	appended, err := appendHostedRoomEventTx(ctx, tx, invite.RoomID, room.Head, room.HeadEnvelopeHash, &event)
	if err != nil {
		return nil, err
	}
	if !appended {
		// A retry after a lost response finds both the invite event and row
		// already stored. Return success only for the identical invite.
		existing, err := scanHostedRoomInvite(tx.QueryRowContext(ctx, `
			SELECT room_id, invite_id, secret_hash, history, created_by, created_at,
			       expires_at, used_at, COALESCE(used_by, ''), revoked_at
			FROM hosted_room_invites WHERE room_id = ? AND invite_id = ?`,
			invite.RoomID, invite.InviteID))
		if err != nil || !bytes.Equal(existing.SecretHash, invite.SecretHash) ||
			existing.History != invite.History || existing.CreatedBy != invite.CreatedBy {
			return nil, ErrHostedRoomInviteInvalid
		}
		return room, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hosted_room_invites (room_id, invite_id, secret_hash, history, created_by,
			created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		invite.RoomID, invite.InviteID, invite.SecretHash, invite.History, invite.CreatedBy,
		millis(invite.CreatedAt), millis(invite.ExpiresAt)); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrHostedRoomInviteInvalid
		}
		return nil, fmt.Errorf("hosted: create invite: %w", err)
	}
	for i := range keys {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO hosted_room_invite_keys (room_id, invite_id, epoch, wrapped)
			VALUES (?, ?, ?, ?)`,
			invite.RoomID, invite.InviteID, keys[i].Epoch, keys[i].Wrapped); err != nil {
			return nil, fmt.Errorf("hosted: store invite key: %w", err)
		}
	}
	if err := updateHostedRoomHead(ctx, tx, invite.RoomID, event.Seq, event.EnvelopeHash, room.Epoch, false, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: commit invite create: %w", err)
	}
	return s.GetHostedRoom(ctx, invite.RoomID)
}

// InviteBySecretHash finds an invitation. The caller then checks expiry and
// use before redemption.
func (s *Store) InviteBySecretHash(ctx context.Context, roomID string, secretHash []byte) (*HostedRoomInvite, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT room_id, invite_id, secret_hash, history, created_by, created_at,
		       expires_at, used_at, COALESCE(used_by, ''), revoked_at
		FROM hosted_room_invites WHERE room_id = ? AND secret_hash = ?`, roomID, secretHash)
	return scanHostedRoomInvite(row)
}

func scanHostedRoomInvite(row interface {
	Scan(dest ...any) error
}) (*HostedRoomInvite, error) {
	var inv HostedRoomInvite
	var created, expires int64
	var used, revoked sql.NullInt64
	if err := row.Scan(&inv.RoomID, &inv.InviteID, &inv.SecretHash, &inv.History, &inv.CreatedBy,
		&created, &expires, &used, &inv.UsedBy, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomInviteInvalid
		}
		return nil, fmt.Errorf("hosted: get invite: %w", err)
	}
	inv.CreatedAt = timeFromMillis(created)
	inv.ExpiresAt = timeFromMillis(expires)
	inv.UsedAt = nullableTime(used)
	inv.RevokedAt = nullableTime(revoked)
	return &inv, nil
}

// RedeemHostedRoomInvite admits one account device and returns the visible
// events, invite-wrapped keys, and the remaining room snapshot.
func (s *Store) RedeemHostedRoomInvite(ctx context.Context, invite *HostedRoomInvite, join HostedRoomJoin, maxMembers, maxDevices int) (*HostedRoom, *HostedRoomMember, *HostedRoomDevice, []RoomKeyPackage, []room.Event, bool, error) {
	if invite == nil || invite.RoomID != join.RoomID {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomInviteInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, nil, nil, false, fmt.Errorf("hosted: begin invite redeem: %w", err)
	}
	defer tx.Rollback()

	// Re-read inside the transaction so two simultaneous redemptions cannot
	// both succeed.
	inv, err := scanHostedRoomInvite(tx.QueryRowContext(ctx, `
		SELECT room_id, invite_id, secret_hash, history, created_by, created_at,
		       expires_at, used_at, COALESCE(used_by, ''), revoked_at
		FROM hosted_room_invites WHERE room_id = ? AND invite_id = ?`, invite.RoomID, invite.InviteID))
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	if inv.RevokedAt != nil {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomInviteInvalid
	}
	if inv.UsedAt != nil {
		// A join response may have been lost after the host committed it.
		// Replaying the same invite from the same device returns the state
		// that was installed instead of a permanent "used" error.
		if inv.UsedBy == join.DeviceID {
			idempotentRoom, err := hostedRoomTx(ctx, tx, join.RoomID)
			if err == nil {
				member, memberErr := hostedRoomMemberTx(ctx, tx, join.RoomID, join.AccountID)
				device, deviceErr := hostedRoomDeviceTx(ctx, tx, join.RoomID, join.DeviceID)
				if memberErr == nil && deviceErr == nil && member.Status == "active" &&
					device.AccountID == join.AccountID && device.RevokedAt == nil {
					keys, keyErr := inviteKeysTx(ctx, tx, join.RoomID, inv.InviteID)
					events, hasMore, eventErr := listHostedRoomEventsTx(ctx, tx, join.RoomID,
						device.FromSeq-1, MaxRoomEvents)
					if keyErr == nil && eventErr == nil {
						return idempotentRoom, member, device, keys, events, hasMore, nil
					}
				}
			}
		}
		return nil, nil, nil, nil, nil, false, ErrHostedRoomInviteUsed
	}
	now := join.Now.UTC().Truncate(time.Millisecond)
	if !now.Before(inv.ExpiresAt) {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomInviteExpired
	}
	room, err := hostedRoomTx(ctx, tx, join.RoomID)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	member, err := hostedRoomMemberTx(ctx, tx, join.RoomID, join.AccountID)
	if err == nil && member.Status == "active" {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomExists
	}
	if err != nil && !errors.Is(err, ErrHostedRoomForbidden) {
		return nil, nil, nil, nil, nil, false, err
	}
	if _, err := hostedRoomDeviceTx(ctx, tx, join.RoomID, join.DeviceID); err == nil {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomExists
	} else if !errors.Is(err, ErrHostedRoomForbidden) {
		return nil, nil, nil, nil, nil, false, err
	}
	activeMembers, err := hostedRoomActiveMemberCountTx(ctx, tx, join.RoomID)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	if activeMembers >= maxMembers {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomMemberLimit
	}
	devices, err := hostedRoomDeviceCountTx(ctx, tx, join.RoomID)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	if devices >= maxDevices {
		return nil, nil, nil, nil, nil, false, ErrHostedRoomDeviceLimit
	}

	from, err := historyStartTx(ctx, tx, room, join.History)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	member = &HostedRoomMember{
		RoomID: join.RoomID, AccountID: join.AccountID, Role: "member",
		Status: "active", History: join.History, JoinedAt: now,
	}
	if err := upsertHostedRoomMember(ctx, tx, member); err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	device := &HostedRoomDevice{
		RoomID: join.RoomID, DeviceID: join.DeviceID, AccountID: join.AccountID,
		DisplayName: join.DisplayName, SigningPublicKey: join.SigningPublicKey,
		EncryptionPublicKey: join.EncryptionPublicKey, History: join.History,
		FromSeq: from, JoinedAt: now,
	}
	if err := insertHostedRoomDevice(ctx, tx, device); err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE hosted_room_invites SET used_at = ?, used_by = ? WHERE room_id = ? AND invite_id = ?`,
		millis(now), join.DeviceID, join.RoomID, inv.InviteID); err != nil {
		return nil, nil, nil, nil, nil, false, fmt.Errorf("hosted: mark invite used: %w", err)
	}
	keys, err := inviteKeysTx(ctx, tx, join.RoomID, inv.InviteID)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	events, hasMore, err := listHostedRoomEventsTx(ctx, tx, join.RoomID, from-1, MaxRoomEvents)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, nil, nil, false, fmt.Errorf("hosted: commit invite redeem: %w", err)
	}
	return room, member, device, keys, events, hasMore, nil
}

// SaveHostedRoomKeys deposits wrapped keys for active devices. An existing
// package is only replaced by its own target device, so one member cannot
// overwrite another member's key material.
func (s *Store) SaveHostedRoomKeys(ctx context.Context, roomID, uploaderDeviceID string, packages []RoomKeyPackage) (int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("hosted: begin key upload: %w", err)
	}
	defer tx.Rollback()

	room, err := hostedRoomTx(ctx, tx, roomID)
	if err != nil {
		return 0, 0, err
	}
	stored, skipped := 0, 0
	for i := range packages {
		p := &packages[i]
		if p.Epoch < 1 || p.Epoch > room.Epoch || p.DeviceID == "" || len(p.Wrapped) == 0 {
			skipped++
			continue
		}
		target, err := hostedRoomDeviceTx(ctx, tx, roomID, p.DeviceID)
		if err != nil || target.RevokedAt != nil {
			skipped++
			continue
		}
		member, err := hostedRoomMemberTx(ctx, tx, roomID, target.AccountID)
		if err != nil || member.Status != "active" {
			skipped++
			continue
		}
		// History is enforced at the key store, not only when a peer is asked.
		// A device that joined without history must not receive older epoch
		// keys because some other member uploaded them unprompted.
		if p.Epoch < room.Epoch {
			visible, err := hostedEpochVisible(ctx, tx, roomID, p.Epoch, target.FromSeq)
			if err != nil {
				return 0, 0, err
			}
			if !visible {
				skipped++
				continue
			}
		}
		created := p.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC().Truncate(time.Millisecond)
		}
		if p.DeviceID == uploaderDeviceID {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO hosted_room_keys (room_id, epoch, device_id, wrapped, created_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(room_id, epoch, device_id) DO UPDATE SET wrapped = excluded.wrapped`,
				roomID, p.Epoch, p.DeviceID, p.Wrapped, millis(created))
			if err == nil {
				stored++
			}
			continue
		}
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO hosted_room_keys (room_id, epoch, device_id, wrapped, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			roomID, p.Epoch, p.DeviceID, p.Wrapped, millis(created))
		if err != nil {
			return 0, 0, fmt.Errorf("hosted: save room key: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed > 0 {
			stored++
		} else {
			skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("hosted: commit key upload: %w", err)
	}
	return stored, skipped, nil
}

// HostedRoomKeysForDevice returns every epoch key wrapped to one device.
func (s *Store) HostedRoomKeysForDevice(ctx context.Context, roomID, deviceID string) ([]RoomKeyPackage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT epoch, device_id, wrapped, created_at FROM hosted_room_keys
		WHERE room_id = ? AND device_id = ? ORDER BY epoch`, roomID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("hosted: list room keys: %w", err)
	}
	defer rows.Close()
	var out []RoomKeyPackage
	for rows.Next() {
		var p RoomKeyPackage
		var created int64
		if err := rows.Scan(&p.Epoch, &p.DeviceID, &p.Wrapped, &created); err != nil {
			return nil, fmt.Errorf("hosted: scan room key: %w", err)
		}
		p.CreatedAt = timeFromMillis(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

func inviteKeysTx(ctx context.Context, tx *sql.Tx, roomID, inviteID string) ([]RoomKeyPackage, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT epoch, wrapped FROM hosted_room_invite_keys
		WHERE room_id = ? AND invite_id = ? ORDER BY epoch`, roomID, inviteID)
	if err != nil {
		return nil, fmt.Errorf("hosted: list invite keys: %w", err)
	}
	defer rows.Close()
	var out []RoomKeyPackage
	for rows.Next() {
		var p RoomKeyPackage
		if err := rows.Scan(&p.Epoch, &p.Wrapped); err != nil {
			return nil, fmt.Errorf("hosted: scan invite key: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HostedRoomKeyRequests reports which active peer devices lack requested
// epoch keys. The requester is excluded.
func (s *Store) HostedRoomKeyRequests(ctx context.Context, roomID, requesterDeviceID string, epochs []int) ([]RoomKeyRequest, error) {
	room, err := s.GetHostedRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	requester, err := s.GetHostedRoomDevice(ctx, roomID, requesterDeviceID)
	if err != nil || requester.RevokedAt != nil {
		return nil, ErrHostedRoomForbidden
	}
	wanted := make(map[int]bool)
	for _, epoch := range epochs {
		if epoch < 1 || epoch > room.Epoch {
			continue
		}
		if epoch < room.Epoch {
			visible, err := hostedEpochVisible(ctx, s.db, roomID, epoch, requester.FromSeq)
			if err != nil {
				return nil, err
			}
			if !visible {
				continue
			}
		}
		wanted[epoch] = true
	}
	// The current epoch is always worth distributing; older gaps only on
	// explicit request.
	wanted[room.Epoch] = true
	epochList := make([]int, 0, len(wanted))
	for epoch := range wanted {
		epochList = append(epochList, epoch)
	}
	devices, err := s.ListHostedRoomDevices(ctx, roomID)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for i := range devices {
		d := &devices[i]
		if d.RevokedAt != nil || d.DeviceID == requesterDeviceID || d.DeviceID == "" {
			continue
		}
		member, err := s.GetHostedRoomMember(ctx, roomID, d.AccountID)
		if err != nil || member.Status != "active" {
			continue
		}
		active[d.DeviceID] = true
	}
	if len(active) == 0 {
		return nil, nil
	}
	present := make(map[string]map[int]bool)
	for _, epoch := range epochList {
		rows, err := s.db.QueryContext(ctx, `
			SELECT device_id FROM hosted_room_keys WHERE room_id = ? AND epoch = ?`, roomID, epoch)
		if err != nil {
			return nil, fmt.Errorf("hosted: check room key coverage: %w", err)
		}
		for rows.Next() {
			var deviceID string
			if err := rows.Scan(&deviceID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("hosted: scan key coverage: %w", err)
			}
			if active[deviceID] {
				if present[deviceID] == nil {
					present[deviceID] = make(map[int]bool)
				}
				present[deviceID][epoch] = true
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	var requests []RoomKeyRequest
	for deviceID := range active {
		var missing []int
		for _, epoch := range epochList {
			if !present[deviceID][epoch] {
				missing = append(missing, epoch)
			}
		}
		if len(missing) > 0 {
			requests = append(requests, RoomKeyRequest{DeviceID: deviceID, Epochs: missing})
			if len(requests) >= 200 {
				break
			}
		}
	}
	return requests, nil
}

// LeaveHostedRoom appends a member_left event, revokes that account's
// devices, deletes its readable key packages, and marks rotation required
// while anyone remains.
func (s *Store) LeaveHostedRoom(ctx context.Context, roomID, accountID, deviceID string, event room.Event, now time.Time) (*HostedRoom, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("hosted: begin leave: %w", err)
	}
	defer tx.Rollback()

	room, err := hostedRoomTx(ctx, tx, roomID)
	if err != nil {
		return nil, false, err
	}
	member, err := hostedRoomMemberTx(ctx, tx, roomID, accountID)
	if err != nil || member.Status != "active" {
		return nil, false, ErrHostedRoomForbidden
	}
	caller, err := hostedRoomDeviceTx(ctx, tx, roomID, deviceID)
	if err != nil || caller.AccountID != accountID || caller.RevokedAt != nil {
		return nil, false, ErrHostedRoomForbidden
	}
	appended, err := appendHostedRoomEventTx(ctx, tx, roomID, room.Head, room.HeadEnvelopeHash, &event)
	if err != nil {
		return nil, false, err
	}
	if !appended {
		// The exact leave is already committed. Return the current state
		// without advancing or rewinding the head.
		return room, room.RotationRequired, nil
	}
	now = now.UTC().Truncate(time.Millisecond)
	if _, err := tx.ExecContext(ctx, `
		UPDATE hosted_room_members SET status = 'left', left_at = ?
		WHERE room_id = ? AND account_id = ?`, millis(now), roomID, accountID); err != nil {
		return nil, false, fmt.Errorf("hosted: mark member left: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE hosted_room_devices SET revoked_at = ?
		WHERE room_id = ? AND account_id = ? AND revoked_at IS NULL`, millis(now), roomID, accountID); err != nil {
		return nil, false, fmt.Errorf("hosted: revoke member devices: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM hosted_room_keys WHERE room_id = ? AND device_id IN (
			SELECT device_id FROM hosted_room_devices WHERE room_id = ? AND account_id = ?)`,
		roomID, roomID, accountID); err != nil {
		return nil, false, fmt.Errorf("hosted: delete member keys: %w", err)
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM hosted_room_members WHERE room_id = ? AND status = 'active'`,
		roomID).Scan(&remaining); err != nil {
		return nil, false, fmt.Errorf("hosted: count remaining members: %w", err)
	}
	rotationRequired := remaining > 0
	if rotationRequired {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosted_rooms SET rotation_required = 1 WHERE id = ?`, roomID); err != nil {
			return nil, false, fmt.Errorf("hosted: require rotation: %w", err)
		}
	} else {
		// No active member remains, so an outstanding invite must not
		// resurrect a room whose owner and members have all left.
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosted_room_invites SET revoked_at = ?
			WHERE room_id = ? AND used_at IS NULL AND revoked_at IS NULL`,
			millis(now), roomID); err != nil {
			return nil, false, fmt.Errorf("hosted: revoke unused invites: %w", err)
		}
	}
	if member.Role == "owner" && remaining > 0 {
		var heir string
		if err := tx.QueryRowContext(ctx, `
			SELECT account_id FROM hosted_room_members
			WHERE room_id = ? AND status = 'active'
			ORDER BY joined_at, account_id LIMIT 1`, roomID).Scan(&heir); err != nil {
			return nil, false, fmt.Errorf("hosted: choose new owner: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosted_room_members SET role = 'owner'
			WHERE room_id = ? AND account_id = ?`, roomID, heir); err != nil {
			return nil, false, fmt.Errorf("hosted: transfer ownership: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosted_rooms SET owner_account_id = ? WHERE id = ?`, heir, roomID); err != nil {
			return nil, false, fmt.Errorf("hosted: update room owner: %w", err)
		}
	}
	if err := updateHostedRoomHead(ctx, tx, roomID, event.Seq, event.EnvelopeHash, room.Epoch, rotationRequired, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("hosted: commit leave: %w", err)
	}
	updated, err := s.GetHostedRoom(ctx, roomID)
	return updated, rotationRequired, err
}

// RotateHostedRoomEpoch installs a new epoch after checking that every active
// device has a wrapped key for it.
func (s *Store) RotateHostedRoomEpoch(ctx context.Context, roomID string, rotation HostedRoomRotation) (*HostedRoom, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin rotation: %w", err)
	}
	defer tx.Rollback()

	room, err := hostedRoomTx(ctx, tx, roomID)
	if err != nil {
		return nil, err
	}
	if room.Epoch == rotation.Epoch {
		// A retry after a lost response: accept the same new epoch and treat
		// the event as already installed if it matches.
		existing, err := hostedRoomEventByIDTx(ctx, tx, roomID, rotation.Event.ID)
		if err == nil && existing.Seq == rotation.Event.Seq &&
			existing.Hash == rotation.Event.Hash && existing.EnvelopeHash == rotation.Event.EnvelopeHash {
			return room, nil
		}
		return nil, ErrHostedRoomConflict
	}
	if rotation.Epoch != room.Epoch+1 {
		return nil, ErrHostedRoomConflict
	}
	if _, err := appendHostedRoomEventTx(ctx, tx, roomID, room.Head, room.HeadEnvelopeHash, &rotation.Event); err != nil {
		return nil, err
	}
	active, err := activeRoomDevicesTx(ctx, tx, roomID)
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool, len(rotation.Keys))
	for i := range rotation.Keys {
		p := &rotation.Keys[i]
		if p.Epoch != rotation.Epoch || p.DeviceID == "" || len(p.Wrapped) == 0 {
			return nil, ErrHostedRoomKeyCoverage
		}
		if _, ok := active[p.DeviceID]; !ok {
			return nil, ErrHostedRoomKeyCoverage
		}
		if covered[p.DeviceID] {
			return nil, ErrHostedRoomKeyCoverage
		}
		covered[p.DeviceID] = true
		created := p.CreatedAt
		if created.IsZero() {
			created = rotation.Now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hosted_room_keys (room_id, epoch, device_id, wrapped, created_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(room_id, epoch, device_id) DO UPDATE SET wrapped = excluded.wrapped`,
			roomID, p.Epoch, p.DeviceID, p.Wrapped, millis(created)); err != nil {
			return nil, fmt.Errorf("hosted: save rotated key: %w", err)
		}
	}
	for deviceID := range active {
		if !covered[deviceID] {
			return nil, ErrHostedRoomKeyCoverage
		}
	}
	if err := updateHostedRoomHead(ctx, tx, roomID, rotation.Event.Seq, rotation.Event.EnvelopeHash, rotation.Epoch, false, rotation.Now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: commit rotation: %w", err)
	}
	return s.GetHostedRoom(ctx, roomID)
}

// ListHostedRoomEvents returns visible events above after, oldest first.
func (s *Store) ListHostedRoomEvents(ctx context.Context, roomID string, after int64, limit int) ([]room.Event, bool, error) {
	return listHostedRoomEventsTx(ctx, s.db, roomID, after, limit)
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func listHostedRoomEventsTx(ctx context.Context, q queryer, roomID string, after int64, limit int) ([]room.Event, bool, error) {
	if limit <= 0 || limit > MaxRoomEvents {
		limit = MaxRoomEvents
	}
	if after < 0 {
		after = 0
	}
	rows, err := q.QueryContext(ctx, `
		SELECT room_id, seq, event_id, type, epoch, author_id, author_device, created_at,
		       payload, prev_id, prev_hash, hash, signature,
		       envelope_hash, envelope_prev_hash, envelope_signature
		FROM hosted_room_events WHERE room_id = ? AND seq > ?
		ORDER BY seq LIMIT ?`, roomID, after, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("hosted: list room events: %w", err)
	}
	defer rows.Close()
	var out []room.Event
	for rows.Next() {
		event, err := scanHostedRoomEvent(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, *event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// HostedRoomHeadEvent returns the newest event, or nil when the room has no
// events.
func (s *Store) HostedRoomHeadEvent(ctx context.Context, roomID string) (*room.Event, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT room_id, seq, event_id, type, epoch, author_id, author_device, created_at,
		       payload, prev_id, prev_hash, hash, signature,
		       envelope_hash, envelope_prev_hash, envelope_signature
		FROM hosted_room_events WHERE room_id = ? ORDER BY seq DESC LIMIT 1`, roomID)
	event, err := scanHostedRoomEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return event, nil
}

func scanHostedRoomEvent(row interface {
	Scan(dest ...any) error
}) (*room.Event, error) {
	var event room.Event
	var created int64
	var payload string
	if err := row.Scan(&event.RoomID, &event.Seq, &event.ID, &event.Type, &event.Epoch,
		&event.AuthorID, &event.AuthorDevice, &created, &payload, &event.PrevID,
		&event.PrevHash, &event.Hash, &event.Signature, &event.EnvelopeHash,
		&event.EnvelopePrevHash, &event.EnvelopeSignature); err != nil {
		return nil, err
	}
	event.CreatedAt = timeFromMillis(created)
	event.Payload = json.RawMessage(payload)
	return &event, nil
}

func hostedRoomTx(ctx context.Context, tx *sql.Tx, roomID string) (*HostedRoom, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, owner_account_id, epoch, head, head_envelope_hash, rotation_required,
		       created_at, updated_at
		FROM hosted_rooms WHERE id = ?`, roomID)
	var r HostedRoom
	var created, updated int64
	var rotation int
	if err := row.Scan(&r.ID, &r.OwnerAccountID, &r.Epoch, &r.Head, &r.HeadEnvelopeHash,
		&rotation, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomNotFound
		}
		return nil, fmt.Errorf("hosted: get room: %w", err)
	}
	r.RotationRequired = rotation != 0
	r.CreatedAt = timeFromMillis(created)
	r.UpdatedAt = timeFromMillis(updated)
	return &r, nil
}

func hostedRoomMemberTx(ctx context.Context, tx *sql.Tx, roomID, accountID string) (*HostedRoomMember, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT room_id, account_id, role, status, history, joined_at, left_at
		FROM hosted_room_members WHERE room_id = ? AND account_id = ?`, roomID, accountID)
	var m HostedRoomMember
	var joined int64
	var left sql.NullInt64
	if err := row.Scan(&m.RoomID, &m.AccountID, &m.Role, &m.Status, &m.History, &joined, &left); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomForbidden
		}
		return nil, fmt.Errorf("hosted: get room member: %w", err)
	}
	m.JoinedAt = timeFromMillis(joined)
	m.LeftAt = nullableTime(left)
	return &m, nil
}

func hostedRoomDeviceTx(ctx context.Context, tx *sql.Tx, roomID, deviceID string) (*HostedRoomDevice, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT room_id, device_id, account_id, display_name, signing_public_key,
		       encryption_public_key, history, from_seq, joined_at, revoked_at
		FROM hosted_room_devices WHERE room_id = ? AND device_id = ?`, roomID, deviceID)
	var d HostedRoomDevice
	var joined int64
	var revoked sql.NullInt64
	if err := row.Scan(&d.RoomID, &d.DeviceID, &d.AccountID, &d.DisplayName, &d.SigningPublicKey,
		&d.EncryptionPublicKey, &d.History, &d.FromSeq, &joined, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomForbidden
		}
		return nil, fmt.Errorf("hosted: get room device: %w", err)
	}
	d.JoinedAt = timeFromMillis(joined)
	d.RevokedAt = nullableTime(revoked)
	return &d, nil
}

// GetHostedRoomEvent returns one stored event by ID. It lets an idempotent
// mutation, such as a retried leave, replay the committed result.
func (s *Store) GetHostedRoomEvent(ctx context.Context, roomID, eventID string) (*room.Event, error) {
	return hostedRoomEventByID(ctx, s.db, roomID, eventID)
}

func hostedRoomEventByID(ctx context.Context, q queryer, roomID, eventID string) (*room.Event, error) {
	row := q.QueryRowContext(ctx, `
		SELECT room_id, seq, event_id, type, epoch, author_id, author_device, created_at,
		       payload, prev_id, prev_hash, hash, signature,
		       envelope_hash, envelope_prev_hash, envelope_signature
		FROM hosted_room_events WHERE room_id = ? AND event_id = ?`, roomID, eventID)
	event, err := scanHostedRoomEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHostedRoomConflict
	}
	if err != nil {
		return nil, fmt.Errorf("hosted: get room event: %w", err)
	}
	return event, nil
}

func hostedRoomEventByIDTx(ctx context.Context, tx *sql.Tx, roomID, eventID string) (*room.Event, error) {
	return hostedRoomEventByID(ctx, tx, roomID, eventID)
}

func hostedRoomActiveMemberCountTx(ctx context.Context, tx *sql.Tx, roomID string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM hosted_room_members WHERE room_id = ? AND status = 'active'`,
		roomID).Scan(&count); err != nil {
		return 0, fmt.Errorf("hosted: count room members: %w", err)
	}
	return count, nil
}

func hostedRoomDeviceCountTx(ctx context.Context, tx *sql.Tx, roomID string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM hosted_room_devices WHERE room_id = ?`, roomID).Scan(&count); err != nil {
		return 0, fmt.Errorf("hosted: count room devices: %w", err)
	}
	return count, nil
}

func activeRoomDevicesTx(ctx context.Context, tx *sql.Tx, roomID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT d.device_id FROM hosted_room_devices d
		JOIN hosted_room_members m ON m.room_id = d.room_id AND m.account_id = d.account_id
		WHERE d.room_id = ? AND d.revoked_at IS NULL AND m.status = 'active'`, roomID)
	if err != nil {
		return nil, fmt.Errorf("hosted: list active room devices: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var deviceID string
		if err := rows.Scan(&deviceID); err != nil {
			return nil, fmt.Errorf("hosted: scan active room device: %w", err)
		}
		out[deviceID] = true
	}
	return out, rows.Err()
}

// historyStartTx computes the first visible sequence for a history policy.
// "all" starts at one, "recent" at the first event of the current epoch, and
// "none" at the next event after the current head.
func historyStartTx(ctx context.Context, tx *sql.Tx, rm *HostedRoom, history string) (int64, error) {
	switch history {
	case room.HistoryAll:
		return 1, nil
	case room.HistoryRecent:
		var first sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT MIN(seq) FROM hosted_room_events WHERE room_id = ? AND epoch = ?`,
			rm.ID, rm.Epoch).Scan(&first); err != nil {
			return 0, fmt.Errorf("hosted: find epoch start: %w", err)
		}
		if first.Valid {
			return first.Int64, nil
		}
		return rm.Head + 1, nil
	case room.HistoryNone:
		return rm.Head + 1, nil
	default:
		return 0, ErrHostedRoomInviteInvalid
	}
}

// hostedEpochVisible reports whether a device whose readable window starts at
// fromSeq may receive an epoch key. The current epoch is handled by callers;
// older epochs need at least one event inside that device's window.
func hostedEpochVisible(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, roomID string, epoch int, fromSeq int64) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `
		SELECT 1 FROM hosted_room_events
		WHERE room_id = ? AND epoch = ? AND seq >= ? LIMIT 1`,
		roomID, epoch, fromSeq).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hosted: check epoch visibility: %w", err)
	}
	return true, nil
}

func insertHostedRoomMember(ctx context.Context, tx *sql.Tx, m *HostedRoomMember) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO hosted_room_members (room_id, account_id, role, status, history, joined_at, left_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.RoomID, m.AccountID, m.Role, m.Status, m.History, millis(m.JoinedAt), nullableMillis(m.LeftAt))
	if err != nil {
		return fmt.Errorf("hosted: insert room member: %w", err)
	}
	return nil
}

func upsertHostedRoomMember(ctx context.Context, tx *sql.Tx, m *HostedRoomMember) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO hosted_room_members (room_id, account_id, role, status, history, joined_at, left_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id, account_id) DO UPDATE SET
			role = excluded.role, status = excluded.status, history = excluded.history,
			joined_at = excluded.joined_at, left_at = excluded.left_at`,
		m.RoomID, m.AccountID, m.Role, m.Status, m.History, millis(m.JoinedAt), nullableMillis(m.LeftAt))
	if err != nil {
		return fmt.Errorf("hosted: upsert room member: %w", err)
	}
	return nil
}

func insertHostedRoomDevice(ctx context.Context, tx *sql.Tx, d *HostedRoomDevice) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO hosted_room_devices (room_id, device_id, account_id, display_name,
			signing_public_key, encryption_public_key, history, from_seq, joined_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.RoomID, d.DeviceID, d.AccountID, d.DisplayName, d.SigningPublicKey,
		d.EncryptionPublicKey, d.History, d.FromSeq, millis(d.JoinedAt), nullableMillis(d.RevokedAt))
	if err != nil {
		return fmt.Errorf("hosted: insert room device: %w", err)
	}
	return nil
}

func insertHostedRoomEvent(ctx context.Context, tx *sql.Tx, event *room.Event) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO hosted_room_events (room_id, seq, event_id, type, epoch, author_id,
			author_device, created_at, payload, prev_id, prev_hash, hash, signature,
			envelope_hash, envelope_prev_hash, envelope_signature)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RoomID, event.Seq, event.ID, event.Type, event.Epoch, event.AuthorID,
		event.AuthorDevice, millis(event.CreatedAt), string(event.Payload), event.PrevID,
		event.PrevHash, event.Hash, event.Signature, event.EnvelopeHash,
		event.EnvelopePrevHash, event.EnvelopeSignature)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrHostedRoomConflict
		}
		return fmt.Errorf("hosted: insert room event: %w", err)
	}
	return nil
}

func insertHostedRoomKey(ctx context.Context, tx *sql.Tx, roomID string, p *RoomKeyPackage, ignoreExisting bool) error {
	verb := "INSERT"
	if ignoreExisting {
		verb = "INSERT OR IGNORE"
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC().Truncate(time.Millisecond)
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		%s INTO hosted_room_keys (room_id, epoch, device_id, wrapped, created_at)
		VALUES (?, ?, ?, ?, ?)`, verb),
		roomID, p.Epoch, p.DeviceID, p.Wrapped, millis(created))
	if err != nil {
		return fmt.Errorf("hosted: insert room key: %w", err)
	}
	return nil
}

// appendHostedRoomEventTx checks sequence and both chains, then inserts.
// Returns false when the exact event is already present.
func appendHostedRoomEventTx(ctx context.Context, tx *sql.Tx, roomID string, head int64, headHash string, event *room.Event) (bool, error) {
	if event.RoomID != roomID || event.ID == "" || event.Seq <= 0 || event.EnvelopeHash == "" {
		return false, ErrHostedRoomConflict
	}
	if event.Seq <= head {
		existing, err := hostedRoomEventByIDTx(ctx, tx, roomID, event.ID)
		if err == nil && existing.Seq == event.Seq && existing.Type == event.Type &&
			existing.Hash == event.Hash && existing.EnvelopeHash == event.EnvelopeHash {
			return false, nil
		}
		return false, ErrHostedRoomConflict
	}
	if event.Seq != head+1 {
		return false, ErrHostedRoomConflict
	}
	if head == 0 {
		if event.PrevID != "" || event.PrevHash != "" || event.EnvelopePrevHash != "" {
			return false, ErrHostedRoomConflict
		}
	} else {
		var prevID, prevHash string
		if err := tx.QueryRowContext(ctx, `
			SELECT event_id, hash FROM hosted_room_events WHERE room_id = ? AND seq = ?`,
			roomID, head).Scan(&prevID, &prevHash); err != nil {
			return false, fmt.Errorf("hosted: load room head: %w", err)
		}
		if event.PrevID != prevID || event.PrevHash != prevHash || event.EnvelopePrevHash != headHash {
			return false, ErrHostedRoomConflict
		}
	}
	if err := insertHostedRoomEvent(ctx, tx, event); err != nil {
		return false, err
	}
	return true, nil
}

func updateHostedRoomHead(ctx context.Context, tx *sql.Tx, roomID string, head int64, headHash string, epoch int, rotationRequired bool, now time.Time) error {
	rotation := 0
	if rotationRequired {
		rotation = 1
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE hosted_rooms SET head = ?, head_envelope_hash = ?, epoch = ?,
			rotation_required = ?, updated_at = ? WHERE id = ?`,
		head, headHash, epoch, rotation, millis(now), roomID)
	if err != nil {
		return fmt.Errorf("hosted: update room head: %w", err)
	}
	return nil
}

func nullableMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return millis(*t)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "primary key")
}
