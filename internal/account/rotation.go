package account

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/sealbox"
	"github.com/sidenor/toget/internal/vault"
)

var (
	// ErrKeyRotationConflict means a peer proposed a different key at a
	// generation this device already knows. The device fails closed.
	ErrKeyRotationConflict = errors.New("account key rotation conflicts with the local key history")
	// ErrKeyRotationGap means a rotation chain skips a generation.
	ErrKeyRotationGap = errors.New("account key rotation chain has a gap")
	// ErrKeyRotationEnvelope means the new account key did not arrive sealed
	// to this device.
	ErrKeyRotationEnvelope = errors.New("account key rotation is missing a usable key envelope")
)

// RotateKeyResult summarizes an account-key rotation.
type RotateKeyResult struct {
	Generation int    `json:"generation"`
	PublicKey  []byte `json:"public_key"`
	Devices    int    `json:"devices"`
}

// RotateKey replaces the account signing key, re-signs every active device
// certificate, and persists the new key encrypted by the local vault. Devices
// receive it through account sync. A revoked device is not re-signed.
func (s *Service) RotateKey(ctx context.Context) (*RotateKeyResult, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Device.RevokedAt != nil {
		return nil, ErrCurrentDevice
	}
	if !s.vault.Ready() {
		return nil, vault.ErrLocked
	}
	oldKey, err := s.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		return nil, err
	}
	generation := snapshot.Account.KeyGeneration
	if generation <= 0 {
		generation = 1
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	newPub, newPriv, err := identity.NewAccountKey()
	if err != nil {
		return nil, err
	}
	statement, err := identity.SignAccountKeyRotation(
		snapshot.Account.ID, generation+1, ed25519.PublicKey(snapshot.Account.PublicKey), oldKey,
		newPub, newPriv, now,
	)
	if err != nil {
		return nil, err
	}
	devices, err := s.store.ListDevices(ctx, snapshot.Account.ID)
	if err != nil {
		return nil, err
	}
	updated := make([]Device, 0, len(devices))
	for i := range devices {
		device := devices[i]
		if device.RevokedAt == nil {
			cert, err := identity.SignDeviceCertificate(device.ID, snapshot.Account.ID,
				device.PublicKey, device.EncryptionPublicKey, newPriv, now)
			if err != nil {
				return nil, err
			}
			device.Certificate = string(cert)
		}
		updated = append(updated, device)
	}
	sealed, err := s.vault.Seal(newPriv, aadAccountKey)
	if err != nil {
		return nil, fmt.Errorf("seal rotated account key: %w", err)
	}
	rotated := *snapshot.Account
	rotated.PublicKey = append([]byte(nil), newPub...)
	rotated.PrivateKey = []byte(sealed)
	rotated.KeyGeneration = generation + 1
	rotated.KeyRotations = append(append([]string(nil), snapshot.Account.KeyRotations...), statement)
	if err := s.store.UpdateAccountKey(ctx, &rotated, updated); err != nil {
		return nil, err
	}
	signed := 0
	for i := range updated {
		if updated[i].RevokedAt == nil {
			signed++
		}
	}
	return &RotateKeyResult{Generation: rotated.KeyGeneration, PublicKey: newPub, Devices: signed}, nil
}

