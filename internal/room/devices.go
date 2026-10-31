package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

// DeviceRoomSnapshot is a room's membership, device registry, and epoch keys
// wrapped to a newly paired device. It deliberately carries no events: the
// new device fetches the signed log from the room's home node, which keeps
// pairing cheap and lets the home enforce its own sync authorization.
type DeviceRoomSnapshot struct {
	Room    Room         `json:"room"`
	Members []WireMember `json:"members"`
	Devices []WireDevice `json:"devices"`
	Keys    []BundleKey  `json:"keys"`
}

const (
	// MaxDeviceSnapshotEpochs bounds epoch keys carried by one pairing
	// snapshot.
	MaxDeviceSnapshotEpochs = 10000
	// MaxDeviceSnapshotRooms bounds rooms transferred during pairing.
	MaxDeviceSnapshotRooms = 2000
)

// ExportDeviceRoom wraps every epoch key this node holds for a room to a new
// device on the same account. The caller must be an active device in the
// room; the recipient account must match the caller's account.
func (s *Service) ExportDeviceRoom(ctx context.Context, roomID, exporterDeviceID, recipientAccountID, recipientDeviceID string, recipientEncryptionKey []byte) (*DeviceRoomSnapshot, error) {
	if len(recipientEncryptionKey) != 32 || recipientDeviceID == "" {
		return nil, ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exporter, err := s.store.GetRoomDevice(ctx, roomID, exporterDeviceID)
	if err != nil || exporter.RevokedAt != nil {
		return nil, ErrForbidden
	}
	if exporter.AccountID != recipientAccountID {
		return nil, ErrForbidden
	}
	member, err := s.store.GetMember(ctx, roomID, exporter.AccountID)
	if err != nil || member.Status != "active" {
		return nil, ErrNotMember
	}
	current, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	members, err := s.store.ListMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	devices, err := s.store.ListRoomDevices(ctx, roomID)
	if err != nil {
		return nil, err
	}
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if len(epochs) > MaxDeviceSnapshotEpochs {
		return nil, fmt.Errorf("%w: room has too many epochs to pair", ErrInvalidEvent)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	keys := make([]BundleKey, 0, len(epochs))
	for i := range epochs {
		key, err := s.roomKeyLocked(ctx, roomID, epochs[i].Epoch)
		if err != nil {
			// Missing local key material is not fatal during pairing: every
			// key this device can read is still copied, and the home node can
			// register the device and provide the rest during sync.
			continue
		}
		wrapped, err := WrapRoomKey(roomID, epochs[i].Epoch, recipientDeviceID, recipientEncryptionKey, key)
		if err != nil {
			return nil, err
		}
		keys = append(keys, BundleKey{Epoch: epochs[i].Epoch, Wrapped: wrapped, CreatedAt: now})
	}
	return &DeviceRoomSnapshot{
		Room:    *current,
		Members: limitMembers(WireMembers(members)),
		Devices: limitDevices(WireDevices(devices)),
		Keys:    keys,
	}, nil
}

// ImportDeviceRoom installs a paired room snapshot without events. Events are
// fetched by normal sync so their signatures and hash chain are verified in
// sequence. Importing the same snapshot again is idempotent.
func (s *Service) ImportDeviceRoom(ctx context.Context, snapshot *DeviceRoomSnapshot, accountID, localDeviceID string, encryptionPrivateKey []byte) (*Room, error) {
	if snapshot == nil || snapshot.Room.ID == "" || snapshot.Room.HomeServerID == "" {
		return nil, fmt.Errorf("%w: room snapshot is incomplete", ErrInvalidEvent)
	}
	if len(snapshot.Members) > MaxBundleMembers || len(snapshot.Devices) > MaxBundleDevices {
		return nil, fmt.Errorf("%w: room snapshot is too large", ErrInvalidEvent)
	}
	if len(snapshot.Keys) == 0 || len(snapshot.Keys) > MaxDeviceSnapshotEpochs {
		return nil, fmt.Errorf("%w: room snapshot has no usable keys", ErrInvalidEvent)
	}
	if snapshot.Room.Epoch < 1 {
		snapshot.Room.Epoch = 1
	}
	members := MembersFromWire(snapshot.Members)
	devices := DevicesFromWire(snapshot.Devices)
	foundMember := false
	for i := range members {
		member := &members[i]
		member.RoomID = snapshot.Room.ID
		if member.AccountID == "" || len(member.AccountPublicKey) != ed25519.PublicKeySize ||
			(member.Role != "owner" && member.Role != "member" && member.Role != "agent") {
			return nil, fmt.Errorf("%w: room snapshot member is invalid", ErrInvalidEvent)
		}
		if member.AccountID == accountID && member.Status == "active" {
			foundMember = true
		}
	}
	if !foundMember {
		return nil, ErrNotMember
	}
	for i := range devices {
		device := &devices[i]
		device.RoomID = snapshot.Room.ID
		if device.DeviceID == "" || len(device.PublicKey) != ed25519.PublicKeySize ||
			(device.EncryptionPublicKey != nil && len(device.EncryptionPublicKey) != 32) {
			return nil, fmt.Errorf("%w: room snapshot device is invalid", ErrInvalidEvent)
		}
	}
	keys := make(map[int][]byte, len(snapshot.Keys))
	sealedKeys := make(map[int]BundleKey, len(snapshot.Keys))
	memberKeys := make([]RoomMemberKey, 0, len(snapshot.Keys))
	now := s.now().UTC().Truncate(time.Millisecond)
	for _, entry := range snapshot.Keys {
		if entry.Epoch <= 0 || len(entry.Wrapped) == 0 {
			return nil, fmt.Errorf("%w: room snapshot key is malformed", ErrInvalidEvent)
		}
		if _, exists := keys[entry.Epoch]; exists {
			return nil, fmt.Errorf("%w: room snapshot has a duplicate epoch", ErrInvalidEvent)
		}
		key, err := UnwrapRoomKey(snapshot.Room.ID, entry.Epoch, localDeviceID, encryptionPrivateKey, entry.Wrapped)
		if err != nil {
			return nil, fmt.Errorf("%w: unwrap paired room key: %v", ErrInvalidEvent, err)
		}
		keys[entry.Epoch] = key
		sealedKeys[entry.Epoch] = entry
		memberKeys = append(memberKeys, RoomMemberKey{
			RoomID: snapshot.Room.ID, Epoch: entry.Epoch, DeviceID: localDeviceID,
			Wrapped: append([]byte(nil), entry.Wrapped...), CreatedAt: entry.CreatedAt,
		})
	}
	if _, ok := keys[snapshot.Room.Epoch]; !ok {
		return nil, fmt.Errorf("%w: current room key is missing from the snapshot", ErrInvalidEvent)
	}
	epochs := make([]RoomEpoch, 0, len(keys))
	maxEpoch := 0
	for _, epoch := range sortedEpochs(keys) {
		if epoch > maxEpoch {
			maxEpoch = epoch
		}
		sealed, err := s.vault.Seal(keys[epoch], roomEpochAAD(snapshot.Room.ID, epoch))
		if err != nil {
			return nil, fmt.Errorf("seal paired room key: %w", err)
		}
		created := now
		if entry, ok := sealedKeys[epoch]; ok && !entry.CreatedAt.IsZero() {
			created = entry.CreatedAt
		}
		epochs = append(epochs, RoomEpoch{RoomID: snapshot.Room.ID, Epoch: epoch, KeySealed: sealed, CreatedAt: created})
	}
	if maxEpoch > snapshot.Room.Epoch {
		snapshot.Room.Epoch = maxEpoch
	}
	if snapshot.Room.CreatedAt.IsZero() {
		snapshot.Room.CreatedAt = now
	}
	if existing, err := s.store.GetRoom(ctx, snapshot.Room.ID); err == nil {
		if existing.HomeServerID != "" && existing.HomeServerID != snapshot.Room.HomeServerID {
			return nil, fmt.Errorf("%w: room snapshot names a different home node", ErrForbidden)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := s.store.MergeRoomSnapshot(ctx, &snapshot.Room, members, devices, epochs, memberKeys); err != nil {
		return nil, err
	}
	return s.store.GetRoom(ctx, snapshot.Room.ID)
}

// UpdateMemberAccountKey advances one member's account signing key to the
// newest generation proven by a rotation chain. The chain must include the key
// the room already trusts, so a replayed or unrelated chain cannot replace it.
func (s *Service) UpdateMemberAccountKey(ctx context.Context, roomID, accountID string, rotations []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, err := s.store.GetMember(ctx, roomID, accountID)
	if err != nil {
		return 0, err
	}
	if member.Status != "active" {
		return 0, ErrNotMember
	}
	if len(member.AccountPublicKey) != ed25519.PublicKeySize {
		return 0, fmt.Errorf("%w: member account key is invalid", ErrInvalidEvent)
	}
	keys, err := identity.VerifyAccountKeyChain(accountID, rotations)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	anchor := 0
	for generation, key := range keys {
		if bytes.Equal(key, member.AccountPublicKey) {
			anchor = generation
			break
		}
	}
	if anchor == 0 {
		return 0, fmt.Errorf("%w: rotation chain does not include the room's account key", ErrForbidden)
	}
	newest := anchor
	for generation := range keys {
		if generation > newest {
			newest = generation
		}
	}
	if newest == anchor {
		return newest, nil
	}
	member.AccountPublicKey = append([]byte(nil), keys[newest]...)
	if err := s.store.UpdateMember(ctx, member); err != nil {
		return 0, err
	}
	return newest, nil
}

// RegisterDevice adds a device certificate to a room's trusted device set and
// wraps every available epoch key to it. Certificates are signed by the
// account key, so any home node can verify that the device belongs to an
// active member without knowing the account's other devices in advance.
func (s *Service) RegisterDevice(ctx context.Context, roomID string, certificate []byte) (*RoomDevice, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parsed, err := identity.ParseDeviceCertificate(certificate)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	member, err := s.store.GetMember(ctx, roomID, parsed.AccountID)
	if err != nil || member.Status != "active" {
		return nil, 0, ErrNotMember
	}
	cert, err := identity.VerifyDeviceCertificate(certificate, ed25519.PublicKey(member.AccountPublicKey))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	signingKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: device signing key is invalid", ErrInvalidEvent)
	}
	encryptionKey, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
	if err != nil || len(encryptionKey) != 32 {
		return nil, 0, fmt.Errorf("%w: device encryption key is invalid", ErrInvalidEvent)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	existing, getErr := s.store.GetRoomDevice(ctx, roomID, cert.DeviceID)
	switch {
	case getErr == nil:
		if existing.RevokedAt != nil {
			return nil, 0, ErrForbidden
		}
		if !bytes.Equal(existing.PublicKey, signingKey) ||
			!bytes.Equal(existing.EncryptionPublicKey, encryptionKey) ||
			existing.AccountID != cert.AccountID {
			return nil, 0, ErrForbidden
		}
	case errors.Is(getErr, ErrNotFound):
		device := &RoomDevice{
			RoomID: roomID, DeviceID: cert.DeviceID, AccountID: cert.AccountID,
			PublicKey: append([]byte(nil), signingKey...), EncryptionPublicKey: append([]byte(nil), encryptionKey...),
			JoinedAt: now,
		}
		if err := s.store.UpsertRoomDevice(ctx, device); err != nil {
			return nil, 0, err
		}
		existing = device
	default:
		return nil, 0, getErr
	}
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return nil, 0, err
	}
	wrapped := 0
	for i := range epochs {
		if _, err := s.store.GetRoomMemberKey(ctx, roomID, epochs[i].Epoch, cert.DeviceID); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return nil, 0, err
		}
		if err := s.shareEpochWithDeviceLocked(ctx, roomID, epochs[i].Epoch, cert.DeviceID, encryptionKey); err != nil {
			return nil, 0, err
		}
		wrapped++
	}
	return existing, wrapped, nil
}

// RevokeDevice removes another device on the same account from a room and
// rotates the epoch key so it cannot decrypt future events. Membership and
// other devices on the account are unaffected.
func (s *Service) RevokeDevice(ctx context.Context, roomID, accountID, deviceID, actorDeviceID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if deviceID == "" || actorDeviceID == "" || deviceID == actorDeviceID {
		return 0, ErrInvalidEvent
	}
	actor, err := s.store.GetRoomDevice(ctx, roomID, actorDeviceID)
	if err != nil || actor.RevokedAt != nil || actor.AccountID != accountID {
		return 0, ErrForbidden
	}
	member, err := s.store.GetMember(ctx, roomID, accountID)
	if err != nil || member.Status != "active" {
		return 0, ErrNotMember
	}
	target, err := s.store.GetRoomDevice(ctx, roomID, deviceID)
	if err != nil || target.AccountID != accountID {
		return 0, ErrNotFound
	}
	if target.RevokedAt != nil {
		current, err := s.store.GetRoom(ctx, roomID)
		if err != nil {
			return 0, err
		}
		return current.Epoch, nil
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	target.RevokedAt = &now
	if err := s.store.UpdateRoomDevice(ctx, target); err != nil {
		return 0, err
	}
	epoch, err := s.rotateEpochLocked(ctx, roomID)
	if err != nil {
		return 0, err
	}
	return epoch, nil
}
