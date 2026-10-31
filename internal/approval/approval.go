// Package approval implements exact, single-use, human-approved actions.
//
// An approval records the canonical action the Agent asked to perform, not a
// model-written description. Humans approve the digest; execution must present
// the same action again to consume the approval.
package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
)

var (
	// ErrNotFound means the approval does not exist.
	ErrNotFound = errors.New("approval not found")
	// ErrNotPending means the approval was already decided.
	ErrNotPending = errors.New("approval is not pending")
	// ErrExpired means the approval window has closed.
	ErrExpired = errors.New("approval has expired")
	// ErrNotGranted means execution was attempted without a grant.
	ErrNotGranted = errors.New("approval is not granted")
	// ErrConsumed means the approval was already used once.
	ErrConsumed = errors.New("approval has already been consumed")
	// ErrDigestMismatch means the action differs from what was approved.
	ErrDigestMismatch = errors.New("action does not match the approved digest")
	// ErrInvalidAction means the action is empty or malformed.
	ErrInvalidAction = errors.New("invalid approval action")
	// ErrNotAgentOwner means the caller does not own the Agent.
	ErrNotAgentOwner = errors.New("caller does not own this agent")
	// ErrAgentRevoked means the Agent cannot request approvals.
	ErrAgentRevoked = errors.New("agent is revoked")
	// ErrNotInRoom means the Agent is not joined to the room.
	ErrNotInRoom = errors.New("agent is not in the room")

	// ErrInvalidDecision means the decision is not granted/rejected.
	ErrInvalidDecision = errors.New("invalid approval decision")
)

const (
	StatusPending  = "pending"
	StatusGranted  = "granted"
	StatusRejected = "rejected"
	StatusExpired  = "expired"

	DecisionGrant   = "granted"
	DecisionReject  = "rejected"
	DefaultTTL      = 15 * time.Minute
	MaxTTL          = 24 * time.Hour
	MaxDescription  = 500
	MaxKindLength   = 64
	MaxToolLength   = 128
	MaxTargetLength = 512
)

