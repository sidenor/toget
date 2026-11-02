//go:build !js

package hosted

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

var (
	errAccountNotFound   = errors.New("account not found")
	errDirectoryDisabled = errors.New("directory lookup is not configured")
)

// handleAccountLookup returns a host-signed public profile. It never returns
// email addresses, device records, or account keys.
func (s *Service) handleAccountLookup(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "account_lookup", 60) {
		return
	}
	accountID := r.PathValue("accountID")
	if !digits(accountID, 14) || accountID[:8] != s.host.Server.Code {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	ref, err := s.store.GetAccountRef(r.Context(), accountID)
	if errors.Is(err, ErrAccountNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	lookup, err := SignAccountLookup(AccountLookup{
		Protocol: Protocol, AccountID: ref.ID, Name: ref.Name,
		HostID: s.host.Server.ID, HostCode: s.host.Server.Code,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}, s.host.Server.PrivateKey)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lookup)
}

// handleResolveAccount resolves an ID owned by another account host. The
// caller must be a signed-in device so the official host is not an anonymous
// directory proxy.
func (s *Service) handleResolveAccount(w http.ResponseWriter, r *http.Request) {
	body, ok := roomBody(w, r)
	if !ok {
		return
	}
	auth, ok := s.authenticateRoomDevice(w, r, body)
	if !ok {
		return
	}
	var request ResolveAccountRequest
	if err := decodeRoomBody(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || !digits(request.AccountID, 14) {
		writeError(w, http.StatusBadRequest, "invalid_account", "account id must be 14 digits")
		return
	}
	resolved, err := s.resolveAccount(r.Context(), request.AccountID)
	switch {
	case errors.Is(err, errAccountNotFound):
		writeError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	case errors.Is(err, errDirectoryDisabled):
		writeError(w, http.StatusServiceUnavailable, "directory_disabled", "this host cannot resolve other servers")
		return
	case err != nil:
		s.logger.Warn("resolve account failed", "account_id", request.AccountID, "error", err)
		writeError(w, http.StatusBadGateway, "resolve_failed", "could not resolve the account")
		return
	}
	resolved.Self = resolved.AccountID == auth.account.ID
	writeJSON(w, http.StatusOK, resolved)
}

func (s *Service) resolveAccount(ctx context.Context, accountID string) (*ResolvedAccount, error) {
	if !digits(accountID, 14) {
		return nil, errAccountNotFound
	}
	serverCode := accountID[:8]
	if serverCode == s.host.Server.Code {
		ref, err := s.store.GetAccountRef(ctx, accountID)
		if errors.Is(err, ErrAccountNotFound) {
			return nil, errAccountNotFound
		}
		if err != nil {
			return nil, err
		}
		devices, err := s.activeAccountDevices(ctx, accountID)
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
		return &ResolvedAccount{
			Protocol: Protocol, AccountID: ref.ID, Name: ref.Name,
			HostID: s.host.Server.ID, HostCode: s.host.Server.Code,
			HostURL: s.publicHostURL(), ServerName: s.host.Name,
			Kind: "account", Devices: resolvedDevices,
		}, nil
	}
	if s.directoryLookup == nil {
		return nil, errDirectoryDisabled
	}
	record, err := s.directoryLookup(ctx, serverCode)
	if err != nil || record == nil {
		if err == nil {
			err = errAccountNotFound
		}
		return nil, err
	}
	if record.ServerCode != serverCode || record.ServerID == "" {
		return nil, errAccountNotFound
	}
	switch record.Kind {
	case "host":
		manifest := record.HostManifest
		if manifest == nil || manifest.ServerID != record.ServerID ||
			manifest.ServerCode != serverCode {
			return nil, errAccountNotFound
		}
		hostURL := manifest.URL
		if manifest.TLSFingerprint != "" {
			hostURL += "#fp=" + manifest.TLSFingerprint
		} else {
			hostURL += "#id=" + manifest.ServerID
		}
		client, err := NewClient(hostURL)
		if err != nil {
			return nil, err
		}
		lookup, err := client.LookupAccount(ctx, accountID)
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
		return &ResolvedAccount{
			Protocol: Protocol, AccountID: lookup.AccountID, Name: lookup.Name,
			HostID: lookup.HostID, HostCode: lookup.HostCode,
			HostURL: manifest.URL, ServerName: manifest.Name, Kind: "account",
		}, nil
	case "node":
		if record.AccountID != accountID {
			return nil, errAccountNotFound
		}
		return &ResolvedAccount{
			Protocol: Protocol, AccountID: accountID,
			HostID: record.ServerID, HostCode: record.ServerCode,
			HostURL: record.Endpoint, Kind: "node",
		}, nil
	default:
		return nil, errAccountNotFound
	}
}

func (s *Service) publicHostURL() string {
	if s.publicURL != "" {
		return s.publicURL
	}
	for _, endpoint := range s.host.Server.Manifest.Endpoints {
		if endpoint.Transport != "https" || endpoint.Addr == "" {
			continue
		}
		host, port, err := net.SplitHostPort(endpoint.Addr)
		if err == nil && port == "443" {
			return "https://" + host
		}
		return "https://" + strings.TrimSuffix(endpoint.Addr, "/")
	}
	return ""
}
