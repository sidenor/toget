// Package identity defines server, account, and device keys and the signed
// statements that bind them together.
package identity

import (
	"crypto/ecdh"
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
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate server nonce: %w", err)
	}
	return NewServerFromKey(priv, nonce, endpoints)
}

// NewServerFromKey restores a server identity from persisted key material and
// issues a fresh signed manifest for the supplied endpoints. It is used by
// long-lived services whose identity outlives one process.
func NewServerFromKey(privateKey ed25519.PrivateKey, nonce []byte, endpoints []Endpoint) (*Server, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("server private key is invalid")
	}
	if len(nonce) != 16 {
		return nil, errors.New("server nonce is invalid")
	}
	pub, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("server public key is invalid")
	}
	if endpoints == nil {
		endpoints = []Endpoint{}
	}

	now := time.Now().UTC()
	m := Manifest{
		ServerID:         ServerID(pub),
		ServerCode:       ServerCode(pub, nonce),
		RootPubKey:       EncodePublicKey(pub),
		ServerNonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Endpoints:        endpoints,
		ProtocolVersions: []int{version.Protocol},
		IssuedAt:         now.Truncate(time.Second),
		ExpiresAt:        now.AddDate(1, 0, 0).Truncate(time.Second),
	}
	manifestJSON, err := SignManifest(m, privateKey)
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
		PrivateKey:   privateKey,
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

// ValidServerID reports whether id is a canonical full server fingerprint.
func ValidServerID(id string) bool {
	if len(id) <= len(ServerPrefix) || id[:len(ServerPrefix)] != ServerPrefix {
		return false
	}
	encoded := id[len(ServerPrefix):]
	raw, err := crockford.DecodeString(encoded)
	return err == nil && len(raw) == sha256.Size && crockford.EncodeToString(raw) == encoded
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

// NewEncryptionKey creates an X25519 key pair for wrapping room keys.
func NewEncryptionKey() ([]byte, []byte, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate encryption key: %w", err)
	}
	return priv.PublicKey().Bytes(), priv.Bytes(), nil
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
	DeviceID            string    `json:"device_id"`
	AccountID           string    `json:"account_id"`
	DevicePubKey        string    `json:"device_pubkey"`
	EncryptionPublicKey string    `json:"encryption_pubkey,omitempty"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	Signature           string    `json:"signature"`
}

type deviceCertificatePayload struct {
	DeviceID            string    `json:"device_id"`
	AccountID           string    `json:"account_id"`
	DevicePubKey        string    `json:"device_pubkey"`
	EncryptionPublicKey string    `json:"encryption_pubkey,omitempty"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
}

// SignDeviceCertificate creates a device certificate signed by the account key.
func SignDeviceCertificate(deviceID, accountID string, devicePub ed25519.PublicKey, encryptionPub []byte, accountPriv ed25519.PrivateKey, now time.Time) ([]byte, error) {
	payload := deviceCertificatePayload{
		DeviceID:            deviceID,
		AccountID:           accountID,
		DevicePubKey:        EncodePublicKey(devicePub),
		EncryptionPublicKey: EncodeEncryptionPublicKey(encryptionPub),
		IssuedAt:            now.UTC().Truncate(time.Second),
		ExpiresAt:           now.UTC().AddDate(1, 0, 0).Truncate(time.Second),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode device certificate: %w", err)
	}
	cert := DeviceCertificate{
		DeviceID:            payload.DeviceID,
		AccountID:           payload.AccountID,
		DevicePubKey:        payload.DevicePubKey,
		EncryptionPublicKey: payload.EncryptionPublicKey,
		IssuedAt:            payload.IssuedAt,
		ExpiresAt:           payload.ExpiresAt,
		Signature:           base64.RawStdEncoding.EncodeToString(ed25519.Sign(accountPriv, data)),
	}
	out, err := json.Marshal(cert)
	if err != nil {
		return nil, fmt.Errorf("encode signed device certificate: %w", err)
	}
	return out, nil
}

// EncodeEncryptionPublicKey encodes an X25519 public key.
func EncodeEncryptionPublicKey(pub []byte) string {
	if len(pub) == 0 {
		return ""
	}
	return "x25519:" + base64.RawStdEncoding.EncodeToString(pub)
}

// DecodeEncryptionPublicKey decodes a key produced by
// EncodeEncryptionPublicKey.
func DecodeEncryptionPublicKey(encoded string) ([]byte, error) {
	const prefix = "x25519:"
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) <= len(prefix) || encoded[:len(prefix)] != prefix {
		return nil, errors.New("unsupported encryption key format")
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("encryption key has %d bytes, want 32", len(raw))
	}
	return raw, nil
}