// ApplyAccountKeyRotations verifies a peer's rotation chain and, when it moves
// this device forward, installs the new account key from its sealed envelope.
// It returns whether anything changed.
func (s *Service) ApplyAccountKeyRotations(ctx context.Context, chain []string, envelope string) (*Snapshot, bool, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(chain) == 0 {
		return snapshot, false, nil
	}
	currentGen := snapshot.Account.KeyGeneration
	if currentGen <= 0 {
		currentGen = 1
	}
	keys, err := identity.VerifyAccountKeyChain(snapshot.Account.ID, chain)
	if err != nil {
		return nil, false, err
	}
	newest := 0
	for generation := range keys {
		if generation > newest {
			newest = generation
		}
	}
	if newest < currentGen {
		return snapshot, false, nil
	}
	if key, ok := keys[currentGen]; !ok || !bytes.Equal(key, snapshot.Account.PublicKey) {
		if ok {
			return nil, false, ErrKeyRotationConflict
		}
		return nil, false, ErrKeyRotationGap
	}
	// The chain must be contiguous up to its newest generation.
	for generation := 2; generation <= newest; generation++ {
		if _, ok := keys[generation]; !ok {
			return nil, false, ErrKeyRotationGap
		}
	}
	// A different key at a generation this device already stores is a fork.
	if len(snapshot.Account.KeyRotations) > 0 {
		stored, err := identity.VerifyAccountKeyChain(snapshot.Account.ID, snapshot.Account.KeyRotations)
		if err != nil {
			return nil, false, fmt.Errorf("local account key history: %w", err)
		}
		for generation, key := range stored {
			if incoming, ok := keys[generation]; ok && !bytes.Equal(incoming, key) {
				return nil, false, ErrKeyRotationConflict
			}
		}
	}
	if newest == currentGen {
		return snapshot, false, nil
	}
	targetGen := newest
	if !s.vault.Ready() {
		return nil, false, vault.ErrLocked
	}
	if envelope == "" {
		return nil, false, ErrKeyRotationEnvelope
	}
	encryptionKey, err := s.UnsealDeviceEncryptionKey(snapshot.Device)
	if err != nil {
		return nil, false, err
	}
	plaintext, err := sealbox.Open(encryptionKey,
		[]byte(envelope), accountKeyEnvelopeContext(snapshot.Account.ID, targetGen, snapshot.Device.ID))
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrKeyRotationEnvelope, err)
	}
	if len(plaintext) != ed25519.PrivateKeySize {
		return nil, false, fmt.Errorf("%w: account key has the wrong size", ErrKeyRotationEnvelope)
	}
	newPriv := ed25519.PrivateKey(plaintext)
	newPub := keys[targetGen]
	if !bytes.Equal(newPriv.Public().(ed25519.PublicKey), newPub) {
		return nil, false, fmt.Errorf("%w: account key does not match the rotation proof", ErrKeyRotationEnvelope)
	}
	sealed, err := s.vault.Seal(newPriv, aadAccountKey)
	if err != nil {
		return nil, false, fmt.Errorf("seal rotated account key: %w", err)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	cert, err := identity.SignDeviceCertificate(snapshot.Device.ID, snapshot.Account.ID,
		snapshot.Device.PublicKey, snapshot.Device.EncryptionPublicKey, newPriv, now)
	if err != nil {
		return nil, false, err
	}
	device := *snapshot.Device
	device.Certificate = string(cert)
	rotated := *snapshot.Account
	rotated.PublicKey = append([]byte(nil), newPub...)
	rotated.PrivateKey = []byte(sealed)
	rotated.KeyGeneration = targetGen
	rotated.KeyRotations = canonicalRotationChain(snapshot.Account.KeyRotations, chain)
	if err := s.store.UpdateAccountKey(ctx, &rotated, []Device{device}); err != nil {
		return nil, false, err
	}
	return &Snapshot{Account: &rotated, Server: snapshot.Server, Device: &device}, true, nil
}

// SealAccountKeyForDevice wraps the current account private key to one active
// sibling device. ctx binds the envelope to the account, generation, and
// recipient, so it cannot be replayed to another device.
func (s *Service) SealAccountKeyForDevice(ctx context.Context, deviceID string) (int, string, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return 0, "", err
	}
	device, err := s.store.GetDeviceByID(ctx, deviceID)
	if err != nil {
		return 0, "", err
	}
	if device.AccountID != snapshot.Account.ID || device.RevokedAt != nil {
		return 0, "", ErrNotFound
	}
	return s.SealAccountKeyFor(ctx, device.ID, device.EncryptionPublicKey)
}

// SealAccountKeyFor wraps the current account private key to a recipient
// device ID and encryption public key. The caller may know the recipient from
// an account-sync record rather than from the local device store.
func (s *Service) SealAccountKeyFor(ctx context.Context, deviceID string, encryptionPublicKey []byte) (int, string, error) {
	if deviceID == "" || len(encryptionPublicKey) != 32 {
		return 0, "", ErrKeyRotationEnvelope
	}
	snapshot, err := s.Get(ctx)
	if err != nil {
		return 0, "", err
	}
	if !s.vault.Ready() {
		return 0, "", vault.ErrLocked
	}
	key, err := s.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		return 0, "", err
	}
	generation := snapshot.Account.KeyGeneration
	if generation <= 0 {
		generation = 1
	}
	sealed, err := sealbox.Seal(encryptionPublicKey, key,
		accountKeyEnvelopeContext(snapshot.Account.ID, generation, deviceID))
	if err != nil {
		return 0, "", err
	}
	return generation, string(sealed), nil
}

func accountKeyEnvelopeContext(accountID string, generation int, deviceID string) string {
	return fmt.Sprintf("toget:account-key-envelope:v1|%s|%d|%s", accountID, generation, deviceID)
}

// canonicalRotationChain keeps both the local history and every incoming
// statement. A peer may send only an incremental chain; retaining older local
// statements lets this device later prove the full history to a device that is
// still several generations behind.
func canonicalRotationChain(current, incoming []string) []string {
	out := make([]string, 0, len(current)+len(incoming))
	seen := make(map[string]bool, len(current)+len(incoming))
	for _, statement := range current {
		if !seen[statement] {
			seen[statement] = true
			out = append(out, statement)
		}
	}
	for _, statement := range incoming {
		if !seen[statement] {
			seen[statement] = true
			out = append(out, statement)
		}
	}
	return out
}
