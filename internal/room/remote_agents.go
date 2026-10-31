package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Home-node limits for Agent events proposed by remote members. They mirror
// the local services; the room package cannot import those packages without a
// cycle, so the small constants are repeated here.
const (
	maxRemoteAgentIDLength  = 64
	maxRemoteAgentName      = 64
	maxRemoteAgentRuntime   = 64
	maxRemoteAgentDetail    = 512
	maxRemoteReceiptID      = 64
	maxRemoteToolName       = 128
	maxRemoteToolOutput     = 64 * 1024
	maxRemoteApprovalID     = 64
	maxRemoteApprovalDigest = 128
	maxRemoteApprovalDesc   = 500
	maxRemoteApprovalString = 512
)

// validateRemoteEvent runs after the event signature has been verified and
// before the home node appends it. It returns the projection change that must
// be committed with agent.joined or agent.left.
func (s *Service) validateRemoteEvent(ctx context.Context, plain *Event) (*RoomAgent, error) {
	switch plain.Type {
	case EventMessagePosted:
		return nil, s.validateRemoteMessage(ctx, plain)
	case EventAgentJoined:
		return s.validateRemoteAgentJoined(ctx, plain)
	case EventAgentLeft:
		return s.validateRemoteAgentLeft(ctx, plain)
	case EventAgentStatus:
		return nil, s.validateRemoteAgentStatus(ctx, plain)
	case EventToolRequested, EventToolResult, EventToolFailed:
		return nil, s.validateRemoteToolEvent(ctx, plain)
	case EventApprovalRequested, EventApprovalGranted, EventApprovalRejected,
		EventApprovalExpired, EventApprovalConsumed:
		return nil, s.validateRemoteApprovalEvent(ctx, plain)
	default:
		return nil, fmt.Errorf("%w: remote event type is not accepted", ErrForbidden)
	}
}

func (s *Service) validateRemoteMessage(ctx context.Context, plain *Event) error {
	var body MessagePostedPayload
	if err := json.Unmarshal(plain.Payload, &body); err != nil {
		return fmt.Errorf("%w: message payload is invalid", ErrInvalidEvent)
	}
	if body.Text == "" && len(body.Attachments) == 0 {
		return fmt.Errorf("%w: message payload is empty", ErrInvalidEvent)
	}
	if len(body.Text) > MaxMessageLength {
		return fmt.Errorf("%w: message is too long", ErrInvalidEvent)
	}
	if body.AgentID == "" {
		return nil
	}
	// An Agent attribution is display metadata, but a member must not borrow
	// another account's Agent identity.
	_, err := s.activeRoomAgent(ctx, plain.RoomID, body.AgentID, plain.AuthorID)
	return err
}

func (s *Service) validateRemoteAgentJoined(ctx context.Context, plain *Event) (*RoomAgent, error) {
	var body AgentJoinedPayload
	if err := json.Unmarshal(plain.Payload, &body); err != nil {
		return nil, fmt.Errorf("%w: agent.joined payload is invalid", ErrInvalidEvent)
	}
	if body.AgentID == "" || len(body.AgentID) > maxRemoteAgentIDLength ||
		body.OwnerID == "" || body.Name == "" || len(body.Name) > maxRemoteAgentName ||
		len(body.DisplayName) > maxRemoteAgentName || len(body.Runtime) > maxRemoteAgentRuntime {
		return nil, fmt.Errorf("%w: agent.joined payload is invalid", ErrInvalidEvent)
	}
	if body.OwnerID != plain.AuthorID {
		return nil, fmt.Errorf("%w: agent owner does not match the event author", ErrForbidden)
	}
	existing, err := s.store.GetRoomAgent(ctx, plain.RoomID, body.AgentID)
	switch {
	case errors.Is(err, ErrNotFound):
		return &RoomAgent{
			RoomID: plain.RoomID, AgentID: body.AgentID, AccountID: body.OwnerID,
			Active: true, JoinedSeq: plain.Seq,
		}, nil
	case err != nil:
		return nil, err
	case existing.AccountID != body.OwnerID:
		return nil, fmt.Errorf("%w: agent belongs to another account", ErrForbidden)
	case existing.Active:
		// A retry after a lost response is idempotent: keep the original
		// join sequence and accept the duplicate declaration.
		return &RoomAgent{
			RoomID: plain.RoomID, AgentID: body.AgentID, AccountID: body.OwnerID,
			Active: true, JoinedSeq: existing.JoinedSeq, LeftSeq: existing.LeftSeq,
		}, nil
	default:
		// Re-joining after agent.left keeps the original owner.
		return &RoomAgent{
			RoomID: plain.RoomID, AgentID: body.AgentID, AccountID: body.OwnerID,
			Active: true, JoinedSeq: plain.Seq,
		}, nil
	}
}

