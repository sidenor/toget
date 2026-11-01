package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
)

type createRoomRequest struct {
	Name  string `json:"name"`
	Topic string `json:"topic"`
	// Home is "node" (default) or "official" for a blind hosted room.
	Home string `json:"home"`
}

type roomEventResponse struct {
	Room  *room.Room  `json:"room"`
	Event *room.Event `json:"event,omitempty"`
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.rooms.ListRooms(r.Context())
	if err != nil {
		writeRoomError(w, err)
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "1"
	out := make([]room.Room, 0, len(rooms))
	for _, item := range rooms {
		if item.ArchivedAt != nil && !includeArchived {
			continue
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req createRoomRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	home := strings.TrimSpace(req.Home)
	if home == "" {
		home = room.HomeKindNode
	}
	if home != room.HomeKindNode && home != room.HomeKindOfficial {
		writeError(w, http.StatusBadRequest, "invalid_home", "home must be node or official")
		return
	}
	if home == room.HomeKindOfficial {
		if s.node == nil {
			writeError(w, http.StatusServiceUnavailable, "node_disabled", "official rooms need the node service")
			return
		}
		created, event, err := s.node.CreateOfficialRoom(r.Context(), req.Name, req.Topic)
		if err != nil {
			writeRoomError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, roomEventResponse{Room: created, Event: event})
		return
	}
	created, event, err := s.rooms.CreateRoom(r.Context(), req.Name, req.Topic, actor)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	if s.node != nil {
		if updated, err := s.node.EnsureRoomHome(r.Context(), created.ID); err == nil {
			created = updated
		}
	}
	writeJSON(w, http.StatusCreated, roomEventResponse{Room: created, Event: event})
}

func (s *Server) handleGetRoom(w http.ResponseWriter, r *http.Request) {
	got, err := s.rooms.GetRoom(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

type updateRoomRequest struct {
	Name  *string `json:"name"`
	Topic *string `json:"topic"`
}

func (s *Server) handleUpdateRoom(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req updateRoomRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	current, err := s.rooms.GetRoom(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	name := current.Name
	topic := current.Topic
	if req.Name != nil {
		name = *req.Name
	}
	if req.Topic != nil {
		topic = *req.Topic
	}
	var updated *room.Room
	var event *room.Event
	if current.HomeKind == room.HomeKindOfficial && s.node != nil {
		updated, event, err = s.node.RenameOfficialRoom(r.Context(), current.ID, actor, name, topic)
	} else {
		updated, event, err = s.rooms.Rename(r.Context(), current.ID, actor, name, topic)
	}
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roomEventResponse{Room: updated, Event: event})
}

func (s *Server) handleArchiveRoom(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var archived *room.Room
	var event *room.Event
	if s.node != nil {
		archived, event, err = s.node.ArchiveRoom(r.Context(), r.PathValue("id"), actor)
	} else {
		archived, event, err = s.rooms.Archive(r.Context(), r.PathValue("id"), actor)
	}
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roomEventResponse{Room: archived, Event: event})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.rooms.ListEvents(r.Context(), r.PathValue("id"), after, limit)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	if want := strings.TrimSpace(r.URL.Query().Get("type")); want != "" {
		filtered := events[:0]
		for _, event := range events {
			if event.Type == want {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

type postMessageRequest struct {
	Text        string   `json:"text"`
	Format      string   `json:"format"`
	Attachments []string `json:"attachments"`
	Mentions    []string `json:"mentions"`
	ReplyTo     string   `json:"reply_to"`
	// AgentID attributes this message to a local Agent owned by the account.
	// The signed author remains the account's device.
	AgentID string `json:"agent_id"`
}

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req postMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = "plain"
	}
	if format != "plain" && format != "markdown" {
		writeError(w, http.StatusBadRequest, "invalid_format", "format must be plain or markdown")
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID != "" {
		owned, err := s.ownedAgent(r, agentID)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		if owned.RevokedAt != nil {
			writeAgentError(w, agent.ErrRevoked)
			return
		}
		inRoom, err := s.agents.IsInRoom(r.Context(), agentID, r.PathValue("id"))
		if err != nil {
			writeAgentError(w, err)
			return
		}
		if !inRoom {
			writeAgentError(w, agent.ErrNotInRoom)
			return
		}
	}
	mentions := req.Mentions
	if roomAgents, err := s.listRoomAgents(r.Context(), r.PathValue("id")); err == nil {
		mentions = mergeMentions(mentions, agent.DetectMentions(req.Text, roomAgents))
	}
	body := room.MessagePostedPayload{
		Text: req.Text, Format: format, Attachments: req.Attachments,
		Mentions: mentions, ReplyTo: req.ReplyTo, AgentID: agentID,
	}
	var event *room.Event
	if s.node != nil {
		event, err = s.node.PostRemoteMessage(r.Context(), r.PathValue("id"), actor, body)
	} else {
		event, err = s.rooms.PostMessage(r.Context(), r.PathValue("id"), actor, body)
	}
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

func mergeMentions(explicit, detected []string) []string {
	seen := make(map[string]bool, len(explicit)+len(detected))
	var out []string
	for _, list := range [][]string{explicit, detected} {
		for _, id := range list {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

type editMessageRequest struct {
	Text string `json:"text"`
}

func (s *Server) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req editMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	event, err := s.rooms.EditMessage(r.Context(), r.PathValue("id"), actor, r.PathValue("eventID"), req.Text)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	members, err := s.rooms.ListMembers(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleListRoomDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.rooms.ListDevices(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

type createInviteRequest struct {
	History    string `json:"history"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req createInviteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	record, err := s.rooms.GetRoom(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	if record.HomeKind == room.HomeKindOfficial {
		if s.node == nil {
			writeError(w, http.StatusServiceUnavailable, "node_disabled", "official rooms need the node service")
			return
		}
		invite, event, code, err := s.node.CreateOfficialInvite(r.Context(), record.ID, actor, req.History, ttl)
		if err != nil {
			writeRoomError(w, err)
			return
		}
		invite.Code = code
		writeJSON(w, http.StatusCreated, map[string]any{"invite": invite, "event": event})
		return
	}
	invite, event, err := s.rooms.CreateInvite(r.Context(), record.ID, actor, req.History, ttl)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	if s.node != nil {
		if code, err := s.node.InviteCode(r.Context(), record.ID, invite.Secret, record.Name); err == nil {
			invite.Code = code
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invite": invite, "event": event})
}

type redeemInviteRequest struct {
	Secret string `json:"secret"`
}

func (s *Server) handleRedeemInvite(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req redeemInviteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	secret := strings.TrimSpace(req.Secret)
	if s.node != nil && node.IsInviteCode(secret) {
		s.redeemRemoteInvite(w, r, secret)
		return
	}
	joinedRoom, member, event, err := s.rooms.RedeemInvite(r.Context(), secret, actor)
	if err != nil {
		if errors.Is(err, room.ErrMemberExists) {
			writeError(w, http.StatusConflict, "already_member", "this account is already in the room")
			return
		}
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": joinedRoom, "member": member, "event": event})
}

func (s *Server) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var event *room.Event
	if s.node != nil {
		event, err = s.node.LeaveRoom(r.Context(), r.PathValue("id"), actor)
	} else {
		event, err = s.rooms.Leave(r.Context(), r.PathValue("id"), actor)
	}
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (s *Server) handleExportAudit(w http.ResponseWriter, r *http.Request) {
	audit, err := s.rooms.ExportAudit(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRoomError(w, err)
		return
	}
	verified := true
	if err := room.VerifyAudit(audit); err != nil {
		verified = false
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": audit, "verified": verified})
}

func (s *Server) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	var audit room.Audit
	if err := decodeJSONLimit(w, r, &audit, 64<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := room.VerifyAudit(&audit); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "audit_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "head": audit.Head, "events": len(audit.Events)})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	s.search(w, r, "")
}

func (s *Server) handleSearchRoom(w http.ResponseWriter, r *http.Request) {
	s.search(w, r, r.PathValue("id"))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, roomID string) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "missing_query", "q is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.rooms.Search(r.Context(), query, limit)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	if roomID != "" {
		filtered := make([]room.SearchHit, 0, len(hits))
		for _, hit := range hits {
			if hit.RoomID == roomID {
				filtered = append(filtered, hit)
			}
		}
		hits = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (s *Server) handleGetReadCursor(w http.ResponseWriter, r *http.Request) {
	_, snapshot, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	seq, err := s.rooms.ReadCursor(r.Context(), r.PathValue("id"), snapshot.Device.ID)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "device_id": snapshot.Device.ID})
}

type readCursorRequest struct {
	Seq int64 `json:"seq"`
}

func (s *Server) handleSetReadCursor(w http.ResponseWriter, r *http.Request) {
	_, snapshot, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	var req readCursorRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.rooms.SetReadCursor(r.Context(), r.PathValue("id"), snapshot.Device.ID, req.Seq); err != nil {
		if errors.Is(err, room.ErrInvalidEvent) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "seq must be between zero and the last event")
			return
		}
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": req.Seq, "device_id": snapshot.Device.ID})
}

func (s *Server) handleUnread(w http.ResponseWriter, r *http.Request) {
	_, snapshot, err := s.actor(r.Context())
	if err != nil {
		writeActorError(w, err)
		return
	}
	count, err := s.rooms.Unread(r.Context(), r.PathValue("id"), snapshot.Device.ID)
	if err != nil {
		writeRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unread": count})
}

func writeActorError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusConflict, "identity_unavailable", "could not unlock the local device identity: "+err.Error())
}
