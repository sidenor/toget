package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/policy"
	"github.com/sidenor/toget/internal/room"
)

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	list, err := s.policies.List(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": list})
}

type putPolicyRequest struct {
	AgentID string        `json:"agent_id"`
	Rules   []policy.Rule `json:"rules"`
}

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req putPolicyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	saved, event, err := s.policies.Save(r.Context(), r.PathValue("id"), strings.TrimSpace(req.AgentID), req.Rules, actor)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": saved, "event": event})
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	event, err := s.policies.Delete(r.Context(), r.PathValue("id"), strings.TrimSpace(r.URL.Query().Get("agent_id")), actor)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": event})
}

type evaluatePolicyRequest struct {
	AgentID string          `json:"agent_id"`
	Action  approval.Action `json:"action"`
	At      string          `json:"at,omitempty"`
}

func (s *Server) handleEvaluatePolicy(w http.ResponseWriter, r *http.Request) {
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
	var req evaluatePolicyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	at := time.Time{}
	if req.At != "" {
		parsed, err := time.Parse(time.RFC3339, req.At)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_time", "at must be RFC3339")
			return
		}
		at = parsed
	}
	decision, err := s.policies.Evaluate(r.Context(), roomID, strings.TrimSpace(req.AgentID), policy.Action{
		Kind: req.Action.Kind, Tool: req.Action.Tool, Target: req.Action.Target,
		Recipient: req.Action.Recipient, Amount: req.Action.Amount, Currency: req.Action.Currency,
	}, at)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

func writePolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, policy.ErrNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found", "policy not found")
	case errors.Is(err, policy.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_policy", err.Error())
	case errors.Is(err, policy.ErrForbidden):
		writeError(w, http.StatusForbidden, "policy_forbidden", "this policy can only be managed by the room owner or the Agent owner")
	case errors.Is(err, policy.ErrAgentNotInRoom):
		writeError(w, http.StatusConflict, "agent_not_in_room", "agent is not in this room")
	case errors.Is(err, room.ErrNotFound):
		writeRoomError(w, err)
	case errors.Is(err, room.ErrNotMember), errors.Is(err, room.ErrForbidden):
		writeRoomError(w, err)
	case errors.Is(err, room.ErrRemoteReadOnly):
		writeRoomError(w, err)
	default:
		writeError(w, http.StatusInternalServerError, "internal", "could not apply the policy")
	}
}
