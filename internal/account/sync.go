package account

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

const (
	// MaxDeviceRecords bounds one account-sync payload.
	MaxDeviceRecords = 256
	// MaxNodeURLLength bounds the stored pinned address of a device.
	MaxNodeURLLength = 4096
	// MaxDeviceCertificateLength bounds a device certificate.
	MaxDeviceCertificateLength = 16 << 10

	maxDeviceIDLength   = 128
	maxDeviceNameLength = 64
	maxDeviceRoleLength = 32
)

var (
	// ErrTooManyDeviceRecords means one sync carried too many devices.
	ErrTooManyDeviceRecords = errors.New("too many device records")
	// ErrInvalidDeviceRecord means a synced device record is malformed.
	ErrInvalidDeviceRecord = errors.New("invalid device record")
)

// DeviceRecord is the synced, public description of one account device. It
// never carries private keys. A revoked record must include an account-signed
// revocation, which also lets any device relay the revocation to its peers.
type DeviceRecord struct {
	ID                  string     `json:"id"`
	AccountID           string     `json:"account_id"`
	Name                string     `json:"name"`
	PublicKey           []byte     `json:"public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key,omitempty"`
	Certificate         string     `json:"certificate"`
	NodeURL             string     `json:"node_url,omitempty"`
	Role                string     `json:"role"`
	Revocation          string     `json:"revocation,omitempty"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

// MergeResult describes what a merge changed.
type MergeResult struct {
	Devices    []Device
	Added      int
	Updated    int
	Revoked    int
	Skipped    int
	Warnings   []string
	RevokedIDs []string
}

// ExportDeviceRecords builds the account's device set for a peer. selfURL is
// this node's current pinned address; it overrides the stored value so peers
// learn the live path after a restart or network change.
func (s *Service) ExportDeviceRecords(ctx context.Context, selfURL string) ([]DeviceRecord, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := s.store.ListDevices(ctx, snapshot.Account.ID)
	if err != nil {
		return nil, err
	}
	if len(devices) > MaxDeviceRecords {
		return nil, ErrTooManyDeviceRecords
	}
	var accountKey ed25519.PrivateKey
	for i := range devices {
		if devices[i].RevokedAt != nil {
			accountKey, err = s.UnsealPrivateKey(snapshot.Account)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	out := make([]DeviceRecord, 0, len(devices))
	for i := range devices {
		device := &devices[i]
		nodeURL := device.NodeURL
		if device.ID == snapshot.Device.ID && strings.TrimSpace(selfURL) != "" && ValidNodeURL(selfURL) {
			nodeURL = strings.TrimSpace(selfURL)
		}
		record := DeviceRecord{
			ID: device.ID, AccountID: device.AccountID, Name: device.Name,
			PublicKey:           append([]byte(nil), device.PublicKey...),
			EncryptionPublicKey: append([]byte(nil), device.EncryptionPublicKey...),
			Certificate:         device.Certificate, NodeURL: nodeURL, Role: device.Role,
			RevokedAt: device.RevokedAt, CreatedAt: device.CreatedAt,
		}
		if device.RevokedAt != nil {
			proof, err := identity.SignAccountRevocation(snapshot.Account.ID, device.ID, *device.RevokedAt, accountKey)
			if err != nil {
				return nil, err
			}
			record.Revocation = proof
		}
		out = append(out, record)
	}
	return out, nil
}

// MergeDeviceRecords validates peer device records and merges additions,
// renewals, address hints, and revocations. Revocation is monotonic: a valid
// signed revocation always wins, and no later record can un-revoke a device.
// Malformed records are skipped with a warning so one bad peer cannot block
// the rest of the account from converging.
func (s *Service) MergeDeviceRecords(ctx context.Context, records []DeviceRecord) (*MergeResult, error) {
	if len(records) > MaxDeviceRecords {
		return nil, ErrTooManyDeviceRecords
	}
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	accountPub := ed25519.PublicKey(snapshot.Account.PublicKey)
	devices, err := s.store.ListDevices(ctx, snapshot.Account.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*Device, len(devices))
	for i := range devices {
		byID[devices[i].ID] = &devices[i]
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	result := &MergeResult{Warnings: make([]string, 0)}
	seen := make(map[string]bool, len(records))
	for i := range records {
		record := records[i]
		if seen[record.ID] {
			result.Skipped++
			result.Warnings = append(result.Warnings, "ignored a duplicate device record")
			continue
		}
		seen[record.ID] = true
		incoming, revokedAt, err := validateDeviceRecord(record, snapshot.Account.ID, accountPub, now)
		if err != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, err.Error())
			continue
		}
		if existing, ok := byID[incoming.ID]; ok {
			changed, newlyRevoked, err := mergeDevice(existing, incoming, revokedAt)
			if err != nil {
				result.Skipped++
				result.Warnings = append(result.Warnings, err.Error())
				continue
			}
			if !changed {
				continue
			}
			if err := s.store.UpdateDevice(ctx, existing); err != nil {
				return nil, err
			}
			result.Updated++
			if newlyRevoked {
				result.Revoked++
				result.RevokedIDs = append(result.RevokedIDs, existing.ID)
			}
			continue
		}
		if len(byID) >= MaxDeviceRecords {
			result.Skipped++
			result.Warnings = append(result.Warnings, "the account already has the maximum number of devices")
			continue
		}
		incoming.Local = false
		incoming.PrivateKey = nil
		incoming.EncryptionPrivateKey = nil
		incoming.RevokedAt = revokedAt
		if err := s.store.AddDevice(ctx, incoming); err != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("could not add a device: %v", err))
			continue
		}
		byID[incoming.ID] = incoming
		result.Added++
	}
	result.Devices, err = s.store.ListDevices(ctx, snapshot.Account.ID)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// validateDeviceRecord checks that a record's certificate binds its keys to
// this account. Expired certificates are accepted here because a device that
// was offline for a year still needs to sync a renewed certificate.
func validateDeviceRecord(record DeviceRecord, accountID string, accountPub ed25519.PublicKey, now time.Time) (*Device, *time.Time, error) {
	fail := func(format string, args ...any) (*Device, *time.Time, error) {
		return nil, nil, fmt.Errorf("%w: %s", ErrInvalidDeviceRecord, fmt.Sprintf(format, args...))
	}
	if record.ID == "" || len(record.ID) > maxDeviceIDLength {
		return fail("device id is invalid")
	}
	if record.AccountID != accountID {
		return fail("device belongs to another account")
	}
	if len(record.PublicKey) != ed25519.PublicKeySize {
		return fail("device signing key is invalid")
	}
	if len(record.EncryptionPublicKey) != 0 && len(record.EncryptionPublicKey) != 32 {
		return fail("device encryption key is invalid")
	}
	if record.Certificate == "" || len(record.Certificate) > MaxDeviceCertificateLength {
		return fail("device certificate is missing or too large")
	}
	cert, err := identity.VerifyDeviceCertificateSignature([]byte(record.Certificate), accountPub)
	if err != nil {
		return fail("device certificate: %v", err)
	}
	if cert.DeviceID != record.ID || cert.AccountID != accountID {
		return fail("device certificate identity does not match")
	}
	signingKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || !bytes.Equal(signingKey, record.PublicKey) {
		return fail("device signing key does not match its certificate")
	}
	encryptionKey, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
	if err != nil || !bytes.Equal(encryptionKey, record.EncryptionPublicKey) {
		return fail("device encryption key does not match its certificate")
	}
	name := strings.TrimSpace(record.Name)
	if name == "" || len(name) > maxDeviceNameLength {
		return fail("device name is invalid")
	}
	role := strings.TrimSpace(record.Role)
	if role == "" {
		role = "node"
	}
	if len(role) > maxDeviceRoleLength {
		return fail("device role is invalid")
	}
	nodeURL := strings.TrimSpace(record.NodeURL)
	if !ValidNodeURL(nodeURL) {
		return fail("device node address is invalid")
	}
	created := record.CreatedAt.UTC().Truncate(time.Millisecond)
	if created.IsZero() {
		created = cert.IssuedAt.UTC().Truncate(time.Millisecond)
	}
	if created.IsZero() || created.After(now.Add(5*time.Minute)) {
		return fail("device creation time is invalid")
	}
	var revokedAt *time.Time
	if record.Revocation != "" {
		proof, err := identity.VerifyAccountRevocation(record.Revocation, accountPub)
		if err != nil {
			return fail("device revocation: %v", err)
		}
		if proof.AccountID != accountID || proof.DeviceID != record.ID {
			return fail("device revocation identity does not match")
		}
		revoked := proof.RevokedAt.UTC().Truncate(time.Millisecond)
		revokedAt = &revoked
	} else if record.RevokedAt != nil {
		return fail("a revoked device is missing its account-signed revocation")
	}
	return &Device{
		ID: record.ID, AccountID: accountID, Name: name,
		PublicKey:           append([]byte(nil), record.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), record.EncryptionPublicKey...),
		Certificate:         record.Certificate, NodeURL: nodeURL, Role: role,
		RevokedAt: revokedAt, CreatedAt: created,
	}, revokedAt, nil
}

func mergeDevice(existing, incoming *Device, revokedAt *time.Time) (changed, newlyRevoked bool, err error) {
	if !bytes.Equal(existing.PublicKey, incoming.PublicKey) ||
		!bytes.Equal(existing.EncryptionPublicKey, incoming.EncryptionPublicKey) {
		return false, false, fmt.Errorf("%w: device keys do not match the existing device", ErrInvalidDeviceRecord)
	}
	if existing.RevokedAt != nil {
		// Revocation is permanent. Retain the earliest signed revocation time
		// but do not let a later record replace identity fields.
		if revokedAt != nil && revokedAt.Before(*existing.RevokedAt) {
			existing.RevokedAt = revokedAt
			return true, false, nil
		}
		return false, false, nil
	}
	if revokedAt != nil {
		existing.RevokedAt = revokedAt
		return true, true, nil
	}
	if incoming.Name != "" && incoming.Name != existing.Name {
		existing.Name = incoming.Name
		changed = true
	}
	if incoming.Role != "" && incoming.Role != existing.Role {
		existing.Role = incoming.Role
		changed = true
	}
	if certIssuedAfter(incoming.Certificate, existing.Certificate) {
		existing.Certificate = incoming.Certificate
		changed = true
	}
	if incoming.NodeURL != "" && incoming.NodeURL != existing.NodeURL && !existing.Local {
		existing.NodeURL = incoming.NodeURL
		changed = true
	}
	return changed, false, nil
}

func certIssuedAfter(candidate, current string) bool {
	candidateCert, err := identity.ParseDeviceCertificate([]byte(candidate))
	if err != nil {
		return false
	}
	currentCert, err := identity.ParseDeviceCertificate([]byte(current))
	if err != nil {
		return true
	}
	return candidateCert.ExpiresAt.After(currentCert.ExpiresAt)
}

// SetDeviceNodeURL records a device's last known pinned address. It is a
// reachability hint only; trust still comes from the device certificate.
func (s *Service) SetDeviceNodeURL(ctx context.Context, deviceID, nodeURL string) error {
	nodeURL = strings.TrimSpace(nodeURL)
	if !ValidNodeURL(nodeURL) {
		return ErrInvalidDeviceRecord
	}
	device, err := s.store.GetDeviceByID(ctx, deviceID)
	if err != nil {
		return err
	}
	if device.NodeURL == nodeURL {
		return nil
	}
	device.NodeURL = nodeURL
	return s.store.UpdateDevice(ctx, device)
}

// ValidNodeURL accepts an empty address or an HTTPS node address carrying a
// SHA-256 certificate fingerprint. Requiring the pin here keeps malformed
// reachability hints out of the device store; node client construction still
// performs the full endpoint and relay-route validation.
func ValidNodeURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	if len(raw) > MaxNodeURLLength {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" {
		return false
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		return false
	}
	fingerprint := values.Get("fp")
	if fingerprint == "" {
		fingerprint = values.Get("sha256")
	}
	fingerprint = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fingerprint)), "sha256:")
	decoded, err := hex.DecodeString(fingerprint)
	return err == nil && len(decoded) == sha256.Size
}
