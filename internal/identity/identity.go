// Package identity defines server, account, and device keys and the signed
// statements that bind them together.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"

	"github.com/sidenor/toget/internal/version"
)

const (
	// ServerPrefix marks a full server fingerprint.
	ServerPrefix = "tog1:s:"

	serverCodeModulus = 100_000_000
)

// Crockford base32 avoids I, L, O, and U.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// Endpoint is one way to reach a server.
type Endpoint struct {
	Transport string `json:"transport"`
	Addr      string `json:"addr"`
}

// Manifest is the signed public description of a server.
type Manifest struct {
	ServerID         string     `json:"server_id"`
	ServerCode       string     `json:"server_code"`
	RootPubKey       string     `json:"root_pubkey"`
	ServerNonce      string     `json:"server_nonce"`
	Endpoints        []Endpoint `json:"endpoints"`
	RelayParent      string     `json:"relay_parent,omitempty"`
	ProtocolVersions []int      `json:"protocol_versions"`
	IssuedAt         time.Time  `json:"issued_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	Signature        string     `json:"signature"`
}

type manifestPayload struct {
	ServerID         string     `json:"server_id"`
	ServerCode       string     `json:"server_code"`
	RootPubKey       string     `json:"root_pubkey"`
	ServerNonce      string     `json:"server_nonce"`
	Endpoints        []Endpoint `json:"endpoints"`
	RelayParent      string     `json:"relay_parent,omitempty"`
	ProtocolVersions []int      `json:"protocol_versions"`
	IssuedAt         time.Time  `json:"issued_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
}

// Server is a server identity with its private key.
type Server struct {
	ID           string
	Code         string
	PublicKey    ed25519.PublicKey
	PrivateKey   ed25519.PrivateKey
	Nonce        []byte
	Manifest     Manifest
	ManifestJSON []byte
}

// NewServer creates a self-certifying server identity.
func NewServer(endpoints []Endpoint) (*Server, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate server nonce: %w", err)
	}
	if endpoints == nil {
		endpoints = []Endpoint{}
	}

	m := Manifest{
		ServerID:         ServerID(pub),
		ServerCode:       ServerCode(pub, nonce),
		RootPubKey:       EncodePublicKey(pub),
		ServerNonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Endpoints:        endpoints,
		ProtocolVersions: []int{version.Protocol},
		IssuedAt:         time.Now().UTC().Truncate(time.Second),
		ExpiresAt:        time.Now().UTC().AddDate(1, 0, 0).Truncate(time.Second),
	}
	manifestJSON, err := SignManifest(m, priv)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("decode signed manifest: %w", err)
	}
	return &Server{
		ID:           m.ServerID,
		Code:         m.ServerCode,
		PublicKey:    pub,
		PrivateKey:   priv,
		Nonce:        nonce,
		Manifest:     m,
		ManifestJSON: manifestJSON,
	}, nil
}

// SignManifest returns the canonical signed JSON for a manifest.
func SignManifest(m Manifest, priv ed25519.PrivateKey) ([]byte, error) {
	payload := manifestPayload{
		ServerID:         m.ServerID,
		ServerCode:       m.ServerCode,
		RootPubKey:       m.RootPubKey,
		ServerNonce:      m.ServerNonce,
		Endpoints:        m.Endpoints,
		RelayParent:      m.RelayParent,
		ProtocolVersions: m.ProtocolVersions,
		IssuedAt:         m.IssuedAt,
		ExpiresAt:        m.ExpiresAt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	sig := ed25519.Sign(priv, data)
	m.Signature = base64.RawStdEncoding.EncodeToString(sig)
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode signed manifest: %w", err)
	}
	return out, nil
}

// VerifyManifestJSON verifies the signature and the self-certifying fields.
func VerifyManifestJSON(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	pub, err := DecodePublicKey(m.RootPubKey)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawStdEncoding.DecodeString(m.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode manifest signature: %w", err)
	}
	payload := manifestPayload{
		ServerID:         m.ServerID,
		ServerCode:       m.ServerCode,
		RootPubKey:       m.RootPubKey,
		ServerNonce:      m.ServerNonce,
		Endpoints:        m.Endpoints,
		RelayParent:      m.RelayParent,
		ProtocolVersions: m.ProtocolVersions,
		IssuedAt:         m.IssuedAt,
		ExpiresAt:        m.ExpiresAt,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode manifest payload: %w", err)
	}
	if !ed25519.Verify(pub, canonical, sig) {
		return nil, errors.New("manifest signature is invalid")
	}
	now := time.Now().UTC()
	if m.ExpiresAt.IsZero() || !now.Before(m.ExpiresAt) {
		return nil, errors.New("manifest has expired")
	}
	if m.IssuedAt.IsZero() || m.IssuedAt.After(now.Add(5*time.Minute)) {
		return nil, errors.New("manifest was issued in the future")
	}
	if m.ServerID != ServerID(pub) {
		return nil, errors.New("manifest server_id does not match its key")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(m.ServerNonce)
	if err != nil {
		return nil, fmt.Errorf("decode server nonce: %w", err)
	}
	if m.ServerCode != ServerCode(pub, nonce) {
		return nil, errors.New("manifest server_code does not match its key and nonce")
	}
	return &m, nil
}

// ServerID returns the full fingerprint of a server root key.
func ServerID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return ServerPrefix + crockford.EncodeToString(sum[:])
}

