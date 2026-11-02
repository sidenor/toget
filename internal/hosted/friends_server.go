//go:build !js

package hosted

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

const (
	friendRequestLimit = 30
	friendListLimit    = 120
	friendDeleteLimit  = 60
	friendSentLimit    = 120
	friendCancelLimit  = 60
)

type activeAccountDevice struct {
	ID                  string
	EncryptionPublicKey []byte
}

// activeAccountDevices returns the target account's unexpired, unrevoked
// devices with certificates signed by an account key. Invalid or stale
// records are skipped rather than breaking an otherwise usable account.
func (s *Service) activeAccountDevices(ctx context.Context, accountID string) ([]activeAccountDevice, error) {
	acct, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	keys, err := accountKeys(acct)
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListDevices(ctx, accountID)
	if err != nil {
		return nil, err
	}
	devices := make([]activeAccountDevice, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if seen[record.ID] || record.ID == "" || record.AccountID != accountID ||
			record.RevokedAt != nil {
			continue
		}
		var cert *identity.DeviceCertificate
		for _, key := range keys {
			candidate, verifyErr := identity.VerifyDeviceCertificate(
				[]byte(record.Certificate), key)
			if verifyErr == nil && candidate.AccountID == accountID &&
				candidate.DeviceID == record.ID {
				cert = candidate
				break
			}
		}
		if cert == nil {
			continue
		}
		signing, err := identity.DecodePublicKey(cert.DevicePubKey)
		if err != nil || !bytes.Equal(signing, record.PublicKey) {
			continue
		}
		encryption, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
		if err != nil || len(encryption) != 32 ||
			!bytes.Equal(encryption, record.EncryptionPublicKey) {
			continue
		}
		seen[record.ID] = true
		devices = append(devices, activeAccountDevice{
			ID: record.ID, EncryptionPublicKey: append([]byte(nil), encryption...),
		})
	}
	return devices, nil
}

func validFriendRequestID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

func validFriendRequestSeal(sealed string) bool {
	const prefix = "toget-sealed:v1:"
	return strings.HasPrefix(sealed, prefix) && len(sealed) > len(prefix) &&
		len(sealed) <= MaxFriendRequestSealLength
}

