//go:build !js

package hosted

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

// remoteAccountResult is one verified federated account plus the peer
// metadata needed to present it in the local resolver.
type remoteAccountResult struct {
	record     *FederatedAccount
	hostURL    string
	serverName string
}

// cachedRemoteAccount returns a still-fresh cached statement if its internal
// host signature verifies.
func (s *Service) cachedRemoteAccount(ctx context.Context, accountID string, now time.Time) (*remoteAccountResult, error) {
	record, err := s.store.GetRemoteAccount(ctx, accountID, now)
	if err != nil {
		return nil, err
	}
	return verifyCachedRemoteAccount(record, now)
}

// lookupRemoteAccount returns a verified signed account record for another
// host. A cached record is reused for a few minutes; otherwise the directory
// record pins the peer transport and the peer returns a freshly signed record.
func (s *Service) lookupRemoteAccount(ctx context.Context, accountID string, allowCache bool) (*remoteAccountResult, error) {
	now := s.now().UTC().Truncate(time.Millisecond)
	if allowCache {
		if cached, err := s.cachedRemoteAccount(ctx, accountID, now); err == nil {
			return cached, nil
		}
		// A missing, expired, or damaged cache entry is repaired from the
		// directory rather than surfacing a stale local failure.
	}
	if s.directoryLookup == nil {
		return nil, errDirectoryDisabled
	}
	directory, err := s.directoryLookup(ctx, accountID[:8])
	if err != nil || directory == nil {
		if err == nil {
			err = errAccountNotFound
		}
		return nil, err
	}
	return s.fetchRemoteAccount(ctx, directory, accountID)
}

// fetchRemoteAccount calls one peer through a directory-pinned client and
// caches the signed result for a short time.
func (s *Service) fetchRemoteAccount(ctx context.Context, directory *DirectoryRecord, accountID string) (*remoteAccountResult, error) {
	serverCode := accountID[:8]
	client, manifest, err := s.federatedClient(directory, serverCode)
	if err != nil {
		return nil, err
	}
	record, err := client.LookupFederatedAccount(ctx, s.federationCaller(), accountID)
	if err != nil {
		if errors.Is(err, ErrHostUnavailable) {
			return nil, fmt.Errorf("account host is unreachable: %w", err)
		}
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Status == http.StatusNotFound {
			return nil, errAccountNotFound
		}
		return nil, err
	}
	if record.AccountID[:8] != serverCode {
		return nil, errAccountNotFound
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	cacheExpiry := now.Add(federationAccountTTL)
	if record.ExpiresAt.Before(cacheExpiry) {
		cacheExpiry = record.ExpiresAt
	}
	if cacheExpiry.After(now) {
		if err := s.store.SaveRemoteAccount(ctx, RemoteAccountRecord{
			AccountID: record.AccountID, HostID: record.HostID,
			HostCode: record.HostCode, HostURL: record.HostURL,
			Record: *record, FetchedAt: now, ExpiresAt: cacheExpiry,
		}); err != nil {
			s.logger.Warn("cache federated account failed", "account_id", accountID, "error", err)
		}
	}
	// A fresh fetch is a cheap opportunity to bound the cache without a
	// background timer. Expired rows are already invisible to lookups.
	if err := s.store.DeleteExpiredRemoteAccounts(ctx, now); err != nil {
		s.logger.Warn("cleanup federated account cache failed", "error", err)
	}
	return &remoteAccountResult{
		record: record, hostURL: manifest.URL, serverName: manifest.Name,
	}, nil
}

// remoteHostClient builds a pinned client for the account host behind a server
// code. The directory record is the only source of the transport pin, so a
// stored URL alone is never dialed.
func (s *Service) remoteHostClient(ctx context.Context, serverCode string) (*Client, error) {
	if s.directoryLookup == nil {
		return nil, errDirectoryDisabled
	}
	directory, err := s.directoryLookup(ctx, serverCode)
	if err != nil || directory == nil {
		if err == nil {
			err = errAccountNotFound
		}
		return nil, err
	}
	client, _, err := s.federatedClient(directory, serverCode)
	return client, err
}

// federatedClient builds a pinned client for a verified directory host record.
func (s *Service) federatedClient(directory *DirectoryRecord, serverCode string) (*Client, *identity.HostManifest, error) {
	if directory.ServerCode != serverCode || directory.ServerID == "" ||
		directory.Kind != "host" {
		return nil, nil, errAccountNotFound
	}
	manifest := directory.HostManifest
	if manifest == nil || manifest.ServerID != directory.ServerID ||
		manifest.ServerCode != serverCode || manifest.URL == "" {
		return nil, nil, errAccountNotFound
	}
	base, err := normalizeFederationHostURL(manifest.URL)
	if err != nil {
		return nil, nil, errAccountNotFound
	}
	hostURL := base
	if manifest.TLSFingerprint != "" {
		hostURL += "#fp=" + manifest.TLSFingerprint
	} else {
		hostURL += "#id=" + manifest.ServerID
	}
	client, err := NewClient(hostURL)
	if err != nil {
		return nil, nil, err
	}
	return client, manifest, nil
}

// verifyCachedRemoteAccount rechecks a cached statement using the host key
// carried inside it. The key is bound to HostID (a hash of the key), and the
// entry was originally written only after a directory-pinned fetch.
func verifyCachedRemoteAccount(record *RemoteAccountRecord, now time.Time) (*remoteAccountResult, error) {
	if record == nil || record.Record.AccountID != record.AccountID {
		return nil, errAccountNotFound
	}
	hostKey := ed25519.PublicKey(record.Record.HostPublicKey)
	if err := VerifyFederatedAccountAt(record.Record, hostKey, now); err != nil {
		return nil, err
	}
	if record.Record.HostID != record.HostID || record.Record.HostCode != record.HostCode {
		return nil, errAccountNotFound
	}
	keys, err := FederatedAccountKeys(record.Record)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(record.Record.Devices))
	for _, device := range record.Record.Devices {
		if seen[device.ID] {
			return nil, errAccountNotFound
		}
		seen[device.ID] = true
		if _, err := VerifyFederatedDeviceCertificate(device, keys, now); err != nil {
			return nil, err
		}
	}
	copied := record.Record
	return &remoteAccountResult{
		record: &copied, hostURL: record.HostURL,
	}, nil
}

