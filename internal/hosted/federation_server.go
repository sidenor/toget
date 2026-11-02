//go:build !js

package hosted

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/requestauth"
)

const (
	federationLookupLimit = 120
	federationPushLimit   = 120
	federationCancelLimit = 120

	federationAccountTTL = 5 * time.Minute
	// MaxFederationSealBytes bounds the total seal payload one request may
	// carry, leaving room for JSON framing inside MaxBody.
	MaxFederationSealBytes = 6 << 20
)

// federationAuth is one authenticated peer server. The self-certifying
// manifest and the request signature over the same envelope prove that the
// caller holds the root key behind HostID.
type federationAuth struct {
	manifest *identity.Manifest
	hostKey  ed25519.PublicKey
}

func (a *federationAuth) hostID() string   { return a.manifest.ServerID }
func (a *federationAuth) hostCode() string { return a.manifest.ServerCode }

func (s *Service) federationLimited(w http.ResponseWriter, hostID, bucket string, limit int) bool {
	if !s.rateLimited {
		return false
	}
	if s.limiter.allow("federation:"+hostID+":"+bucket, limit, time.Minute, s.now()) {
		return false
	}
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
	return true
}

// authenticateFederation verifies the peer manifest and its signature over
// the exact request body. The envelope is parsed strictly so a future field
// cannot be smuggled past an old host.
func (s *Service) authenticateFederation(w http.ResponseWriter, r *http.Request, body []byte) (*federationAuth, *FederationEnvelope, bool) {
	var envelope FederationEnvelope
	if err := decodeRoomBody(body, &envelope); err != nil ||
		envelope.Protocol != Protocol || len(envelope.Manifest) == 0 ||
		len(envelope.Request) == 0 || len(envelope.Request) > MaxBody {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid federation envelope")
		return nil, nil, false
	}
	manifest, err := identity.VerifyManifestJSON(envelope.Manifest)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "peer manifest is invalid")
		return nil, nil, false
	}
	hostKey, err := identity.DecodePublicKey(manifest.RootPubKey)
	if err != nil || len(hostKey) != ed25519.PublicKeySize {
		writeError(w, http.StatusUnauthorized, "unauthorized", "peer manifest is invalid")
		return nil, nil, false
	}
	nonce, err := base64.RawStdEncoding.DecodeString(manifest.ServerNonce)
	if err != nil || len(nonce) != 16 ||
		manifest.ServerCode != identity.ServerCode(hostKey, nonce) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "peer manifest is invalid")
		return nil, nil, false
	}
	if manifest.ServerID == s.host.Server.ID {
		writeError(w, http.StatusBadRequest, "invalid_request", "a host cannot federate with itself")
		return nil, nil, false
	}
	if !containsProtocol(manifest.ProtocolVersions, Protocol) {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return nil, nil, false
	}
	if err := requestauth.Verify(r, body, hostKey, s.federationNonces, s.now()); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "peer signature is invalid")
		return nil, nil, false
	}
	return &federationAuth{manifest: manifest, hostKey: hostKey}, &envelope, true
}

func containsProtocol(versions []int, want int) bool {
	for _, version := range versions {
		if version == want {
			return true
		}
	}
	return false
}

