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
	"github.com/sidenor/toget/internal/identity"
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
}

// AccountSyncResponse returns the responder's device set. ReadOnly is set when
// the sender is revoked; a revoked device may still learn about the account so
// it discovers its own revocation, but cannot change the responder's records.
type AccountSyncResponse struct {
	AccountID string                 `json:"account_id"`
	DeviceID  string                 `json:"device_id"`
	Records   []account.DeviceRecord `json:"records"`
	ReadOnly  bool                   `json:"read_only,omitempty"`
	Warnings  []string               `json:"warnings,omitempty"`
}

// AccountSyncResult summarizes one account-sync round.
type AccountSyncResult struct {
	Peers    int      `json:"peers"`
	Synced   int      `json:"synced"`
	Warnings []string `json:"warnings,omitempty"`
	Revoked  []string `json:"revoked,omitempty"`
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
	result := &AccountSyncResult{Peers: len(peers)}
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
		revoked := make(map[string]bool)
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
	}
	sort.Strings(result.Warnings)
	return result, nil
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
	response, err := client.SyncAccount(peerCtx, AccountSyncRequest{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID, Records: records,
	})
	if err != nil {
		return accountPeerResult{warnings: []string{fmt.Sprintf("%s: account sync failed: %v", peer.Name, err)}}
	}
	if response.AccountID != snapshot.Account.ID {
		return accountPeerResult{warnings: []string{fmt.Sprintf("%s: account sync returned another account", peer.Name)}}
	}
	result := accountPeerResult{synced: true}
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
		if record.ID != peer.ID || record.NodeURL == "" {
			continue
		}
		if err := s.accounts.SetDeviceNodeURL(peerCtx, peer.ID, record.NodeURL); err != nil {
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
	accountPub := ed25519.PublicKey(snapshot.Account.PublicKey)
	cert, err := identity.VerifyDeviceCertificateSignature([]byte(sender.Certificate), accountPub)
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
	readOnly := false
	if local, getErr := s.accounts.GetDeviceByID(r.Context(), request.DeviceID); getErr == nil && local.RevokedAt != nil {
		readOnly = true
	}
	if !readOnly && sender.Revocation != "" {
		if proof, verifyErr := identity.VerifyAccountRevocation(sender.Revocation, accountPub); verifyErr == nil &&
			proof.AccountID == request.AccountID && proof.DeviceID == request.DeviceID {
			readOnly = true
		}
	}
	warnings := make([]string, 0)
	if !readOnly {
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
		warnings = merged.Warnings
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
	writeJSON(w, http.StatusOK, AccountSyncResponse{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		Records: records, ReadOnly: readOnly, Warnings: warnings,
	})
}
