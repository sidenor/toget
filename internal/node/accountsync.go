package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/vault"
)

const (
	maxAccountSyncBody         = 2 << 20
	defaultAccountSyncEvery    = 30 * time.Second
	maxAccountPeersPerSync     = 8
	maxAccountSyncConcurrency  = 4
	accountPeerSyncTimeout     = 15 * time.Second
	accountSyncRoundTimeout    = 20 * time.Second
	accountRevocationTimeout   = 25 * time.Second
	accountSyncTriggerDebounce = 500 * time.Millisecond
)

// AccountSyncRequest exchanges the complete public device set of one account.
// The sender must include its own account-signed device record so a peer that
// has never seen it can verify both the record and the request signature.
type AccountSyncRequest struct {
	AccountID string                 `json:"account_id"`
	DeviceID  string                 `json:"device_id"`
	Records   []account.DeviceRecord `json:"records"`
	// KeyGeneration and KeyRotations carry the sender's account-key history.
	// KeyEnvelopes holds the current account private key sealed to the
	// recipient device (usually the peer being called).
	KeyGeneration int               `json:"key_generation,omitempty"`
	KeyRotations  []string          `json:"key_rotations,omitempty"`
	KeyEnvelopes  map[string]string `json:"key_envelopes,omitempty"`
}

// AccountSyncResponse returns the responder's device set. ReadOnly is set when
// the sender is revoked; a revoked device may still learn about the account so
// it discovers its own revocation, but cannot change the responder's records.
type AccountSyncResponse struct {
	AccountID     string                 `json:"account_id"`
	DeviceID      string                 `json:"device_id"`
	Records       []account.DeviceRecord `json:"records"`
	KeyGeneration int                    `json:"key_generation,omitempty"`
	KeyRotations  []string               `json:"key_rotations,omitempty"`
	KeyEnvelopes  map[string]string      `json:"key_envelopes,omitempty"`
	ReadOnly      bool                   `json:"read_only,omitempty"`
	Warnings      []string               `json:"warnings,omitempty"`
}

