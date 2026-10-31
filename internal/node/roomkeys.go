package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/account"
)

const maxAccountKeyPropagationBody = 1 << 20

// RotateAccountKey rotates the local account signing key and tells every room
// home that this account's member key moved forward. Failure to reach a room is
// a warning: account sync retries the update every round.
func (s *Service) RotateAccountKey(ctx context.Context) (*account.RotateKeyResult, []string, error) {
	result, err := s.accounts.RotateKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.TriggerAccountSync()
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return result, nil, err
	}
	return result, s.propagateAccountKey(ctx, snapshot), nil
}

// propagateAccountKey advances this account's member key in each room whose
// home still trusts an older generation. It is idempotent and safe to call on
// every account-sync round.
func (s *Service) propagateAccountKey(ctx context.Context, snapshot *account.Snapshot) []string {
	if snapshot == nil || snapshot.Device.RevokedAt != nil || len(snapshot.Account.KeyRotations) == 0 {
		return nil
	}
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		return []string{"could not list rooms to propagate the account key: " + err.Error()}
	}
	warnings := make([]string, 0)
	for i := range rooms {
		record := rooms[i]
		member, memberErr := s.rooms.GetMember(ctx, record.ID, snapshot.Account.ID)
		if memberErr != nil || member.Status != "active" ||
			bytes.Equal(member.AccountPublicKey, snapshot.Account.PublicKey) {
			continue
		}
		if record.HomeServerID == snapshot.Server.ID || record.HomeServerID == snapshot.Account.ServerID {
			if _, err := s.rooms.UpdateMemberAccountKey(ctx, record.ID, snapshot.Account.ID,
				snapshot.Account.KeyRotations); err != nil {
				warnings = append(warnings, fmt.Sprintf("could not update the account key in %s: %v", record.Name, err))
			}
			continue
		}
		client, clientErr := s.clientForRoom(ctx, record.ID)
		if clientErr != nil {
			warnings = append(warnings, fmt.Sprintf("could not update the account key in %s: %v", record.Name, clientErr))
			continue
		}
		_, updateErr := client.UpdateRoomAccountKey(ctx, record.ID, snapshot.Account.KeyRotations)
		client.Close()
		if updateErr != nil {
			warnings = append(warnings, fmt.Sprintf("could not update the account key in %s: %v", record.Name, updateErr))
			continue
		}
		// The home accepted the update; mirror it locally so this round does
		// not retry and local views show the same member key.
		if _, err := s.rooms.UpdateMemberAccountKey(ctx, record.ID, snapshot.Account.ID,
			snapshot.Account.KeyRotations); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not record the account key in %s: %v", record.Name, err))
		}
	}
	return warnings
}

func (s *Service) handleRoomAccountKey(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxAccountKeyPropagationBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req struct {
		RoomID    string   `json:"room_id"`
		Rotations []string `json:"rotations"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.RoomID == "" ||
		len(req.Rotations) == 0 || len(req.Rotations) > 64 {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room_id and rotations are required")
		return
	}
	device, err := s.requestDevice(r, req.RoomID)
	if err != nil || device.RevokedAt != nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderAccount), device.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "account does not match the device")
		return
	}
	if err := VerifyRequest(r, body, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "account key update signature is invalid")
		return
	}
	generation, err := s.rooms.UpdateMemberAccountKey(r.Context(), req.RoomID, device.AccountID, req.Rotations)
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"key_generation": generation})
}
