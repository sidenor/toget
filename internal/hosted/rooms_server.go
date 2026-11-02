//go:build !js

// The account host's storage and HTTP service are native-only. The reset
// page compiles this package for GOOS=js and only needs the client.
package hosted

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/requestauth"
	"github.com/sidenor/toget/internal/room"
)

type roomAuth struct {
	account    *AccountRecord
	record     *account.DeviceRecord
	cert       *identity.DeviceCertificate
	publicKey  ed25519.PublicKey
	encryption []byte
}

// roomBody reads a bounded JSON body. It is kept separate from decodeJSON
// because room requests are signed over the exact bytes.
func roomBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return nil, false
	}
	return body, true
}

func decodeRoomBody(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

// authenticateRoomDevice verifies an account device certificate and the
// device-key signature over the exact request body. It does not itself prove
// room membership; handlers check that against the room tables.
func (s *Service) authenticateRoomDevice(w http.ResponseWriter, r *http.Request, body []byte) (*roomAuth, bool) {
	accountID := strings.TrimSpace(r.Header.Get(requestauth.HeaderAccount))
	deviceID := strings.TrimSpace(r.Header.Get(requestauth.HeaderDevice))
	if !digits(accountID, 14) || deviceID == "" || len(deviceID) > 128 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	if accountID[:8] != s.host.Server.Code {
		return s.authenticateRemoteRoomDevice(w, r, body, accountID, deviceID)
	}
	acct, err := s.store.GetAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	record, err := s.store.GetDevice(r.Context(), accountID, deviceID)
	if err != nil || record.RevokedAt != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	keys, err := accountKeys(acct)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	var cert *identity.DeviceCertificate
	for _, key := range keys {
		candidate, verifyErr := identity.VerifyDeviceCertificateSignature([]byte(record.Certificate), key)
		if verifyErr == nil && candidate.AccountID == accountID && candidate.DeviceID == deviceID {
			cert = candidate
			break
		}
	}
	if cert == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	publicKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || !bytes.Equal(publicKey, record.PublicKey) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	encryptionKey, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	if err := requestauth.Verify(r, body, publicKey, s.deviceNonces, s.now()); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device signature is invalid")
		return nil, false
	}
	return &roomAuth{account: acct, record: record, cert: cert, publicKey: publicKey, encryption: encryptionKey}, true
}

// authenticateRemoteRoomDevice accepts a device whose account belongs to
// another host. The account-signed certificate in the request header binds the
// device to the account; a previously joined room device row is trusted for
// key continuity so routine room traffic keeps working if the device's home
// host is briefly unreachable.
func (s *Service) authenticateRemoteRoomDevice(w http.ResponseWriter, r *http.Request, body []byte, accountID, deviceID string) (*roomAuth, bool) {
	unauthorized := func() (*roomAuth, bool) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return nil, false
	}
	certificate, err := decodeDeviceCertificateHeader(r.Header.Get(HeaderDeviceCertificate))
	if err != nil {
		return unauthorized()
	}
	cert, err := identity.ParseDeviceCertificate(certificate)
	if err != nil || cert.AccountID != accountID || cert.DeviceID != deviceID {
		return unauthorized()
	}
	publicKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return unauthorized()
	}
	encryptionKey, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
	if err != nil || len(encryptionKey) != 32 {
		return unauthorized()
	}
	now := s.now().UTC()

	// The cached statement and an already-joined room device are both local
	// checks. A fresh network lookup is the last resort, so routine traffic
	// does not depend on the device's home host being online.
	var acct *AccountRecord
	var record *account.DeviceRecord
	matchDevice := func(candidate *remoteAccountResult) bool {
		if candidate == nil || candidate.record == nil {
			return false
		}
		devices, err := federatedAccountDevices(candidate.record, now)
		if err != nil {
			return false
		}
		for _, device := range devices {
			if device.ID != deviceID || !bytes.Equal(device.SigningPublicKey, publicKey) ||
				!bytes.Equal(device.EncryptionPublicKey, encryptionKey) ||
				!bytes.Equal(device.Certificate, certificate) {
				continue
			}
			acct = remoteAccountRecord(candidate.record)
			record = &account.DeviceRecord{
				ID: deviceID, AccountID: accountID,
				PublicKey: publicKey, EncryptionPublicKey: encryptionKey,
				Certificate: string(certificate),
			}
			return true
		}
		return false
	}
	if cached, err := s.cachedRemoteAccount(r.Context(), accountID, now); err == nil {
		matchDevice(cached)
	}
	if record == nil {
		// Membership is a durable grant, so a device that already joined a
		// room here stays trusted even without its account host.
		known, err := s.store.GetHostedRoomDeviceByAccountDevice(r.Context(), accountID, deviceID)
		if err == nil && known.RevokedAt == nil &&
			bytes.Equal(known.SigningPublicKey, publicKey) &&
			bytes.Equal(known.EncryptionPublicKey, encryptionKey) {
			acct = &AccountRecord{ID: accountID}
			record = &account.DeviceRecord{
				ID: deviceID, AccountID: accountID,
				PublicKey: publicKey, EncryptionPublicKey: encryptionKey,
				Certificate: string(certificate),
			}
		}
	}
	if record == nil {
		// The cache may predate this device, so force one fresh statement.
		if fresh, err := s.lookupRemoteAccount(r.Context(), accountID, false); err == nil {
			matchDevice(fresh)
		}
	}
	if record == nil {
		return unauthorized()
	}
	if err := requestauth.Verify(r, body, publicKey, s.deviceNonces, s.now()); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device signature is invalid")
		return nil, false
	}
	return &roomAuth{
		account: acct, record: record, cert: cert,
		publicKey: publicKey, encryption: encryptionKey,
	}, true
}

