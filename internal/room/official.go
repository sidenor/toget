package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

// Official hosted rooms store sealed events and device-wrapped epoch keys on
// a blind host. The host verifies an envelope signature over ciphertext; the
// client verifies the ordinary event signature after decrypting.

var (
	// ErrNotOfficialRoom means the operation requires an officially hosted
	// room.
	ErrNotOfficialRoom = errors.New("room is not hosted by the official home")
	// ErrOfficialAttachmentsUnsupported means hosted rooms do not yet carry
	// attachment ciphertext.
	ErrOfficialAttachmentsUnsupported = errors.New("official rooms do not support attachments yet")
)

// KeyPackage is one room epoch key wrapped to one device. A blind host stores
// it without being able to open it.
type KeyPackage struct {
	Epoch     int       `json:"epoch"`
	DeviceID  string    `json:"device_id,omitempty"`
	Wrapped   []byte    `json:"wrapped_key"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// KeyRequest asks for epoch keys to be wrapped to one device.
type KeyRequest struct {
	DeviceID string `json:"device_id"`
	Epochs   []int  `json:"epochs"`
}

// KeyTarget is a device's public encryption key.
type KeyTarget struct {
	DeviceID            string
	EncryptionPublicKey []byte
}

// OfficialHome identifies a pinned official room home.
type OfficialHome struct {
	ServerID string
	Address  string
}

// PreparedOfficialRoom is a room created locally but not yet accepted by the
// official home.
type PreparedOfficialRoom struct {
	Room    Room
	Event   Event
	Key     []byte
	Package KeyPackage
}

// OfficialInvite is an invitation built for the official host. Its keys are
// wrapped under the invite secret, not under a device key.
type OfficialInvite struct {
	ID        string
	Secret    string
	History   string
	ExpiresAt time.Time
	Event     Event
	Keys      []KeyPackage
}

// OfficialRotation is a new epoch prepared locally. Every active device has a
// package, so the host can verify coverage before installing it.
type OfficialRotation struct {
	Epoch int
	Event Event
	Keys  []KeyPackage
	Key   []byte
}

// InviteKeyInfo is the HKDF context for invite-secret key wrapping.
func inviteKeyInfo(roomID string, epoch int) []byte {
	return []byte(fmt.Sprintf("toget:invite-key-wrap:v1|%s|%d", roomID, epoch))
}

// WrapInviteRoomKey encrypts an epoch key under a high-entropy invite secret.
func WrapInviteRoomKey(secret, roomID string, epoch int, key []byte) ([]byte, error) {
	if len(secret) < 16 {
		return nil, ErrInviteSecret
	}
	if len(key) != roomKeySize {
		return nil, errors.New("room key must be 32 bytes")
	}
	info := inviteKeyInfo(roomID, epoch)
	aead, err := deriveWrapCipher([]byte(secret), info)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate invite key nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(key)+aead.Overhead())
	out = append(out, wrapVersion)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, key, info)
	return out, nil
}

// UnwrapInviteRoomKey opens a key produced by WrapInviteRoomKey.
func UnwrapInviteRoomKey(secret, roomID string, epoch int, wrapped []byte) ([]byte, error) {
	if len(secret) < 16 {
		return nil, ErrInviteSecret
	}
	if len(wrapped) < 1 || wrapped[0] != wrapVersion {
		return nil, errors.New("unsupported invite key wrap version")
	}
	info := inviteKeyInfo(roomID, epoch)
	aead, err := deriveWrapCipher([]byte(secret), info)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if len(wrapped) < 1+nonceSize {
		return nil, errors.New("wrapped invite key is too short")
	}
	nonce := wrapped[1 : 1+nonceSize]
	key, err := aead.Open(nil, nonce, wrapped[1+nonceSize:], info)
	if err != nil {
		return nil, errors.New("invite key authentication failed")
	}
	if len(key) != roomKeySize {
		return nil, errors.New("unwrapped invite key has the wrong size")
	}
	return key, nil
}

// SignOfficialEvent adds an envelope signature over the sealed event. It is
// safe to call once per event; signing twice would produce a different
// signature but the same hash.
func SignOfficialEvent(event *Event, key []byte) error {
	if event == nil {
		return ErrInvalidEvent
	}
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("signing key is invalid")
	}
	return SignEnvelope(event, ed25519.PrivateKey(key))
}

// PrepareOfficialRoom builds a room and its first sealed event without storing
// anything. Commit it locally only after the official home accepts it.
func (s *Service) PrepareOfficialRoom(name, topic string, actor Actor, home OfficialHome) (*PreparedOfficialRoom, error) {
	name = strings.TrimSpace(name)
	topic = strings.TrimSpace(topic)
	if name == "" || len(name) > MaxNameLength {
		return nil, ErrInvalidName
	}
	if len(topic) > MaxTopicLength {
		return nil, ErrInvalidTopic
	}
	if !actor.Valid() {
		return nil, ErrForbidden
	}
	if home.ServerID == "" || home.Address == "" {
		return nil, errors.New("official room home is incomplete")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC().Truncate(time.Millisecond)
	rm := &Room{
		ID: identity.NewID(), Name: name, Topic: topic,
		HomeNode: home.ServerID, HomeServerID: home.ServerID, HomeAddress: home.Address,
		HomeKind: HomeKindOfficial, CreatedBy: actor.AccountID, Epoch: 1, CreatedAt: now,
	}
	payload, err := json.Marshal(RoomCreatedPayload{Name: name, Topic: topic})
	if err != nil {
		return nil, fmt.Errorf("encode room.created: %w", err)
	}
	event, err := s.buildEvent(rm.ID, 1, nil, EventRoomCreated, actor, payload, now)
	if err != nil {
		return nil, err
	}
	key, err := newRoomKey()
	if err != nil {
		return nil, err
	}
	event.Epoch = 1
	sealedEvent, err := s.sealEvent(event, 1, key)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(sealedEvent, actor.SigningKey); err != nil {
		return nil, err
	}
	wrapped, err := WrapRoomKey(rm.ID, 1, actor.DeviceID, actor.EncryptionPublicKey, key)
	if err != nil {
		return nil, err
	}
	return &PreparedOfficialRoom{
		Room: *rm, Event: *sealedEvent, Key: key,
		Package: KeyPackage{Epoch: 1, DeviceID: actor.DeviceID, Wrapped: wrapped, CreatedAt: now},
	}, nil
}

// CommitOfficialRoom stores a host-accepted room locally.
func (s *Service) CommitOfficialRoom(ctx context.Context, prepared *PreparedOfficialRoom, actor Actor) (*Room, error) {
	if prepared == nil || prepared.Room.HomeKind != HomeKindOfficial || !actor.Valid() {
		return nil, ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC().Truncate(time.Millisecond)
	sealedKey, err := s.vault.Seal(prepared.Key, roomEpochAAD(prepared.Room.ID, 1))
	if err != nil {
		return nil, fmt.Errorf("seal room key: %w", err)
	}
	member := &Member{
		RoomID: prepared.Room.ID, AccountID: actor.AccountID, DeviceID: actor.DeviceID,
		DisplayName: actor.DisplayName, Role: "owner", Status: "active",
		AccountPublicKey: actor.AccountPublicKey, EncryptionPublicKey: actor.EncryptionPublicKey,
		JoinedAt: now,
	}
	device := &RoomDevice{
		RoomID: prepared.Room.ID, DeviceID: actor.DeviceID, AccountID: actor.AccountID,
		PublicKey:           append([]byte(nil), actor.SigningKey.Public().(ed25519.PublicKey)...),
		EncryptionPublicKey: append([]byte(nil), actor.EncryptionPublicKey...),
		JoinedAt:            now,
	}
	prepared.Room.Epoch = 1
	prepared.Event.Epoch = 1
	prepared.Room.HomeKind = HomeKindOfficial
	epoch := &RoomEpoch{RoomID: prepared.Room.ID, Epoch: 1, KeySealed: sealedKey, CreatedAt: now}
	keys := []RoomMemberKey{{
		RoomID: prepared.Room.ID, Epoch: 1, DeviceID: actor.DeviceID,
		Wrapped: prepared.Package.Wrapped, CreatedAt: now,
	}}
	if err := s.store.CreateRoom(ctx, &prepared.Room, &prepared.Event, member, device, epoch, keys); err != nil {
		return nil, err
	}
	return s.store.GetRoom(ctx, prepared.Room.ID)
}

// PrepareOfficialEvent builds the next sealed event and signs its envelope.
func (s *Service) PrepareOfficialEvent(ctx context.Context, roomID string, actor Actor, eventType string, payload any, anchor *Event) (*Event, error) {
	if eventType == "" {
		return nil, ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	_, sealed, err := s.prepareLocalLocked(ctx, roomID, actor, eventType, payload, anchor)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(sealed, actor.SigningKey); err != nil {
		return nil, err
	}
	return sealed, nil
}

// PrepareOfficialMessage builds, seals, and envelope-signs a message event at
// the next local sequence. The host accepts it only after checking the
// envelope and device signature.
func (s *Service) PrepareOfficialMessage(ctx context.Context, roomID string, actor Actor, body MessagePostedPayload, anchor *Event) (*Event, error) {
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		return nil, ErrEmptyMessage
	}
	if len(body.Text) > MaxMessageLength {
		return nil, ErrMessageTooLong
	}
	if len(body.Attachments) > 0 {
		return nil, ErrOfficialAttachmentsUnsupported
	}
	if body.Format == "" {
		body.Format = "plain"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	_, sealed, err := s.prepareLocalLocked(ctx, roomID, actor, EventMessagePosted, body, anchor)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(sealed, actor.SigningKey); err != nil {
		return nil, err
	}
	return sealed, nil
}

// PrepareOfficialLeave builds the leave event and signs its envelope.
func (s *Service) PrepareOfficialLeave(ctx context.Context, roomID string, actor Actor, anchor *Event) (*Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	event, err := s.prepareLeaveLocked(ctx, roomID, actor, anchor)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(event, actor.SigningKey); err != nil {
		return nil, err
	}
	return event, nil
}

func (s *Service) prepareLeaveLocked(ctx context.Context, roomID string, actor Actor, anchor *Event) (*Event, error) {
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	last, err := s.lastEventForPrepare(ctx, roomID, anchor)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(MemberLeftPayload{AccountID: actor.AccountID, Reason: "left"})
	if err != nil {
		return nil, fmt.Errorf("encode member.left: %w", err)
	}
	epoch, key, err := s.latestRoomKeyLocked(ctx, roomID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	event, err := s.buildEvent(roomID, last.Seq+1, last, EventMemberLeft, actor, raw, now)
	if err != nil {
		return nil, err
	}
	event.Epoch = epoch
	return s.sealEvent(event, epoch, key)
}

// PrepareOfficialInvite builds an invitation event and invite-secret-wrapped
// epoch keys. The host must accept them before the invite is usable.
func (s *Service) PrepareOfficialInvite(ctx context.Context, roomID string, actor Actor, history string, ttl time.Duration, anchor *Event) (*OfficialInvite, error) {
	switch history {
	case "", HistoryNone:
		history = HistoryNone
	case HistoryRecent, HistoryAll:
	default:
		return nil, ErrInvalidInviteHistory
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if ttl > 30*24*time.Hour {
		ttl = 30 * 24 * time.Hour
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	expires := now.Add(ttl)
	inviteID := identity.NewID()
	last, err := s.lastEventForPrepare(ctx, roomID, anchor)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(MemberInvitedPayload{
		InviteID: inviteID, History: history, ExpiresAt: expires.Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("encode member.invited: %w", err)
	}
	epoch, key, err := s.latestRoomKeyLocked(ctx, roomID)
	if err != nil {
		return nil, err
	}
	event, err := s.buildEvent(roomID, last.Seq+1, last, EventMemberInvited, actor, raw, now)
	if err != nil {
		return nil, err
	}
	event.Epoch = epoch
	sealedEvent, err := s.sealEvent(event, epoch, key)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(sealedEvent, actor.SigningKey); err != nil {
		return nil, err
	}
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	var keys []KeyPackage
	for i := range epochs {
		entry := &epochs[i]
		if history != HistoryAll && entry.Epoch != epoch {
			continue
		}
		plain, err := s.roomKeyLocked(ctx, roomID, entry.Epoch)
		if err != nil {
			continue
		}
		wrapped, err := WrapInviteRoomKey(secret, roomID, entry.Epoch, plain)
		if err != nil {
			return nil, err
		}
		keys = append(keys, KeyPackage{Epoch: entry.Epoch, Wrapped: wrapped, CreatedAt: entry.CreatedAt})
	}
	return &OfficialInvite{
		ID: inviteID, Secret: secret, History: history, ExpiresAt: expires,
		Event: *sealedEvent, Keys: keys,
	}, nil
}

// PrepareOfficialRotation builds the next epoch, wraps it to every active
// device, and signs the rotation event.
func (s *Service) PrepareOfficialRotation(ctx context.Context, roomID string, actor Actor, anchor *Event) (*OfficialRotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	// The host is authoritative for the current epoch. After a sync, Room.Epoch
	// reflects the host even when this device is missing an older key, so the
	// next epoch can never collide with one this device never received.
	if rm.Epoch < 1 {
		return nil, ErrInvalidEvent
	}
	next := rm.Epoch + 1
	key, err := newRoomKey()
	if err != nil {
		return nil, err
	}
	last, err := s.lastEventForPrepare(ctx, roomID, anchor)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(EpochRotatedPayload{Epoch: next, Reason: "membership"})
	if err != nil {
		return nil, fmt.Errorf("encode epoch.rotated: %w", err)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	event, err := s.buildEvent(roomID, last.Seq+1, last, EventEpochRotated, actor, raw, now)
	if err != nil {
		return nil, err
	}
	event.Epoch = next
	sealedEvent, err := s.sealEvent(event, next, key)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(sealedEvent, actor.SigningKey); err != nil {
		return nil, err
	}
	members, err := s.store.ListMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(members))
	for i := range members {
		if members[i].Status == "active" {
			active[members[i].AccountID] = true
		}
	}
	devices, err := s.store.ListRoomDevices(ctx, roomID)
	if err != nil {
		return nil, err
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceID < devices[j].DeviceID })
	var packages []KeyPackage
	for i := range devices {
		d := &devices[i]
		if d.RevokedAt != nil || !active[d.AccountID] {
			continue
		}
		if len(d.EncryptionPublicKey) != 32 {
			return nil, fmt.Errorf("room device %s has no encryption key", d.DeviceID)
		}
		wrapped, err := WrapRoomKey(roomID, next, d.DeviceID, d.EncryptionPublicKey, key)
		if err != nil {
			return nil, err
		}
		packages = append(packages, KeyPackage{
			Epoch: next, DeviceID: d.DeviceID, Wrapped: wrapped, CreatedAt: now,
		})
	}
	return &OfficialRotation{Epoch: next, Event: *sealedEvent, Keys: packages, Key: key}, nil
}

// CommitOfficialRotation installs an accepted rotation locally.
func (s *Service) CommitOfficialRotation(ctx context.Context, rotation *OfficialRotation, actor Actor) error {
	if rotation == nil || !actor.Valid() {
		return ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, rotation.Event.RoomID)
	if err != nil {
		return err
	}
	if rm.HomeKind != HomeKindOfficial {
		return ErrNotOfficialRoom
	}
	if _, err := s.store.GetRoomEpoch(ctx, rm.ID, rotation.Epoch); err != nil {
		sealedKey, sealErr := s.vault.Seal(rotation.Key, roomEpochAAD(rm.ID, rotation.Epoch))
		if sealErr != nil {
			return fmt.Errorf("seal rotated room key: %w", sealErr)
		}
		if saveErr := s.store.SaveRoomEpoch(ctx, &RoomEpoch{
			RoomID: rm.ID, Epoch: rotation.Epoch, KeySealed: sealedKey, CreatedAt: s.now().UTC(),
		}); saveErr != nil {
			return saveErr
		}
	}
	for i := range rotation.Keys {
		p := &rotation.Keys[i]
		if p.DeviceID != actor.DeviceID {
			continue
		}
		if err := s.store.SaveRoomMemberKey(ctx, &RoomMemberKey{
			RoomID: rm.ID, Epoch: rotation.Epoch, DeviceID: actor.DeviceID,
			Wrapped: p.Wrapped, CreatedAt: p.CreatedAt,
		}); err != nil {
			return err
		}
	}
	if err := s.appendRemoteLocked(ctx, &rotation.Event, nil); err != nil {
		return err
	}
	rm, err = s.store.GetRoom(ctx, rm.ID)
	if err != nil {
		return err
	}
	rm.Epoch = rotation.Epoch
	return s.store.UpdateRoom(ctx, rm)
}

// WrapOfficialKeys wraps every requested epoch key this device holds to the
// devices named in the host's key-request list.
func (s *Service) WrapOfficialKeys(ctx context.Context, roomID string, requests []KeyRequest, devices []KeyTarget) ([]KeyPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	targets := make(map[string][]byte, len(devices))
	for i := range devices {
		if len(devices[i].EncryptionPublicKey) == 32 {
			targets[devices[i].DeviceID] = devices[i].EncryptionPublicKey
		}
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var out []KeyPackage
	for _, request := range requests {
		pub, ok := targets[request.DeviceID]
		if !ok {
			continue
		}
		for _, epoch := range request.Epochs {
			key, err := s.roomKeyLocked(ctx, roomID, epoch)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			wrapped, err := WrapRoomKey(roomID, epoch, request.DeviceID, pub, key)
			if err != nil {
				return nil, err
			}
			out = append(out, KeyPackage{Epoch: epoch, DeviceID: request.DeviceID, Wrapped: wrapped, CreatedAt: now})
		}
	}
	return out, nil
}

// WrapOfficialKeysForDevice wraps every local epoch to one device. A newly
// joined or newly registered device uploads these so later syncs can serve
// its keys.
func (s *Service) WrapOfficialKeysForDevice(ctx context.Context, roomID, deviceID string, encryptionPub []byte) ([]KeyPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if rm.HomeKind != HomeKindOfficial {
		return nil, ErrNotOfficialRoom
	}
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var out []KeyPackage
	for i := range epochs {
		key, err := s.roomKeyLocked(ctx, roomID, epochs[i].Epoch)
		if err != nil {
			return nil, err
		}
		wrapped, err := WrapRoomKey(roomID, epochs[i].Epoch, deviceID, encryptionPub, key)
		if err != nil {
			return nil, err
		}
		out = append(out, KeyPackage{
			Epoch: epochs[i].Epoch, DeviceID: deviceID, Wrapped: wrapped, CreatedAt: now,
		})
	}
	return out, nil
}

// OfficialLocalEpochs lists the epoch numbers this device can open.
func (s *Service) OfficialLocalEpochs(ctx context.Context, roomID string) ([]int, error) {
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(epochs))
	for i := range epochs {
		out = append(out, epochs[i].Epoch)
	}
	return out, nil
}

// MissingOfficialEpochs returns epochs up to hostEpoch that this device still
// lacks and is entitled to see. The first locally stored event marks the start
// of this device's visible history: an invite that hides older history must
// never turn into a key request for those epochs.
func (s *Service) MissingOfficialEpochs(ctx context.Context, roomID string, hostEpoch int) ([]int, error) {
	if hostEpoch < 1 {
		return nil, nil
	}
	local, err := s.OfficialLocalEpochs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	have := make(map[int]bool, len(local))
	for _, epoch := range local {
		have[epoch] = true
	}
	first, err := s.store.ListEvents(ctx, roomID, 0, 1)
	if err != nil {
		return nil, err
	}
	if len(first) == 0 {
		// A room with no visible events still needs the current epoch key so
		// future messages can be opened. The host requests that automatically.
		return nil, nil
	}
	start := first[0].Epoch
	if start < 1 {
		start = 1
	}
	var missing []int
	for epoch := start; epoch <= hostEpoch; epoch++ {
		if !have[epoch] {
			missing = append(missing, epoch)
		}
	}
	return missing, nil
}

// ImportOfficialRoom is implemented by the node layer with the host client;
// this file only provides the crypto and local-state primitives it needs.
