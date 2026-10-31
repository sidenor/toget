package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sidenor/toget/internal/approval"
)

// CreateApproval inserts an approval.
func (d *DB) CreateApproval(ctx context.Context, a *approval.Approval) error {
	action, err := json.Marshal(a.Action)
	if err != nil {
		return fmt.Errorf("storage: encode approval action: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO approvals (id, room_id, agent_id, requested_by, action, action_digest,
			description, status, expires_at, decided_by, decided_at, consumed_at, policy_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.RoomID, a.AgentID, a.RequestedBy, string(action), a.ActionDigest,
		a.Description, a.Status, toMillis(a.ExpiresAt), nullableString(a.DecidedBy),
		nullableMillis(a.DecidedAt), nullableMillis(a.ConsumedAt), a.PolicyID, toMillis(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: create approval: %w", err)
	}
	return nil
}

// GetApproval returns one approval.
func (d *DB) GetApproval(ctx context.Context, id string) (*approval.Approval, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, agent_id, requested_by, action, action_digest, description,
		       status, expires_at, decided_by, decided_at, consumed_at, policy_id, created_at
		FROM approvals WHERE id = ?`, id)
	a, err := scanApproval(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, approval.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get approval: %w", err)
	}
	return a, nil
}

// ListApprovals returns approvals in a room, newest first.
func (d *DB) ListApprovals(ctx context.Context, roomID, status string) ([]approval.Approval, error) {
	query := `
		SELECT id, room_id, agent_id, requested_by, action, action_digest, description,
		       status, expires_at, decided_by, decided_at, consumed_at, policy_id, created_at
		FROM approvals WHERE 1 = 1`
	var args []any
	if roomID != "" {
		query += ` AND room_id = ?`
		args = append(args, roomID)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list approvals: %w", err)
	}
	defer rows.Close()
	var out []approval.Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan approval: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// DecideApproval records a decision if the approval is still pending.
func (d *DB) DecideApproval(ctx context.Context, id, status, by string, at time.Time) error {
	res, err := d.db.ExecContext(ctx, `
		UPDATE approvals SET status = ?, decided_by = ?, decided_at = ?
		WHERE id = ? AND status = 'pending'`,
		status, by, toMillis(at), id)
	if err != nil {
		return fmt.Errorf("storage: decide approval: %w", err)
	}
	return requireOneRow(res, approval.ErrNotPending)
}

// ExpireApproval marks a pending approval expired.
func (d *DB) ExpireApproval(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `
		UPDATE approvals SET status = 'expired' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("storage: expire approval: %w", err)
	}
	return requireOneRow(res, approval.ErrNotPending)
}

// ConsumeApproval marks a granted approval as used exactly once.
func (d *DB) ConsumeApproval(ctx context.Context, id string, at time.Time) error {
	res, err := d.db.ExecContext(ctx, `
		UPDATE approvals SET consumed_at = ?
		WHERE id = ? AND status = 'granted' AND consumed_at IS NULL`,
		toMillis(at), id)
	if err != nil {
		return fmt.Errorf("storage: consume approval: %w", err)
	}
	return requireOneRow(res, approval.ErrConsumed)
}

func requireOneRow(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: rows affected: %w", err)
	}
	if n == 0 {
		return notFound
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanApproval(row rowScanner) (*approval.Approval, error) {
	var a approval.Approval
	var action string
	var expires, created int64
	var decidedBy sql.NullString
	var decidedAt, consumedAt sql.NullInt64
	if err := row.Scan(&a.ID, &a.RoomID, &a.AgentID, &a.RequestedBy, &action,
		&a.ActionDigest, &a.Description, &a.Status, &expires, &decidedBy,
		&decidedAt, &consumedAt, &a.PolicyID, &created); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(action), &a.Action); err != nil {
		return nil, fmt.Errorf("decode approval action: %w", err)
	}
	a.ExpiresAt = fromMillis(expires)
	a.CreatedAt = fromMillis(created)
	if decidedBy.Valid {
		a.DecidedBy = decidedBy.String
	}
	if decidedAt.Valid {
		t := fromMillis(decidedAt.Int64)
		a.DecidedAt = &t
	}
	if consumedAt.Valid {
		t := fromMillis(consumedAt.Int64)
		a.ConsumedAt = &t
	}
	return &a, nil
}
