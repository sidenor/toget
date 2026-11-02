//go:build !js

package hosted

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
)

const federatedAccountPurpose = "toget:host-federated-account:v1"

type federatedAccountPayload struct {
	Purpose       string            `json:"purpose"`
	Protocol      int               `json:"protocol"`
	AccountID     string            `json:"account_id"`
	Name          string            `json:"name"`
	HostID        string            `json:"host_id"`
	HostCode      string            `json:"host_code"`
	HostURL       string            `json:"host_url"`
	HostPublicKey []byte            `json:"host_public_key"`
	PublicKey     []byte            `json:"public_key"`
	KeyGeneration int               `json:"key_generation"`
	KeyRotations  []string          `json:"key_rotations,omitempty"`
	Devices       []FederatedDevice `json:"devices"`
	IssuedAt      time.Time         `json:"issued_at"`
	ExpiresAt     time.Time         `json:"expires_at"`
}

func federatedAccountBytes(record FederatedAccount) ([]byte, error) {
	return json.Marshal(federatedAccountPayload{
		Purpose: federatedAccountPurpose, Protocol: record.Protocol,
		AccountID: record.AccountID, Name: record.Name,
		HostID: record.HostID, HostCode: record.HostCode, HostURL: record.HostURL,
		HostPublicKey: record.HostPublicKey,
		PublicKey:     record.PublicKey, KeyGeneration: record.KeyGeneration,
		KeyRotations: record.KeyRotations, Devices: record.Devices,
		IssuedAt: record.IssuedAt, ExpiresAt: record.ExpiresAt,
	})
}

// SignFederatedAccount signs a federation account statement with the host
// root key.
func SignFederatedAccount(record FederatedAccount, hostKey ed25519.PrivateKey) (FederatedAccount, error) {
	if len(hostKey) != ed25519.PrivateKeySize {
		return FederatedAccount{}, errors.New("host signing key is invalid")
	}
	record.HostPublicKey = append([]byte(nil), hostKey.Public().(ed25519.PublicKey)...)
	payload, err := federatedAccountBytes(record)
	if err != nil {
		return FederatedAccount{}, err
	}
	record.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(hostKey, payload))
	return record, nil
}

// VerifyFederatedAccount verifies a host-signed account statement at the
// current time.
func VerifyFederatedAccount(record FederatedAccount, hostKey ed25519.PublicKey) error {
	return VerifyFederatedAccountAt(record, hostKey, time.Now().UTC())
}

// VerifyFederatedAccountAt verifies a host-signed account statement. The
// caller must supply the root key from the sending host's verified identity
// manifest.
func VerifyFederatedAccountAt(record FederatedAccount, hostKey ed25519.PublicKey, now time.Time) error {
	if len(hostKey) != ed25519.PublicKeySize {
		return errors.New("host root key is invalid")
	}
	if record.Protocol != Protocol || !digits(record.AccountID, 14) ||
		record.Name == "" || len(record.Name) > MaxNameLength ||
		record.HostID == "" || record.HostCode == "" ||
		len(record.PublicKey) != ed25519.PublicKeySize ||
		len(record.HostPublicKey) != ed25519.PublicKeySize ||
		record.IssuedAt.IsZero() || record.ExpiresAt.IsZero() {
		return errors.New("federated account is incomplete")
	}
	if !equalBytes(record.HostPublicKey, hostKey) {
		return errors.New("federated account host key does not match the caller")
	}
	if record.HostID != identity.ServerID(hostKey) {
		return errors.New("federated account host does not match its key")
	}
	if record.HostCode != record.AccountID[:8] {
		return errors.New("federated account host code does not match the account")
	}
	if len(record.Devices) > account.MaxDeviceRecords {
		return errors.New("federated account has too many devices")
	}
	now = now.UTC()
	if record.IssuedAt.After(now.Add(5*time.Minute)) || !now.Before(record.ExpiresAt) {
		return errors.New("federated account is not fresh")
	}
	signature, err := base64.RawStdEncoding.DecodeString(record.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("federated account signature is malformed")
	}
	payload, err := federatedAccountBytes(record)
	if err != nil {
		return err
	}
	if !ed25519.Verify(hostKey, payload, signature) {
		return errors.New("federated account signature is invalid")
	}
	return nil
}

// VerifyFederatedDeviceCertificate checks that one device entry is a valid,
// unexpired certificate signed by an account key and that its keys agree with
// the listing.
func VerifyFederatedDeviceCertificate(device FederatedDevice, accountKeys []ed25519.PublicKey, now time.Time) (*identity.DeviceCertificate, error) {
	if device.ID == "" || len(device.SigningPublicKey) != ed25519.PublicKeySize ||
		len(device.EncryptionPublicKey) != 32 || len(device.Certificate) == 0 {
		return nil, errors.New("federated device is incomplete")
	}
	for _, key := range accountKeys {
		cert, err := identity.VerifyDeviceCertificateSignature(device.Certificate, key)
		if err != nil || cert.DeviceID != device.ID {
			continue
		}
		signing, err := identity.DecodePublicKey(cert.DevicePubKey)
		if err != nil || !equalBytes(signing, device.SigningPublicKey) {
			continue
		}
		encryption, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
		if err != nil || !equalBytes(encryption, device.EncryptionPublicKey) {
			continue
		}
		if cert.ExpiresAt.IsZero() || !now.UTC().Before(cert.ExpiresAt) {
			continue
		}
		return cert, nil
	}
	return nil, errors.New("federated device certificate is invalid")
}

// FederatedAccountKeys returns the account's current and historical signing
// keys. It validates the rotation chain.
func FederatedAccountKeys(record FederatedAccount) ([]ed25519.PublicKey, error) {
	return accountKeys(&AccountRecord{
		ID: record.AccountID, PublicKey: record.PublicKey,
		KeyGeneration: record.KeyGeneration, KeyRotations: record.KeyRotations,
	})
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := range left {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}

// normalizeFederationHostURL accepts only an HTTPS origin (or a loopback HTTP
// origin for local development) and drops a trailing slash.
func normalizeFederationHostURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("host URL is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", errors.New("host URL must use HTTPS")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}