// ServerCode returns the 8-digit short code derived from a key and nonce.
func ServerCode(pub ed25519.PublicKey, nonce []byte) string {
	h := sha256.New()
	h.Write(pub)
	h.Write(nonce)
	sum := h.Sum(nil)
	n := new(big.Int).SetBytes(sum)
	n.Mod(n, big.NewInt(serverCodeModulus))
	return fmt.Sprintf("%08d", n.Uint64())
}

// AccountID joins an 8-digit server code and a 6-digit account code.
func AccountID(serverCode, accountCode string) string {
	return serverCode + accountCode
}

// NewAccountKey creates the account signing key.
func NewAccountKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate account key: %w", err)
	}
	return pub, priv, nil
}

// NewDeviceKey creates the device signing key.
func NewDeviceKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate device key: %w", err)
	}
	return pub, priv, nil
}

// EncodePublicKey encodes an Ed25519 public key for manifests and certificates.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.RawStdEncoding.EncodeToString(pub)
}

// DecodePublicKey decodes a key produced by EncodePublicKey.
func DecodePublicKey(encoded string) (ed25519.PublicKey, error) {
	const prefix = "ed25519:"
	if len(encoded) <= len(prefix) || encoded[:len(prefix)] != prefix {
		return nil, errors.New("unsupported public key format")
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key has %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// DeviceCertificate binds a device key to an account.
type DeviceCertificate struct {
	DeviceID     string    `json:"device_id"`
	AccountID    string    `json:"account_id"`
	DevicePubKey string    `json:"device_pubkey"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Signature    string    `json:"signature"`
}

type deviceCertificatePayload struct {
	DeviceID     string    `json:"device_id"`
	AccountID    string    `json:"account_id"`
	DevicePubKey string    `json:"device_pubkey"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// SignDeviceCertificate creates a device certificate signed by the account key.
func SignDeviceCertificate(deviceID, accountID string, devicePub ed25519.PublicKey, accountPriv ed25519.PrivateKey, now time.Time) ([]byte, error) {
	payload := deviceCertificatePayload{
		DeviceID:     deviceID,
		AccountID:    accountID,
		DevicePubKey: EncodePublicKey(devicePub),
		IssuedAt:     now.UTC().Truncate(time.Second),
		ExpiresAt:    now.UTC().AddDate(1, 0, 0).Truncate(time.Second),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode device certificate: %w", err)
	}
	cert := DeviceCertificate{
		DeviceID:     payload.DeviceID,
		AccountID:    payload.AccountID,
		DevicePubKey: payload.DevicePubKey,
		IssuedAt:     payload.IssuedAt,
		ExpiresAt:    payload.ExpiresAt,
		Signature:    base64.RawStdEncoding.EncodeToString(ed25519.Sign(accountPriv, data)),
	}
	out, err := json.Marshal(cert)
	if err != nil {
		return nil, fmt.Errorf("encode signed device certificate: %w", err)
	}
	return out, nil
}

// VerifyDeviceCertificate verifies a certificate against an account key.
func VerifyDeviceCertificate(data []byte, accountPub ed25519.PublicKey) (*DeviceCertificate, error) {
	var cert DeviceCertificate
	if err := json.Unmarshal(data, &cert); err != nil {
		return nil, fmt.Errorf("decode device certificate: %w", err)
	}
	sig, err := base64.RawStdEncoding.DecodeString(cert.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode device certificate signature: %w", err)
	}
	payload := deviceCertificatePayload{
		DeviceID:     cert.DeviceID,
		AccountID:    cert.AccountID,
		DevicePubKey: cert.DevicePubKey,
		IssuedAt:     cert.IssuedAt,
		ExpiresAt:    cert.ExpiresAt,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode device certificate payload: %w", err)
	}
	if !ed25519.Verify(accountPub, canonical, sig) {
		return nil, errors.New("device certificate signature is invalid")
	}
	if cert.ExpiresAt.IsZero() || !time.Now().UTC().Before(cert.ExpiresAt) {
		return nil, errors.New("device certificate has expired")
	}
	return &cert, nil
}

// NewID returns a time-ordered UUIDv7 string for local objects.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// crypto/rand failure is not recoverable; fall back to a random UUID.
		return uuid.NewString()
	}
	return id.String()
}

// Fingerprint returns a short display fingerprint of a public key.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}