// Action is the exact operation being approved. Parameters are canonicalized
// before hashing, so JSON object key order does not matter.
type Action struct {
	Kind       string         `json:"kind"`
	Tool       string         `json:"tool,omitempty"`
	Target     string         `json:"target,omitempty"`
	Recipient  string         `json:"recipient,omitempty"`
	Amount     string         `json:"amount,omitempty"`
	Currency   string         `json:"currency,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Canonical returns the canonical JSON encoding of an action.
func Canonical(a Action) ([]byte, error) {
	normalized, err := normalize(a)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode approval action: %w", err)
	}
	return data, nil
}

// Digest returns the canonical hash of an action.
func Digest(a Action) (string, error) {
	data, err := Canonical(a)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("toget:approval-action:v1\n"))
	h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Summary is a deterministic human-readable description built only from the
// action fields. It never trusts arbitrary model prose.
func Summary(a Action) string {
	parts := []string{"Action " + a.Kind}
	if a.Tool != "" {
		parts = append(parts, "with "+a.Tool)
	}
	if a.Target != "" {
		parts = append(parts, "on "+a.Target)
	}
	if a.Recipient != "" {
		parts = append(parts, "to "+a.Recipient)
	}
	if a.Amount != "" {
		amount := a.Amount
		if a.Currency != "" {
			amount += " " + a.Currency
		}
		parts = append(parts, "amount "+amount)
	}
	if len(a.Parameters) > 0 {
		keys := make([]string, 0, len(a.Parameters))
		for k := range a.Parameters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var pairs []string
		for _, k := range keys {
			v, _ := json.Marshal(a.Parameters[k])
			pairs = append(pairs, k+"="+string(v))
		}
		parts = append(parts, "parameters {"+strings.Join(pairs, ", ")+"}")
	}
	out := strings.Join(parts, " ")
	if len(out) > MaxDescription {
		out = out[:MaxDescription]
	}
	return out
}

func normalize(a Action) (Action, error) {
	a.Kind = strings.TrimSpace(a.Kind)
	a.Tool = strings.TrimSpace(a.Tool)
	a.Target = strings.TrimSpace(a.Target)
	a.Recipient = strings.TrimSpace(a.Recipient)
	a.Amount = strings.TrimSpace(a.Amount)
	a.Currency = strings.TrimSpace(a.Currency)
	if a.Kind == "" || len(a.Kind) > MaxKindLength {
		return Action{}, ErrInvalidAction
	}
	if len(a.Tool) > MaxToolLength || len(a.Target) > MaxTargetLength ||
		len(a.Recipient) > MaxTargetLength || len(a.Amount) > 64 || len(a.Currency) > 16 {
		return Action{}, ErrInvalidAction
	}
	if a.Parameters == nil {
		a.Parameters = nil
	}
	return a, nil
}

// Approval is a pending or decided action request.
type Approval struct {
	ID           string     `json:"id"`
	RoomID       string     `json:"room_id"`
	AgentID      string     `json:"agent_id"`
	RequestedBy  string     `json:"requested_by"`
	Action       Action     `json:"action"`
	ActionDigest string     `json:"action_digest"`
	Description  string     `json:"description"`
	Status       string     `json:"status"`
	ExpiresAt    time.Time  `json:"expires_at"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	ConsumedAt   *time.Time `json:"consumed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Store persists approvals and checks Agent membership.
type Store interface {
	CreateApproval(ctx context.Context, a *Approval) error
	GetApproval(ctx context.Context, id string) (*Approval, error)
	ListApprovals(ctx context.Context, roomID, status string) ([]Approval, error)
	DecideApproval(ctx context.Context, id, status, by string, at time.Time) error
	ExpireApproval(ctx context.Context, id string) error
	ConsumeApproval(ctx context.Context, id string, at time.Time) error
	GetAgent(ctx context.Context, id string) (*agent.Agent, error)
	IsAgentInRoom(ctx context.Context, agentID, roomID string) (bool, error)
}

// Service owns approval rules.
type Service struct {
	store Store
	rooms *room.Service
	now   func() time.Time
}

// New creates an approval service.
func New(store Store, rooms *room.Service) *Service {
	return &Service{store: store, rooms: rooms, now: time.Now}
}

// Request creates a pending approval and writes it to the room log.
func (s *Service) Request(ctx context.Context, roomID, agentID string, actor room.Actor, action Action, ttl time.Duration) (*Approval, *room.Event, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	normalized, err := normalize(action)
	if err != nil {
		return nil, nil, err
	}
	digest, err := Digest(normalized)
	if err != nil {
		return nil, nil, err
	}
	ag, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, nil, err
	}
	if ag.RevokedAt != nil {
		return nil, nil, ErrAgentRevoked
	}
	if ag.AccountID != actor.AccountID {
		return nil, nil, ErrNotAgentOwner
	}
	inRoom, err := s.store.IsAgentInRoom(ctx, agentID, roomID)
	if err != nil {
		return nil, nil, err
	}
	if !inRoom {
		return nil, nil, ErrNotInRoom
	}
	if err := s.rooms.RequireMember(ctx, roomID, actor); err != nil {
		return nil, nil, err
	}

	now := s.now().UTC().Truncate(time.Millisecond)
	a := &Approval{
		ID:           identity.NewID(),
		RoomID:       roomID,
		AgentID:      agentID,
		RequestedBy:  actor.AccountID,
		Action:       normalized,
		ActionDigest: digest,
		Description:  Summary(normalized),
		Status:       StatusPending,
		ExpiresAt:    now.Add(ttl),
		CreatedAt:    now,
	}
	if err := s.store.CreateApproval(ctx, a); err != nil {
		return nil, nil, err
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventApprovalRequested, room.ApprovalRequestedPayload{
		ApprovalID: a.ID, AgentID: a.AgentID, ActionDigest: a.ActionDigest,
		Description: a.Description, ExpiresAt: a.ExpiresAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, nil, err
	}
	return a, event, nil
}

// Get returns one approval. It does not mutate state; use ExpireDue to close
// pending approvals whose window elapsed.
func (s *Service) Get(ctx context.Context, id string) (*Approval, error) {
	return s.store.GetApproval(ctx, id)
}

// List returns approvals in a room, optionally filtered by status.
func (s *Service) List(ctx context.Context, roomID, status string) ([]Approval, error) {
	if status != "" && !validStatus(status) {
		return nil, errors.New("invalid approval status")
	}
	return s.store.ListApprovals(ctx, roomID, status)
}

// Decide grants or rejects a pending approval.
func (s *Service) Decide(ctx context.Context, id string, actor room.Actor, decision string) (*Approval, *room.Event, error) {
	if decision != DecisionGrant && decision != DecisionReject {
		return nil, nil, ErrInvalidDecision
	}
	a, err := s.store.GetApproval(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := s.rooms.RequireMember(ctx, a.RoomID, actor); err != nil {
		return nil, nil, err
	}
	if a.Status != StatusPending {
		return nil, nil, ErrNotPending
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if !now.Before(a.ExpiresAt) {
		if err := s.store.ExpireApproval(ctx, id); err != nil {
			return nil, nil, err
		}
		a.Status = StatusExpired
		if _, err := s.rooms.Append(ctx, a.RoomID, actor, room.EventApprovalExpired, room.ApprovalDecidedPayload{
			ApprovalID: a.ID, AgentID: a.AgentID, Decision: "expired", By: actor.AccountID, Digest: a.ActionDigest,
		}); err != nil {
			return nil, nil, err
		}
		return a, nil, ErrExpired
	}
	if err := s.store.DecideApproval(ctx, id, decision, actor.AccountID, now); err != nil {
		return nil, nil, err
	}
	a.Status = decision
	a.DecidedBy = actor.AccountID
	a.DecidedAt = &now
	eventType := room.EventApprovalGranted
	if decision == DecisionReject {
		eventType = room.EventApprovalRejected
	}
	event, err := s.rooms.Append(ctx, a.RoomID, actor, eventType, room.ApprovalDecidedPayload{
		ApprovalID: a.ID, AgentID: a.AgentID, Decision: decision, By: actor.AccountID, Digest: a.ActionDigest,
	})
	if err != nil {
		return nil, nil, err
	}
	return a, event, nil
}

// Consume validates a granted approval against the exact action and marks it
// used. It must be called immediately before executing the action.
func (s *Service) Consume(ctx context.Context, id, digest string, actor room.Actor) (*Approval, *room.Event, error) {
	a, err := s.store.GetApproval(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := s.rooms.RequireMember(ctx, a.RoomID, actor); err != nil {
		return nil, nil, err
	}
	if a.Status == StatusExpired {
		return nil, nil, ErrExpired
	}
	if a.Status == StatusPending {
		return nil, nil, ErrNotGranted
	}
	if a.Status != StatusGranted {
		return nil, nil, ErrNotGranted
	}
	if a.ConsumedAt != nil {
		return nil, nil, ErrConsumed
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if !now.Before(a.ExpiresAt) {
		if err := s.store.ExpireApproval(ctx, id); err != nil {
			return nil, nil, err
		}
		a.Status = StatusExpired
		return a, nil, ErrExpired
	}
	if digest != a.ActionDigest {
		return nil, nil, ErrDigestMismatch
	}
	if err := s.store.ConsumeApproval(ctx, id, now); err != nil {
		return nil, nil, err
	}
	a.ConsumedAt = &now
	event, err := s.rooms.Append(ctx, a.RoomID, actor, room.EventApprovalConsumed, room.ApprovalConsumedPayload{
		ApprovalID: a.ID, AgentID: a.AgentID, Digest: a.ActionDigest,
	})
	if err != nil {
		return nil, nil, err
	}
	return a, event, nil
}

// ExpireDue closes pending approvals in a room whose window has elapsed and
// writes the corresponding audit events.
func (s *Service) ExpireDue(ctx context.Context, roomID string, actor room.Actor) ([]Approval, error) {
	if err := s.rooms.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	pending, err := s.store.ListApprovals(ctx, roomID, StatusPending)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var expired []Approval
	for i := range pending {
		a := pending[i]
		if now.Before(a.ExpiresAt) {
			continue
		}
		if err := s.store.ExpireApproval(ctx, a.ID); err != nil {
			return expired, err
		}
		if _, err := s.rooms.Append(ctx, a.RoomID, actor, room.EventApprovalExpired, room.ApprovalDecidedPayload{
			ApprovalID: a.ID, AgentID: a.AgentID, Decision: "expired", By: "system", Digest: a.ActionDigest,
		}); err != nil {
			return expired, err
		}
		a.Status = StatusExpired
		expired = append(expired, a)
	}
	return expired, nil
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusGranted, StatusRejected, StatusExpired:
		return true
	default:
		return false
	}
}

// ToolAction builds the canonical action for one tool invocation. The tool
// service and the approval request must use this helper so the digests match.
func ToolAction(tool string, input json.RawMessage) (Action, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return Action{}, fmt.Errorf("tool input must be JSON: %w", err)
	}
	return Action{
		Kind:       "tool.call",
		Tool:       tool,
		Parameters: map[string]any{"input": value},
	}, nil
}