// decodeDeviceCertificateHeader reads the base64url certificate header with a
// strict size cap.
func decodeDeviceCertificateHeader(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64<<10 {
		return nil, errors.New("device certificate header is missing or too large")
	}
	certificate, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(certificate) == 0 || len(certificate) > 48<<10 {
		return nil, errors.New("device certificate header is invalid")
	}
	return certificate, nil
}

func roomRefMatches(ref RoomDeviceRef, auth *roomAuth) bool {
	return ref.AccountID == auth.account.ID && ref.DeviceID == auth.record.ID &&
		bytes.Equal(ref.SigningPublicKey, []byte(auth.publicKey)) &&
		bytes.Equal(ref.EncryptionPublicKey, auth.encryption) &&
		len(ref.Certificate) > 0 && bytes.Equal(ref.Certificate, []byte(auth.record.Certificate))
}

func (s *Service) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomCreateRequest
	if err := decodeRoomBody(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	if auth.account.ID[:8] != s.host.Server.Code {
		writeError(w, http.StatusForbidden, "forbidden", "a room is created on its owner's account host")
		return
	}
	if !roomRefMatches(request.Device, auth) {
		writeError(w, http.StatusForbidden, "forbidden", "device identity does not match the signed request")
		return
	}
	if !validRoomID(request.RoomID) {
		writeError(w, http.StatusBadRequest, "invalid_room", "invalid room id")
		return
	}
	event := request.Event.ToEvent()
	if event.RoomID != request.RoomID || event.Seq != 1 || event.Epoch != 1 ||
		event.Type != room.EventRoomCreated || event.AuthorID != auth.account.ID ||
		event.AuthorDevice != auth.record.ID || event.PrevID != "" || event.PrevHash != "" ||
		event.EnvelopePrevHash != "" {
		writeError(w, http.StatusBadRequest, "invalid_event", "room creation event is invalid")
		return
	}
	if err := validateSealedEvent(&event, auth.publicKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", err.Error())
		return
	}
	if len(request.Keys) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_keys", "the creator needs one wrapped epoch key")
		return
	}
	for i := range request.Keys {
		pkg := &request.Keys[i]
		if pkg.Epoch != 1 || pkg.DeviceID != auth.record.ID || len(pkg.Wrapped) == 0 ||
			len(pkg.Wrapped) > MaxRoomWrappedKey {
			writeError(w, http.StatusBadRequest, "invalid_keys", "the creator key package is invalid")
			return
		}
	}
	if existing, err := s.store.GetHostedRoom(r.Context(), request.RoomID); err == nil {
		storedEvent, eventErr := s.store.GetHostedRoomEvent(r.Context(), request.RoomID, event.ID)
		if existing.OwnerAccountID == auth.account.ID && eventErr == nil &&
			storedEvent.Hash == event.Hash && storedEvent.EnvelopeHash == event.EnvelopeHash {
			writeJSON(w, http.StatusOK, RoomCreateResponse{Protocol: Protocol, Room: roomInfo(existing)})
			return
		}
		writeError(w, http.StatusConflict, "already_member", "room already exists")
		return
	} else if !errors.Is(err, ErrHostedRoomNotFound) {
		s.roomStoreError(w, r, err)
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	created := HostedRoomCreation{
		Room: HostedRoom{
			ID: request.RoomID, OwnerAccountID: auth.account.ID, Epoch: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		Member: HostedRoomMember{
			RoomID: request.RoomID, AccountID: auth.account.ID, Role: "owner",
			Status: "active", History: room.HistoryAll, JoinedAt: now,
		},
		Device: HostedRoomDevice{
			RoomID: request.RoomID, DeviceID: auth.record.ID, AccountID: auth.account.ID,
			DisplayName: auth.account.Name, SigningPublicKey: append([]byte(nil), auth.publicKey...),
			EncryptionPublicKey: append([]byte(nil), auth.encryption...),
			History:             room.HistoryAll, FromSeq: 1, JoinedAt: now,
		},
		Event: event,
		Keys:  request.Keys,
	}
	stored, err := s.store.CreateHostedRoom(r.Context(), created)
	if err != nil {
		// A concurrent retry may have committed between the existence check
		// and the insert. Replay it when it is the same signed creation.
		if errors.Is(err, ErrHostedRoomExists) {
			if existing, getErr := s.store.GetHostedRoom(r.Context(), request.RoomID); getErr == nil &&
				existing.OwnerAccountID == auth.account.ID {
				if storedEvent, eventErr := s.store.GetHostedRoomEvent(r.Context(), request.RoomID, event.ID); eventErr == nil &&
					storedEvent.Hash == event.Hash && storedEvent.EnvelopeHash == event.EnvelopeHash {
					writeJSON(w, http.StatusOK, RoomCreateResponse{Protocol: Protocol, Room: roomInfo(existing)})
					return
				}
			}
		}
		s.roomStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomCreateResponse{Protocol: Protocol, Room: roomInfo(stored)})
}

func (s *Service) handleRoomSync(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomSyncRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol || !validRoomID(request.RoomID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sync request")
		return
	}
	if len(request.NeedKeys) > MaxRoomEvents {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many requested epochs")
		return
	}
	roomInfoValue, member, device, err := s.store.HostedRoomAccess(r.Context(), request.RoomID, auth.account.ID, auth.record.ID)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	after := request.After
	if after < device.FromSeq-1 {
		after = device.FromSeq - 1
	}
	if after > roomInfoValue.Head {
		after = roomInfoValue.Head
	}
	limit := request.Limit
	if limit <= 0 || limit > MaxRoomEvents {
		limit = MaxRoomEvents
	}
	events, hasMore, err := s.store.ListHostedRoomEvents(r.Context(), request.RoomID, after, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	keys, err := s.store.HostedRoomKeysForDevice(r.Context(), request.RoomID, auth.record.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	requests, err := s.store.HostedRoomKeyRequests(r.Context(), request.RoomID, auth.record.ID, request.NeedKeys)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	members, err := s.store.ListHostedRoomMembers(r.Context(), request.RoomID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	devices, err := s.store.ListHostedRoomDevices(r.Context(), request.RoomID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	_ = member
	info, err := s.roomInfoWithHead(r.Context(), roomInfoValue)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomSyncResponse{
		Protocol: Protocol, Room: info,
		Members: roomMemberInfos(members, devices), Devices: roomDeviceInfos(devices),
		Keys: keys, Events: room.WireEvents(events), HasMore: hasMore,
		KeyRequests: requests, RotationRequired: roomInfoValue.RotationRequired,
	})
}

func (s *Service) handleRoomEvent(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomEventRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid event request")
		return
	}
	event := request.Event.ToEvent()
	if !validRoomID(event.RoomID) {
		writeError(w, http.StatusBadRequest, "invalid_room", "invalid room id")
		return
	}
	hostedRoom, member, _, err := s.store.HostedRoomAccess(r.Context(), event.RoomID, auth.account.ID, auth.record.ID)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	if hostedRoom.RotationRequired {
		writeError(w, http.StatusConflict, "rotation_required", "rotate the room epoch before posting")
		return
	}
	if event.AuthorID != auth.account.ID || event.AuthorDevice != auth.record.ID {
		writeError(w, http.StatusForbidden, "forbidden", "event author does not match the device")
		return
	}
	if event.Epoch != hostedRoom.Epoch {
		writeError(w, http.StatusConflict, "epoch_conflict", "event epoch is stale")
		return
	}
	if !roomEventAllowed(event.Type) {
		writeError(w, http.StatusForbidden, "forbidden", "event type is not accepted by the room home")
		return
	}
	if event.Type == room.EventRoomSettingsChanged && member.Role != "owner" {
		writeError(w, http.StatusForbidden, "forbidden", "only the room owner can change settings")
		return
	}
	if err := validateSealedEvent(&event, auth.publicKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", err.Error())
		return
	}
	updated, err := s.store.AppendHostedRoomEvent(r.Context(), event.RoomID, event, false)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomEventResponse{
		Protocol: Protocol, Head: updated.Head, HeadEnvelopeHash: updated.HeadEnvelopeHash,
	})
}

func (s *Service) handleRoomInvite(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomInviteRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol ||
		!validRoomID(request.RoomID) || request.InviteID == "" || len(request.InviteID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid invite request")
		return
	}
	hostedRoom, _, _, err := s.store.HostedRoomAccess(r.Context(), request.RoomID, auth.account.ID, auth.record.ID)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	if hostedRoom.RotationRequired {
		writeError(w, http.StatusConflict, "rotation_required", "rotate the room epoch before inviting")
		return
	}
	if len(request.SecretHash) != sha256.Size {
		writeError(w, http.StatusBadRequest, "invalid_invite", "invalid invite secret hash")
		return
	}
	switch request.History {
	case room.HistoryNone, room.HistoryRecent, room.HistoryAll:
	default:
		writeError(w, http.StatusBadRequest, "invalid_invite", "invalid history choice")
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.Add(RoomInviteTTL)) {
		writeError(w, http.StatusBadRequest, "invalid_invite", "invalid invite expiry")
		return
	}
	event := request.Event.ToEvent()
	if event.RoomID != request.RoomID || event.Type != room.EventMemberInvited ||
		event.AuthorID != auth.account.ID || event.AuthorDevice != auth.record.ID ||
		event.Epoch != hostedRoom.Epoch {
		writeError(w, http.StatusBadRequest, "invalid_event", "invite event is invalid")
		return
	}
	if err := validateSealedEvent(&event, auth.publicKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", err.Error())
		return
	}
	if len(request.Keys) > MaxRoomInviteKeys {
		writeError(w, http.StatusBadRequest, "invalid_keys", "too many invite keys")
		return
	}
	hasCurrent := false
	seenEpochs := make(map[int]bool, len(request.Keys))
	for i := range request.Keys {
		pkg := &request.Keys[i]
		if pkg.Epoch < 1 || pkg.Epoch > hostedRoom.Epoch || len(pkg.Wrapped) == 0 ||
			len(pkg.Wrapped) > MaxRoomWrappedKey || seenEpochs[pkg.Epoch] {
			writeError(w, http.StatusBadRequest, "invalid_keys", "invite key package is invalid")
			return
		}
		seenEpochs[pkg.Epoch] = true
		if pkg.Epoch == hostedRoom.Epoch {
			hasCurrent = true
		}
	}
	if !hasCurrent {
		writeError(w, http.StatusBadRequest, "invalid_keys", "the current epoch key is required")
		return
	}
	invite := HostedRoomInvite{
		RoomID: request.RoomID, InviteID: request.InviteID, SecretHash: request.SecretHash,
		History: request.History, CreatedBy: auth.account.ID, CreatedAt: now,
		ExpiresAt: request.ExpiresAt.UTC(),
	}
	updated, err := s.store.CreateHostedRoomInvite(r.Context(), invite, event, request.Keys)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	_ = updated
	writeJSON(w, http.StatusOK, RoomInviteResponse{
		Protocol: Protocol, InviteID: request.InviteID, ExpiresAt: invite.ExpiresAt,
	})
}

func (s *Service) handleRoomJoin(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomJoinRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol ||
		!validRoomID(request.RoomID) || len(request.SecretHash) != sha256.Size {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid join request")
		return
	}
	if !roomRefMatches(request.Device, auth) {
		writeError(w, http.StatusForbidden, "forbidden", "device identity does not match the signed request")
		return
	}
	invite, err := s.store.InviteBySecretHash(r.Context(), request.RoomID, request.SecretHash)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	hostedRoom, member, device, keys, events, hasMore, err := s.store.RedeemHostedRoomInvite(r.Context(), invite,
		HostedRoomJoin{
			RoomID: request.RoomID, AccountID: auth.account.ID, DeviceID: auth.record.ID,
			DisplayName: auth.account.Name, SigningPublicKey: append([]byte(nil), auth.publicKey...),
			EncryptionPublicKey: append([]byte(nil), auth.encryption...),
			History:             invite.History, Now: now,
		}, s.roomMemberLimit, s.roomDeviceLimit)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	// Joining the room answers any pending friend request addressed to this
	// account, whether it came from a local account or a federated host.
	if _, err := s.store.DeleteFriendRequestForMember(r.Context(), member.AccountID, request.RoomID); err != nil {
		s.logger.Warn("clear answered friend request failed",
			"room_id", request.RoomID, "account_id", member.AccountID, "error", err)
	}
	members, err := s.store.ListHostedRoomMembers(r.Context(), request.RoomID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	devices, err := s.store.ListHostedRoomDevices(r.Context(), request.RoomID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	_ = device
	joinedAt := now
	if member != nil && !member.JoinedAt.IsZero() {
		joinedAt = member.JoinedAt
	}
	info, err := s.roomInfoWithHead(r.Context(), hostedRoom)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomJoinResponse{
		Protocol: Protocol, Room: info,
		Members: roomMemberInfos(members, devices), Devices: roomDeviceInfos(devices),
		InviteKeys: keys, Events: room.WireEvents(events), History: invite.History,
		InviteID: invite.InviteID, JoinedAt: joinedAt, HasMore: hasMore,
	})
}

func (s *Service) handleRoomKeys(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomKeyUploadRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol ||
		!validRoomID(request.RoomID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid key upload")
		return
	}
	if len(request.Packages) > MaxRoomPackages {
		writeError(w, http.StatusBadRequest, "too_many_keys", "too many key packages")
		return
	}
	if _, _, _, err := s.store.HostedRoomAccess(r.Context(), request.RoomID, auth.account.ID, auth.record.ID); err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	for i := range request.Packages {
		p := &request.Packages[i]
		if p.Epoch < 1 || p.DeviceID == "" || len(p.DeviceID) > 128 ||
			len(p.Wrapped) == 0 || len(p.Wrapped) > MaxRoomWrappedKey {
			writeError(w, http.StatusBadRequest, "invalid_keys", "invalid key package")
			return
		}
	}
	stored, skipped, err := s.store.SaveHostedRoomKeys(r.Context(), request.RoomID, auth.record.ID, request.Packages)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomKeyUploadResponse{Protocol: Protocol, Stored: stored, Skipped: skipped})
}

func (s *Service) handleRoomLeave(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomLeaveRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid leave request")
		return
	}
	event := request.Event.ToEvent()
	if !validRoomID(event.RoomID) || event.Type != room.EventMemberLeft ||
		event.AuthorID != auth.account.ID || event.AuthorDevice != auth.record.ID {
		writeError(w, http.StatusBadRequest, "invalid_event", "leave event is invalid")
		return
	}
	if err := validateSealedEvent(&event, auth.publicKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", err.Error())
		return
	}
	// A retry may arrive after the host committed the leave but the response
	// was lost. Replay the stored result before checking active membership or
	// the current epoch, both of which have already moved on.
	stored, lookupErr := s.store.GetHostedRoomEvent(r.Context(), event.RoomID, event.ID)
	switch {
	case lookupErr == nil:
		if stored.Type != event.Type || stored.Hash != event.Hash ||
			stored.EnvelopeHash != event.EnvelopeHash ||
			stored.AuthorID != auth.account.ID || stored.AuthorDevice != auth.record.ID {
			writeError(w, http.StatusConflict, "event_conflict", "event id is already used")
			return
		}
		// Replay the head this leave produced, not the room's current head.
		// A departed device no longer needs to learn how the room moved on.
		writeJSON(w, http.StatusOK, RoomEventResponse{
			Protocol: Protocol, Head: stored.Seq, HeadEnvelopeHash: stored.EnvelopeHash,
		})
		return
	case !errors.Is(lookupErr, ErrHostedRoomConflict):
		s.roomStoreError(w, r, lookupErr)
		return
	}
	hostedRoom, err := s.store.GetHostedRoom(r.Context(), event.RoomID)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	if event.Epoch != hostedRoom.Epoch {
		writeError(w, http.StatusConflict, "epoch_conflict", "event epoch is stale")
		return
	}
	updated, rotationRequired, err := s.store.LeaveHostedRoom(r.Context(), event.RoomID, auth.account.ID,
		auth.record.ID, event, s.now().UTC())
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomEventResponse{
		Protocol: Protocol, Head: updated.Head, HeadEnvelopeHash: updated.HeadEnvelopeHash,
		RotationRequired: rotationRequired,
	})
}

