package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sidenor/toget/internal/policy"
)

// GetPolicy returns the policy for one room and Agent. An empty agentID is the
// room default policy.
func (d *DB) GetPolicy(ctx context.Context, roomID, agentID string) (*policy.Policy, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, room_id, agent_id, rules, created_at, updated_at
		FROM policies WHERE room_id = ? AND agent_id = ?`, roomID, agentID)
	p, err := scanPolicy(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, policy.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get policy: %w", err)
	}
	return p, nil
}

// ListPolicies returns the room default policy first, then Agent policies.
func (d *DB) ListPolicies(ctx context.Context, roomID string) ([]policy.Policy, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, room_id, agent_id, rules, created_at, updated_at
		FROM policies WHERE room_id = ?
		ORDER BY CASE WHEN agent_id = '' THEN 0 ELSE 1 END, agent_id`, roomID)
	if err != nil {
		return nil, fmt.Errorf("storage: list policies: %w", err)
	}
	defer rows.Close()
	var out []policy.Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan policy: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpsertPolicy inserts or replaces the rules for one room and Agent.
func (d *DB) UpsertPolicy(ctx context.Context, p *policy.Policy) error {
	rules, err := json.Marshal(p.Rules)
	if err != nil {
		return fmt.Errorf("storage: encode policy rules: %w", err)
	}
	if len(rules) > policy.MaxRulesJSON {
		return fmt.Errorf("storage: policy rules exceed %d bytes", policy.MaxRulesJSON)
	}
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO policies (id, room_id, agent_id, rules, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id, agent_id) DO UPDATE SET
			rules = excluded.rules, updated_at = excluded.updated_at`,
		p.ID, p.RoomID, p.AgentID, string(rules), toMillis(p.CreatedAt), toMillis(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("storage: upsert policy: %w", err)
	}
	return nil
}

// DeletePolicy removes one policy document.
func (d *DB) DeletePolicy(ctx context.Context, roomID, agentID string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM policies WHERE room_id = ? AND agent_id = ?`, roomID, agentID)
	if err != nil {
		return fmt.Errorf("storage: delete policy: %w", err)
	}
	return requireOneRow(res, policy.ErrNotFound)
}

func scanPolicy(row rowScanner) (*policy.Policy, error) {
	var p policy.Policy
	var rules string
	var created, updated int64
	if err := row.Scan(&p.ID, &p.RoomID, &p.AgentID, &rules, &created, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(rules), &p.Rules); err != nil {
		return nil, fmt.Errorf("decode policy rules: %w", err)
	}
	p.CreatedAt = fromMillis(created)
	p.UpdatedAt = fromMillis(updated)
	return &p, nil
}
