package api

import (
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
	if _, err := s.ownedAgent(r, r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
	}
	if err := s.agents.Revoke(r.Context(), r.PathValue("id")); err != nil {
		writeAgentError(w, err)
		return
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
	agents, err := s.agents.ListInRoom(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
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