func (s *Service) validateRemoteAgentLeft(ctx context.Context, plain *Event) (*RoomAgent, error) {
	var body AgentLeftPayload
	if err := json.Unmarshal(plain.Payload, &body); err != nil {
		return nil, fmt.Errorf("%w: agent.left payload is invalid", ErrInvalidEvent)
	}
	if body.AgentID == "" || len(body.AgentID) > maxRemoteAgentIDLength {
		return nil, fmt.Errorf("%w: agent.left payload is invalid", ErrInvalidEvent)
	}
	current, err := s.store.GetRoomAgent(ctx, plain.RoomID, body.AgentID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: agent is not joined to this room", ErrForbidden)
		}
		return nil, err
	}
	if current.AccountID != plain.AuthorID {
		return nil, fmt.Errorf("%w: agent does not belong to the event author", ErrForbidden)
	}
	if !current.Active {
		// A retry after a lost response, or a revocation that left the Agent
		// more than once, keeps the original leave sequence.
		return &RoomAgent{
			RoomID: current.RoomID, AgentID: current.AgentID, AccountID: current.AccountID,
			Active: false, JoinedSeq: current.JoinedSeq, LeftSeq: current.LeftSeq,
		}, nil
	}
	return &RoomAgent{
		RoomID: current.RoomID, AgentID: current.AgentID, AccountID: current.AccountID,
		Active: false, JoinedSeq: current.JoinedSeq, LeftSeq: plain.Seq,
	}, nil
}

func (s *Service) validateRemoteAgentStatus(ctx context.Context, plain *Event) error {
	var body AgentStatusPayload
	if err := json.Unmarshal(plain.Payload, &body); err != nil {
		return fmt.Errorf("%w: agent.status payload is invalid", ErrInvalidEvent)
	}
	if !validRemoteAgentStatus(body.Status) || len(body.Detail) > maxRemoteAgentDetail {
		return fmt.Errorf("%w: agent.status payload is invalid", ErrInvalidEvent)
	}
	_, err := s.activeRoomAgent(ctx, plain.RoomID, body.AgentID, plain.AuthorID)
	return err
}

func (s *Service) validateRemoteToolEvent(ctx context.Context, plain *Event) error {
	var body ToolEventPayload
	if err := json.Unmarshal(plain.Payload, &body); err != nil {
		return fmt.Errorf("%w: tool payload is invalid", ErrInvalidEvent)
	}
	if body.ReceiptID == "" || len(body.ReceiptID) > maxRemoteReceiptID ||
		body.Tool == "" || len(body.Tool) > maxRemoteToolName ||
		len(body.ApprovalID) > maxRemoteApprovalID || len(body.Output) > maxRemoteToolOutput {
		return fmt.Errorf("%w: tool payload is invalid", ErrInvalidEvent)
	}
	if len(body.Input) > 0 && !json.Valid(body.Input) {
		return fmt.Errorf("%w: tool input is not JSON", ErrInvalidEvent)
	}
	want := map[string]string{
		EventToolRequested: "requested",
		EventToolResult:    "succeeded",
		EventToolFailed:    "failed",
	}[plain.Type]
	if body.Status != want {
		return fmt.Errorf("%w: tool status does not match the event", ErrInvalidEvent)
	}
	_, err := s.activeRoomAgent(ctx, plain.RoomID, body.AgentID, plain.AuthorID)
	return err
}

