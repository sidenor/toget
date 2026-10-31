package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sidenor/toget/internal/room"
)

// GetRoomAgent returns the home node's projection of a remote Agent.
func (d *DB) GetRoomAgent(ctx context.Context, roomID, agentID string) (*room.RoomAgent, error) {
	var record room.RoomAgent
	var active int
	err := d.db.QueryRowContext(ctx, `
		SELECT room_id, agent_id, account_id, active, joined_seq, left_seq
		FROM room_agents WHERE room_id = ? AND agent_id = ?`, roomID, agentID).
		Scan(&record.RoomID, &record.AgentID, &record.AccountID, &active, &record.JoinedSeq, &record.LeftSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, room.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: get room agent: %w", err)
	}
	record.Active = active != 0
	return &record, nil
}

// AppendEventWithRoomAgent appends an event and updates the remote-Agent
// projection in one transaction, so a crash cannot leave one without the
// other.
func (d *DB) AppendEventWithRoomAgent(ctx context.Context, event *room.Event, agent *room.RoomAgent) error {
	if event == nil || agent == nil {
		return errors.New("storage: event and room agent are required")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin event: %w", err)
	}
	defer tx.Rollback()
	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}
	var owner string
	err = tx.QueryRowContext(ctx, `
		SELECT account_id FROM room_agents WHERE room_id = ? AND agent_id = ?`,
		agent.RoomID, agent.AgentID).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// First join.
	case err != nil:
		return fmt.Errorf("storage: read room agent: %w", err)
	case owner != agent.AccountID:
		return fmt.Errorf("storage: agent belongs to another account")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_agents (room_id, agent_id, account_id, active, joined_seq, left_seq)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id, agent_id) DO UPDATE SET
			active = excluded.active,
			joined_seq = excluded.joined_seq,
			left_seq = excluded.left_seq`,
		agent.RoomID, agent.AgentID, agent.AccountID, boolInt(agent.Active),
		agent.JoinedSeq, agent.LeftSeq); err != nil {
		return fmt.Errorf("storage: save room agent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit event: %w", err)
	}
	return nil
}
