package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/room"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	agents, err := s.agents.List(r.Context(), snapshot.Account.ID)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if agents == nil {
		agents = []agent.Agent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

type createAgentRequest struct {
	Name        string          `json:"name"`
	Runtime     string          `json:"runtime"`
	Permissions json.RawMessage `json:"permissions"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	var req createAgentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	created, err := s.agents.Register(r.Context(), snapshot.Account.ID, req.Name, req.Runtime, req.Permissions)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) ownedAgent(r *http.Request, id string) (*agent.Agent, error) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		return nil, err
	}
	got, err := s.agents.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if got.AccountID != snapshot.Account.ID {
		return nil, agent.ErrNotFound
	}
	return got, nil
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	got, err := s.ownedAgent(r, r.PathValue("id"))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

type updateAgentRequest struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
	RoomID string `json:"room_id"`
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	if _, err := s.ownedAgent(r, r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
	}
	var req updateAgentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updated, event, err := s.agents.SetStatus(r.Context(), r.PathValue("id"), req.Status, req.Detail, req.RoomID, actor)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": updated, "event": event})
}

func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	if _, err := s.ownedAgent(r, r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
	}
	if err := s.agents.Revoke(r.Context(), r.PathValue("id"), actor); err != nil {
		writeAgentError(w, err)
		return
	}
	// A run that was already active must not keep working after revocation.
	if s.agentRuns != nil {
		s.agentRuns.Stop(r.PathValue("id"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentRooms(w http.ResponseWriter, r *http.Request) {
	if _, err := s.ownedAgent(r, r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
	}
	rooms, err := s.agents.Rooms(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

type agentSessionResponse struct {
	agent.Session
	RoomName string `json:"room_name,omitempty"`
}

func (s *Server) handleListAgentSessions(w http.ResponseWriter, r *http.Request) {
	if _, err := s.ownedAgent(r, r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
	}
	sessions, err := s.agents.Sessions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	out := make([]agentSessionResponse, 0, len(sessions))
	for _, session := range sessions {
		view := agentSessionResponse{Session: session}
		if record, err := s.rooms.GetRoom(r.Context(), session.RoomID); err == nil {
			view.RoomName = record.Name
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleResetAgentSession(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	if _, err := s.ownedAgent(r, r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
	}
	roomID := r.PathValue("roomID")
	agentID := r.PathValue("id")
	if err := s.rooms.RequireMember(r.Context(), roomID, actor); err != nil {
		writeRoomError(w, err)
		return
	}
	// A reset abandons the in-flight answer for this room. Stop the run first;
	// the runner only persists a finishing run if its trigger is still active.
	if session, err := s.agents.Session(r.Context(), agentID, roomID); err == nil &&
		session.Status == agent.StatusRunning && s.agentRuns != nil {
		s.agentRuns.Stop(agentID)
	}
	session, err := s.agents.ResetSession(r.Context(), agentID, roomID)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) {
			writeRoomError(w, err)
			return
		}
		writeAgentError(w, err)
		return
	}
	record, err := s.rooms.GetRoom(r.Context(), roomID)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agentSessionResponse{Session: *session, RoomName: record.Name})
}

func (s *Server) handleStopAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, err := s.ownedAgent(r, agentID); err != nil {
		writeAgentError(w, err)
		return
	}
	if s.agentRuns == nil || !s.agentRuns.Stop(agentID) {
		writeError(w, http.StatusConflict, "agent_not_running", "agent has no active run")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
}

func (s *Server) handleListRoomAgents(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	if err := s.rooms.RequireMember(r.Context(), r.PathValue("id"), actor); err != nil {
		writeRoomError(w, err)
		return
	}
	agents, err := s.listRoomAgents(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleListAgentStreams(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	roomID := r.PathValue("id")
	if err := s.rooms.RequireMember(r.Context(), roomID, actor); err != nil {
		writeRoomError(w, err)
		return
	}
	streams := make([]agent.RunStream, 0)
	if s.agentStreams != nil {
		streams = s.agentStreams.Streams(roomID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"streams": streams})
}

// listRoomAgents returns this node's Agents plus Agents announced in the room
// log by other members. The local registry wins for a matching ID because it
// carries the live record; remote entries come from the event-log projection.
func (s *Server) listRoomAgents(ctx context.Context, roomID string) ([]agent.Agent, error) {
	local, err := s.agents.ListInRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	presence, err := s.rooms.ListAgentPresence(ctx, roomID)
	if err != nil {
		return nil, err
	}
	// ListInRoom omits revoked Agents, so read the full owned list and keep
	// the log projection from bringing an old revoked Agent back into view.
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	owned, err := s.agents.List(ctx, snapshot.Account.ID)
	if err != nil {
		return nil, err
	}
	revoked := make(map[string]bool)
	for _, a := range owned {
		if a.RevokedAt != nil {
			revoked[a.ID] = true
		}
	}
	return mergeRoomAgents(local, presence, revoked), nil
}

// mergeRoomAgents folds event-log presence into the local registry view. The
// local row wins on an ID collision because it holds live status and settings.
func mergeRoomAgents(local []agent.Agent, presence []room.AgentPresence, revoked map[string]bool) []agent.Agent {
	seen := make(map[string]bool, len(local))
	for _, a := range local {
		seen[a.ID] = true
	}
	out := make([]agent.Agent, 0, len(local)+len(presence))
	out = append(out, local...)
	for _, p := range presence {
		if seen[p.AgentID] || revoked[p.AgentID] {
			continue
		}
		status := p.Status
		if status == "" {
			status = "idle"
		}
		out = append(out, agent.Agent{
			ID: p.AgentID, AccountID: p.AccountID, Name: p.Name, Runtime: p.Runtime,
			Status: status, Permissions: json.RawMessage(`{}`), CreatedAt: p.JoinedAt,
		})
	}
	return out
}

type joinAgentRequest struct {
	AgentID string `json:"agent_id"`
}

func (s *Server) handleJoinAgent(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req joinAgentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := s.ownedAgent(r, req.AgentID); err != nil {
		writeAgentError(w, err)
		return
	}
	joined, event, err := s.agents.JoinRoom(r.Context(), req.AgentID, r.PathValue("id"), actor)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": joined, "event": event})
}

func (s *Server) handleLeaveAgent(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	if _, err := s.ownedAgent(r, r.PathValue("agentID")); err != nil {
		writeAgentError(w, err)
		return
	}
	event, err := s.agents.LeaveRoom(r.Context(), r.PathValue("agentID"), r.PathValue("id"), actor)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	roomID := r.PathValue("id")
	if err := s.rooms.RequireMember(r.Context(), roomID, actor); err != nil {
		writeRoomError(w, err)
		return
	}
	if _, err := s.approvals.ExpireDue(r.Context(), roomID, actor); err != nil {
		writeApprovalError(w, err)
		return
	}
	status := r.URL.Query().Get("status")
	list, err := s.approvals.List(r.Context(), roomID, status)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_status", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": list})
}

type requestApprovalRequest struct {
	AgentID    string          `json:"agent_id"`
	Action     approval.Action `json:"action"`
	TTLSeconds int             `json:"ttl_seconds"`
}

func (s *Server) handleRequestApproval(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req requestApprovalRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := s.ownedAgent(r, req.AgentID); err != nil {
		writeAgentError(w, err)
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	created, event, err := s.approvals.Request(r.Context(), r.PathValue("id"), req.AgentID, actor, req.Action, ttl)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeApprovalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"approval": created, "event": event})
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	got, err := s.approvals.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeApprovalError(w, err)
		return
	}
	if err := s.rooms.RequireMember(r.Context(), got.RoomID, actor); err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

type decideApprovalRequest struct {
	Decision string `json:"decision"`
}

func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req decideApprovalRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	decided, event, err := s.approvals.Decide(r.Context(), r.PathValue("id"), actor, req.Decision)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeApprovalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": decided, "event": event})
}

type consumeApprovalRequest struct {
	Digest string `json:"digest"`
}

func (s *Server) handleConsumeApproval(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req consumeApprovalRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	consumed, event, err := s.approvals.Consume(r.Context(), r.PathValue("id"), req.Digest, actor)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeApprovalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": consumed, "event": event})
}

func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	if err := s.rooms.RequireMember(r.Context(), r.PathValue("id"), actor); err != nil {
		writeRoomError(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	receipts, err := s.tools.List(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeToolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipts": receipts})
}

type requestToolRequest struct {
	AgentID    string          `json:"agent_id"`
	Kind       string          `json:"kind"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	ApprovalID string          `json:"approval_id"`
}

