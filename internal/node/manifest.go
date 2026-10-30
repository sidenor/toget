package node

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// Manifest announces a node's identity and pinned endpoint. It is signed by
// the account's server key, which is the same key that signs the account's
// public server manifest.
type Manifest struct {
	Format         string    `json:"format"`
	Version        int       `json:"version"`
	ServerID       string    `json:"server_id"`
	ServerCode     string    `json:"server_code"`
	AccountID      string    `json:"account_id"`
	Endpoint       string    `json:"endpoint"`
	TLSFingerprint string    `json:"tls_fingerprint"`
	Protocol       int       `json:"protocol"`
	IssuedAt       time.Time `json:"issued_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	Signature      string    `json:"signature,omitempty"`
}

// ManifestFormat identifies a Toget node manifest.
const ManifestFormat = "toget-node-manifest"

func signManifest(manifest *Manifest, key ed25519.PrivateKey) error {
	manifest.Format = ManifestFormat
	manifest.Version = 1
	manifest.Protocol = Protocol
	manifest.Signature = ""
	payload, err := manifestPayload(manifest)
	if err != nil {
		return err
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, payload))
	return nil
}

func manifestPayload(manifest *Manifest) ([]byte, error) {
	copy := *manifest
	copy.Signature = ""
	return json.Marshal(copy)
}

// VerifyManifest validates a node manifest against a server public key.
func VerifyManifest(manifest *Manifest, publicKey ed25519.PublicKey) error {
	if manifest == nil || manifest.Format != ManifestFormat || manifest.Version != 1 {
		return errors.New("unsupported node manifest")
	}
	if manifest.Protocol != Protocol {
		return errors.New("unsupported node protocol")
	}
	if manifest.ServerID == "" || manifest.AccountID == "" || manifest.Endpoint == "" {
		return errors.New("node manifest is incomplete")
	}
	if _, err := ParseFingerprint(manifest.TLSFingerprint); err != nil {
		return err
	}
	if manifest.ExpiresAt.IsZero() || time.Now().UTC().After(manifest.ExpiresAt) {
		return errors.New("node manifest has expired")
	}
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("node manifest signature is malformed")
	}
	payload, err := manifestPayload(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("node manifest signature is invalid")
	}
	return nil
}
