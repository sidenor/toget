package node

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/sidenor/toget/internal/relayproto"
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
	RelayURL       string    `json:"relay_url,omitempty"`
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

// VerifyManifest validates a current node manifest against a server public key.
func VerifyManifest(manifest *Manifest, publicKey ed25519.PublicKey) error {
	return verifyManifest(manifest, publicKey, true)
}

// verifyManifestSignature checks a node manifest without requiring it to be
// fresh. A card can be imported after its 24-hour node manifest has expired;
// the card is still only a hint and the live node is verified before use.
func verifyManifestSignature(manifest *Manifest, publicKey ed25519.PublicKey) error {
	return verifyManifest(manifest, publicKey, false)
}

func verifyManifest(manifest *Manifest, publicKey ed25519.PublicKey, requireFresh bool) error {
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
	if manifest.RelayURL != "" {
		_, _, target, err := relayproto.ParseNodeRoute(manifest.RelayURL)
		if err != nil {
			return err
		}
		if target != manifest.ServerID {
			return errors.New("node manifest relay target does not match its server ID")
		}
	}
	now := time.Now().UTC()
	if manifest.IssuedAt.IsZero() || manifest.IssuedAt.After(now.Add(5*time.Minute)) {
		return errors.New("node manifest was issued in the future")
	}
	if requireFresh && (manifest.ExpiresAt.IsZero() || !now.Before(manifest.ExpiresAt)) {
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
