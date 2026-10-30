package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sidenor/toget/internal/agent"
)

// CreateAgent inserts an Agent.
func (d *DB) CreateAgent(ctx context.Context, a *agent.Agent) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO agents (id, account_id, name, runtime, status, permissions, created_at, last_seen_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.AccountID, a.Name, a.Runtime, a.Status, string(a.Permissions), toMillis(a.CreatedAt),
		nullableMillis(a.LastSeenAt), nullableMillis(a.RevokedAt))
	if err != nil {
		return fmt.Errorf("storage: create agent: %w", err)
	}
	return nil
}

// GetAgent returns one Agent.
func (d *DB) GetAgent(ctx context.Context, id string) (*agent.Agent, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, account_id, name, runtime, status, permissions, created_at, last_seen_at, revoked_at
		FROM agents WHERE id = ?`, id)
	a, err := scanAgent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, agent.ErrNotFound
		}
		return nil, fmt.Errorf("storage: get agent: %w", err)
	}
	return a, nil
}

// ListAgents returns Agents for an account.
func (d *DB) ListAgents(ctx context.Context, accountID string) ([]agent.Agent, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, account_id, name, runtime, status, permissions, created_at, last_seen_at, revoked_at
		FROM agents WHERE account_id = ? ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("storage: list agents: %w", err)
	}
	defer rows.Close()
	var out []agent.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan agent: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// UpdateAgent updates Agent presence and metadata.
func (d *DB) UpdateAgent(ctx context.Context, a *agent.Agent) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE agents SET name = ?, runtime = ?, status = ?, permissions = ?, last_seen_at = ?, revoked_at = ?
		WHERE id = ?`,
		a.Name, a.Runtime, a.Status, string(a.Permissions), nullableMillis(a.LastSeenAt), nullableMillis(a.RevokedAt), a.ID)
	if err != nil {
		return fmt.Errorf("storage: update agent: %w", err)
	}
	return nil
}

// AddAgentRoom records an Agent room membership.
func (d *DB) AddAgentRoom(ctx context.Context, agentID, roomID string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO agent_rooms (agent_id, room_id, joined_at) VALUES (?, ?, ?)`,
		agentID, roomID, toMillis(at))
	if err != nil {
		if isUniqueViolation(err) {
			return agent.ErrAlreadyInRoom
		}
		return fmt.Errorf("storage: add agent room: %w", err)
	}
	return nil
}

// RemoveAgentRoom removes an Agent room membership.
func (d *DB) RemoveAgentRoom(ctx context.Context, agentID, roomID string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM agent_rooms WHERE agent_id = ? AND room_id = ?`, agentID, roomID); err != nil {
		return fmt.Errorf("storage: remove agent room: %w", err)
	}
	return nil
}

// IsAgentInRoom reports membership.
func (d *DB) IsAgentInRoom(ctx context.Context, agentID, roomID string) (bool, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_rooms WHERE agent_id = ? AND room_id = ?`, agentID, roomID).Scan(&n); err != nil {
		return false, fmt.Errorf("storage: check agent room: %w", err)
	}
	return n > 0, nil
}

// ListAgentRooms returns an Agent's room memberships.
func (d *DB) ListAgentRooms(ctx context.Context, agentID string) ([]agent.RoomRef, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT room_id, joined_at FROM agent_rooms WHERE agent_id = ? ORDER BY joined_at`, agentID)
	if err != nil {
		return nil, fmt.Errorf("storage: list agent rooms: %w", err)
	}
	defer rows.Close()
	var out []agent.RoomRef
	for rows.Next() {
		var ref agent.RoomRef
		var joined int64
		if err := rows.Scan(&ref.RoomID, &joined); err != nil {
			return nil, fmt.Errorf("storage: scan agent room: %w", err)
		}
		ref.JoinedAt = fromMillis(joined)
		out = append(out, ref)
	}
	return out, rows.Err()
}

// ListRoomAgents returns Agents that joined a room.
func (d *DB) ListRoomAgents(ctx context.Context, roomID string) ([]agent.Agent, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT a.id, a.account_id, a.name, a.runtime, a.status, a.permissions, a.created_at,
		       a.last_seen_at, a.revoked_at
		FROM agents a JOIN agent_rooms ar ON ar.agent_id = a.id
		WHERE ar.room_id = ? AND a.revoked_at IS NULL ORDER BY ar.joined_at`, roomID)
	if err != nil {
		return nil, fmt.Errorf("storage: list room agents: %w", err)
	}
	defer rows.Close()
	var out []agent.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan room agent: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func scanAgent(row rowScanner) (*agent.Agent, error) {
	var a agent.Agent
	var created int64
	var lastSeen, revoked sql.NullInt64
	var permissions string
	if err := row.Scan(&a.ID, &a.AccountID, &a.Name, &a.Runtime, &a.Status, &permissions,
		&created, &lastSeen, &revoked); err != nil {
		return nil, err
	}
	a.Permissions = []byte(permissions)
	a.CreatedAt = fromMillis(created)
	if lastSeen.Valid {
		t := fromMillis(lastSeen.Int64)
		a.LastSeenAt = &t
	}
	if revoked.Valid {
		t := fromMillis(revoked.Int64)
		a.RevokedAt = &t
	}
	return &a, nil
}
