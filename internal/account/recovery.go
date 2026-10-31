package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/vault"
)

const (
	// RecoveryCredentialVersion is the current recovery file format.
	RecoveryCredentialVersion = 1
	// RecoveryCredentialKind identifies a Toget device recovery file.
	RecoveryCredentialKind = "toget-device-recovery"
	// MaxRecoveryCredentialBytes bounds a credential submitted to the API.
	MaxRecoveryCredentialBytes = 16 << 10
)

var (
	// ErrInvalidRecoveryCredential means the file is malformed, fails its
	// checksum, or belongs to a different account or device.
	ErrInvalidRecoveryCredential = errors.New("invalid recovery credential")
	// ErrRecoveryUnavailable means this vault has no recovery wrap.
	ErrRecoveryUnavailable = errors.New("recovery is not configured on this device")
)

// RecoveryStatus is the public state of the local recovery wrap.
type RecoveryStatus struct {
	Enabled   bool       `json:"enabled"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	DeviceID  string     `json:"device_id"`
}

// RecoveryCredential is the JSON file a user stores offline. It contains the
// high-entropy key that unwraps this device's vault master key after a
// forgotten password. It is a bearer secret: anyone with the file and local
// access can reset the password.
type RecoveryCredential struct {
	Version           int       `json:"version"`
	Kind              string    `json:"kind"`
	AccountID         string    `json:"account_id"`
	DeviceID          string    `json:"device_id"`
	ServerID          string    `json:"server_id"`
	ServerFingerprint string    `json:"server_fingerprint"`
	CreatedAt         time.Time `json:"created_at"`
	Key               string    `json:"key"`
	Checksum          string    `json:"checksum"`
}

// Filename returns the suggested download name for this credential.
func (c *RecoveryCredential) Filename() string {
	device := c.DeviceID
	if len(device) > 8 {
		device = device[:8]
	}
	return fmt.Sprintf("toget-recovery-%s-%s.togetkey", c.AccountID, device)
}

// Encode returns the credential file content.
func (c *RecoveryCredential) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode recovery credential: %w", err)
	}
	return append(data, '\n'), nil
}

// Manifest describes the local vault and account that a recovery credential
// unlocks. Keep it separate from the file format.
type recoveryTarget struct {
	accountID         string
	deviceID          string
	serverID          string
	serverFingerprint string
}

func recoveryAAD(accountID, deviceID string) string {
	return "toget:vault-recovery:v1:" + accountID + ":" + deviceID
}

func recoveryChecksum(c *RecoveryCredential, key []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\n%s\n%s\n%s\n%s\n%s\n%s\n",
		c.Version, c.Kind, c.AccountID, c.DeviceID, c.ServerID,
		c.ServerFingerprint, c.CreatedAt.UTC().Format(time.RFC3339Nano))
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil))
}

// ParseRecoveryCredential validates a recovery file and returns its key.
func ParseRecoveryCredential(data []byte) (*RecoveryCredential, []byte, error) {
	if len(data) == 0 || len(data) > MaxRecoveryCredentialBytes {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c RecoveryCredential
	if err := dec.Decode(&c); err != nil {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	if c.Version != RecoveryCredentialVersion || c.Kind != RecoveryCredentialKind ||
		!isDigits(c.AccountID, 14) || strings.TrimSpace(c.DeviceID) == "" ||
		len(c.DeviceID) > maxDeviceIDLength || strings.TrimSpace(c.ServerID) == "" ||
		strings.TrimSpace(c.ServerFingerprint) == "" || c.CreatedAt.IsZero() {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	key, err := base64.RawURLEncoding.DecodeString(c.Key)
	if err != nil || len(key) != vault.RecoveryKeySize {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	want, err := hex.DecodeString(c.Checksum)
	if err != nil || len(want) != sha256.Size {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	got := recoveryChecksum(&c, key)
	if subtle.ConstantTimeCompare([]byte(c.Checksum), []byte(got)) != 1 {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	return &c, key, nil
}

// Recovery reports whether this device has a recovery key and when it was
// created.
func (s *Service) Recovery(ctx context.Context) (*RecoveryStatus, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	status := &RecoveryStatus{DeviceID: snapshot.Device.ID}
	if s.vault.RecoveryEnabled() {
		created := s.vault.RecoveryCreatedAt().UTC()
		status.Enabled = true
		status.CreatedAt = &created
	}
	return status, nil
}

// CreateRecovery generates a new recovery key, replaces any existing one, and
// returns the credential file. The vault must be unlocked because the current
// master key is being wrapped. The previous credential becomes void.
func (s *Service) CreateRecovery(ctx context.Context) (*RecoveryCredential, []byte, error) {
	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !s.vault.Ready() {
		return nil, nil, vault.ErrLocked
	}
	key := make([]byte, vault.RecoveryKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, fmt.Errorf("generate recovery key: %w", err)
	}
	defer zeroBytes(key)
	created := s.now().UTC().Truncate(time.Second)
	credential := &RecoveryCredential{
		Version:           RecoveryCredentialVersion,
		Kind:              RecoveryCredentialKind,
		AccountID:         snapshot.Account.ID,
		DeviceID:          snapshot.Device.ID,
		ServerID:          snapshot.Server.ID,
		ServerFingerprint: identity.Fingerprint(snapshot.Server.PublicKey),
		CreatedAt:         created,
		Key:               base64.RawURLEncoding.EncodeToString(key),
	}
	credential.Checksum = recoveryChecksum(credential, key)
	content, err := credential.Encode()
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.vault.SetRecoveryKey(key, recoveryAAD(snapshot.Account.ID, snapshot.Device.ID)); err != nil {
		return nil, nil, err
	}
	return credential, content, nil
}

// RemoveRecovery removes the local recovery wrap. The password is unchanged.
func (s *Service) RemoveRecovery(ctx context.Context) error {
	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	if _, err := s.Get(ctx); err != nil {
		return err
	}
	if !s.vault.Ready() {
		return vault.ErrLocked
	}
	if err := s.vault.RemoveRecoveryKey(); err != nil {
		if errors.Is(err, vault.ErrRecoveryNotConfigured) {
			return ErrRecoveryUnavailable
		}
		return err
	}
	return nil
}

// ResetPasswordWithRecovery changes the account password using a recovery
// file. It works while the vault is locked and preserves all sealed data. If
// the database verifier cannot be updated, or the vault cannot be rewrapped,
// the previous verifier is restored.
func (s *Service) ResetPasswordWithRecovery(ctx context.Context, credentialData []byte, newPassword string) (*Snapshot, error) {
	if len(newPassword) < minPasswordLength || len(newPassword) > maxPasswordLength {
		return nil, ErrInvalidPassword
	}
	credential, key, err := ParseRecoveryCredential(credentialData)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(key)

	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	want := recoveryTarget{
		accountID:         snapshot.Account.ID,
		deviceID:          snapshot.Device.ID,
		serverID:          snapshot.Server.ID,
		serverFingerprint: identity.Fingerprint(snapshot.Server.PublicKey),
	}
	if credential.AccountID != want.accountID || credential.DeviceID != want.deviceID ||
		credential.ServerID != want.serverID || credential.ServerFingerprint != want.serverFingerprint {
		return nil, ErrInvalidRecoveryCredential
	}
	if !s.vault.RecoveryEnabled() {
		return nil, ErrRecoveryUnavailable
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	oldHash := snapshot.Account.PasswordHash
	if err := s.store.UpdateAccountPasswordHash(ctx, snapshot.Account.ID, newHash); err != nil {
		return nil, err
	}
	if err := s.vault.ResetPasswordWithRecoveryKey(key, recoveryAAD(want.accountID, want.deviceID), newPassword); err != nil {
		if rollbackErr := s.store.UpdateAccountPasswordHash(ctx, snapshot.Account.ID, oldHash); rollbackErr != nil {
			return nil, fmt.Errorf("reset password: %w (restore verifier: %v)", err, rollbackErr)
		}
		return nil, err
	}
	return s.Get(ctx)
}

// ResetPasswordUnlocked resets the password while this device is unlocked.
// It is the local-administrator path for a forgotten password: the unlocked
// daemon already holds the vault master key, so it re-wraps that key without
// asking for the old password. Sealed history and the recovery wrap are
// preserved. Other devices are unchanged.
func (s *Service) ResetPasswordUnlocked(ctx context.Context, newPassword string) (*Snapshot, error) {
	if len(newPassword) < minPasswordLength || len(newPassword) > maxPasswordLength {
		return nil, ErrInvalidPassword
	}
	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !s.vault.Ready() {
		return nil, vault.ErrLocked
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	oldHash := snapshot.Account.PasswordHash
	if err := s.store.UpdateAccountPasswordHash(ctx, snapshot.Account.ID, newHash); err != nil {
		return nil, err
	}
	if err := s.vault.ResetPasswordUnlocked(newPassword); err != nil {
		if rollbackErr := s.store.UpdateAccountPasswordHash(ctx, snapshot.Account.ID, oldHash); rollbackErr != nil {
			return nil, fmt.Errorf("reset password: %w (restore verifier: %v)", err, rollbackErr)
		}
		return nil, err
	}
	return s.Get(ctx)
}

// ChangePassword changes the password using the current password. The vault
// may be locked; a successful change unlocks it. The recovery wrap, if any, is
// preserved.
func (s *Service) ChangePassword(ctx context.Context, currentPassword, newPassword string) (*Snapshot, error) {
	if len(newPassword) < minPasswordLength || len(newPassword) > maxPasswordLength {
		return nil, ErrInvalidPassword
	}
	s.passwordMu.Lock()
	defer s.passwordMu.Unlock()
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(snapshot.Account.PasswordHash, currentPassword) {
		return nil, ErrInvalidCredentials
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	oldHash := snapshot.Account.PasswordHash
	if err := s.store.UpdateAccountPasswordHash(ctx, snapshot.Account.ID, newHash); err != nil {
		return nil, err
	}
	if err := s.vault.ChangePassword(currentPassword, newPassword); err != nil {
		if rollbackErr := s.store.UpdateAccountPasswordHash(ctx, snapshot.Account.ID, oldHash); rollbackErr != nil {
			return nil, fmt.Errorf("change password: %w (restore verifier: %v)", err, rollbackErr)
		}
		return nil, err
	}
	return s.Get(ctx)
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