func (s *Service) handleRoomRotate(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomRotateRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol ||
		request.Epoch < 2 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid rotation request")
		return
	}
	event := request.Event.ToEvent()
	if !validRoomID(event.RoomID) || event.Type != room.EventEpochRotated ||
		event.Epoch != request.Epoch || event.AuthorID != auth.account.ID ||
		event.AuthorDevice != auth.record.ID {
		writeError(w, http.StatusBadRequest, "invalid_event", "rotation event is invalid")
		return
	}
	if _, _, _, err := s.store.HostedRoomAccess(r.Context(), event.RoomID, auth.account.ID, auth.record.ID); err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	if err := validateSealedEvent(&event, auth.publicKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", err.Error())
		return
	}
	if len(request.Keys) > MaxRoomPackages {
		writeError(w, http.StatusBadRequest, "too_many_keys", "too many key packages")
		return
	}
	seenDevices := make(map[string]bool, len(request.Keys))
	for i := range request.Keys {
		p := &request.Keys[i]
		if p.Epoch != request.Epoch || p.DeviceID == "" || len(p.DeviceID) > 128 ||
			len(p.Wrapped) == 0 || len(p.Wrapped) > MaxRoomWrappedKey || seenDevices[p.DeviceID] {
			writeError(w, http.StatusBadRequest, "invalid_keys", "rotation key package is invalid")
			return
		}
		seenDevices[p.DeviceID] = true
	}
	updated, err := s.store.RotateHostedRoomEpoch(r.Context(), event.RoomID, HostedRoomRotation{
		Epoch: request.Epoch, Event: event, Keys: request.Keys, Now: s.now().UTC(),
	})
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomCreateResponse{Protocol: Protocol, Room: roomInfo(updated)})
}

