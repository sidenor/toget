package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/sidenor/toget/internal/room"
)

// SaveRoomEpoch stores a sealed room key.
func (d *DB) SaveRoomEpoch(ctx context.Context, e *room.RoomEpoch) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO room_epochs (room_id, epoch, key_sealed, created_at) VALUES (?, ?, ?, ?)`,
		e.RoomID, e.Epoch, e.KeySealed, toMillis(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: save room epoch: %w", err)
	}
	return nil
}

// GetRoomEpoch returns one sealed room key.
func (d *DB) GetRoomEpoch(ctx context.Context, roomID string, epoch int) (*room.RoomEpoch, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT room_id, epoch, key_sealed, created_at FROM room_epochs
		WHERE room_id = ? AND epoch = ?`, roomID, epoch)
	var e room.RoomEpoch
	var created int64
	if err := row.Scan(&e.RoomID, &e.Epoch, &e.KeySealed, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get room epoch: %w", err)
	}
	e.CreatedAt = fromMillis(created)
	return &e, nil
}

// LatestRoomEpoch returns the newest sealed key for a room.
func (d *DB) LatestRoomEpoch(ctx context.Context, roomID string) (*room.RoomEpoch, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT room_id, epoch, key_sealed, created_at FROM room_epochs
		WHERE room_id = ? ORDER BY epoch DESC LIMIT 1`, roomID)
	var e room.RoomEpoch
	var created int64
	if err := row.Scan(&e.RoomID, &e.Epoch, &e.KeySealed, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: latest room epoch: %w", err)
	}
	e.CreatedAt = fromMillis(created)
	return &e, nil
}

// ListRoomEpochs returns every sealed key for a room in epoch order.
func (d *DB) ListRoomEpochs(ctx context.Context, roomID string) ([]room.RoomEpoch, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT room_id, epoch, key_sealed, created_at FROM room_epochs
		WHERE room_id = ? ORDER BY epoch`, roomID)
	if err != nil {
		return nil, fmt.Errorf("storage: list room epochs: %w", err)
	}
	defer rows.Close()
	var out []room.RoomEpoch
	for rows.Next() {
		var e room.RoomEpoch
		var created int64
		if err := rows.Scan(&e.RoomID, &e.Epoch, &e.KeySealed, &created); err != nil {
			return nil, fmt.Errorf("storage: scan room epoch: %w", err)
		}
		e.CreatedAt = fromMillis(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveRoomMemberKey stores a wrapped room key for one device.
func (d *DB) SaveRoomMemberKey(ctx context.Context, k *room.RoomMemberKey) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO room_member_keys (room_id, epoch, device_id, wrapped_key, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		k.RoomID, k.Epoch, k.DeviceID, k.Wrapped, toMillis(k.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: save room member key: %w", err)
	}
	return nil
}

// GetRoomMemberKey returns a wrapped key for one device.
func (d *DB) GetRoomMemberKey(ctx context.Context, roomID string, epoch int, deviceID string) (*room.RoomMemberKey, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT room_id, epoch, device_id, wrapped_key, created_at FROM room_member_keys
		WHERE room_id = ? AND epoch = ? AND device_id = ?`, roomID, epoch, deviceID)
	var k room.RoomMemberKey
	var created int64
	if err := row.Scan(&k.RoomID, &k.Epoch, &k.DeviceID, &k.Wrapped, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get room member key: %w", err)
	}
	k.CreatedAt = fromMillis(created)
	return &k, nil
}

// ListRoomMemberKeys returns wrapped keys for an epoch.
func (d *DB) ListRoomMemberKeys(ctx context.Context, roomID string, epoch int) ([]room.RoomMemberKey, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT room_id, epoch, device_id, wrapped_key, created_at FROM room_member_keys
		WHERE room_id = ? AND epoch = ? ORDER BY device_id`, roomID, epoch)
	if err != nil {
		return nil, fmt.Errorf("storage: list room member keys: %w", err)
	}
	defer rows.Close()
	var out []room.RoomMemberKey
	for rows.Next() {
		var k room.RoomMemberKey
		var created int64
		if err := rows.Scan(&k.RoomID, &k.Epoch, &k.DeviceID, &k.Wrapped, &created); err != nil {
			return nil, fmt.Errorf("storage: scan room member key: %w", err)
		}
		k.CreatedAt = fromMillis(created)
		out = append(out, k)
	}
	return out, rows.Err()
}

func insertRoomEpoch(ctx context.Context, tx *sql.Tx, e *room.RoomEpoch) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_epochs (room_id, epoch, key_sealed, created_at) VALUES (?, ?, ?, ?)`,
		e.RoomID, e.Epoch, e.KeySealed, toMillis(e.CreatedAt)); err != nil {
		return fmt.Errorf("storage: insert room epoch: %w", err)
	}
	return nil
}

func insertRoomMemberKey(ctx context.Context, tx *sql.Tx, k *room.RoomMemberKey) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_member_keys (room_id, epoch, device_id, wrapped_key, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		k.RoomID, k.Epoch, k.DeviceID, k.Wrapped, toMillis(k.CreatedAt)); err != nil {
		return fmt.Errorf("storage: insert room member key: %w", err)
	}
	return nil
}
