package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sidenor/toget/internal/room"
)

// CreateAttachment stores attachment metadata.
func (d *DB) CreateAttachment(ctx context.Context, a *room.Attachment) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO attachments (id, room_id, event_id, name, mime, size, sha256, path, key_sealed, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.RoomID, a.EventID, a.Name, a.MIME, a.Size, a.SHA256, a.Path, a.KeySealed, toMillis(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: create attachment: %w", err)
	}
	return nil
}

// GetAttachment returns attachment metadata by id.
func (d *DB) GetAttachment(ctx context.Context, id string) (*room.Attachment, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, event_id, name, mime, size, sha256, path, key_sealed, created_at
		FROM attachments WHERE id = ?`, id)
	return scanAttachment(row)
}

// ListAttachments returns attachments in a room, oldest first.
func (d *DB) ListAttachments(ctx context.Context, roomID string) ([]room.Attachment, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, room_id, event_id, name, mime, size, sha256, path, key_sealed, created_at
		FROM attachments WHERE room_id = ? ORDER BY created_at, id`, roomID)
	if err != nil {
		return nil, fmt.Errorf("storage: list attachments: %w", err)
	}
	defer rows.Close()
	var out []room.Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// AttachToEvent binds unattached attachments to an event.
func (d *DB) AttachToEvent(ctx context.Context, eventID string, attachmentIDs []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin attach: %w", err)
	}
	defer tx.Rollback()
	for _, id := range attachmentIDs {
		res, err := tx.ExecContext(ctx, `
			UPDATE attachments SET event_id = ? WHERE id = ? AND event_id = ''`, eventID, id)
		if err != nil {
			return fmt.Errorf("storage: attach %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storage: attach rows: %w", err)
		}
		if n != 1 {
			return room.ErrAttachmentUsed
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit attach: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAttachment(row scanner) (*room.Attachment, error) {
	var a room.Attachment
	var created int64
	if err := row.Scan(&a.ID, &a.RoomID, &a.EventID, &a.Name, &a.MIME, &a.Size,
		&a.SHA256, &a.Path, &a.KeySealed, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, room.ErrAttachmentNotFound
		}
		return nil, fmt.Errorf("storage: scan attachment: %w", err)
	}
	a.CreatedAt = fromMillis(created)
	return &a, nil
}

// UpsertAttachment inserts or refreshes metadata received from a home node.
// A locally cached ciphertext path is preserved; an event binding is only
// added, never cleared.
func (d *DB) UpsertAttachment(ctx context.Context, a *room.Attachment) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO attachments (id, room_id, event_id, name, mime, size, sha256, path, key_sealed, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			event_id = CASE WHEN excluded.event_id <> '' THEN excluded.event_id ELSE attachments.event_id END,
			name = excluded.name,
			mime = excluded.mime,
			size = excluded.size,
			sha256 = excluded.sha256,
			key_sealed = excluded.key_sealed,
			path = CASE WHEN attachments.path <> '' THEN attachments.path ELSE excluded.path END`,
		a.ID, a.RoomID, a.EventID, a.Name, a.MIME, a.Size, a.SHA256, a.Path, a.KeySealed, toMillis(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: upsert attachment: %w", err)
	}
	return nil
}

// SetAttachmentPath records the local ciphertext path after a download.
func (d *DB) SetAttachmentPath(ctx context.Context, id, path string) error {
	res, err := d.db.ExecContext(ctx, `UPDATE attachments SET path = ? WHERE id = ?`, path, id)
	if err != nil {
		return fmt.Errorf("storage: set attachment path: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: set attachment path rows: %w", err)
	}
	if n != 1 {
		return room.ErrAttachmentNotFound
	}
	return nil
}