func (s *Service) handleRoomDevices(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request RoomDeviceRegisterRequest
	if err := decodeRoomBody(body, &request); err != nil || request.Protocol != Protocol ||
		!validRoomID(request.RoomID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid device request")
		return
	}
	if request.Device.AccountID != auth.account.ID || request.Device.DeviceID == "" ||
		len(request.Device.DeviceID) > 128 || len(request.Device.SigningPublicKey) != ed25519.PublicKeySize ||
		len(request.Device.EncryptionPublicKey) != 32 || len(request.Device.Certificate) == 0 ||
		len(request.Device.DisplayName) > MaxNameLength {
		writeError(w, http.StatusBadRequest, "invalid_device", "invalid room device")
		return
	}
	hostedRoom, err := s.store.GetHostedRoom(r.Context(), request.RoomID)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	member, err := s.store.GetHostedRoomMember(r.Context(), request.RoomID, auth.account.ID)
	if err != nil || member.Status != "active" {
		writeError(w, http.StatusForbidden, "forbidden", "room access is not allowed")
		return
	}
	if hostedRoom.RotationRequired {
		writeError(w, http.StatusConflict, "rotation_required", "rotate the room epoch before adding a device")
		return
	}
	var signingKey ed25519.PublicKey
	var encryptionKey []byte
	selfRegistration := request.Device.DeviceID == auth.record.ID
	if selfRegistration {
		// A newly signed-in account device may add itself once its account has
		// an active membership. The signed account-level device certificate is
		// the authorization; it is not trusted merely because it is new.
		if !roomRefMatches(request.Device, auth) {
			writeError(w, http.StatusForbidden, "forbidden", "device identity does not match the signed request")
			return
		}
		signingKey = auth.publicKey
		encryptionKey = auth.encryption
	} else {
		if _, _, _, err := s.store.HostedRoomAccess(r.Context(), request.RoomID, auth.account.ID, auth.record.ID); err != nil {
			s.roomStoreError(w, r, err)
			return
		}
		if len(auth.account.PublicKey) != ed25519.PublicKeySize {
			writeError(w, http.StatusConflict, "account_unavailable",
				"the account key is not cached; try again later")
			return
		}
		keys, err := accountKeys(auth.account)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		var cert *identity.DeviceCertificate
		for _, key := range keys {
			candidate, verifyErr := identity.VerifyDeviceCertificate(request.Device.Certificate, key)
			if verifyErr == nil && candidate.AccountID == auth.account.ID &&
				candidate.DeviceID == request.Device.DeviceID {
				cert = candidate
				break
			}
		}
		if cert == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "device certificate is invalid")
			return
		}
		signingKey, err = identity.DecodePublicKey(cert.DevicePubKey)
		if err != nil || !bytes.Equal(signingKey, request.Device.SigningPublicKey) {
			writeError(w, http.StatusBadRequest, "invalid_device", "device signing key does not match its certificate")
			return
		}
		encryptionKey, err = identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
		if err != nil || !bytes.Equal(encryptionKey, request.Device.EncryptionPublicKey) {
			writeError(w, http.StatusBadRequest, "invalid_device", "device encryption key does not match its certificate")
			return
		}
	}
	existing, err := s.store.GetHostedRoomDevice(r.Context(), request.RoomID, request.Device.DeviceID)
	if err == nil {
		if existing.RevokedAt != nil || existing.AccountID != auth.account.ID ||
			!bytes.Equal(existing.SigningPublicKey, request.Device.SigningPublicKey) ||
			!bytes.Equal(existing.EncryptionPublicKey, request.Device.EncryptionPublicKey) {
			writeError(w, http.StatusForbidden, "forbidden", "device is not eligible to join this room")
			return
		}
		writeJSON(w, http.StatusOK, RoomDeviceRegisterResponse{Protocol: Protocol, Room: roomInfo(hostedRoom)})
		return
	}
	_ = member
	now := s.now().UTC().Truncate(time.Millisecond)
	updated, err := s.store.RegisterHostedRoomDevice(r.Context(), request.RoomID, HostedRoomDevice{
		RoomID: request.RoomID, DeviceID: request.Device.DeviceID, AccountID: auth.account.ID,
		DisplayName: auth.account.Name, SigningPublicKey: append([]byte(nil), signingKey...),
		EncryptionPublicKey: append([]byte(nil), encryptionKey...), JoinedAt: now,
	}, s.roomDeviceLimit)
	if err != nil {
		s.roomStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RoomDeviceRegisterResponse{Protocol: Protocol, Room: roomInfo(updated)})
}

