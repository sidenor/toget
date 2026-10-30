// Package tool records every Agent tool call and enforces approval-before-use
// for operations that can change the world.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
)

var (
	// ErrNotFound means the receipt does not exist.
	ErrNotFound = errors.New("tool receipt not found")
	// ErrApprovalRequired means a risky tool was called without approval.
	ErrApprovalRequired = errors.New("approval required for this tool")
	// ErrNotActive means the receipt was already completed.
	ErrNotActive = errors.New("tool receipt is not active")
	// ErrInvalidTool means the tool name is missing or too long.
	ErrInvalidTool = errors.New("invalid tool name")
	// ErrInvalidKind means the tool class is unknown.
	ErrInvalidKind = errors.New("invalid tool kind")
	// ErrNotInRoom means the Agent is not joined to the room.
	ErrNotInRoom = errors.New("agent is not in the room")
	// ErrAgentRevoked means the Agent cannot call tools.
	ErrAgentRevoked = errors.New("agent is revoked")
	// ErrNotAgentOwner means the caller does not own the Agent.
	ErrNotAgentOwner = errors.New("caller does not own this agent")
)

// Tool classes. Everything except KindRead requires a human approval.
const (
	KindRead     = "read"
	KindWrite    = "write"
	KindExternal = "external"
	KindShell    = "shell"
	KindNative   = "native"

	StatusRequested = "requested"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"

	MaxToolLength   = 128
	MaxOutputLength = 64 * 1024
)

// Receipt is the audit record for one tool invocation.
type Receipt struct {
	ID         string          `json:"id"`
	RoomID     string          `json:"room_id"`
	AgentID    string          `json:"agent_id"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Kind       string          `json:"kind"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	Output     string          `json:"output,omitempty"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Store persists tool receipts and checks Agent membership.
type Store interface {
	CreateToolReceipt(ctx context.Context, r *Receipt) error
	GetToolReceipt(ctx context.Context, id string) (*Receipt, error)
	ListToolReceipts(ctx context.Context, roomID string, limit int) ([]Receipt, error)
	CompleteToolReceipt(ctx context.Context, id, status, output string) error
	GetAgent(ctx context.Context, id string) (*agent.Agent, error)
	IsAgentInRoom(ctx context.Context, agentID, roomID string) (bool, error)
}

// Service owns tool-call rules.
type Service struct {
	store     Store
	rooms     *room.Service
	approvals *approval.Service
	now       func() time.Time
}

// New creates a tool service.
func New(store Store, rooms *room.Service, approvals *approval.Service) *Service {
	return &Service{store: store, rooms: rooms, approvals: approvals, now: time.Now}
}

// Request records a tool call. Risky tool kinds require a granted, unconsumed
// approval over the exact same call.
func (s *Service) Request(ctx context.Context, roomID, agentID string, actor room.Actor, kind, name string, input json.RawMessage, approvalID string) (*Receipt, *room.Event, error) {
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	if !validKind(kind) {
		return nil, nil, ErrInvalidKind
	}
	if name == "" || len(name) > MaxToolLength {
		return nil, nil, ErrInvalidTool
	}
	canonicalInput, value, err := canonicalInput(input)
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

	action := approval.Action{
		Kind:       "tool.call",
		Tool:       name,
		Parameters: map[string]any{"input": value},
	}
	if requiresApproval(kind) && approvalID == "" {
		return nil, nil, ErrApprovalRequired
	}
	if approvalID != "" {
		a, err := s.approvals.Get(ctx, approvalID)
		if err != nil {
			return nil, nil, err
		}
		if a.RoomID != roomID || a.AgentID != agentID {
			return nil, nil, approval.ErrDigestMismatch
		}
		digest, err := approval.Digest(action)
		if err != nil {
			return nil, nil, err
		}
		if _, _, err := s.approvals.Consume(ctx, approvalID, digest, actor); err != nil {
			return nil, nil, err
		}
	}

	now := s.now().UTC().Truncate(time.Millisecond)
	r := &Receipt{
		ID:         identity.NewID(),
		RoomID:     roomID,
		AgentID:    agentID,
		ApprovalID: approvalID,
		Kind:       kind,
		Tool:       name,
		Input:      canonicalInput,
		Status:     StatusRequested,
		CreatedAt:  now,
	}
	if err := s.store.CreateToolReceipt(ctx, r); err != nil {
		return nil, nil, err
	}
	event, err := s.rooms.Append(ctx, roomID, actor, room.EventToolRequested, room.ToolEventPayload{
		ReceiptID: r.ID, AgentID: agentID, Tool: name, Status: StatusRequested,
		Input: canonicalInput, ApprovalID: approvalID,
	})
	if err != nil {
		return nil, nil, err
	}
	return r, event, nil
}

// Complete records the result of a tool call exactly once.
func (s *Service) Complete(ctx context.Context, receiptID string, actor room.Actor, output string, success bool) (*Receipt, *room.Event, error) {
	r, err := s.store.GetToolReceipt(ctx, receiptID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.rooms.RequireMember(ctx, r.RoomID, actor); err != nil {
		return nil, nil, err
	}
	if r.Status != StatusRequested {
		return nil, nil, ErrNotActive
	}
	status := StatusSucceeded
	eventType := room.EventToolResult
	if !success {
		status = StatusFailed
		eventType = room.EventToolFailed
	}
	if len(output) > MaxOutputLength {
		output = output[:MaxOutputLength]
	}
	if err := s.store.CompleteToolReceipt(ctx, receiptID, status, output); err != nil {
		return nil, nil, err
	}
	r.Status = status
	r.Output = output
	event, err := s.rooms.Append(ctx, r.RoomID, actor, eventType, room.ToolEventPayload{
		ReceiptID: r.ID, AgentID: r.AgentID, Tool: r.Tool, Status: status,
		Input: r.Input, Output: output, ApprovalID: r.ApprovalID,
	})
	if err != nil {
		return nil, nil, err
	}
	return r, event, nil
}

// Get returns a receipt.
func (s *Service) Get(ctx context.Context, id string) (*Receipt, error) {
	return s.store.GetToolReceipt(ctx, id)
}

// List returns receipts for a room, newest first.
func (s *Service) List(ctx context.Context, roomID string, limit int) ([]Receipt, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.ListToolReceipts(ctx, roomID, limit)
}

// RequiresApproval reports whether a tool kind is gated.
func RequiresApproval(kind string) bool {
	return requiresApproval(strings.TrimSpace(kind))
}

func requiresApproval(kind string) bool {
	switch kind {
	case KindRead:
		return false
	case KindWrite, KindExternal, KindShell, KindNative:
		return true
	default:
		return true
	}
}

func validKind(kind string) bool {
	switch kind {
	case KindRead, KindWrite, KindExternal, KindShell, KindNative:
		return true
	default:
		return false
	}
}

func canonicalInput(input json.RawMessage) (json.RawMessage, any, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return nil, nil, fmt.Errorf("tool input must be JSON: %w", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode tool input: %w", err)
	}
	return data, value, nil
}
