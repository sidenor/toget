// Package agent owns Agent identities, room membership, and presence.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
)

var (
	// ErrNotFound means the Agent does not exist.
	ErrNotFound = errors.New("agent not found")
	// ErrInvalidName means the Agent name is empty or too long.
	ErrInvalidName = errors.New("invalid agent name")
	// ErrInvalidRuntime means no runtime was provided.
	ErrInvalidRuntime = errors.New("invalid agent runtime")
	// ErrNotInRoom means the Agent is not a member of the room.
	ErrNotInRoom = errors.New("agent is not in the room")
	// ErrAlreadyInRoom means the Agent already joined.
	ErrAlreadyInRoom = errors.New("agent already joined the room")
	// ErrRevoked means the Agent has been revoked.
	ErrRevoked = errors.New("agent is revoked")
	// ErrNotAgentOwner means the caller's account does not own the Agent.
	ErrNotAgentOwner = errors.New("caller does not own this agent")
)

const (
	// MaxNameLength is the maximum Agent display name.
	MaxNameLength = 64

	StatusIdle    = "idle"
	StatusRunning = "running"
	StatusWaiting = "waiting_approval"
	StatusOffline = "offline"
	StatusRevoked = "revoked"
)

// Agent is a locally registered Agent identity.
type Agent struct {
	ID          string          `json:"id"`
	AccountID   string          `json:"account_id"`
	Name        string          `json:"name"`
	Runtime     string          `json:"runtime"`
	Status      string          `json:"status"`
	Permissions json.RawMessage `json:"permissions"`
	CreatedAt   time.Time       `json:"created_at"`
	LastSeenAt  *time.Time      `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time      `json:"revoked_at,omitempty"`
}

// RoomRef is a room the Agent belongs to.
type RoomRef struct {
	RoomID   string    `json:"room_id"`
	JoinedAt time.Time `json:"joined_at"`
}

// Store persists Agents and their room membership.
type Store interface {
	CreateAgent(ctx context.Context, a *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context, accountID string) ([]Agent, error)
	UpdateAgent(ctx context.Context, a *Agent) error
	AddAgentRoom(ctx context.Context, agentID, roomID string, at time.Time) error
	RemoveAgentRoom(ctx context.Context, agentID, roomID string) error
	IsAgentInRoom(ctx context.Context, agentID, roomID string) (bool, error)
	ListAgentRooms(ctx context.Context, agentID string) ([]RoomRef, error)
	ListRoomAgents(ctx context.Context, roomID string) ([]Agent, error)
}

// Service owns Agent rules.
type Service struct {
	store Store
	rooms *room.Service
	now   func() time.Time
}

// New creates an Agent service.
func New(store Store, rooms *room.Service) *Service {
	return &Service{store: store, rooms: rooms, now: time.Now}
}

// Register creates an Agent identity on this device.
func (s *Service) Register(ctx context.Context, accountID, name, runtime string, permissions json.RawMessage) (*Agent, error) {
	name = strings.TrimSpace(name)
	runtime = strings.TrimSpace(runtime)
	if name == "" || len(name) > MaxNameLength {
		return nil, ErrInvalidName
	}
	if runtime == "" || len(runtime) > 64 {
		return nil, ErrInvalidRuntime
	}
	if len(permissions) == 0 {
		permissions = json.RawMessage(`{}`)
	}
	var probe any
	if err := json.Unmarshal(permissions, &probe); err != nil {
		return nil, fmt.Errorf("permissions must be JSON: %w", err)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	a := &Agent{
		ID:          identity.NewID(),
		AccountID:   accountID,
		Name:        name,
		Runtime:     runtime,
		Status:      StatusIdle,
		Permissions: permissions,
		CreatedAt:   now,
	}
	if err := s.store.CreateAgent(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Get returns one Agent.
func (s *Service) Get(ctx context.Context, id string) (*Agent, error) {
	return s.store.GetAgent(ctx, id)
}

// List returns all local Agents.
func (s *Service) List(ctx context.Context, accountID string) ([]Agent, error) {
	return s.store.ListAgents(ctx, accountID)
}

// ListInRoom returns Agents that joined a room.
func (s *Service) ListInRoom(ctx context.Context, roomID string) ([]Agent, error) {
	return s.store.ListRoomAgents(ctx, roomID)
}

// Rooms returns the Agent's room memberships.
func (s *Service) Rooms(ctx context.Context, agentID string) ([]RoomRef, error) {
	return s.store.ListAgentRooms(ctx, agentID)
}

// JoinRoom adds an Agent to a room and records the membership in the room log.
// The human actor signs the event.
func (s *Service) JoinRoom(ctx context.Context, agentID, roomID string, actor room.Actor) (*Agent, *room.Event, error) {
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, nil, err
	}
	if a.RevokedAt != nil {
		return nil, nil, ErrRevoked
	}
	if a.AccountID != actor.AccountID {
		return nil, nil, ErrNotAgentOwner
	}
	exists, err := s.store.IsAgentInRoom(ctx, agentID, roomID)
	if err != nil {
		return nil, nil, err
	}
	if exists {
		return nil, nil, ErrAlreadyInRoom
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventAgentJoined, room.AgentJoinedPayload{
		AgentID: agentID, Name: a.Name, OwnerID: a.AccountID,
		Runtime: a.Runtime, DisplayName: a.Name,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.store.AddAgentRoom(ctx, agentID, roomID, s.now().UTC()); err != nil {
		return nil, nil, err
	}
	s.ensureSessionAfterJoin(ctx, agentID, roomID, event.Seq)
	return a, event, nil
}

// LeaveRoom removes an Agent from a room and records the change.
func (s *Service) LeaveRoom(ctx context.Context, agentID, roomID string, actor room.Actor) (*room.Event, error) {
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a.AccountID != actor.AccountID {
		return nil, ErrNotAgentOwner
	}
	exists, err := s.store.IsAgentInRoom(ctx, agentID, roomID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotInRoom
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventAgentLeft, room.AgentLeftPayload{AgentID: agentID})
	if err != nil {
		return nil, err
	}
	if err := s.store.RemoveAgentRoom(ctx, agentID, roomID); err != nil {
		return nil, err
	}
	_ = s.DeleteSession(ctx, agentID, roomID)
	return event, nil
}

// SetStatus updates presence, optionally posting a status event in a room.
func (s *Service) SetStatus(ctx context.Context, agentID, status, detail, roomID string, actor room.Actor) (*Agent, *room.Event, error) {
	if !validStatus(status) {
		return nil, nil, errors.New("invalid agent status")
	}
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, nil, err
	}
	if a.RevokedAt != nil {
		return nil, nil, ErrRevoked
	}
	if a.AccountID != actor.AccountID {
		return nil, nil, ErrNotAgentOwner
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	a.Status = status
	a.LastSeenAt = &now
	if err := s.store.UpdateAgent(ctx, a); err != nil {
		return nil, nil, err
	}
	if roomID == "" {
		return a, nil, nil
	}
	exists, err := s.store.IsAgentInRoom(ctx, agentID, roomID)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, ErrNotInRoom
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventAgentStatus, room.AgentStatusPayload{
		AgentID: agentID, Status: status, Detail: detail,
	})
	if err != nil {
		return nil, nil, err
	}
	return a, event, nil
}

// Revoke permanently disables an Agent. It first removes the Agent from every
// room with an agent.left event carrying reason "revoked", so other members
// stop seeing it and the home node closes its ownership projection. If a room
// cannot be reached the call fails without marking the Agent revoked; the
// membership already removed is not retried, and calling Revoke again finishes
// the remaining rooms.
func (s *Service) Revoke(ctx context.Context, agentID string, actor room.Actor) error {
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if a.AccountID != actor.AccountID {
		return ErrNotAgentOwner
	}
	if a.RevokedAt != nil {
		return nil
	}
	refs, err := s.store.ListAgentRooms(ctx, agentID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := s.rooms.Append(ctx, ref.RoomID, actor, room.EventAgentLeft, room.AgentLeftPayload{
			AgentID: agentID, Reason: "revoked",
		}); err != nil {
			return err
		}
		if err := s.store.RemoveAgentRoom(ctx, agentID, ref.RoomID); err != nil {
			return err
		}
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	a.Status = StatusRevoked
	a.RevokedAt = &now
	return s.store.UpdateAgent(ctx, a)
}

func validStatus(status string) bool {
	switch status {
	case StatusIdle, StatusRunning, StatusWaiting, StatusOffline:
		return true
	default:
		return false
	}
}

// DetectMentions returns Agent IDs mentioned as @Name in text. Matching is
// case-insensitive and requires a word boundary after the name.
func DetectMentions(text string, agents []Agent) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, a := range agents {
		needle := "@" + strings.ToLower(a.Name)
		for start := 0; start+len(needle) <= len(lower); {
			index := strings.Index(lower[start:], needle)
			if index < 0 {
				break
			}
			end := start + index + len(needle)
			if end == len(lower) || !isNameChar(lower[end]) {
				out = append(out, a.ID)
				break
			}
			start = end
		}
	}
	return out
}

func isNameChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}