// validateSealedEvent checks all fields the host can verify without a room
// key: the envelope chain hash and device signature, sealed payload, plaintext
// chain fields, and size limits.
func validateSealedEvent(event *room.Event, publicKey ed25519.PublicKey) error {
	if event.ID == "" || len(event.ID) > 128 || event.Hash == "" || event.Signature == "" ||
		event.CreatedAt.IsZero() {
		return errors.New("event is incomplete")
	}
	if !room.IsSealedPayload(event.Payload) {
		return errors.New("event payload is not sealed")
	}
	if len(event.Payload) > MaxRoomEventSize {
		return errors.New("event payload is too large")
	}
	if event.EnvelopeHash == "" || event.EnvelopeSignature == "" {
		return errors.New("event envelope is missing")
	}
	if err := room.VerifyEnvelope(event, publicKey); err != nil {
		return errors.New("event envelope signature is invalid")
	}
	// The host cannot recompute the plaintext hash, but it can still verify
	// that the author signed the hash committed to by the envelope.
	if err := room.VerifyEventSignature(event, publicKey); err != nil {
		return errors.New("event author signature is invalid")
	}
	return nil
}

func roomEventAllowed(eventType string) bool {
	switch eventType {
	case room.EventMessagePosted, room.EventMessageEdited,
		room.EventAgentJoined, room.EventAgentLeft, room.EventAgentStatus,
		room.EventToolRequested, room.EventToolResult, room.EventToolFailed,
		room.EventApprovalRequested, room.EventApprovalGranted,
		room.EventApprovalRejected, room.EventApprovalExpired, room.EventApprovalConsumed,
		room.EventRoomSettingsChanged:
		return true
	default:
		return false
	}
}