// ParseDeviceCertificate decodes an unverified device certificate. Callers
// must still verify its signature and expiry before trusting it.
func ParseDeviceCertificate(data []byte) (*DeviceCertificate, error) {
	var cert DeviceCertificate
	if err := json.Unmarshal(data, &cert); err != nil {
		return nil, fmt.Errorf("decode device certificate: %w", err)
	}
	if cert.DeviceID == "" || cert.AccountID == "" || cert.DevicePubKey == "" ||
		cert.Signature == "" || cert.IssuedAt.IsZero() || cert.ExpiresAt.IsZero() {
		return nil, errors.New("device certificate is incomplete")
	}
	return &cert, nil
}

// VerifyDeviceCertificate verifies a certificate against an account key,
// including its expiry.
func VerifyDeviceCertificate(data []byte, accountPub ed25519.PublicKey) (*DeviceCertificate, error) {
	cert, err := VerifyDeviceCertificateSignature(data, accountPub)
	if err != nil {
		return nil, err
	}
	if cert.ExpiresAt.IsZero() || !time.Now().UTC().Before(cert.ExpiresAt) {
		return nil, errors.New("device certificate has expired")
	}
	return cert, nil
}

// VerifyDeviceCertificateSignature verifies that a certificate was signed by
// the account key without enforcing its expiry. Account-record sync uses this
// so a device that has been offline past its certificate lifetime can still
// reconnect long enough to receive a renewed certificate.
func VerifyDeviceCertificateSignature(data []byte, accountPub ed25519.PublicKey) (*DeviceCertificate, error) {
	if len(accountPub) != ed25519.PublicKeySize {
		return nil, errors.New("account public key is invalid")
	}
	var cert DeviceCertificate
	if err := json.Unmarshal(data, &cert); err != nil {
		return nil, fmt.Errorf("decode device certificate: %w", err)
	}
	sig, err := base64.RawStdEncoding.DecodeString(cert.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode device certificate signature: %w", err)
	}
	payload := deviceCertificatePayload{
		DeviceID:            cert.DeviceID,
		AccountID:           cert.AccountID,
		DevicePubKey:        cert.DevicePubKey,
		EncryptionPublicKey: cert.EncryptionPublicKey,
		IssuedAt:            cert.IssuedAt,
		ExpiresAt:           cert.ExpiresAt,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode device certificate payload: %w", err)
	}
	if !ed25519.Verify(accountPub, canonical, sig) {
		return nil, errors.New("device certificate signature is invalid")
	}
	return &cert, nil
}

const accountRevocationPurpose = "toget:account-device-revocation:v1"

type accountRevocationPayload struct {
	Purpose   string    `json:"purpose"`
	AccountID string    `json:"account_id"`
	DeviceID  string    `json:"device_id"`
	RevokedAt time.Time `json:"revoked_at"`
}

// AccountRevocation is an account-key-signed statement that one device is
// revoked. It is separate from a device certificate so revocations can be
// relayed by any device on the account and can never be confused with a
// certificate signature.
type AccountRevocation struct {
	AccountID string    `json:"account_id"`
	DeviceID  string    `json:"device_id"`
	RevokedAt time.Time `json:"revoked_at"`
	Signature string    `json:"signature"`
}

