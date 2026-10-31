package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/mdns"
	"github.com/sidenor/toget/internal/relayproto"
)

const (
	mdnsLookupWait  = 1200 * time.Millisecond
	mdnsPeerTimeout = 4 * time.Second
	maxMDNSPeers    = 8
	maxPublicJSON   = 1 << 20
)

// mdnsLoop waits until this node has an identity, then starts LAN discovery.
// Discovery is optional: a host without multicast continues serving rooms.
func (s *Service) mdnsLoop(ctx context.Context) {
	attempts := 0
	wait := 2 * time.Second
	for {
		snapshot, err := s.accounts.Get(ctx)
		if err == nil && snapshot.Server != nil && snapshot.Account != nil {
			if err := s.startMDNS(ctx, snapshot); err != nil {
				attempts++
				if attempts <= 2 || attempts%10 == 0 {
					s.logger.Warn("LAN discovery could not start", "error", err)
				}
				wait = 30 * time.Second
			} else {
				return
			}
		} else if ctx.Err() != nil {
			return
		}
		if !sleepContext(ctx, wait) {
			return
		}
	}
}

func (s *Service) startMDNS(ctx context.Context, snapshot *account.Snapshot) error {
	s.mu.RLock()
	endpoint, fingerprint := s.endpoint, s.fingerprint
	s.mu.RUnlock()
	if endpoint == "" || fingerprint == "" {
		return errors.New("node endpoint is not ready")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return errors.New("node endpoint is invalid")
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return fmt.Errorf("parse node endpoint: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("node endpoint port is invalid")
	}
	var addresses []net.IP
	if ip := net.ParseIP(host); ip != nil {
		addresses = []net.IP{ip}
	}
	service, err := mdns.New(mdns.Info{
		ID: snapshot.Server.ID, Code: snapshot.Server.Code, Protocol: Protocol,
		Fingerprint: fingerprint, Port: port, Addresses: addresses,
	}, s.logger)
	if err != nil {
		return err
	}
	if err := service.Start(ctx); err != nil {
		return err
	}
	s.mdnsMu.Lock()
	s.discovery = service
	s.mdnsMu.Unlock()
	s.logger.Info("LAN discovery enabled", "service", mdns.ServiceName)
	return nil
}

func (s *Service) discoveryService() *mdns.Service {
	if s == nil {
		return nil
	}
	s.mdnsMu.RLock()
	defer s.mdnsMu.RUnlock()
	return s.discovery
}

// MDNSEnabled reports whether multicast discovery is configured. It may be
// true before the account exists and the first advertisement is sent.
func (s *Service) MDNSEnabled() bool {
	return s != nil && s.mdnsSetting != ""
}

// Discover returns nodes currently visible on the local network. The
// advertisement is self-reported; LookupServer verifies a signature before
// using a peer as an endpoint.
func (s *Service) Discover(ctx context.Context) []mdns.Peer {
	service := s.discoveryService()
	if service == nil {
		return nil
	}
	if len(service.Peers()) == 0 {
		service.Refresh()
		deadline := time.NewTimer(800 * time.Millisecond)
		defer deadline.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for len(service.Peers()) == 0 {
			select {
			case <-ctx.Done():
				return service.Peers()
			case <-deadline.C:
				return service.Peers()
			case <-ticker.C:
			}
		}
	}
	return service.Peers()
}

// lookupMDNS finds and verifies a node advertising code. It waits briefly for
// an mDNS response, then returns an error so the caller can fall back to the
// directory.
func (s *Service) lookupMDNS(ctx context.Context, code string) (*DirectoryRecord, error) {
	service := s.discoveryService()
	if service == nil {
		return nil, errors.New("LAN discovery is not ready")
	}
	service.Refresh()
	deadline := time.NewTimer(mdnsLookupWait)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		peers := service.Lookup(code)
		if len(peers) > 0 {
			record, err := s.verifyMDNSPeers(ctx, code, peers)
			if err == nil {
				return record, nil
			}
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("no LAN node advertises server %s", code)
		case <-ticker.C:
		}
	}
}

func (s *Service) verifyMDNSPeers(ctx context.Context, code string, peers []mdns.Peer) (*DirectoryRecord, error) {
	attempts := 0
	var verified *DirectoryRecord
	var lastErr error
	for _, peer := range peers {
		if peer.ServerCode != code {
			continue
		}
		if attempts >= maxMDNSPeers {
			break
		}
		attempts++
		record, err := s.verifyMDNSPeer(ctx, peer)
		if err != nil {
			lastErr = err
			continue
		}
		if verified != nil && verified.ServerID != record.ServerID {
			return nil, errors.New("multiple LAN nodes advertise that server code")
		}
		verified = record
	}
	if verified != nil {
		return verified, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("LAN node could not be verified: %w", lastErr)
	}
	return nil, fmt.Errorf("no LAN node advertises server %s", code)
}

