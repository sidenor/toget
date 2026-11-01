//go:build !js

package hosted

import (
	"net/http"
)

func (s *Service) handleRoomList(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomListRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid room list request")
		return
	}
	memberships, err := s.store.ListHostedRoomsForAccount(r.Context(), auth.account.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	entries := make([]RoomListEntry, 0, len(memberships))
	for i := range memberships {
		entry := &memberships[i]
		info, err := s.roomInfoWithHead(r.Context(), &entry.Room)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		entries = append(entries, RoomListEntry{
			Room: info, Role: entry.Role, Status: entry.Status,
			History: entry.History, JoinedAt: entry.JoinedAt,
		})
	}
	writeJSON(w, http.StatusOK, RoomListResponse{Protocol: Protocol, Rooms: entries})
}
