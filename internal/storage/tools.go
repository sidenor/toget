package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sidenor/toget/internal/tool"
)

// CreateToolReceipt inserts a tool receipt.
func (d *DB) CreateToolReceipt(ctx context.Context, r *tool.Receipt) error {
	input := r.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO tool_receipts (id, room_id, agent_id, approval_id, kind, tool, input, output, status, policy_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.RoomID, r.AgentID, r.ApprovalID, r.Kind, r.Tool, string(input), r.Output, r.Status, r.PolicyID, toMillis(r.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: create tool receipt: %w", err)
	}
	return nil
}

// GetToolReceipt returns one receipt.
func (d *DB) GetToolReceipt(ctx context.Context, id string) (*tool.Receipt, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, agent_id, approval_id, kind, tool, input, output, status, policy_id, created_at
		FROM tool_receipts WHERE id = ?`, id)
	r, err := scanToolReceipt(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, tool.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get tool receipt: %w", err)
	}
	return r, nil
}

// ListToolReceipts returns recent receipts in a room.
func (d *DB) ListToolReceipts(ctx context.Context, roomID string, limit int) ([]tool.Receipt, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, room_id, agent_id, approval_id, kind, tool, input, output, status, policy_id, created_at
		FROM tool_receipts WHERE room_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list tool receipts: %w", err)
	}
	defer rows.Close()
	var out []tool.Receipt
	for rows.Next() {
		r, err := scanToolReceipt(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan tool receipt: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CompleteToolReceipt completes a requested receipt exactly once.
func (d *DB) CompleteToolReceipt(ctx context.Context, id, status, output string) error {
	res, err := d.db.ExecContext(ctx, `
		UPDATE tool_receipts SET status = ?, output = ? WHERE id = ? AND status = 'requested'`,
		status, output, id)
	if err != nil {
		return fmt.Errorf("storage: complete tool receipt: %w", err)
	}
	return requireOneRow(res, tool.ErrNotActive)
}

func scanToolReceipt(row rowScanner) (*tool.Receipt, error) {
	var r tool.Receipt
	var input string
	var created int64
	if err := row.Scan(&r.ID, &r.RoomID, &r.AgentID, &r.ApprovalID, &r.Kind, &r.Tool,
		&input, &r.Output, &r.Status, &r.PolicyID, &created); err != nil {
		return nil, err
	}
	r.Input = json.RawMessage(input)
	r.CreatedAt = fromMillis(created)
	return &r, nil
}
