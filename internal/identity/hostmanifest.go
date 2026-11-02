package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/version"
)

// HostManifestFormat identifies a signed account-host record. A host manifest
// is the short-lived discovery statement for an account host. It is separate
// from the long-lived server identity manifest so a host can move or rotate its
// public address without changing its self-certifying server ID.
const HostManifestFormat = "toget-host-manifest"

// HostManifest announces the public HTTPS origin of an account host. It is
// signed by the same server root key as the identity manifest.
type HostManifest struct {
	Format         string    `json:"format"`
	Version        int       `json:"version"`
	ServerID       string    `json:"server_id"`
	ServerCode     string    `json:"server_code"`
	ServerNonce    string    `json:"server_nonce"`
	Name           string    `json:"name,omitempty"`
	URL            string    `json:"url"`
	TLSFingerprint string    `json:"tls_fingerprint,omitempty"`
	Protocol       int       `json:"protocol"`
	IssuedAt       time.Time `json:"issued_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	Signature      string    `json:"signature,omitempty"`
}

type hostManifestPayload struct {
	Format         string    `json:"format"`
	Version        int       `json:"version"`
	ServerID       string    `json:"server_id"`
	ServerCode     string    `json:"server_code"`
	ServerNonce    string    `json:"server_nonce"`
	Name           string    `json:"name,omitempty"`
	URL            string    `json:"url"`
	TLSFingerprint string    `json:"tls_fingerprint,omitempty"`
	Protocol       int       `json:"protocol"`
	IssuedAt       time.Time `json:"issued_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// SignHostManifest issues a fresh host manifest. ttl must be positive and no
// longer than one year. The URL is the browser-facing HTTPS origin without a
// path, query, or fragment. TLSFingerprint is optional and used for direct
// self-signed hosts; public Web PKI hosts leave it empty.
func SignHostManifest(server *Server, name, rawURL, rawFingerprint string, now time.Time, ttl time.Duration) ([]byte, error) {
	if server == nil || len(server.PrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("host identity is missing")
	}
	hostURL, err := normalizeHostManifestURL(rawURL)
	if err != nil {
		return nil, err
	}
	fingerprint, err := normalizeHostFingerprint(rawFingerprint)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 || ttl > 365*24*time.Hour {
		return nil, errors.New("host manifest lifetime is invalid")
	}
	now = now.UTC().Truncate(time.Second)
	manifest := HostManifest{
		Format: HostManifestFormat, Version: 1,
		ServerID: server.ID, ServerCode: server.Code,
		ServerNonce: base64.RawStdEncoding.EncodeToString(server.Nonce),
		Name:        strings.TrimSpace(name), URL: hostURL, TLSFingerprint: fingerprint,
		Protocol: version.Protocol,
		IssuedAt: now, ExpiresAt: now.Add(ttl),
	}
	payload, err := hostManifestBytes(manifest)
	if err != nil {
		return nil, err
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(server.PrivateKey, payload))
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode host manifest: %w", err)
	}
	return data, nil
}

// ParseHostManifest decodes a host manifest without verifying it.
func ParseHostManifest(data []byte) (*HostManifest, error) {
	var manifest HostManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode host manifest: %w", err)
	}
	return &manifest, nil
}

// VerifyHostManifest verifies a host manifest signature, binding, and
// freshness against the server root key that signed the identity manifest.
func VerifyHostManifest(data []byte, serverKey ed25519.PublicKey) (*HostManifest, error) {
	manifest, err := ParseHostManifest(data)
	if err != nil {
		return nil, err
	}
	if manifest.Format != HostManifestFormat || manifest.Version != 1 {
		return nil, errors.New("unsupported host manifest")
	}
	if manifest.Protocol != version.Protocol {
		return nil, errors.New("unsupported host protocol")
	}
	if len(serverKey) != ed25519.PublicKeySize {
		return nil, errors.New("host root key is invalid")
	}
	if manifest.ServerID == "" || manifest.ServerCode == "" ||
		manifest.ServerNonce == "" || manifest.URL == "" {
		return nil, errors.New("host manifest is incomplete")
	}
	if manifest.ServerID != ServerID(serverKey) {
		return nil, errors.New("host manifest does not match its root key")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(manifest.ServerNonce)
	if err != nil || len(nonce) != 16 {
		return nil, errors.New("host manifest server nonce is invalid")
	}
	if manifest.ServerCode != ServerCode(serverKey, nonce) {
		return nil, errors.New("host manifest server code does not match its key")
	}
	if _, err := normalizeHostManifestURL(manifest.URL); err != nil {
		return nil, err
	}
	if _, err := normalizeHostFingerprint(manifest.TLSFingerprint); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if manifest.IssuedAt.IsZero() || manifest.IssuedAt.After(now.Add(5*time.Minute)) {
		return nil, errors.New("host manifest was issued in the future")
	}
	if manifest.ExpiresAt.IsZero() || !now.Before(manifest.ExpiresAt) {
		return nil, errors.New("host manifest has expired")
	}
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("host manifest signature is malformed")
	}
	payload, err := hostManifestBytes(*manifest)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(serverKey, payload, signature) {
		return nil, errors.New("host manifest signature is invalid")
	}
	return manifest, nil
}

func hostManifestBytes(manifest HostManifest) ([]byte, error) {
	return json.Marshal(hostManifestPayload{
		Format: manifest.Format, Version: manifest.Version,
		ServerID: manifest.ServerID, ServerCode: manifest.ServerCode,
		ServerNonce: manifest.ServerNonce,
		Name:        manifest.Name, URL: manifest.URL, TLSFingerprint: manifest.TLSFingerprint,
		Protocol: manifest.Protocol, IssuedAt: manifest.IssuedAt, ExpiresAt: manifest.ExpiresAt,
	})
}

func normalizeHostManifestURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("host manifest URL must be an HTTPS origin")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func normalizeHostFingerprint(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "sha256:")
	if raw == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("host manifest TLS fingerprint is invalid")
	}
	return hex.EncodeToString(decoded), nil
}