// handleFriendRequestCreate stores a sealed invite for another local account.
// The host cannot open the seals; it only checks that each one targets an
// active device belonging to the recipient.
func (s *Service) handleFriendRequestCreate(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "friend_request", friendRequestLimit) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request FriendRequestCreate
	if err := decodeRoomBody(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid friend request")
		return
	}
	if request.Protocol != Protocol || !validFriendRequestID(request.ID) ||
		!digits(request.ToAccountID, 14) || !validRoomID(request.RoomID) ||
		len(request.Seals) == 0 || len(request.Seals) > MaxFriendRequestSeals {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid friend request")
		return
	}
	if request.ToAccountID == auth.account.ID {
		writeError(w, http.StatusBadRequest, "friend_request_self", "cannot add yourself")
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if err := s.store.DeleteExpiredFriendRequests(r.Context(), now); err != nil {
		s.internal(w, r, err)
		return
	}
	pending, err := s.store.CountFriendRequestsFrom(r.Context(), auth.account.ID, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if pending >= MaxFriendRequestsPerAccount {
		writeError(w, http.StatusTooManyRequests, "too_many_requests", "too many pending friend requests")
		return
	}
	roomInfoValue, err := s.store.GetHostedRoom(r.Context(), request.RoomID)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	if roomInfoValue.OwnerAccountID != auth.account.ID {
		writeError(w, http.StatusForbidden, "forbidden", "only the room owner can send this request")
		return
	}
	member, err := s.store.GetHostedRoomMember(r.Context(), request.RoomID, auth.account.ID)
	if err != nil || member.Status != "active" {
		writeError(w, http.StatusForbidden, "forbidden", "room access is not allowed")
		return
	}
	target, err := s.store.GetAccountRef(r.Context(), request.ToAccountID)
	if errors.Is(err, ErrAccountNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if existing, err := s.store.GetHostedRoomMember(r.Context(), request.RoomID, target.ID); err == nil &&
		existing.Status == "active" {
		writeError(w, http.StatusConflict, "already_member", "this account is already in the room")
		return
	} else if err != nil && !errors.Is(err, ErrHostedRoomForbidden) {
		s.internal(w, r, err)
		return
	}
	devices, err := s.activeAccountDevices(r.Context(), target.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(devices) == 0 {
		writeError(w, http.StatusConflict, "target_unavailable", "the account has no active devices")
		return
	}
	active := make(map[string]bool, len(devices))
	for _, device := range devices {
		active[device.ID] = true
	}
	sealed := make(map[string]string, len(request.Seals))
	for _, seal := range request.Seals {
		if !active[seal.DeviceID] || !validFriendRequestSeal(seal.Sealed) {
			writeError(w, http.StatusBadRequest, "invalid_seal", "friend request seal is invalid")
			return
		}
		if _, duplicate := sealed[seal.DeviceID]; duplicate {
			writeError(w, http.StatusBadRequest, "invalid_seal", "friend request seals a device twice")
			return
		}
		sealed[seal.DeviceID] = seal.Sealed
	}
	expires := now.Add(FriendRequestTTL)
	if err := s.store.SaveFriendRequest(r.Context(), FriendRequestRecord{
		ID: request.ID, FromAccountID: auth.account.ID, ToAccountID: target.ID,
		RoomID: request.RoomID, Sealed: sealed, CreatedAt: now, ExpiresAt: expires,
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, FriendRequestCreated{
		Protocol: Protocol, ID: request.ID, ExpiresAt: expires,
	})
}

// handleFriendRequestList returns only seals addressed to the current device.
func (s *Service) handleFriendRequestList(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "friend_list", friendListLimit) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request struct {
		Protocol int `json:"protocol"`
	}
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid friend request list")
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	rows, err := s.store.ListFriendRequestsForDevice(
		r.Context(), auth.account.ID, auth.record.ID, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	requests := make([]FriendRequestSummary, 0, len(rows))
	for _, row := range rows {
		requests = append(requests, FriendRequestSummary{
			ID: row.ID, FromAccountID: row.FromAccountID, FromName: row.FromName,
			RoomID: row.RoomID, Sealed: row.Sealed[auth.record.ID],
			CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, FriendRequestList{Protocol: Protocol, Requests: requests})
}

// handleFriendRequestSent lists outgoing requests for the signed-in account.
// It never returns seals: the sender already has the invite it created.
func (s *Service) handleFriendRequestSent(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "friend_sent", friendSentLimit) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request struct {
		Protocol int `json:"protocol"`
	}
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid friend request list")
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	rows, err := s.store.ListFriendRequestsFrom(r.Context(), auth.account.ID, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	requests := make([]SentFriendRequestSummary, 0, len(rows))
	for _, row := range rows {
		requests = append(requests, SentFriendRequestSummary{
			ID: row.ID, ToAccountID: row.ToAccountID, ToName: row.ToName,
			RoomID: row.RoomID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, SentFriendRequestList{
		Protocol: Protocol, Requests: requests,
	})
}

// handleFriendRequestCancel withdraws an outgoing request. A request the
// recipient already accepted or declined is simply reported as not deleted.
func (s *Service) handleFriendRequestCancel(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "friend_cancel", friendCancelLimit) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request FriendRequestDelete
	if err := decodeRoomBody(body, &request); err != nil ||
		request.Protocol != Protocol || !validFriendRequestID(request.ID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid friend request")
		return
	}
	deleted, err := s.store.CancelFriendRequest(r.Context(), auth.account.ID, request.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, FriendRequestDeleteResponse{
		Protocol: Protocol, Deleted: deleted,
	})
}

// handleFriendRequestDelete lets the recipient decline a request from any of
// their devices, even one that cannot open the seal.
func (s *Service) handleFriendRequestDelete(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "friend_delete", friendDeleteLimit) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request FriendRequestDelete
	if err := decodeRoomBody(body, &request); err != nil ||
		request.Protocol != Protocol || !validFriendRequestID(request.ID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid friend request")
		return
	}
	deleted, err := s.store.DeleteFriendRequest(r.Context(), auth.account.ID, request.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, FriendRequestDeleteResponse{
		Protocol: Protocol, Deleted: deleted,
	})
}