// resolvedRemoteAccount converts a verified federated record into the public
// resolver shape. Device certificates are rechecked so an expired device
// disappears from lookup even before its cache entry does.
func resolvedRemoteAccount(remote *remoteAccountResult, now time.Time) (*ResolvedAccount, error) {
	devices, err := federatedAccountDevices(remote.record, now)
	if err != nil {
		return nil, err
	}
	resolvedDevices := make([]ResolvedDevice, 0, len(devices))
	for _, device := range devices {
		resolvedDevices = append(resolvedDevices, ResolvedDevice{
			ID: device.ID,
			EncryptionPublicKey: identity.EncodeEncryptionPublicKey(
				device.EncryptionPublicKey),
		})
	}
	hostURL := remote.hostURL
	if hostURL == "" {
		hostURL = remote.record.HostURL
	}
	return &ResolvedAccount{
		Protocol: Protocol, AccountID: remote.record.AccountID,
		Name: remote.record.Name, HostID: remote.record.HostID,
		HostCode: remote.record.HostCode, HostURL: hostURL,
		ServerName: remote.serverName, Kind: "account",
		Devices: resolvedDevices,
	}, nil
}

// federatedAccountDevices returns active devices from a verified record.
func federatedAccountDevices(record *FederatedAccount, now time.Time) ([]activeAccountDevice, error) {
	keys, err := FederatedAccountKeys(*record)
	if err != nil {
		return nil, err
	}
	devices := make([]activeAccountDevice, 0, len(record.Devices))
	seen := make(map[string]bool, len(record.Devices))
	for _, device := range record.Devices {
		if device.ID == "" || seen[device.ID] {
			continue
		}
		if _, err := VerifyFederatedDeviceCertificate(device, keys, now); err != nil {
			continue
		}
		seen[device.ID] = true
		devices = append(devices, activeAccountDevice{
			ID: device.ID, SigningPublicKey: append([]byte(nil), device.SigningPublicKey...),
			EncryptionPublicKey: append([]byte(nil), device.EncryptionPublicKey...),
			Certificate:         append([]byte(nil), device.Certificate...),
		})
	}
	return devices, nil
}

// remoteAccountRecord adapts a federated record to the AccountRecord shape
// the room handlers use. Secrets are intentionally absent.
func remoteAccountRecord(record *FederatedAccount) *AccountRecord {
	return &AccountRecord{
		ID: record.AccountID, Name: record.Name,
		PublicKey: record.PublicKey, KeyGeneration: record.KeyGeneration,
		KeyRotations: append([]string(nil), record.KeyRotations...),
	}
}