func validRoomID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "\r\n\x00")
}

func (s *Service) roomStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrHostedRoomNotFound), errors.Is(err, ErrHostedRoomForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "room access is not allowed")
	case errors.Is(err, ErrHostedRoomDeviceRevoked):
		writeError(w, http.StatusForbidden, "device_revoked", "room device is revoked")
	case errors.Is(err, ErrHostedRoomConflict):
		writeError(w, http.StatusConflict, "conflict", "room head has moved; sync and retry")
	case errors.Is(err, ErrHostedRoomRotationNeeded):
		writeError(w, http.StatusConflict, "rotation_required", "rotate the room epoch first")
	case errors.Is(err, ErrHostedRoomInviteInvalid):
		writeError(w, http.StatusBadRequest, "invite_invalid", "invite is invalid")
	case errors.Is(err, ErrHostedRoomInviteUsed):
		writeError(w, http.StatusConflict, "invite_used", "invite has been used")
	case errors.Is(err, ErrHostedRoomInviteExpired):
		writeError(w, http.StatusGone, "invite_expired", "invite has expired")
	case errors.Is(err, ErrHostedRoomExists):
		writeError(w, http.StatusConflict, "already_member", "room or membership already exists")
	case errors.Is(err, ErrHostedRoomKeyCoverage):
		writeError(w, http.StatusBadRequest, "key_coverage", "new epoch keys do not cover every active device")
	case errors.Is(err, ErrHostedRoomMemberLimit):
		writeError(w, http.StatusConflict, "room_full", "the room has reached its member limit")
	case errors.Is(err, ErrHostedRoomDeviceLimit):
		writeError(w, http.StatusConflict, "device_limit", "the room has reached its device limit")
	default:
		s.internal(w, r, err)
	}
}

