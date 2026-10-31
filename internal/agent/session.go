package agent

import (
	"context"
	"errors"
	"time"
)

// ErrSessionNotFound means the Agent has no runtime session for the room.
var ErrSessionNotFound = errors.New("agent session not found")

// Session is the persisted runtime state for one (Agent, Room) pair. It lets an
// Agent resume its own runtime conversation and catch up on messages it has not
// processed yet. Session state is local to this endpoint and is never part of
// the room log.
type Session struct {
	AgentID string `json:"agent_id"`
	RoomID  string `json:"room_id"`
	// LastSeenSeq is the highest room event scanned for mentions.
	LastSeenSeq int64 `json:"last_seen_seq"`
	// LastRunSeq is the highest room event included in a completed run. The
	// next run receives every event after it, so messages that arrived while
	// the Agent was busy or offline are part of the catch-up context.
	LastRunSeq int64 `json:"last_run_seq"`
	// ActiveEventID and ActiveSeq identify the mention being executed. They
	// let the next daemon process notice an interrupted run instead of silently
	// repeating or forgetting it.
	ActiveEventID  string    `json:"active_event_id,omitempty"`
	ActiveSeq      int64     `json:"active_seq,omitempty"`
	RuntimeSession string    `json:"runtime_session,omitempty"`
	RunCount       int64     `json:"run_count"`
	Status         string    `json:"status"`
	LastError      string    `json:"last_error,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SessionStore persists per-(Agent, Room) runtime state.
type SessionStore interface {
	GetAgentSession(ctx context.Context, agentID, roomID string) (*Session, error)
	ListAgentSessions(ctx context.Context, agentID string) ([]Session, error)
	SaveAgentSession(ctx context.Context, session *Session) error
	// SaveAgentSessionIfActive updates a session only while it still belongs
	// to expectedEvent. It reports whether the row was updated.
	SaveAgentSessionIfActive(ctx context.Context, session *Session, expectedEvent string) (bool, error)
	DeleteAgentSession(ctx context.Context, agentID, roomID string) error
}

func (s *Service) sessionStore() SessionStore {
	store, _ := s.store.(SessionStore)
	return store
}

// Session returns one Agent room session.
func (s *Service) Session(ctx context.Context, agentID, roomID string) (*Session, error) {
	store := s.sessionStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	return store.GetAgentSession(ctx, agentID, roomID)
}

// Sessions returns every persisted session for an Agent.
func (s *Service) Sessions(ctx context.Context, agentID string) ([]Session, error) {
	store := s.sessionStore()
	if store == nil {
		return nil, nil
	}
	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		return nil, err
	}
	return store.ListAgentSessions(ctx, agentID)
}

// EnsureSession creates a session at floor when none exists. The floor is the
// sequence of the Agent's join event, so the Agent never receives room history
// from before it joined.
func (s *Service) EnsureSession(ctx context.Context, agentID, roomID string, floor int64) (*Session, error) {
	store := s.sessionStore()
	if store == nil {
		return nil, ErrSessionNotFound
	}
	if floor < 0 {
		floor = 0
	}
	existing, err := store.GetAgentSession(ctx, agentID, roomID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrSessionNotFound) {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	session := &Session{
		AgentID: agentID, RoomID: roomID,
		LastSeenSeq: floor, LastRunSeq: floor,
		Status: StatusIdle, UpdatedAt: now,
	}
	if err := store.SaveAgentSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// SaveSession persists runtime state.
func (s *Service) SaveSession(ctx context.Context, session *Session) error {
	store := s.sessionStore()
	if store == nil {
		return ErrSessionNotFound
	}
	session.UpdatedAt = s.now().UTC().Truncate(time.Millisecond)
	return store.SaveAgentSession(ctx, session)
}

// SaveSessionIfActive persists a finishing run only if the session still
// belongs to the same trigger. A concurrent reset, leave, or newer run wins.
func (s *Service) SaveSessionIfActive(ctx context.Context, session *Session, expectedEvent string) (bool, error) {
	store := s.sessionStore()
	if store == nil {
		return false, ErrSessionNotFound
	}
	session.UpdatedAt = s.now().UTC().Truncate(time.Millisecond)
	return store.SaveAgentSessionIfActive(ctx, session, expectedEvent)
}

// DeleteSession removes runtime state, for example when an Agent leaves a room.
func (s *Service) DeleteSession(ctx context.Context, agentID, roomID string) error {
	store := s.sessionStore()
	if store == nil {
		return nil
	}
	return store.DeleteAgentSession(ctx, agentID, roomID)
}

// ResetSession clears the runtime conversation and moves both cursors to the
// current room head. It keeps the Agent from replaying a long backlog while
// preserving the current room context for the next mention.
func (s *Service) ResetSession(ctx context.Context, agentID, roomID string) (*Session, error) {
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a.RevokedAt != nil {
		return nil, ErrRevoked
	}
	inRoom, err := s.store.IsAgentInRoom(ctx, agentID, roomID)
	if err != nil {
		return nil, err
	}
	if !inRoom {
		return nil, ErrNotInRoom
	}
	head, err := s.rooms.LastSeq(ctx, roomID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	session := &Session{
		AgentID: agentID, RoomID: roomID,
		LastSeenSeq: head, LastRunSeq: head,
		Status: StatusIdle, UpdatedAt: now,
	}
	if err := s.SaveSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// ensureSessionAfterJoin records the join sequence without making a successful
// room join depend on session persistence.
func (s *Service) ensureSessionAfterJoin(ctx context.Context, agentID, roomID string, seq int64) {
	_, _ = s.EnsureSession(ctx, agentID, roomID, seq)
}
