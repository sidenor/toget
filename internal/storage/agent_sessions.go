package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sidenor/toget/internal/agent"
)

// GetAgentSession returns one Agent room session.
func (d *DB) GetAgentSession(ctx context.Context, agentID, roomID string) (*agent.Session, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT agent_id, room_id, last_seen_seq, last_run_seq, active_event_id, active_seq,
		       runtime_session, run_count, status, last_error, updated_at
		FROM agent_sessions WHERE agent_id = ? AND room_id = ?`, agentID, roomID)
	session, err := scanAgentSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, agent.ErrSessionNotFound
		}
		return nil, fmt.Errorf("storage: get agent session: %w", err)
	}
	return session, nil
}

// ListAgentSessions returns every session for an Agent.
func (d *DB) ListAgentSessions(ctx context.Context, agentID string) ([]agent.Session, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT agent_id, room_id, last_seen_seq, last_run_seq, active_event_id, active_seq,
		       runtime_session, run_count, status, last_error, updated_at
		FROM agent_sessions WHERE agent_id = ? ORDER BY updated_at DESC, room_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("storage: list agent sessions: %w", err)
	}
	defer rows.Close()
	var out []agent.Session
	for rows.Next() {
		session, err := scanAgentSession(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan agent session: %w", err)
		}
		out = append(out, *session)
	}
	return out, rows.Err()
}

// SaveAgentSession inserts or replaces runtime state.
func (d *DB) SaveAgentSession(ctx context.Context, session *agent.Session) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO agent_sessions (
			agent_id, room_id, last_seen_seq, last_run_seq, active_event_id, active_seq,
			runtime_session, run_count, status, last_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, room_id) DO UPDATE SET
			last_seen_seq = excluded.last_seen_seq,
			last_run_seq = excluded.last_run_seq,
			active_event_id = excluded.active_event_id,
			active_seq = excluded.active_seq,
			runtime_session = excluded.runtime_session,
			run_count = excluded.run_count,
			status = excluded.status,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		session.AgentID, session.RoomID, session.LastSeenSeq, session.LastRunSeq,
		session.ActiveEventID, session.ActiveSeq, session.RuntimeSession,
		session.RunCount, session.Status, session.LastError, toMillis(session.UpdatedAt))
	if err != nil {
		return fmt.Errorf("storage: save agent session: %w", err)
	}
	return nil
}

// SaveAgentSessionIfActive updates a session only while it still belongs to
// expectedEvent. The condition and the write are one statement, so a reset or
// a newer run cannot be overwritten by a goroutine that was already finishing.
func (d *DB) SaveAgentSessionIfActive(ctx context.Context, session *agent.Session, expectedEvent string) (bool, error) {
	result, err := d.db.ExecContext(ctx, `
		UPDATE agent_sessions SET
			last_seen_seq = ?, last_run_seq = ?, active_event_id = ?, active_seq = ?,
			runtime_session = ?, run_count = ?, status = ?, last_error = ?, updated_at = ?
		WHERE agent_id = ? AND room_id = ? AND active_event_id = ?`,
		session.LastSeenSeq, session.LastRunSeq, session.ActiveEventID, session.ActiveSeq,
		session.RuntimeSession, session.RunCount, session.Status, session.LastError,
		toMillis(session.UpdatedAt), session.AgentID, session.RoomID, expectedEvent)
	if err != nil {
		return false, fmt.Errorf("storage: save active agent session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("storage: save active agent session: %w", err)
	}
	return updated > 0, nil
}

// DeleteAgentSession removes runtime state for one room.
func (d *DB) DeleteAgentSession(ctx context.Context, agentID, roomID string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE agent_id = ? AND room_id = ?`, agentID, roomID); err != nil {
		return fmt.Errorf("storage: delete agent session: %w", err)
	}
	return nil
}

func scanAgentSession(row rowScanner) (*agent.Session, error) {
	var session agent.Session
	var updated int64
	if err := row.Scan(
		&session.AgentID, &session.RoomID, &session.LastSeenSeq, &session.LastRunSeq,
		&session.ActiveEventID, &session.ActiveSeq, &session.RuntimeSession,
		&session.RunCount, &session.Status, &session.LastError, &updated,
	); err != nil {
		return nil, err
	}
	session.UpdatedAt = fromMillis(updated)
	return &session, nil
}