// SignAccountRevocation signs a device revocation with the account key.
func SignAccountRevocation(accountID, deviceID string, revokedAt time.Time, accountPriv ed25519.PrivateKey) (string, error) {
	if accountID == "" || deviceID == "" || revokedAt.IsZero() {
		return "", errors.New("account revocation is incomplete")
	}
	if len(accountPriv) != ed25519.PrivateKeySize {
		return "", errors.New("account signing key is invalid")
	}
	revokedAt = revokedAt.UTC().Truncate(time.Millisecond)
	payload := accountRevocationPayload{
		Purpose: accountRevocationPurpose, AccountID: accountID,
		DeviceID: deviceID, RevokedAt: revokedAt,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode account revocation: %w", err)
	}
	statement := AccountRevocation{
		AccountID: accountID, DeviceID: deviceID, RevokedAt: revokedAt,
		Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(accountPriv, canonical)),
	}
	data, err := json.Marshal(statement)
	if err != nil {
		return "", fmt.Errorf("encode signed account revocation: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(data), nil
}

// VerifyAccountRevocation verifies an account-signed device revocation.
func VerifyAccountRevocation(encoded string, accountPub ed25519.PublicKey) (*AccountRevocation, error) {
	if len(accountPub) != ed25519.PublicKeySize {
		return nil, errors.New("account public key is invalid")
	}
	if encoded == "" || len(encoded) > 4096 {
		return nil, errors.New("account revocation is missing or too large")
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode account revocation: %w", err)
	}
	var statement AccountRevocation
	if err := json.Unmarshal(data, &statement); err != nil {
		return nil, fmt.Errorf("decode account revocation payload: %w", err)
	}
	if statement.AccountID == "" || statement.DeviceID == "" || statement.RevokedAt.IsZero() || statement.Signature == "" {
		return nil, errors.New("account revocation is incomplete")
	}
	sig, err := base64.RawStdEncoding.DecodeString(statement.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode account revocation signature: %w", err)
	}
	payload := accountRevocationPayload{
		Purpose: accountRevocationPurpose, AccountID: statement.AccountID,
		DeviceID: statement.DeviceID, RevokedAt: statement.RevokedAt.UTC().Truncate(time.Millisecond),
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode account revocation payload: %w", err)
	}
	if !ed25519.Verify(accountPub, canonical, sig) {
		return nil, errors.New("account revocation signature is invalid")
	}
	return &statement, nil
}

const accountKeyRotationPurpose = "toget:account-key-rotation:v1"

type accountKeyRotationPayload struct {
	Purpose           string    `json:"purpose"`
	AccountID         string    `json:"account_id"`
	Generation        int       `json:"generation"`
	PreviousPublicKey string    `json:"previous_public_key"`
	NewPublicKey      string    `json:"new_public_key"`
	RotatedAt         time.Time `json:"rotated_at"`
}

// AccountKeyRotation proves that an account key was replaced. Both the old and
// the new key sign the statement, so a holder of only one of them cannot forge
// a rotation. Clients apply statements in generation order.
type AccountKeyRotation struct {
	AccountID         string    `json:"account_id"`
	Generation        int       `json:"generation"`
	PreviousPublicKey []byte    `json:"previous_public_key"`
	NewPublicKey      []byte    `json:"new_public_key"`
	RotatedAt         time.Time `json:"rotated_at"`
	PreviousSignature string    `json:"previous_signature"`
	NewSignature      string    `json:"new_signature"`
}

// SignAccountKeyRotation creates a rotation statement signed by both keys.
func SignAccountKeyRotation(accountID string, generation int, previousPub ed25519.PublicKey, previousPriv ed25519.PrivateKey, newPub ed25519.PublicKey, newPriv ed25519.PrivateKey, rotatedAt time.Time) (string, error) {
	if accountID == "" || generation < 2 {
		return "", errors.New("account key rotation is incomplete")
	}
	if len(previousPub) != ed25519.PublicKeySize || len(newPub) != ed25519.PublicKeySize ||
		len(previousPriv) != ed25519.PrivateKeySize || len(newPriv) != ed25519.PrivateKeySize {
		return "", errors.New("account key rotation keys are invalid")
	}
	if !previousPriv.Public().(ed25519.PublicKey).Equal(previousPub) ||
		!newPriv.Public().(ed25519.PublicKey).Equal(newPub) {
		return "", errors.New("account key rotation keys do not match")
	}
	if previousPub.Equal(newPub) {
		return "", errors.New("account key rotation reuses the previous key")
	}
	rotatedAt = rotatedAt.UTC().Truncate(time.Millisecond)
	payload := accountKeyRotationPayload{
		Purpose: accountKeyRotationPurpose, AccountID: accountID, Generation: generation,
		PreviousPublicKey: EncodePublicKey(previousPub), NewPublicKey: EncodePublicKey(newPub),
		RotatedAt: rotatedAt,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode account key rotation: %w", err)
	}
	statement := AccountKeyRotation{
		AccountID: accountID, Generation: generation,
		PreviousPublicKey: append([]byte(nil), previousPub...),
		NewPublicKey:      append([]byte(nil), newPub...),
		RotatedAt:         rotatedAt,
		PreviousSignature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(previousPriv, canonical)),
		NewSignature:      base64.RawStdEncoding.EncodeToString(ed25519.Sign(newPriv, canonical)),
	}
	data, err := json.Marshal(statement)
	if err != nil {
		return "", fmt.Errorf("encode signed account key rotation: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(data), nil
}

// VerifyAccountKeyRotation verifies both signatures and the statement shape.
// It does not decide whether the statement belongs to the caller's chain.
func VerifyAccountKeyRotation(encoded string) (*AccountKeyRotation, error) {
	if encoded == "" || len(encoded) > 16<<10 {
		return nil, errors.New("account key rotation is missing or too large")
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode account key rotation: %w", err)
	}
	var statement AccountKeyRotation
	if err := json.Unmarshal(data, &statement); err != nil {
		return nil, fmt.Errorf("decode account key rotation payload: %w", err)
	}
	if statement.AccountID == "" || statement.Generation < 2 ||
		statement.RotatedAt.IsZero() || statement.PreviousSignature == "" ||
		statement.NewSignature == "" {
		return nil, errors.New("account key rotation is incomplete")
	}
	if len(statement.PreviousPublicKey) != ed25519.PublicKeySize ||
		len(statement.NewPublicKey) != ed25519.PublicKeySize ||
		ed25519.PublicKey(statement.PreviousPublicKey).Equal(ed25519.PublicKey(statement.NewPublicKey)) {
		return nil, errors.New("account key rotation keys are invalid")
	}
	payload := accountKeyRotationPayload{
		Purpose: accountKeyRotationPurpose, AccountID: statement.AccountID,
		Generation:        statement.Generation,
		PreviousPublicKey: EncodePublicKey(statement.PreviousPublicKey),
		NewPublicKey:      EncodePublicKey(statement.NewPublicKey),
		RotatedAt:         statement.RotatedAt.UTC().Truncate(time.Millisecond),
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode account key rotation payload: %w", err)
	}
	previousSig, err := base64.RawStdEncoding.DecodeString(statement.PreviousSignature)
	if err != nil {
		return nil, errors.New("account key rotation previous signature is invalid")
	}
	if !ed25519.Verify(ed25519.PublicKey(statement.PreviousPublicKey), canonical, previousSig) {
		return nil, errors.New("account key rotation previous signature is invalid")
	}
	newSig, err := base64.RawStdEncoding.DecodeString(statement.NewSignature)
	if err != nil {
		return nil, errors.New("account key rotation new signature is invalid")
	}
	if !ed25519.Verify(ed25519.PublicKey(statement.NewPublicKey), canonical, newSig) {
		return nil, errors.New("account key rotation new signature is invalid")
	}
	statement.RotatedAt = statement.RotatedAt.UTC().Truncate(time.Millisecond)
	return &statement, nil
}

// VerifyAccountKeyChain verifies every statement and returns the public key at
// each generation covered by the chain. Conflicting statements at the same
// generation fail closed.
func VerifyAccountKeyChain(accountID string, chain []string) (map[int]ed25519.PublicKey, error) {
	if len(chain) > 64 {
		return nil, errors.New("account key rotation chain is too long")
	}
	keys := make(map[int]ed25519.PublicKey)
	seen := make(map[string]bool, len(chain))
	for _, encoded := range chain {
		if seen[encoded] {
			continue
		}
		seen[encoded] = true
		statement, err := VerifyAccountKeyRotation(encoded)
		if err != nil {
			return nil, err
		}
		if statement.AccountID != accountID {
			return nil, errors.New("account key rotation belongs to another account")
		}
		previous := ed25519.PublicKey(statement.PreviousPublicKey)
		next := ed25519.PublicKey(statement.NewPublicKey)
		if existing, ok := keys[statement.Generation]; ok && !existing.Equal(next) {
			return nil, errors.New("account key rotation chain forks")
		}
		keys[statement.Generation] = next
		if existing, ok := keys[statement.Generation-1]; ok && !existing.Equal(previous) {
			return nil, errors.New("account key rotation chain is not linear")
		}
		keys[statement.Generation-1] = previous
	}
	if len(keys) == 0 {
		return nil, errors.New("account key rotation chain is empty")
	}
	return keys, nil
}

// AccountPublicKeyAtGeneration returns the account public key for generation
// from a verified rotation chain. Generation 1 is the original key.
func AccountPublicKeyAtGeneration(chain []string, accountID string, generation int) (ed25519.PublicKey, error) {
	if generation < 1 {
		return nil, errors.New("account key generation is invalid")
	}
	keys, err := VerifyAccountKeyChain(accountID, chain)
	if err != nil {
		return nil, err
	}
	key, ok := keys[generation]
	if !ok {
		return nil, errors.New("account key generation is not in the rotation chain")
	}
	return key, nil
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