func (s *Service) validateRemoteApprovalEvent(ctx context.Context, plain *Event) error {
	var agentID string
	switch plain.Type {
	case EventApprovalRequested:
		var body ApprovalRequestedPayload
		if err := json.Unmarshal(plain.Payload, &body); err != nil {
			return fmt.Errorf("%w: approval payload is invalid", ErrInvalidEvent)
		}
		if body.ApprovalID == "" || len(body.ApprovalID) > maxRemoteApprovalID ||
			body.ActionDigest == "" || len(body.ActionDigest) > maxRemoteApprovalDigest ||
			len(body.Description) > maxRemoteApprovalDesc {
			return fmt.Errorf("%w: approval payload is invalid", ErrInvalidEvent)
		}
		if _, err := time.Parse(time.RFC3339Nano, body.ExpiresAt); err != nil {
			return fmt.Errorf("%w: approval expiry is invalid", ErrInvalidEvent)
		}
		agentID = body.AgentID
	case EventApprovalGranted, EventApprovalRejected, EventApprovalExpired:
		var body ApprovalDecidedPayload
		if err := json.Unmarshal(plain.Payload, &body); err != nil {
			return fmt.Errorf("%w: approval payload is invalid", ErrInvalidEvent)
		}
		want := map[string]string{
			EventApprovalGranted:  "granted",
			EventApprovalRejected: "rejected",
			EventApprovalExpired:  "expired",
		}[plain.Type]
		if body.ApprovalID == "" || len(body.ApprovalID) > maxRemoteApprovalID ||
			body.AgentID == "" || body.Decision != want ||
			len(body.Digest) > maxRemoteApprovalDigest ||
			(body.By != plain.AuthorID && !(plain.Type == EventApprovalExpired && body.By == "system")) {
			return fmt.Errorf("%w: approval decision is invalid", ErrInvalidEvent)
		}
		agentID = body.AgentID
	case EventApprovalConsumed:
		var body ApprovalConsumedPayload
		if err := json.Unmarshal(plain.Payload, &body); err != nil {
			return fmt.Errorf("%w: approval payload is invalid", ErrInvalidEvent)
		}
		if body.ApprovalID == "" || len(body.ApprovalID) > maxRemoteApprovalID ||
			body.AgentID == "" || body.Digest == "" || len(body.Digest) > maxRemoteApprovalDigest {
			return fmt.Errorf("%w: approval payload is invalid", ErrInvalidEvent)
		}
		agentID = body.AgentID
	}
	_, err := s.activeRoomAgent(ctx, plain.RoomID, agentID, plain.AuthorID)
	return err
}

func (s *Service) activeRoomAgent(ctx context.Context, roomID, agentID, accountID string) (*RoomAgent, error) {
	if agentID == "" || len(agentID) > maxRemoteAgentIDLength {
		return nil, fmt.Errorf("%w: agent id is invalid", ErrInvalidEvent)
	}
	record, err := s.store.GetRoomAgent(ctx, roomID, agentID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: agent is not joined to this room", ErrForbidden)
		}
		return nil, err
	}
	if !record.Active || record.AccountID != accountID {
		return nil, fmt.Errorf("%w: agent does not belong to the event author", ErrForbidden)
	}
	return record, nil
}

func validRemoteAgentStatus(status string) bool {
	switch status {
	case "idle", "running", "waiting_approval", "offline":
		return true
	default:
		return false
	}
}

// ListAgentPresence rebuilds the room's active Agent list from its join, leave,
// and status events. Members who joined without history may not hold the keys
// for older events; those entries are skipped rather than failing the view.
func (s *Service) ListAgentPresence(ctx context.Context, roomID string) ([]AgentPresence, error) {
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return nil, err
	}
	events, err := s.store.ListEventsByTypes(ctx, roomID, []string{
		EventAgentJoined, EventAgentLeft, EventAgentStatus,
	})
	if err != nil {
		return nil, err
	}
	presence := make(map[string]AgentPresence)
	for i := range events {
		plain, err := s.openEvent(ctx, &events[i])
		if err != nil {
			continue
		}
		switch plain.Type {
		case EventAgentJoined:
			var body AgentJoinedPayload
			if err := json.Unmarshal(plain.Payload, &body); err != nil || body.AgentID == "" {
				continue
			}
			presence[body.AgentID] = AgentPresence{
				AgentID: body.AgentID, AccountID: body.OwnerID, Name: body.Name,
				Runtime: body.Runtime, Status: "idle", JoinedAt: plain.CreatedAt,
			}
		case EventAgentStatus:
			var body AgentStatusPayload
			if err := json.Unmarshal(plain.Payload, &body); err != nil {
				continue
			}
			record, ok := presence[body.AgentID]
			if !ok {
				continue
			}
			record.Status = body.Status
			presence[body.AgentID] = record
		case EventAgentLeft:
			var body AgentLeftPayload
			if err := json.Unmarshal(plain.Payload, &body); err != nil {
				continue
			}
			delete(presence, body.AgentID)
		}
	}
	out := make([]AgentPresence, 0, len(presence))
	for _, record := range presence {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].JoinedAt.Equal(out[j].JoinedAt) {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].JoinedAt.Before(out[j].JoinedAt)
	})
	return out, nil
}