func (s *Service) verifyMDNSPeer(ctx context.Context, peer mdns.Peer) (*DirectoryRecord, error) {
	if peer.Protocol != Protocol {
		return nil, fmt.Errorf("LAN peer %s uses protocol %d", peer.ServerCode, peer.Protocol)
	}
	fingerprint, err := ParseFingerprint(peer.TLSFingerprint)
	if err != nil {
		return nil, err
	}
	if peer.Port < 1 || peer.Port > 65535 {
		return nil, errors.New("LAN peer port is invalid")
	}
	var addresses []net.IP
	for _, raw := range peer.Addresses {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		addresses = append(addresses, ip)
		if len(addresses) >= maxMDNSPeers {
			break
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("LAN peer has no usable address")
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 2*mdnsPeerTimeout)
	defer cancel()
	var lastErr error
	for _, address := range addresses {
		if attemptCtx.Err() != nil {
			break
		}
		endpoint := "https://" + net.JoinHostPort(address.String(), strconv.Itoa(peer.Port))
		record, err := s.verifyPinnedPeer(attemptCtx, endpoint, fingerprint,
			peer.ServerID, peer.ServerCode, peer.LastSeen, "mdns")
		if err == nil {
			return record, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("LAN peer is unreachable")
	}
	return nil, fmt.Errorf("LAN peer %s at %s: %w", peer.ServerCode, strings.Join(peer.Addresses, ","), lastErr)
}

func (s *Service) verifyPinnedPeer(ctx context.Context, endpoint, fingerprint, serverID, serverCode string, updated time.Time, source string) (*DirectoryRecord, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, mdnsPeerTimeout)
	defer cancel()
	tlsConfig, err := PinnedTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	client := pinnedHTTPClient(tlsConfig, mdnsPeerTimeout, mdnsPeerTimeout)
	defer client.CloseIdleConnections()
	identityJSON, err := getPublicJSON(attemptCtx, client, endpoint+"/toget/v1/identity")
	if err != nil {
		return nil, err
	}
	im, err := identity.VerifyManifestJSON(identityJSON)
	if err != nil {
		return nil, fmt.Errorf("identity manifest is invalid: %w", err)
	}
	if im.ServerID != serverID || im.ServerCode != serverCode {
		return nil, errors.New("discovered peer does not match the identity manifest")
	}
	serverKey, err := identity.DecodePublicKey(im.RootPubKey)
	if err != nil {
		return nil, err
	}
	nodeJSON, err := getPublicJSON(attemptCtx, client, endpoint+"/toget/v1/info")
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(nodeJSON, &manifest); err != nil {
		return nil, errors.New("LAN peer returned an invalid node manifest")
	}
	if err := VerifyManifest(&manifest, serverKey); err != nil {
		return nil, fmt.Errorf("node manifest is invalid: %w", err)
	}
	if manifest.ServerID != im.ServerID || manifest.ServerCode != im.ServerCode ||
		manifest.ServerID != serverID || manifest.ServerCode != serverCode {
		return nil, errors.New("discovered manifests disagree about the server identity")
	}
	if manifest.TLSFingerprint != fingerprint {
		return nil, errors.New("discovered node certificate does not match its hint")
	}
	parsed, err := url.Parse(manifest.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("LAN node endpoint is not HTTPS")
	}
	direct := NodeURL(endpoint, fingerprint)
	relayURL := ""
	if manifest.RelayURL != "" {
		if _, _, target, err := relayproto.ParseNodeRoute(manifest.RelayURL); err != nil || target != manifest.ServerID {
			return nil, errors.New("LAN node relay route is invalid")
		}
		relayURL = manifest.RelayURL
	}
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return &DirectoryRecord{
		ServerID: im.ServerID, ServerCode: im.ServerCode, AccountID: manifest.AccountID,
		Endpoint: manifest.Endpoint, URL: relayproto.JoinAddress(direct, relayURL),
		DirectURL: direct, RelayURL: relayURL, TLSFingerprint: fingerprint,
		UpdatedAt: updated, Source: source,
	}, nil
}

func getPublicJSON(ctx context.Context, client *http.Client, raw string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LAN peer is unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPublicJSON+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPublicJSON {
		return nil, errors.New("LAN peer response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("LAN peer returned status %d", resp.StatusCode)
	}
	return data, nil
}