// handleFederationAccountLookup returns a host-signed account statement to an
// authenticated peer server. It is not public: only servers that prove their
// identity receive device records.
func (s *Service) handleFederationAccountLookup(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "federation_lookup_ip", 240) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	peer, envelope, ok := s.authenticateFederation(w, r, body)
	if !ok {
		return
	}
	if s.federationLimited(w, peer.hostID(), "lookup", federationLookupLimit) {
		return
	}
	var request FederationAccountRequest
	if err := decodeRoomBody(envelope.Request, &request); err != nil ||
		request.Protocol != Protocol || !digits(request.AccountID, 14) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid account lookup")
		return
	}
	if request.AccountID[:8] != s.host.Server.Code {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	acct, err := s.store.GetAccount(r.Context(), request.AccountID)
	if errors.Is(err, ErrAccountNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	devices, err := s.activeAccountDevices(r.Context(), acct.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	federated := make([]FederatedDevice, 0, len(devices))
	for _, device := range devices {
		federated = append(federated, FederatedDevice{
			ID: device.ID, SigningPublicKey: device.SigningPublicKey,
			EncryptionPublicKey: device.EncryptionPublicKey,
			Certificate:         device.Certificate,
		})
	}
	now := s.now().UTC().Truncate(time.Second)
	record, err := SignFederatedAccount(FederatedAccount{
		Protocol: Protocol, AccountID: acct.ID, Name: acct.Name,
		HostID: s.host.Server.ID, HostCode: s.host.Server.Code,
		HostURL: s.publicHostURL(), PublicKey: acct.PublicKey,
		KeyGeneration: acct.KeyGeneration, KeyRotations: acct.KeyRotations,
		Devices: federated, IssuedAt: now, ExpiresAt: now.Add(federationAccountTTL),
	}, s.host.Server.PrivateKey)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

// handleFederationFriendRequest stores a sealed invite delivered by the
// sender's account host. The recipient host validates the seals against its
// own active devices and never opens them.
func (s *Service) handleFederationFriendRequest(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "federation_push_ip", 240) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	peer, envelope, ok := s.authenticateFederation(w, r, body)
	if !ok {
		return
	}
	if s.federationLimited(w, peer.hostID(), "push", federationPushLimit) {
		return
	}
	var request FederatedFriendRequest
	if err := decodeRoomBody(envelope.Request, &request); err != nil ||
		request.Protocol != Protocol || !validFriendRequestID(request.ID) ||
		!digits(request.FromAccountID, 14) || !digits(request.ToAccountID, 14) ||
		!validRoomID(request.RoomID) || request.FromAccountID == request.ToAccountID ||
		request.FromAccountID[:8] != peer.hostCode() ||
		request.FromHostID != peer.hostID() ||
		len(request.FromName) > MaxNameLength ||
		!validFederationHostURL(request.FromHostURL) ||
		!validFederationHostURL(request.RoomHostURL) ||
		request.RoomHostID == "" ||
		len(request.Seals) == 0 || len(request.Seals) > MaxFriendRequestSeals {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid federated friend request")
		return
	}
	if request.ToAccountID[:8] != s.host.Server.Code {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if request.CreatedAt.IsZero() || request.CreatedAt.After(now.Add(5*time.Minute)) ||
		!request.ExpiresAt.After(now) ||
		request.ExpiresAt.After(now.Add(FriendRequestTTL+5*time.Minute)) ||
		request.ExpiresAt.Before(request.CreatedAt) {
		writeError(w, http.StatusBadRequest, "invalid_request", "friend request timing is invalid")
		return
	}
	if err := s.store.DeleteExpiredFriendRequests(r.Context(), now); err != nil {
		s.internal(w, r, err)
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
	pending, err := s.store.CountFriendRequestsTo(r.Context(), target.ID, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if pending >= MaxFriendRequestsPerAccount {
		writeError(w, http.StatusTooManyRequests, "too_many_requests", "too many pending friend requests")
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
	total := 0
	for _, seal := range request.Seals {
		if !active[seal.DeviceID] || !validFriendRequestSeal(seal.Sealed) {
			writeError(w, http.StatusBadRequest, "invalid_seal", "friend request seal is invalid")
			return
		}
		if _, duplicate := sealed[seal.DeviceID]; duplicate {
			writeError(w, http.StatusBadRequest, "invalid_seal", "friend request seals a device twice")
			return
		}
		total += len(seal.Sealed)
		if total > MaxFederationSealBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "friend request is too large")
			return
		}
		sealed[seal.DeviceID] = seal.Sealed
	}
	if err := s.store.SaveFriendRequest(r.Context(), FriendRequestRecord{
		ID: request.ID, FromAccountID: request.FromAccountID, FromName: request.FromName,
		FromHostID: request.FromHostID, FromHostURL: request.FromHostURL,
		ToAccountID: target.ID, ToName: target.Name,
		ToHostID: s.host.Server.ID, ToHostURL: s.publicHostURL(),
		RoomID: request.RoomID, Sealed: sealed,
		CreatedAt: request.CreatedAt.UTC().Truncate(time.Millisecond),
		ExpiresAt: request.ExpiresAt.UTC().Truncate(time.Millisecond),
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, FederatedFriendRequestAck{
		Protocol: Protocol, ID: request.ID, ExpiresAt: request.ExpiresAt,
	})
}

// handleFederationFriendRequestCancel withdraws a request the sender's host
// previously delivered. It is scoped to both parties so one host cannot delete
// another host's inbox items.
func (s *Service) handleFederationFriendRequestCancel(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "federation_cancel_ip", 240) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	peer, envelope, ok := s.authenticateFederation(w, r, body)
	if !ok {
		return
	}
	if s.federationLimited(w, peer.hostID(), "cancel", federationCancelLimit) {
		return
	}
	var request FederatedFriendRequestCancel
	if err := decodeRoomBody(envelope.Request, &request); err != nil ||
		request.Protocol != Protocol || !validFriendRequestID(request.ID) ||
		!digits(request.FromAccountID, 14) || !digits(request.ToAccountID, 14) ||
		request.FromAccountID[:8] != peer.hostCode() ||
		request.ToAccountID[:8] != s.host.Server.Code {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid federated friend request")
		return
	}
	deleted, err := s.store.DeleteFriendRequestFromSender(r.Context(),
		request.ToAccountID, request.FromAccountID, request.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, FederatedFriendRequestCancelResponse{
		Protocol: Protocol, Deleted: deleted,
	})
}

// handleFederationFriendRequestDecline removes an outgoing request after the
// recipient declined it. Only the recipient's host can report a decline, and
// only for a request that names one of its own accounts.
func (s *Service) handleFederationFriendRequestDecline(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "federation_decline_ip", 240) {
		return
	}
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	peer, envelope, ok := s.authenticateFederation(w, r, body)
	if !ok {
		return
	}
	if s.federationLimited(w, peer.hostID(), "decline", federationCancelLimit) {
		return
	}
	var request FederatedFriendRequestDecline
	if err := decodeRoomBody(envelope.Request, &request); err != nil ||
		request.Protocol != Protocol || !validFriendRequestID(request.ID) ||
		!digits(request.FromAccountID, 14) || !digits(request.ToAccountID, 14) ||
		request.FromAccountID[:8] != s.host.Server.Code ||
		request.ToAccountID[:8] != peer.hostCode() {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid federated decline")
		return
	}
	deleted, err := s.store.DeleteFriendRequestFromSender(r.Context(),
		request.ToAccountID, request.FromAccountID, request.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, FederatedFriendRequestCancelResponse{
		Protocol: Protocol, Deleted: deleted,
	})
}

func validFederationHostURL(raw string) bool {
	if raw == "" {
		return true
	}
	_, err := normalizeFederationHostURL(raw)
	return err == nil
}

// federationCaller identifies the local host when it calls a peer.
type federationCaller struct {
	manifest json.RawMessage
	key      ed25519.PrivateKey
}

func (s *Service) federationCaller() federationCaller {
	return federationCaller{
		manifest: append(json.RawMessage(nil), s.host.ManifestJSON...),
		key:      ed25519.PrivateKey(append([]byte(nil), s.host.Server.PrivateKey...)),
	}
}
