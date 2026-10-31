package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
)

type nodeInfoResponse struct {
	Enabled bool       `json:"enabled"`
	Info    *node.Info `json:"info,omitempty"`
}

func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeJSON(w, http.StatusOK, nodeInfoResponse{Enabled: false})
		return
	}
	info, err := s.node.Info(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "node_not_ready", "node identity is not ready")
		return
	}
	writeJSON(w, http.StatusOK, nodeInfoResponse{Enabled: true, Info: info})
}

func (s *Server) handleNodeLookup(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the LAN transport is disabled")
		return
	}
	record, err := s.node.LookupServer(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		message := err.Error()
		switch {
		case strings.Contains(message, "must be 8 digits"):
			writeError(w, http.StatusBadRequest, "invalid_code", message)
		case strings.Contains(message, "not in the directory"):
			writeError(w, http.StatusNotFound, "not_found", message)
		case strings.Contains(message, "no directory is configured"):
			writeError(w, http.StatusServiceUnavailable, "directory_disabled", message)
		default:
			writeError(w, http.StatusBadGateway, "lookup_failed", "the directory lookup failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleNodeSync(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the LAN transport is disabled")
		return
	}
	s.node.SyncAll(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRoomSync(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, "node_disabled", "the LAN transport is disabled")
		return
	}
	imported, err := s.node.SyncRoom(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "sync_failed", "could not reach the home node")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported})
}

func (s *Server) redeemRemoteInvite(w http.ResponseWriter, r *http.Request, raw string) {
	if _, err := node.DecodeInvite(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_invite", "invitation code is invalid")
		return
	}
	joined, err := s.node.Join(r.Context(), raw)
	if err != nil {
		writeNodeJoinError(w, err)
		return
	}
	var member *room.Member
	members, listErr := s.rooms.ListMembers(r.Context(), joined.ID)
	if listErr == nil {
		snapshot, snapErr := s.accounts.Get(r.Context())
		if snapErr == nil {
			for i := range members {
				if members[i].AccountID == snapshot.Account.ID {
					member = &members[i]
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"room": joined, "member": member, "event": nil,
	})
}

func writeNodeJoinError(w http.ResponseWriter, err error) {
	var remote *node.RemoteError
	switch {
	case errors.As(err, &remote):
		switch remote.Status {
		case http.StatusBadRequest, http.StatusUnauthorized:
			writeError(w, http.StatusBadRequest, "invalid_invite", "invitation was rejected by the home node")
		case http.StatusForbidden:
			writeError(w, http.StatusForbidden, "forbidden", "the home node rejected this device")
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "room_not_found", "room or invitation was not found")
		case http.StatusConflict:
			writeError(w, http.StatusConflict, "already_member", "this account is already in the room")
		case http.StatusGone:
			writeError(w, http.StatusGone, "invite_expired", "invitation has expired")
		default:
			writeError(w, http.StatusBadGateway, "home_unreachable", "the home node could not complete the invitation")
		}
	case errors.Is(err, node.ErrInvalidInviteCode), errors.Is(err, node.ErrNotInviteCode):
		writeError(w, http.StatusBadRequest, "invalid_invite", "invitation code is invalid")
	default:
		writeError(w, http.StatusBadGateway, "home_unreachable", "could not reach the home node")
	}
}