func roomInfo(r *HostedRoom) RoomInfo {
	return RoomInfo{
		ID: r.ID, OwnerAccountID: r.OwnerAccountID, Epoch: r.Epoch, Head: r.Head,
		HeadEnvelopeHash: r.HeadEnvelopeHash, RotationRequired: r.RotationRequired,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// roomInfoWithHead adds the plaintext chain anchor. It is only needed on room
// reads: a client that joined without history cannot derive the head event id
// or plaintext hash from events it is not allowed to see.
func (s *Service) roomInfoWithHead(ctx context.Context, r *HostedRoom) (RoomInfo, error) {
	info := roomInfo(r)
	head, err := s.store.HostedRoomHeadEvent(ctx, r.ID)
	if err != nil {
		return RoomInfo{}, err
	}
	if head != nil {
		info.HeadEventID = head.ID
		info.HeadHash = head.Hash
	}
	return info, nil
}

func roomMemberInfos(members []HostedRoomMember, devices []HostedRoomDevice) []RoomMemberInfo {
	names := make(map[string]string, len(devices))
	for i := range devices {
		d := &devices[i]
		if d.DisplayName == "" {
			continue
		}
		if _, exists := names[d.AccountID]; !exists || d.RevokedAt == nil {
			names[d.AccountID] = d.DisplayName
		}
	}
	out := make([]RoomMemberInfo, 0, len(members))
	for i := range members {
		m := &members[i]
		var encryption []byte
		for j := range devices {
			if devices[j].AccountID == m.AccountID && devices[j].RevokedAt == nil {
				encryption = append([]byte(nil), devices[j].EncryptionPublicKey...)
				break
			}
		}
		out = append(out, RoomMemberInfo{
			AccountID: m.AccountID, DisplayName: names[m.AccountID], Role: m.Role, Status: m.Status,
			History: m.History, EncryptionPublicKey: encryption,
			JoinedAt: m.JoinedAt, LeftAt: m.LeftAt,
		})
	}
	return out
}

func roomDeviceInfos(devices []HostedRoomDevice) []RoomDeviceInfo {
	out := make([]RoomDeviceInfo, 0, len(devices))
	for i := range devices {
		d := &devices[i]
		out = append(out, RoomDeviceInfo{
			DeviceID: d.DeviceID, AccountID: d.AccountID,
			SigningPublicKey:    append([]byte(nil), d.SigningPublicKey...),
			EncryptionPublicKey: append([]byte(nil), d.EncryptionPublicKey...),
			History:             d.History, JoinedAt: d.JoinedAt, RevokedAt: d.RevokedAt,
		})
	}
	return out
}
