package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/directoryproto"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/relayproto"
)

const directoryPublishEvery = 10 * time.Minute

// DirectoryRecord is a verified directory lookup result.
type DirectoryRecord struct {
	ServerID       string    `json:"server_id"`
	ServerCode     string    `json:"server_code"`
	AccountID      string    `json:"account_id"`
	Endpoint       string    `json:"endpoint"`
	URL            string    `json:"url"`
	DirectURL      string    `json:"direct_url"`
	RelayURL       string    `json:"relay_url,omitempty"`
	TLSFingerprint string    `json:"tls_fingerprint"`
	UpdatedAt      time.Time `json:"updated_at"`
	// Source is "directory", "mdns", or "card".
	Source string `json:"source,omitempty"`
}

// directoryLoop publishes this node and, when relay is "auto", follows the
// directory's default relay. Failures are retried on the next interval.
func (s *Service) directoryLoop(ctx context.Context) {
	delay := time.Second
	for {
		err := s.syncDirectory(ctx)
		if err == nil {
			delay = directoryPublishEvery
		} else {
			if ctx.Err() != nil {
				return
			}
			s.logger.Warn("directory synchronization failed", "directory", s.directoryBase, "error", err)
			if delay < 30*time.Second {
				delay *= 2
			}
		}
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

// syncDirectory refreshes the automatic relay and republishes this node's
// signed manifest. A failure is retried quickly by directoryLoop so a node
// that starts before its account exists becomes visible without waiting for
// the full publish interval.
func (s *Service) syncDirectory(ctx context.Context) error {
	if s.directoryBase == "" {
		return nil
	}
	var firstErr error
	if s.relayMode() == "auto" {
		relayURL, err := s.defaultRelayFromDirectory(ctx)
		if err != nil {
			firstErr = err
		} else if base, fingerprint, err := ParsePinnedURL(relayURL); err != nil {
			firstErr = fmt.Errorf("directory returned an invalid relay: %w", err)
		} else if current, currentFP := s.relayConfig(); current != base || currentFP != fingerprint {
			s.setRelayConfig(base, fingerprint)
			s.logger.Info("using the directory relay", "relay", base)
		}
	}
	if err := s.publishDirectory(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *Service) defaultRelayFromDirectory(ctx context.Context) (string, error) {
	resp, data, err := s.directoryRequest(ctx, http.MethodGet, directoryproto.RelayPath, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", directoryError(resp.StatusCode, data)
	}
	var out directoryproto.RelayResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", errors.New("directory relay response is invalid")
	}
	if out.Protocol != directoryproto.Protocol {
		return "", errors.New("directory uses an unsupported protocol")
	}
	if out.RelayURL == "" {
		return "", errors.New("directory did not advertise a relay")
	}
	return out.RelayURL, nil
}

func (s *Service) publishDirectory(ctx context.Context) error {
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return err
	}
	if snapshot.Server == nil {
		return errors.New("server identity is not ready")
	}
	nodeManifest, err := s.manifestJSON(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(directoryproto.PublishRequest{
		Protocol:         directoryproto.Protocol,
		IdentityManifest: json.RawMessage(snapshot.Server.Manifest),
		NodeManifest:     json.RawMessage(nodeManifest),
	})
	if err != nil {
		return err
	}
	resp, data, err := s.directoryRequest(ctx, http.MethodPost, directoryproto.PublishPath, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return directoryError(resp.StatusCode, data)
	}
	var out directoryproto.PublishResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return errors.New("directory publish response is invalid")
	}
	if out.ServerID != snapshot.Server.ID || out.ServerCode != snapshot.Server.Code {
		return errors.New("directory acknowledged a different server")
	}
	return nil
}

// LookupServer resolves a server code through the configured directory and
// verifies the returned manifests locally. A compromised directory cannot
// substitute a different endpoint without failing these checks.
func (s *Service) LookupServer(ctx context.Context, code string) (*DirectoryRecord, error) {
	code = strings.TrimSpace(code)
	if len(code) != 8 {
		return nil, errors.New("server code must be 8 digits")
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return nil, errors.New("server code must be 8 digits")
		}
	}
	// A saved card is an explicit pin. Try it first, and refuse a different
	// server with the same short code from any later source.
	var savedCards []verifiedCard
	var cardErr error
	if s != nil {
		savedCards, cardErr = s.verifiedCardsForCode(code)
		if cardErr != nil {
			return nil, cardErr
		}
		if len(savedCards) > 0 {
			record, err := s.lookupSavedCards(ctx, savedCards)
			if err == nil {
				return record, nil
			}
			cardErr = err
		}
	}
	// LAN discovery avoids a public round trip when the node is on the same
	// network. Advertisements are only hints; lookupMDNS verifies the node's
	// identity and certificate before returning a record.
	discoveryErr := cardErr
	if s != nil && s.mdnsSetting != "" {
		record, err := s.lookupMDNS(ctx, code)
		if err == nil {
			if pinErr := checkSavedCardPin(savedCards, record); pinErr != nil {
				discoveryErr = pinErr
			} else {
				return record, nil
			}
		} else if discoveryErr == nil {
			discoveryErr = err
		} else {
			discoveryErr = fmt.Errorf("%v; LAN discovery: %w", discoveryErr, err)
		}
	}
	if s == nil || s.directoryBase == "" {
		if discoveryErr != nil {
			return nil, fmt.Errorf("no directory is configured; %w", discoveryErr)
		}
		return nil, errors.New("no directory is configured and no LAN node was discovered")
	}
	path := directoryproto.LookupPrefix + url.PathEscape(code)
	resp, data, err := s.directoryRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("server %s is not in the directory", code)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, directoryError(resp.StatusCode, data)
	}
	var out directoryproto.LookupResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errors.New("directory lookup response is invalid")
	}
	if out.Protocol != directoryproto.Protocol {
		return nil, errors.New("directory uses an unsupported protocol")
	}
	im, err := identity.VerifyManifestJSON(out.IdentityManifest)
	if err != nil {
		return nil, fmt.Errorf("directory returned an invalid server manifest: %w", err)
	}
	if im.ServerCode != code {
		return nil, errors.New("directory returned a different server code")
	}
	serverKey, err := identity.DecodePublicKey(im.RootPubKey)
	if err != nil {
		return nil, err
	}
	var nm Manifest
	if err := json.Unmarshal(out.NodeManifest, &nm); err != nil {
		return nil, errors.New("directory returned an invalid node manifest")
	}
	if err := VerifyManifest(&nm, serverKey); err != nil {
		return nil, fmt.Errorf("directory returned an unverified node manifest: %w", err)
	}
	if nm.ServerID != im.ServerID || nm.ServerCode != im.ServerCode {
		return nil, errors.New("directory node manifest does not match its server")
	}
	parsed, err := url.Parse(nm.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("directory node endpoint is not HTTPS")
	}
	direct := NodeURL(nm.Endpoint, nm.TLSFingerprint)
	// Only the relay route signed inside the node manifest is trusted. An
	// unsigned directory hint must not be able to redirect traffic.
	relayURL := nm.RelayURL
	if relayURL != "" {
		if _, _, target, err := relayproto.ParseNodeRoute(relayURL); err != nil || target != nm.ServerID {
			return nil, errors.New("directory relay route is invalid")
		}
	}
	record := &DirectoryRecord{
		ServerID: im.ServerID, ServerCode: im.ServerCode, AccountID: nm.AccountID,
		Endpoint: nm.Endpoint, URL: relayproto.JoinAddress(direct, relayURL),
		DirectURL: direct, RelayURL: relayURL, TLSFingerprint: nm.TLSFingerprint,
		UpdatedAt: out.UpdatedAt, Source: "directory",
	}
	if err := checkSavedCardPin(savedCards, record); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *Service) directoryRequest(ctx context.Context, method, path string, payload []byte) (*http.Response, []byte, error) {
	if s.directoryHTTP == nil || s.directoryBase == "" {
		return nil, nil, errors.New("no directory is configured")
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.directoryBase+path, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.directoryHTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("directory unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, directoryproto.MaxPublishBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > directoryproto.MaxPublishBody {
		return nil, nil, errors.New("directory response is too large")
	}
	return resp, data, nil
}

func directoryError(status int, data []byte) error {
	var out directoryproto.ErrorBody
	_ = json.Unmarshal(data, &out)
	if out.Error.Message != "" {
		return fmt.Errorf("directory: %s", out.Error.Message)
	}
	return fmt.Errorf("directory request failed (%d)", status)
}

// sleepContext waits for d or context cancellation and reports whether the
// wait completed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