// AccountSyncResult summarizes one account-sync round.
type AccountSyncResult struct {
	Peers      int      `json:"peers"`
	Synced     int      `json:"synced"`
	Host       bool     `json:"host,omitempty"`
	HostSynced bool     `json:"host_synced,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Revoked    []string `json:"revoked,omitempty"`
}

type accountPeerResult struct {
	synced   bool
	warnings []string
	revoked  []string
}

// TriggerAccountSync asks the background loop to sync soon. It never blocks;
// repeated calls collapse into one pending wake-up.
func (s *Service) TriggerAccountSync() {
	if s == nil || s.accountSyncTrigger == nil {
		return
	}
	select {
	case s.accountSyncTrigger <- struct{}{}:
	default:
	}
}

func (s *Service) accountSyncLoop(ctx context.Context) {
	interval := s.accountSyncEvery
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := s.SyncAccount(ctx); err != nil && !errors.Is(err, account.ErrNotFound) {
				s.logger.Debug("account sync failed", "error", err)
			}
			timer.Reset(interval)
		case <-s.accountSyncTrigger:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(accountSyncTriggerDebounce)
		}
	}
}

// SyncAccount exchanges device records with reachable sibling devices. Every
// side sends its full device set; additions, renewed certificates, addresses,
// and revocations merge idempotently. Revocations are then propagated to room
// homes so their epoch keys rotate even if the revoking device was offline.
func (s *Service) SyncAccount(ctx context.Context) (*AccountSyncResult, error) {
	return s.syncAccount(ctx, false)
}

// SyncAccountNow is SyncAccount with an immediate official-host exchange,
// bypassing the background host-sync interval.
func (s *Service) SyncAccountNow(ctx context.Context) (*AccountSyncResult, error) {
	return s.syncAccount(ctx, true)
}

func (s *Service) syncAccount(ctx context.Context, forceHost bool) (*AccountSyncResult, error) {
	if s == nil {
		return nil, errors.New("node transport is disabled")
	}
	s.accountSyncMu.Lock()
	defer s.accountSyncMu.Unlock()

	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	selfURL := s.Address(ctx)
	if selfURL != "" && snapshot.Device.RevokedAt == nil {
		if err := s.accounts.SetDeviceNodeURL(ctx, snapshot.Device.ID, selfURL); err != nil {
			s.logger.Warn("account sync: record local node address", "error", err)
		}
	}
	records, err := s.accounts.ExportDeviceRecords(ctx, selfURL)
	if err != nil {
		return nil, err
	}
	hostWarnings, hostRevoked, hostAttempted, hostSynced := s.syncHostedAccount(ctx, snapshot, records, forceHost)
	if hostAttempted {
		// Host merges may add devices, renew certificates, or revoke this
		// device. Refresh before choosing peer devices.
		if fresh, getErr := s.accounts.Get(ctx); getErr == nil {
			snapshot = fresh
		}
		if refreshed, exportErr := s.accounts.ExportDeviceRecords(ctx, selfURL); exportErr == nil {
			records = refreshed
		} else {
			hostWarnings = append(hostWarnings, "could not refresh account records after host sync: "+exportErr.Error())
		}
	}
	devices, err := s.accounts.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	peers := make([]account.Device, 0, len(devices))
	for i := range devices {
		device := devices[i]
		if device.Local || device.RevokedAt != nil || strings.TrimSpace(device.NodeURL) == "" {
			continue
		}
		peers = append(peers, device)
	}
	if len(peers) == 0 {
		// Devices paired before node addresses were stored have no sibling to
		// call. The account host is the one device whose server code is known
		// from the account ID, so resolve it through a saved card, mDNS, or the
		// directory and use it as the bootstrap peer. Its response teaches both
		// sides each other's current addresses.
		if host, ok := s.bootstrapAccountPeer(ctx, snapshot); ok {
			peers = append(peers, *host)
		}
	}
	result := &AccountSyncResult{
		Peers: len(peers), Host: hostAttempted, HostSynced: hostSynced,
		Warnings: hostWarnings,
	}
	hostRevokedSet := make(map[string]bool, len(hostRevoked))
	for _, id := range hostRevoked {
		hostRevokedSet[id] = true
	}
	chosen := s.nextAccountPeers(peers)
	if len(chosen) > 0 {
		deviceKey, err := s.accounts.UnsealDeviceKey(snapshot.Device)
		if err != nil {
			return nil, err
		}
		roundCtx, cancel := context.WithTimeout(ctx, accountSyncRoundTimeout)
		results := make([]accountPeerResult, len(chosen))
		sem := make(chan struct{}, maxAccountSyncConcurrency)
		var wg sync.WaitGroup
		for i := range chosen {
			wg.Add(1)
			sem <- struct{}{}
			go func(index int, peer account.Device) {
				defer wg.Done()
				defer func() { <-sem }()
				results[index] = s.syncAccountPeer(roundCtx, snapshot, peer, deviceKey, records)
			}(i, chosen[i])
		}
		wg.Wait()
		cancel()
		revoked := hostRevokedSet
		for i := range results {
			if results[i].synced {
				result.Synced++
			}
			result.Warnings = append(result.Warnings, results[i].warnings...)
			for _, id := range results[i].revoked {
				revoked[id] = true
			}
		}
		for id := range revoked {
			result.Revoked = append(result.Revoked, id)
		}
		sort.Strings(result.Revoked)
	} else {
		for id := range hostRevokedSet {
			result.Revoked = append(result.Revoked, id)
		}
		sort.Strings(result.Revoked)
	}
	// Merges may have revoked this device. Refresh before touching rooms: a
	// revoked device must not try to act as a room signer.
	if fresh, getErr := s.accounts.Get(ctx); getErr == nil {
		snapshot = fresh
	}
	if snapshot.Device.RevokedAt == nil {
		// Re-check every round rather than remembering which revocations were
		// propagated. A room home may learn about a membership after the first
		// attempt, and a revoked device can still present a valid certificate
		// until that home applies the revocation. Propagation is idempotent.
		if pending := s.pendingRevocations(ctx); len(pending) > 0 {
			propagateCtx, cancel := context.WithTimeout(ctx, accountRevocationTimeout)
			warnings := s.propagateDeviceRevocations(propagateCtx, snapshot, pending)
			cancel()
			result.Warnings = append(result.Warnings, warnings...)
		}
		// A rotation may have happened while a room home was offline. Check
		// each round; settled rooms cost only a local membership lookup.
		result.Warnings = append(result.Warnings, s.propagateAccountKey(ctx, snapshot)...)
	}
	sort.Strings(result.Warnings)
	return result, nil
}

// syncHostedAccount exchanges the full public device set with an official
// account host. The host never receives an account key or room content. A
// device session is signed by the local device key, so the host password and
// the account master key stay on the client.
func (s *Service) syncHostedAccount(ctx context.Context, snapshot *account.Snapshot, records []account.DeviceRecord, force bool) (warnings, revoked []string, attempted, synced bool) {
	hostURL := strings.TrimSpace(snapshot.Account.HostURL)
	if hostURL == "" || snapshot.Device.RevokedAt != nil {
		return nil, nil, false, false
	}
	if !s.hostSyncDue(force) {
		return nil, nil, false, false
	}
	attempted = true
	deviceKey, err := s.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		return []string{"official account sync needs an unlocked vault: " + err.Error()}, nil, true, false
	}
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		return []string{"official account host address is invalid: " + err.Error()}, nil, true, false
	}
	roundCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := s.hostSession(roundCtx, client, snapshot, deviceKey)
	if err != nil {
		return []string{"official account sign-in: " + hostedError(err)}, nil, true, false
	}
	request := hosted.SyncRequest{Devices: records}
	response, err := client.Sync(roundCtx, token, request)
	if errors.Is(err, hosted.ErrSessionInvalid) {
		s.hostSessions.Clear(snapshot.Account.ID)
		token, err = s.hostSession(roundCtx, client, snapshot, deviceKey)
		if err == nil {
			response, err = client.Sync(roundCtx, token, request)
		}
	}
	if err != nil {
		return []string{"official account sync: " + hostedError(err)}, nil, true, false
	}
	localGeneration := accountKeyGeneration(snapshot.Account)
	remoteGeneration := response.KeyGeneration
	if remoteGeneration <= 0 {
		remoteGeneration = 1
	}
	if remoteGeneration > localGeneration {
		warnings = append(warnings, "the official host has a newer account key; unlock this device with the account password to install it")
	} else if remoteGeneration < localGeneration {
		warnings = append(warnings, "the official host does not have this device's newest account key; other devices learn it through direct account sync")
	} else if len(response.PublicKey) > 0 && !bytes.Equal(response.PublicKey, snapshot.Account.PublicKey) {
		warnings = append(warnings, "the official host reports a different account key at the same generation")
	}
	merged, err := s.accounts.MergeDeviceRecords(roundCtx, response.Devices)
	if err != nil {
		warnings = append(warnings, "could not merge official account device records: "+err.Error())
		return warnings, nil, true, false
	}
	warnings = append(warnings, merged.Warnings...)
	return warnings, merged.RevokedIDs, true, true
}

// hostSyncDue rate-limits official-host exchanges, which are more expensive
// than local peer sync and must not run every 30 seconds. It records the
// attempt before the network call so an unreachable host is not retried on
// every loop tick.
func (s *Service) hostSyncDue(force bool) bool {
	if s == nil {
		return false
	}
	s.hostSyncMu.Lock()
	defer s.hostSyncMu.Unlock()
	if !force && !s.lastHostSync.IsZero() && time.Since(s.lastHostSync) < s.hostSyncEvery {
		return false
	}
	s.lastHostSync = time.Now()
	return true
}

func (s *Service) hostSession(ctx context.Context, client *hosted.Client, snapshot *account.Snapshot, deviceKey ed25519.PrivateKey) (string, error) {
	accountID := snapshot.Account.ID
	if token, ok := s.hostSessions.Get(accountID); ok {
		return token, nil
	}
	token, expires, err := client.DeviceSession(ctx, accountID, snapshot.Device.ID, deviceKey)
	if err != nil {
		return "", err
	}
	s.hostSessions.Set(accountID, token, expires)
	return token, nil
}

func hostedError(err error) string {
	if err == nil {
		return "unknown error"
	}
	if errors.Is(err, hosted.ErrHostUnavailable) {
		return "account host is unreachable"
	}
	if errors.Is(err, hosted.ErrInvalidHost) {
		return "account host identity is invalid"
	}
	var apiError *hosted.APIError
	if errors.As(err, &apiError) && apiError.Message != "" {
		return apiError.Message
	}
	return err.Error()
}

// nextAccountPeers rotates through the device list so accounts with many
// devices do not contact every peer on every round.
func (s *Service) nextAccountPeers(peers []account.Device) []account.Device {
	if len(peers) <= maxAccountPeersPerSync {
		return peers
	}
	start := s.accountSyncCursor % len(peers)
	count := maxAccountPeersPerSync
	out := make([]account.Device, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, peers[(start+i)%len(peers)])
	}
	s.accountSyncCursor = (start + count) % len(peers)
	return out
}

func (s *Service) bootstrapAccountPeer(ctx context.Context, snapshot *account.Snapshot) (*account.Device, bool) {
	accountID := snapshot.Account.ID
	originServerID := strings.TrimSpace(snapshot.Account.OriginServerID)
	if len(accountID) < 8 || originServerID == "" || originServerID == snapshot.Server.ID {
		return nil, false
	}
	record, err := s.LookupServer(ctx, accountID[:8])
	if err != nil {
		s.logger.Debug("account sync: account host is not discoverable", "error", err)
		return nil, false
	}
	if record.ServerID != originServerID || record.AccountID != accountID ||
		record.ServerCode != accountID[:8] {
		s.logger.Warn("account sync: discovered account host does not match the account",
			"server_id", record.ServerID, "server_code", record.ServerCode,
			"account_id", record.AccountID)
		return nil, false
	}
	if err := ValidateNodeURL(record.URL); err != nil {
		s.logger.Warn("account sync: discovered account host address is invalid", "error", err)
		return nil, false
	}
	return &account.Device{ID: record.ServerID, Name: "account host", NodeURL: record.URL}, true
}

func (s *Service) syncAccountPeer(ctx context.Context, snapshot *account.Snapshot, peer account.Device, deviceKey ed25519.PrivateKey, records []account.DeviceRecord) accountPeerResult {
	client, err := NewClientForURL(peer.NodeURL, snapshot.Account.ID, snapshot.Device.ID, deviceKey)
	if err != nil {
		return accountPeerResult{warnings: []string{fmt.Sprintf("%s: account sync skipped: %v", peer.Name, err)}}
	}
	defer client.Close()
	peerCtx, cancel := context.WithTimeout(ctx, accountPeerSyncTimeout)
	defer cancel()
	localGeneration := accountKeyGeneration(snapshot.Account)
	request := AccountSyncRequest{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID, Records: records,
		KeyGeneration: localGeneration, KeyRotations: snapshot.Account.KeyRotations,
	}
	if localGeneration > 1 {
		if generation, envelope, sealErr := s.accounts.SealAccountKeyFor(
			peerCtx, peer.ID, peer.EncryptionPublicKey); sealErr == nil && generation == localGeneration {
			request.KeyEnvelopes = map[string]string{peer.ID: envelope}
		}
	}
	response, err := client.SyncAccount(peerCtx, request)
	if err != nil {
		return accountPeerResult{warnings: []string{fmt.Sprintf("%s: account sync failed: %v", peer.Name, err)}}
	}
	if response.AccountID != snapshot.Account.ID {
		return accountPeerResult{warnings: []string{fmt.Sprintf("%s: account sync returned another account", peer.Name)}}
	}
	if response.KeyGeneration < 0 || response.KeyGeneration > 1_000_000 ||
		len(response.KeyRotations) > 64 || len(response.KeyEnvelopes) > 8 {
		return accountPeerResult{warnings: []string{fmt.Sprintf("%s: account sync returned an invalid key history", peer.Name)}}
	}
	remoteGeneration := response.KeyGeneration
	if remoteGeneration == 0 {
		remoteGeneration = 1
	}
	result := accountPeerResult{synced: true}
	if remoteGeneration > localGeneration {
		updated, changed, applyErr := s.accounts.ApplyAccountKeyRotations(
			peerCtx, response.KeyRotations, response.KeyEnvelopes[snapshot.Device.ID])
		switch {
		case applyErr == nil && changed && updated != nil:
			snapshot = updated
			localGeneration = accountKeyGeneration(snapshot.Account)
		case applyErr == nil:
			result.warnings = append(result.warnings, fmt.Sprintf("%s: account key generation did not advance", peer.Name))
			return result
		case errors.Is(applyErr, account.ErrKeyRotationEnvelope):
			result.warnings = append(result.warnings, fmt.Sprintf("%s: this device has not received the account's newest key yet", peer.Name))
			return result
		case errors.Is(applyErr, account.ErrKeyRotationConflict), errors.Is(applyErr, account.ErrKeyRotationGap):
			result.warnings = append(result.warnings, fmt.Sprintf("%s: account key history conflicts with this device", peer.Name))
			return result
		default:
			result.warnings = append(result.warnings, fmt.Sprintf("%s: could not apply the account key rotation: %v", peer.Name, applyErr))
			return result
		}
	}
	if remoteGeneration > localGeneration {
		result.warnings = append(result.warnings, fmt.Sprintf("%s: account key update is incomplete", peer.Name))
		return result
	}
	merged, err := s.accounts.MergeDeviceRecords(peerCtx, response.Records)
	if err != nil {
		result.warnings = append(result.warnings, fmt.Sprintf("%s: could not merge account records: %v", peer.Name, err))
		return result
	}
	result.revoked = append(result.revoked, merged.RevokedIDs...)
	for _, warning := range merged.Warnings {
		result.warnings = append(result.warnings, fmt.Sprintf("%s: %s", peer.Name, warning))
	}
	for i := range response.Records {
		record := response.Records[i]
		if record.ID != response.DeviceID || record.NodeURL == "" {
			continue
		}
		if err := s.accounts.SetDeviceNodeURL(peerCtx, record.ID, record.NodeURL); err != nil {
			result.warnings = append(result.warnings, fmt.Sprintf("%s: could not update its node address: %v", peer.Name, err))
		}
		break
	}
	if response.ReadOnly {
		result.warnings = append(result.warnings, fmt.Sprintf("%s considers this device revoked", peer.Name))
	}
	return result
}

// pendingRevocations returns every locally known revoked sibling. Propagation
// is re-evaluated against room membership each round; rooms that are already
// settled cost only local queries.
func (s *Service) pendingRevocations(ctx context.Context) []string {
	devices, err := s.accounts.ListDevices(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0)
	for i := range devices {
		device := devices[i]
		if device.Local || device.RevokedAt == nil {
			continue
		}
		out = append(out, device.ID)
	}
	return out
}

func (s *Service) handleAccountSync(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxAccountSyncBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var request AccountSyncRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if request.AccountID == "" || request.DeviceID == "" || len(request.Records) == 0 ||
		len(request.Records) > account.MaxDeviceRecords {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "account sync records are invalid")
		return
	}
	if request.KeyGeneration < 0 || request.KeyGeneration > 1_000_000 ||
		len(request.KeyRotations) > 64 || len(request.KeyEnvelopes) > 8 {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "account key history is invalid")
		return
	}
	senderGeneration := request.KeyGeneration
	if senderGeneration == 0 {
		senderGeneration = 1
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderAccount), request.AccountID) ||
		!ConstantTimeEqual(r.Header.Get(HeaderDevice), request.DeviceID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "request identity does not match the account sync request")
		return
	}
	snapshot, err := s.accounts.Get(r.Context())
	if err != nil {
		writeNodeError(w, http.StatusServiceUnavailable, "not_ready", "account identity is not ready")
		return
	}
	if !ConstantTimeEqual(request.AccountID, snapshot.Account.ID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "account sync is for another account")
		return
	}
	var sender *account.DeviceRecord
	for i := range request.Records {
		if request.Records[i].ID == request.DeviceID {
			sender = &request.Records[i]
			break
		}
	}
	if sender == nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "the request does not include the sending device")
		return
	}
	senderKey, err := accountKeyAt(snapshot.Account, request.KeyRotations, senderGeneration)
	if err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "sending device key generation is not verifiable")
		return
	}
	cert, err := identity.VerifyDeviceCertificateSignature([]byte(sender.Certificate), senderKey)
	if err != nil || cert.AccountID != request.AccountID || cert.DeviceID != request.DeviceID {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "sending device certificate is invalid")
		return
	}
	publicKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || !bytes.Equal(publicKey, sender.PublicKey) {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "sending device key does not match its certificate")
		return
	}
	if err := VerifyRequest(r, body, publicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "account sync signature is invalid")
		return
	}
	localGeneration := accountKeyGeneration(snapshot.Account)
	warnings := make([]string, 0)
	if senderGeneration > localGeneration {
		updated, changed, applyErr := s.accounts.ApplyAccountKeyRotations(
			r.Context(), request.KeyRotations, request.KeyEnvelopes[snapshot.Device.ID])
		switch {
		case applyErr == nil:
			if updated != nil {
				snapshot = updated
			}
			if changed {
				localGeneration = accountKeyGeneration(snapshot.Account)
				s.TriggerAccountSync()
			}
		case errors.Is(applyErr, account.ErrKeyRotationConflict), errors.Is(applyErr, account.ErrKeyRotationGap):
			writeNodeError(w, http.StatusConflict, "key_conflict", applyErr.Error())
			return
		case errors.Is(applyErr, account.ErrKeyRotationEnvelope), errors.Is(applyErr, vault.ErrLocked):
			warnings = append(warnings, "this device has not received the account's newest key yet")
		default:
			s.logger.Warn("account sync: apply account key rotation failed", "error", applyErr)
			writeNodeError(w, http.StatusInternalServerError, "internal", "could not apply the account key rotation")
			return
		}
	}
	keys, err := accountVerificationKeys(snapshot.Account, request.KeyRotations)
	if err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "account key history is invalid")
		return
	}
	readOnly := false
	if local, getErr := s.accounts.GetDeviceByID(r.Context(), request.DeviceID); getErr == nil && local.RevokedAt != nil {
		readOnly = true
	}
	if !readOnly && sender.Revocation != "" {
		for _, key := range keys {
			proof, verifyErr := identity.VerifyAccountRevocation(sender.Revocation, key)
			if verifyErr == nil && proof.AccountID == request.AccountID && proof.DeviceID == request.DeviceID {
				readOnly = true
				break
			}
		}
	}
	generationMatches := senderGeneration == localGeneration
	if !readOnly && generationMatches {
		merged, mergeErr := s.accounts.MergeDeviceRecords(r.Context(), request.Records)
		if mergeErr != nil {
			if errors.Is(mergeErr, account.ErrTooManyDeviceRecords) {
				writeNodeError(w, http.StatusBadRequest, "invalid_request", mergeErr.Error())
				return
			}
			s.logger.Warn("account sync: merge failed", "error", mergeErr)
			writeNodeError(w, http.StatusInternalServerError, "internal", "could not merge account records")
			return
		}
		warnings = append(warnings, merged.Warnings...)
		if merged.Added > 0 || merged.Updated > 0 {
			s.TriggerAccountSync()
		}
	}
	records, err := s.accounts.ExportDeviceRecords(r.Context(), s.Address(r.Context()))
	if err != nil {
		s.logger.Warn("account sync: export failed", "error", err)
		writeNodeError(w, http.StatusInternalServerError, "internal", "could not export account records")
		return
	}
	response := AccountSyncResponse{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		Records: records, KeyGeneration: localGeneration,
		KeyRotations: snapshot.Account.KeyRotations,
		ReadOnly:     readOnly, Warnings: warnings,
	}
	if !readOnly && senderGeneration < localGeneration {
		if _, envelope, sealErr := s.accounts.SealAccountKeyFor(r.Context(), sender.ID, sender.EncryptionPublicKey); sealErr == nil {
			response.KeyEnvelopes = map[string]string{sender.ID: envelope}
		} else {
			response.Warnings = append(response.Warnings, "this device could not address the account key to the sender")
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func accountKeyGeneration(acct *account.Account) int {
	if acct == nil || acct.KeyGeneration < 1 {
		return 1
	}
	return acct.KeyGeneration
}

// accountKeyAt resolves the account key that signed a request. Generations the
// responder already records come from its verified local history; a newer
// generation must be proven by the sender's rotation chain.
func accountKeyAt(acct *account.Account, incoming []string, generation int) (ed25519.PublicKey, error) {
	local := accountKeyGeneration(acct)
	if generation <= local {
		if len(acct.KeyRotations) == 0 {
			if generation != 1 {
				return nil, account.ErrKeyRotationGap
			}
			return ed25519.PublicKey(acct.PublicKey), nil
		}
		return identity.AccountPublicKeyAtGeneration(acct.KeyRotations, acct.ID, generation)
	}
	return identity.AccountPublicKeyAtGeneration(incoming, acct.ID, generation)
}

// accountVerificationKeys combines the local history with any chain the sender
// presented. Revocations and certificates may have been signed at any
// generation reachable from either side.
func accountVerificationKeys(acct *account.Account, incoming []string) ([]ed25519.PublicKey, error) {
	keys, err := account.VerificationKeys(acct)
	if err != nil {
		return nil, err
	}
	if len(incoming) == 0 {
		return keys, nil
	}
	extra, err := identity.VerifyAccountKeyChain(acct.ID, incoming)
	if err != nil {
		return nil, err
	}
	for _, key := range extra {
		duplicate := false
		for _, existing := range keys {
			if existing.Equal(key) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