func (s *Server) handleRequestTool(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req requestToolRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := s.ownedAgent(r, req.AgentID); err != nil {
		writeAgentError(w, err)
		return
	}
	receipt, event, err := s.tools.Request(r.Context(), r.PathValue("id"), req.AgentID, actor, req.Kind, req.Tool, req.Input, req.ApprovalID)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeToolError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"receipt": receipt, "event": event})
}

func (s *Server) handleGetTool(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	receipt, err := s.tools.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeToolError(w, err)
		return
	}
	if err := s.rooms.RequireMember(r.Context(), receipt.RoomID, actor); err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

type completeToolRequest struct {
	Output  string `json:"output"`
	Success *bool  `json:"success"`
}

func (s *Server) handleCompleteTool(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req completeToolRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	success := true
	if req.Success != nil {
		success = *req.Success
	}
	receipt, event, err := s.tools.Complete(r.Context(), r.PathValue("id"), actor, req.Output, success)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotMember) || errors.Is(err, room.ErrForbidden) {
			writeRoomError(w, err)
			return
		}
		writeToolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": receipt, "event": event})
}

type updateAgentSettingsRequest struct {
	Name        string          `json:"name"`
	Runtime     string          `json:"runtime"`
	Permissions json.RawMessage `json:"permissions"`
}

// handleUpdateAgentSettings edits the user-visible Agent configuration. Status
// and room membership are changed through their own endpoints.
func (s *Server) handleUpdateAgentSettings(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeAccountError(w, err)
		return
	}
	var req updateAgentSettingsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updated, err := s.agents.UpdateSettings(r.Context(), r.PathValue("id"), snapshot.Account.ID, req.Name, req.Runtime, req.Permissions)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": updated})
}
