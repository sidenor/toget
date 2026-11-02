// Package directoryclient talks to a Toget directory over HTTPS. It verifies
// every manifest itself and converts the response into the neutral record the
// account host uses for cross-server account discovery.
package directoryclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/directoryproto"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/node"
)

const maxResponse = 1 << 20

// Fetch resolves an 8-digit server code through a directory. baseURL is the
// directory root, for example https://toget.vu/directory or a pinned
// https://host:7720#fp=... address.
func Fetch(ctx context.Context, baseURL, serverCode string) (*hosted.DirectoryRecord, error) {
	if len(serverCode) != 8 || !allDigits(serverCode) {
		return nil, errors.New("server code must be 8 digits")
	}
	body, err := request(ctx, http.MethodGet, baseURL, directoryproto.LookupPrefix+url.PathEscape(serverCode), nil)
	if err != nil {
		return nil, err
	}
	return ParseLookup(body)
}

// PublishHost registers a host record. It returns the directory's expiry.
func PublishHost(ctx context.Context, baseURL string, identityManifest, hostManifest json.RawMessage) error {
	if len(identityManifest) == 0 || len(hostManifest) == 0 {
		return errors.New("host publish is missing a manifest")
	}
	payload, err := json.Marshal(directoryproto.PublishRequest{
		Protocol: directoryproto.Protocol, Kind: directoryproto.RecordKindHost,
		IdentityManifest: identityManifest, HostManifest: hostManifest,
	})
	if err != nil {
		return err
	}
	body, err := request(ctx, http.MethodPost, baseURL, directoryproto.PublishPath, payload)
	if err != nil {
		return err
	}
	var response directoryproto.PublishResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("directory publish response: %w", err)
	}
	if response.Protocol != directoryproto.Protocol || response.Kind != directoryproto.RecordKindHost ||
		response.ServerCode == "" || response.ServerID == "" {
		return errors.New("directory rejected the host record")
	}
	return nil
}

// ParseLookup verifies a directory lookup response and converts it to the
// neutral record shape. It never trusts the directory's outer JSON.
func ParseLookup(data []byte) (*hosted.DirectoryRecord, error) {
	var response directoryproto.LookupResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("directory lookup response: %w", err)
	}
	if response.Protocol != directoryproto.Protocol {
		return nil, errors.New("unsupported directory protocol")
	}
	if response.Kind == "" {
		response.Kind = directoryproto.RecordKindNode
	}
	im, err := identity.VerifyManifestJSON(response.IdentityManifest)
	if err != nil {
		return nil, fmt.Errorf("directory server manifest: %w", err)
	}
	serverKey, err := identity.DecodePublicKey(im.RootPubKey)
	if err != nil {
		return nil, err
	}
	record := &hosted.DirectoryRecord{
		Kind: response.Kind, ServerID: im.ServerID, ServerCode: im.ServerCode,
	}
	switch response.Kind {
	case directoryproto.RecordKindHost:
		hostManifest, err := identity.VerifyHostManifest(response.HostManifest, serverKey)
		if err != nil {
			return nil, fmt.Errorf("directory host manifest: %w", err)
		}
		if hostManifest.ServerID != im.ServerID || hostManifest.ServerCode != im.ServerCode {
			return nil, errors.New("directory host manifest does not match the server identity")
		}
		record.HostManifest = hostManifest
	case directoryproto.RecordKindNode:
		var manifest node.Manifest
		if err := json.Unmarshal(response.NodeManifest, &manifest); err != nil {
			return nil, fmt.Errorf("directory node manifest: %w", err)
		}
		if err := node.VerifyManifest(&manifest, serverKey); err != nil {
			return nil, fmt.Errorf("directory node manifest: %w", err)
		}
		if manifest.ServerID != im.ServerID || manifest.ServerCode != im.ServerCode {
			return nil, errors.New("directory node manifest does not match the server identity")
		}
		record.AccountID = manifest.AccountID
		record.Endpoint = manifest.Endpoint
		record.TLSFingerprint = manifest.TLSFingerprint
	default:
		return nil, errors.New("directory record has an unknown kind")
	}
	return record, nil
}

func request(ctx context.Context, method, baseURL, suffix string, payload []byte) ([]byte, error) {
	base, fingerprint, err := splitBase(baseURL)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+suffix, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	if fingerprint != "" {
		config, err := node.PinnedTLSConfig(fingerprint)
		if err != nil {
			return nil, err
		}
		client.Transport = &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: true}
	} else {
		parsed, err := url.Parse(base)
		if err != nil {
			return nil, err
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
			return nil, errors.New("directory must use HTTPS")
		}
		client.Transport = &http.Transport{
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13},
			ForceAttemptHTTP2: true,
		}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponse {
		return nil, errors.New("directory response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("directory returned %s", response.Status)
	}
	return data, nil
}

func splitBase(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("directory URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname()))) {
		return "", "", errors.New("directory URL must be HTTPS")
	}
	fingerprint := ""
	if parsed.Fragment != "" {
		values, err := url.ParseQuery(parsed.Fragment)
		if err != nil {
			return "", "", errors.New("directory URL fingerprint is invalid")
		}
		fingerprint = values.Get("fp")
		if fingerprint == "" {
			fingerprint = values.Get("sha256")
		}
		if fingerprint == "" {
			return "", "", errors.New("directory URL is missing a fingerprint")
		}
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	// Accept either the origin or the /directory mount as the base URL. The
	// wire paths always include the /directory prefix.
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	switch parsed.Path {
	case "", "/":
		parsed.Path = ""
	case "/directory":
		parsed.Path = ""
	default:
		return "", "", errors.New("directory URL must be an origin or end with /directory")
	}
	parsed.RawPath = ""
	base := strings.TrimSuffix(parsed.String(), "/")
	if base == "" {
		return "", "", errors.New("directory URL is invalid")
	}
	return base, fingerprint, nil
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || strings.HasPrefix(host, "127.") || host == "::1"
}

func allDigits(value string) bool {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
