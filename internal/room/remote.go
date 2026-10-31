package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

// Remote join and sync payloads. Everything here crosses a node boundary, so
// every field is explicit and the receiver validates sizes, membership, chain
// continuity, signatures, and (when it holds the epoch key) payload integrity.

const (
	// MaxBundleEvents bounds one join or sync response.
	MaxBundleEvents = 10000
	// MaxSyncEvents bounds one incremental sync request.
	MaxSyncEvents = 1000
	// MaxBundleMembers bounds membership included in one bundle.
	MaxBundleMembers = 5000
	// MaxBundleDevices bounds devices included in one bundle.
	MaxBundleDevices = 20000
	// MaxBundleAttachments bounds attachment metadata included in one bundle.
	MaxBundleAttachments = 2000
)

// RemoteJoiner is the identity a remote node asks the home node to admit.
type RemoteJoiner struct {
	AccountID           string `json:"account_id"`
	DeviceID            string `json:"device_id"`
	DisplayName         string `json:"display_name"`
	AccountPublicKey    []byte `json:"account_public_key"`
	SigningPublicKey    []byte `json:"signing_public_key"`
	EncryptionPublicKey []byte `json:"encryption_public_key"`
	DeviceCertificate   []byte `json:"device_certificate"`
}

func (j RemoteJoiner) validate() error {
	if j.AccountID == "" || j.DeviceID == "" || strings.TrimSpace(j.DisplayName) == "" {
		return fmt.Errorf("%w: joiner identity is incomplete", ErrInvalidEvent)
	}
	if len(j.AccountPublicKey) != ed25519.PublicKeySize ||
		len(j.SigningPublicKey) != ed25519.PublicKeySize ||
		len(j.EncryptionPublicKey) != 32 {
		return fmt.Errorf("%w: joiner key material is invalid", ErrInvalidEvent)
	}
	if len(j.DeviceCertificate) == 0 {
		return fmt.Errorf("%w: joiner device certificate is missing", ErrInvalidEvent)
	}
	cert, err := identity.VerifyDeviceCertificate(j.DeviceCertificate, ed25519.PublicKey(j.AccountPublicKey))
	if err != nil {
		return fmt.Errorf("%w: joiner device certificate: %v", ErrInvalidEvent, err)
	}
	certSigningKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || !bytes.Equal(certSigningKey, j.SigningPublicKey) {
		return fmt.Errorf("%w: joiner certificate signing key does not match", ErrInvalidEvent)
	}
	certEncryptionKey, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
	if err != nil || !bytes.Equal(certEncryptionKey, j.EncryptionPublicKey) {
		return fmt.Errorf("%w: joiner certificate encryption key does not match", ErrInvalidEvent)
	}
	if cert.AccountID != j.AccountID || cert.DeviceID != j.DeviceID {
		return fmt.Errorf("%w: joiner certificate identity does not match", ErrInvalidEvent)
	}
	return nil
}

// BundleKey is one room epoch key wrapped to the joining/syncing device.
type BundleKey struct {
	Epoch     int       `json:"epoch"`
	Wrapped   []byte    `json:"wrapped_key"`
	CreatedAt time.Time `json:"created_at"`
}

// JoinBundle is the complete snapshot returned when an invitation is
// redeemed. Events may be opaque when the invite did not attach their epoch
// key; opaque events preserve sequence and hash-chain continuity without
// revealing their contents.
type JoinBundle struct {
	Room        Room             `json:"room"`
	Member      Member           `json:"member"`
	Members     []WireMember     `json:"members"`
	Devices     []WireDevice     `json:"devices"`
	Keys        []BundleKey      `json:"keys"`
	Events      []WireEvent      `json:"events"`
	Attachments []WireAttachment `json:"attachments,omitempty"`
	FromSeq     int64            `json:"from_seq"`
	Head        int64            `json:"head"`
	History     string           `json:"history"`
	InviteID    string           `json:"invite_id"`
	JoinedAt    time.Time        `json:"joined_at"`
	ServerTime  time.Time        `json:"server_time"`
}

// SyncBundle is an incremental view of a room from its home node.
type SyncBundle struct {
	Room        Room             `json:"room"`
	Members     []WireMember     `json:"members"`
	Devices     []WireDevice     `json:"devices"`
	Keys        []BundleKey      `json:"keys"`
	Events      []WireEvent      `json:"events"`
	Attachments []WireAttachment `json:"attachments,omitempty"`
	After       int64            `json:"after"`
	Head        int64            `json:"head"`
	HasMore     bool             `json:"has_more"`
	ServerTime  time.Time        `json:"server_time"`
}

// RedeemInviteFor admits a remote account/device and returns the invite so
// the caller can build a join bundle. signer is the local home-node actor
// that authorizes the member.joined event; joiner is the remote identity.
func (s *Service) RedeemInviteFor(ctx context.Context, secret string, joiner RemoteJoiner, signer Actor) (*Room, *Member, *Event, *Invite, error) {
	if !signer.Valid() {
		return nil, nil, nil, nil, ErrForbidden
	}
	if err := joiner.validate(); err != nil {
		return nil, nil, nil, nil, err
	}
	return s.redeemInviteLocked(ctx, secret, joiner, signer)
}

// RedeemInvite joins a room using an invitation secret.
func (s *Service) RedeemInvite(ctx context.Context, secret string, actor Actor) (*Room, *Member, *Event, error) {
	if !actor.Valid() {
		return nil, nil, nil, ErrForbidden
	}
	joiner := RemoteJoiner{
		AccountID:           actor.AccountID,
		DeviceID:            actor.DeviceID,
		DisplayName:         actor.DisplayName,
		AccountPublicKey:    append([]byte(nil), actor.AccountPublicKey...),
		SigningPublicKey:    append([]byte(nil), actor.SigningKey.Public().(ed25519.PublicKey)...),
		EncryptionPublicKey: append([]byte(nil), actor.EncryptionPublicKey...),
	}
	room, member, event, _, err := s.redeemInviteLocked(ctx, secret, joiner, actor)
	return room, member, event, err
}

func (s *Service) redeemInviteLocked(ctx context.Context, secret string, joiner RemoteJoiner, signer Actor) (*Room, *Member, *Event, *Invite, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) < 16 {
		return nil, nil, nil, nil, ErrInviteSecret
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	invite, err := s.store.GetInviteByHash(ctx, HashInviteSecret(secret))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, nil, nil, ErrInviteInvalid
		}
		return nil, nil, nil, nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if invite.UsedAt != nil {
		return nil, nil, nil, nil, ErrInviteUsed
	}
	if !now.Before(invite.ExpiresAt) {
		return nil, nil, nil, nil, ErrInviteExpired
	}
	room, err := s.store.GetRoom(ctx, invite.RoomID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	existing, err := s.store.GetMember(ctx, room.ID, joiner.AccountID)
	if err == nil && existing.Status == "active" {
		return nil, nil, nil, nil, ErrMemberExists
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, nil, nil, err
	}

	member := &Member{
		RoomID:              room.ID,
		AccountID:           joiner.AccountID,
		DeviceID:            joiner.DeviceID,
		DisplayName:         joiner.DisplayName,
		Role:                "member",
		Status:              "active",
		AccountPublicKey:    append([]byte(nil), joiner.AccountPublicKey...),
		EncryptionPublicKey: append([]byte(nil), joiner.EncryptionPublicKey...),
		JoinedAt:            now,
	}
	device := &RoomDevice{
		RoomID:              room.ID,
		DeviceID:            joiner.DeviceID,
		AccountID:           joiner.AccountID,
		PublicKey:           append([]byte(nil), joiner.SigningPublicKey...),
		EncryptionPublicKey: append([]byte(nil), joiner.EncryptionPublicKey...),
		JoinedAt:            now,
	}
	if err := s.store.UpsertMember(ctx, member); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := s.store.UpsertRoomDevice(ctx, device); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := s.store.MarkInviteUsed(ctx, invite.ID, now); err != nil {
		return nil, nil, nil, nil, err
	}
	if invite.History != HistoryNone {
		if err := s.shareHistoryLocked(ctx, room.ID, joiner.DeviceID, joiner.EncryptionPublicKey, invite.History); err != nil {
			return nil, nil, nil, nil, err
		}
		from, to, err := s.sharedHistoryRangeLocked(ctx, room.ID, invite.History, room.Epoch)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if _, err := s.appendUnchecked(ctx, room.ID, signer, EventHistoryAttached, HistoryAttachedPayload{
			AccountID: joiner.AccountID, FromSeq: from, ToSeq: to, Choice: invite.History,
		}, now); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	if _, err := s.rotateEpochLocked(ctx, room.ID); err != nil {
		return nil, nil, nil, nil, err
	}
	room, err = s.store.GetRoom(ctx, room.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	event, err := s.appendUnchecked(ctx, room.ID, signer, EventMemberJoined, MemberJoinedPayload{
		AccountID:   joiner.AccountID,
		DisplayName: joiner.DisplayName,
		Role:        "member",
		History:     invite.History,
	}, now)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	invite.Secret = ""
	return room, member, event, invite, nil
}

// SetHomeInfo records where a room is hosted. It is metadata, not a signed
// room event, and is only applied by the node that owns the room.
func (s *Service) SetHomeInfo(ctx context.Context, roomID, serverID, address string) (*Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	current.HomeServerID = serverID
	current.HomeAddress = address
	if err := s.store.UpdateRoom(ctx, current); err != nil {
		return nil, err
	}
	return current, nil
}

// ExportJoinBundle builds the response to a redeemed invitation for one
// registered room device. The latest history window is capped; a client that
// receives a truncated window resumes from Head with normal sync.
func (s *Service) ExportJoinBundle(ctx context.Context, roomID, deviceID, history string) (*JoinBundle, error) {
	room, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	device, err := s.store.GetRoomDevice(ctx, roomID, deviceID)
	if err != nil {
		return nil, err
	}
	member, err := s.store.GetMember(ctx, roomID, device.AccountID)
	if err != nil || member.Status != "active" || device.RevokedAt != nil {
		return nil, ErrNotMember
	}
	members, err := s.store.ListMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	devices, err := s.store.ListRoomDevices(ctx, roomID)
	if err != nil {
		return nil, err
	}
	keys, err := s.deviceKeysLocked(ctx, roomID, deviceID)
	if err != nil {
		return nil, err
	}
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return nil, err
	}
	from := int64(1)
	if last.Seq > MaxBundleEvents {
		from = last.Seq - MaxBundleEvents + 1
	}
	events, err := s.store.ListEvents(ctx, roomID, from-1, MaxBundleEvents)
	if err != nil {
		return nil, err
	}
	attachments, err := s.bundleAttachments(ctx, roomID, events, keys)
	if err != nil {
		return nil, err
	}
	return &JoinBundle{
		Room:        *room,
		Member:      *member,
		Members:     limitMembers(WireMembers(members)),
		Devices:     limitDevices(WireDevices(devices)),
		Keys:        keys,
		Events:      WireEvents(events),
		Attachments: attachments,
		FromSeq:     from,
		Head:        last.Seq,
		History:     history,
		JoinedAt:    member.JoinedAt,
		ServerTime:  s.now().UTC(),
	}, nil
}

// ExportSync returns room changes after a device's cursor and all epoch keys
// currently wrapped to that device. Events are still sealed end-to-end; a
// home node never needs to be trusted with plaintext room keys.
func (s *Service) ExportSync(ctx context.Context, roomID, deviceID string, after int64, limit int) (*SyncBundle, error) {
	if after < 0 {
		return nil, ErrInvalidEvent
	}
	if limit <= 0 || limit > MaxSyncEvents {
		limit = MaxSyncEvents
	}
	room, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	device, err := s.store.GetRoomDevice(ctx, roomID, deviceID)
	if err != nil {
		return nil, err
	}
	member, err := s.store.GetMember(ctx, roomID, device.AccountID)
	if err != nil || member.Status != "active" || device.RevokedAt != nil {
		return nil, ErrNotMember
	}
	members, err := s.store.ListMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	devices, err := s.store.ListRoomDevices(ctx, roomID)
	if err != nil {
		return nil, err
	}
	keys, err := s.deviceKeysLocked(ctx, roomID, deviceID)
	if err != nil {
		return nil, err
	}
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if after > last.Seq {
		after = last.Seq
	}
	events, err := s.store.ListEvents(ctx, roomID, after, limit+1)
	if err != nil {
		return nil, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	attachments, err := s.bundleAttachments(ctx, roomID, events, keys)
	if err != nil {
		return nil, err
	}
	return &SyncBundle{
		Room:        *room,
		Members:     limitMembers(WireMembers(members)),
		Devices:     limitDevices(WireDevices(devices)),
		Keys:        keys,
		Events:      WireEvents(events),
		Attachments: attachments,
		After:       after,
		Head:        last.Seq,
		HasMore:     hasMore,
		ServerTime:  s.now().UTC(),
	}, nil
}

func (s *Service) deviceKeysLocked(ctx context.Context, roomID, deviceID string) ([]BundleKey, error) {
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]BundleKey, 0, len(epochs))
	for i := range epochs {
		wrapped, err := s.store.GetRoomMemberKey(ctx, roomID, epochs[i].Epoch, deviceID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, BundleKey{Epoch: epochs[i].Epoch, Wrapped: wrapped.Wrapped, CreatedAt: epochs[i].CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Epoch < out[j].Epoch })
	return out, nil
}

// ImportJoinBundle installs a room snapshot. deviceID/privateKey identify the
// local device whose wrapped room keys are being imported. Events for epochs
// we hold are decrypted and signature-checked; older opaque chapters are kept
// as chain anchors only.
func checkBundleAttachments(roomID string, items []WireAttachment) ([]Attachment, error) {
	if len(items) > MaxBundleAttachments {
		return nil, fmt.Errorf("%w: join bundle has too many attachments", ErrInvalidEvent)
	}
	out := make([]Attachment, 0, len(items))
	for i := range items {
		wire := items[i]
		if wire.ID == "" || wire.RoomID != roomID || wire.Size <= 0 || wire.Size > MaxAttachmentSize || len(wire.SHA256) != 64 || wire.Epoch <= 0 {
			return nil, fmt.Errorf("%w: bundled attachment metadata is invalid", ErrInvalidEvent)
		}
		if _, err := hex.DecodeString(wire.SHA256); err != nil {
			return nil, fmt.Errorf("%w: bundled attachment digest is invalid", ErrInvalidEvent)
		}
		out = append(out, wire.ToAttachment())
	}
	return out, nil
}

func (s *Service) ImportJoinBundle(ctx context.Context, bundle *JoinBundle, deviceID string, encryptionPrivateKey []byte) (*Room, error) {
	if bundle == nil || bundle.Room.ID == "" || bundle.Room.HomeServerID == "" {
		return nil, fmt.Errorf("%w: join bundle is incomplete", ErrInvalidEvent)
	}
	if len(bundle.Events) > MaxBundleEvents || len(bundle.Members) > MaxBundleMembers || len(bundle.Devices) > MaxBundleDevices {
		return nil, fmt.Errorf("%w: join bundle is too large", ErrInvalidEvent)
	}
	if bundle.FromSeq < 1 {
		return nil, fmt.Errorf("%w: join bundle has no starting sequence", ErrInvalidEvent)
	}
	if bundle.Head > 0 && len(bundle.Events) == 0 {
		return nil, fmt.Errorf("%w: join bundle has no events", ErrInvalidEvent)
	}
	if len(bundle.Events) > 0 {
		if bundle.Events[0].Seq != bundle.FromSeq {
			return nil, fmt.Errorf("%w: join bundle does not start at its cursor", ErrInvalidEvent)
		}
		if bundle.Events[len(bundle.Events)-1].Seq != bundle.Head {
			return nil, fmt.Errorf("%w: join bundle does not end at its head", ErrInvalidEvent)
		}
	}
	members := MembersFromWire(bundle.Members)
	devices := DevicesFromWire(bundle.Devices)
	keys, sealedKeys, memberKeys, err := s.importKeys(ctx, bundle.Room.ID, bundle.Keys, deviceID, encryptionPrivateKey)
	if err != nil {
		return nil, err
	}
	if _, ok := keys[bundle.Room.Epoch]; !ok {
		return nil, fmt.Errorf("%w: current room key is missing", ErrInvalidEvent)
	}
	events, err := s.checkBundleEvents(ctx, &bundle.Room, nil, EventsFromWire(bundle.Events), keys, devices)
	if err != nil {
		return nil, err
	}
	attachments, err := checkBundleAttachments(bundle.Room.ID, bundle.Attachments)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	epochs := make([]RoomEpoch, 0, len(keys))
	for _, epoch := range sortedEpochs(keys) {
		sealed, err := s.vault.Seal(keys[epoch], roomEpochAAD(bundle.Room.ID, epoch))
		if err != nil {
			return nil, fmt.Errorf("seal imported room key: %w", err)
		}
		created := now
		if k, ok := sealedKeys[epoch]; ok && !k.CreatedAt.IsZero() {
			created = k.CreatedAt
		}
		epochs = append(epochs, RoomEpoch{RoomID: bundle.Room.ID, Epoch: epoch, KeySealed: sealed, CreatedAt: created})
	}
	if bundle.Room.Epoch < 1 {
		bundle.Room.Epoch = 1
	}
	if err := s.store.ImportRoom(ctx, &bundle.Room, members, devices, epochs, memberKeys, events); err != nil {
		return nil, err
	}
	for i := range attachments {
		if err := s.store.UpsertAttachment(ctx, &attachments[i]); err != nil {
			return nil, err
		}
	}
	return s.store.GetRoom(ctx, bundle.Room.ID)
}

// ImportSync appends home-node events after the local head and refreshes
// membership, devices, and epoch keys.
func (s *Service) ImportSync(ctx context.Context, bundle *SyncBundle, deviceID string, encryptionPrivateKey []byte) (int, error) {
	if bundle == nil || bundle.Room.ID == "" {
		return 0, fmt.Errorf("%w: sync bundle is incomplete", ErrInvalidEvent)
	}
	if len(bundle.Events) > MaxSyncEvents {
		return 0, fmt.Errorf("%w: sync bundle is too large", ErrInvalidEvent)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	local, err := s.store.GetRoom(ctx, bundle.Room.ID)
	if err != nil {
		return 0, err
	}
	if local.HomeServerID == "" || bundle.Room.HomeServerID != local.HomeServerID {
		return 0, fmt.Errorf("%w: sync came from a different home node", ErrForbidden)
	}
	members := MembersFromWire(bundle.Members)
	devices := DevicesFromWire(bundle.Devices)
	if _, ok := findDevice(devices, deviceID); !ok {
		return 0, ErrForbidden
	}
	keys, sealedKeys, memberKeys, err := s.importKeys(ctx, local.ID, bundle.Keys, deviceID, encryptionPrivateKey)
	if err != nil {
		return 0, err
	}
	if _, ok := keys[bundle.Room.Epoch]; !ok {
		return 0, fmt.Errorf("%w: current room key is missing", ErrInvalidEvent)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	for _, epoch := range sortedEpochs(keys) {
		if _, err := s.store.GetRoomEpoch(ctx, local.ID, epoch); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return 0, err
		}
		sealed, err := s.vault.Seal(keys[epoch], roomEpochAAD(local.ID, epoch))
		if err != nil {
			return 0, fmt.Errorf("seal imported room key: %w", err)
		}
		created := now
		if entry, ok := sealedKeys[epoch]; ok && !entry.CreatedAt.IsZero() {
			created = entry.CreatedAt
		}
		if err := s.store.SaveRoomEpoch(ctx, &RoomEpoch{RoomID: local.ID, Epoch: epoch, KeySealed: sealed, CreatedAt: created}); err != nil {
			return 0, err
		}
	}
	for i := range memberKeys {
		row := &memberKeys[i]
		if _, err := s.store.GetRoomMemberKey(ctx, local.ID, row.Epoch, row.DeviceID); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return 0, err
		}
		if err := s.store.SaveRoomMemberKey(ctx, row); err != nil {
			return 0, err
		}
	}
	last, err := s.store.LastEvent(ctx, local.ID)
	if errors.Is(err, ErrNotFound) {
		last = nil
	} else if err != nil {
		return 0, err
	}
	events, err := s.checkBundleEvents(ctx, local, last, EventsFromWire(bundle.Events), keys, devices)
	if err != nil {
		return 0, err
	}
	attachments, err := checkBundleAttachments(local.ID, bundle.Attachments)
	if err != nil {
		return 0, err
	}
	for i := range members {
		if err := s.store.UpsertMember(ctx, &members[i]); err != nil {
			return 0, err
		}
	}
	for i := range devices {
		if err := s.store.UpsertRoomDevice(ctx, &devices[i]); err != nil {
			return 0, err
		}
	}
	for i := range attachments {
		if err := s.store.UpsertAttachment(ctx, &attachments[i]); err != nil {
			return 0, err
		}
	}
	if len(events) > 0 {
		if err := s.store.ImportEvents(ctx, events); err != nil {
			return 0, err
		}
	}
	updated := *local
	updated.Name = bundle.Room.Name
	updated.Topic = bundle.Room.Topic
	updated.Epoch = bundle.Room.Epoch
	updated.HomeNode = bundle.Room.HomeNode
	updated.HomeAddress = bundle.Room.HomeAddress
	// A member may archive a remote room for themselves; sync must not clear
	// that local choice just because the home node has not archived the room.
	if bundle.Room.ArchivedAt != nil {
		updated.ArchivedAt = bundle.Room.ArchivedAt
	}
	if err := s.store.UpdateRoom(ctx, &updated); err != nil {
		return 0, err
	}
	return len(events), nil
}

// importKeys unwraps bundle keys for the local device and returns the plain
// keys, the original wrapped records, and member-key rows ready to persist.
func (s *Service) importKeys(ctx context.Context, roomID string, bundleKeys []BundleKey, deviceID string, encryptionPrivateKey []byte) (map[int][]byte, map[int]BundleKey, []RoomMemberKey, error) {
	if len(bundleKeys) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: no room keys were provided", ErrInvalidEvent)
	}
	keys := make(map[int][]byte, len(bundleKeys))
	sealed := make(map[int]BundleKey, len(bundleKeys))
	rows := make([]RoomMemberKey, 0, len(bundleKeys))
	for _, entry := range bundleKeys {
		if entry.Epoch <= 0 || len(entry.Wrapped) == 0 {
			return nil, nil, nil, fmt.Errorf("%w: malformed room key", ErrInvalidEvent)
		}
		if _, exists := keys[entry.Epoch]; exists {
			return nil, nil, nil, fmt.Errorf("%w: duplicate room epoch", ErrInvalidEvent)
		}
		key, err := UnwrapRoomKey(roomID, entry.Epoch, deviceID, encryptionPrivateKey, entry.Wrapped)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%w: unwrap room key: %v", ErrInvalidEvent, err)
		}
		keys[entry.Epoch] = key
		sealed[entry.Epoch] = entry
		rows = append(rows, RoomMemberKey{
			RoomID: roomID, Epoch: entry.Epoch, DeviceID: deviceID,
			Wrapped: append([]byte(nil), entry.Wrapped...), CreatedAt: entry.CreatedAt,
		})
	}
	return keys, sealed, rows, nil
}

// checkBundleEvents validates ordering and, when possible, contents. The
// returned events are ready to store. Events before the first shared epoch
// are intentionally opaque: the sequence and hash link are preserved so later
// events still validate, but their plaintext is never available locally.
func (s *Service) checkBundleEvents(ctx context.Context, room *Room, last *Event, events []Event, keys map[int][]byte, devices []RoomDevice) ([]Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	known := make(map[string]RoomDevice, len(devices))
	for i := range devices {
		known[devices[i].DeviceID] = devices[i]
	}
	out := make([]Event, 0, len(events))
	prev := last
	for i := range events {
		event := events[i]
		if event.RoomID != room.ID || event.ID == "" || event.Type == "" || event.Hash == "" || event.Seq <= 0 {
			return nil, fmt.Errorf("%w: malformed event in bundle", ErrInvalidEvent)
		}
		if event.CreatedAt.IsZero() {
			return nil, fmt.Errorf("%w: event has no timestamp", ErrInvalidEvent)
		}
		device, ok := known[event.AuthorDevice]
		if !ok || device.AccountID != event.AuthorID {
			return nil, fmt.Errorf("%w: event author is not a room device", ErrInvalidEvent)
		}
		if prev != nil {
			if event.Seq != prev.Seq+1 {
				return nil, fmt.Errorf("%w: bundle contains a sequence gap", ErrInvalidEvent)
			}
			if event.PrevID != prev.ID || event.PrevHash != prev.Hash {
				return nil, fmt.Errorf("%w: bundle chain is broken", ErrInvalidEvent)
			}
		} else if event.Seq == 1 {
			if event.PrevID != "" || event.PrevHash != "" {
				return nil, fmt.Errorf("%w: first event has a predecessor", ErrInvalidEvent)
			}
		} else if event.PrevID == "" || event.PrevHash == "" {
			return nil, fmt.Errorf("%w: truncated bundle has no chain anchor", ErrInvalidEvent)
		}
		if event.Epoch <= 0 {
			event.Epoch = 1
		}
		if key, ok := keys[event.Epoch]; ok {
			if !isRoomPayload(event.Payload) {
				return nil, fmt.Errorf("%w: shared-epoch event is not encrypted", ErrInvalidEvent)
			}
			plain, err := openRoomPayload(key, event.Payload, payloadAAD(event.RoomID, event.Epoch, "event"))
			if err != nil {
				return nil, fmt.Errorf("%w: open bundled event: %v", ErrInvalidEvent, err)
			}
			check := event
			check.Payload = plain
			if err := VerifyEvent(&check, device.PublicKey); err != nil {
				return nil, fmt.Errorf("%w: bundled event %s seq %d by %s failed verification: %v", ErrInvalidEvent, event.ID, event.Seq, event.AuthorDevice, err)
			}
		} else if isRoomPayload(event.Payload) {
			// Opaque history the invite intentionally did not share.
		} else {
			return nil, fmt.Errorf("%w: opaque bundle event is not sealed", ErrInvalidEvent)
		}
		out = append(out, event)
		prev = &out[len(out)-1]
	}
	if len(out) > 0 && room.Epoch < out[len(out)-1].Epoch {
		room.Epoch = out[len(out)-1].Epoch
	}
	return out, nil
}

// PrepareMessage builds and seals a message event at the next local sequence
// without appending it. The event is posted to the home node, accepted there,
// and then installed locally with AppendRemote.
func (s *Service) PrepareMessage(ctx context.Context, roomID string, actor Actor, body MessagePostedPayload) (*Event, error) {
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		return nil, ErrEmptyMessage
	}
	if len(body.Text) > MaxMessageLength {
		return nil, ErrMessageTooLong
	}
	if len(body.Attachments) > 0 {
		ids, err := s.validateAttachmentRefs(ctx, roomID, body.Attachments)
		if err != nil {
			return nil, err
		}
		body.Attachments = ids
	}
	if body.Format == "" {
		body.Format = "plain"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode message.posted: %w", err)
	}
	epoch, key, err := s.latestRoomKeyLocked(ctx, roomID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	event, err := s.buildEvent(roomID, last.Seq+1, last, EventMessagePosted, actor, raw, now)
	if err != nil {
		return nil, err
	}
	event.Epoch = epoch
	return s.sealEvent(event, epoch, key)
}

// AcceptRemoteEvent authorizes and appends an event submitted by a remote
// room member. Ordering is serialized by the home node's service lock.
func (s *Service) AcceptRemoteEvent(ctx context.Context, event *Event) error {
	if event == nil || event.RoomID == "" || event.AuthorDevice == "" {
		return ErrInvalidEvent
	}
	// A remote member may propose messages, but membership, key rotation, and
	// room settings are home-node decisions. Without this allowlist a member
	// holding the room key could forge a member.left or epoch event.
	if event.Type != EventMessagePosted {
		return fmt.Errorf("%w: remote event type is not accepted", ErrForbidden)
	}
	device, err := s.store.GetRoomDevice(ctx, event.RoomID, event.AuthorDevice)
	if err != nil {
		return ErrForbidden
	}
	if device.AccountID != event.AuthorID || device.RevokedAt != nil {
		return ErrForbidden
	}
	member, err := s.store.GetMember(ctx, event.RoomID, device.AccountID)
	if err != nil || member.Status != "active" {
		return ErrNotMember
	}
	return s.AppendRemote(ctx, event)
}

// GetOpenedEvent returns one decrypted, verified event by ID.
func (s *Service) GetOpenedEvent(ctx context.Context, roomID, eventID string) (*Event, error) {
	return s.findEvent(ctx, roomID, eventID)
}

// LastSeq returns the local head sequence for a room. A room imported from a
// pairing snapshot has no events yet, which is a valid cursor of zero.
func (s *Service) LastSeq(ctx context.Context, roomID string) (int64, error) {
	last, err := s.store.LastEvent(ctx, roomID)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return last.Seq, nil
}

// bundleAttachments returns metadata for attachments referenced by messages
// the receiving device can actually decrypt. Hidden epochs stay hidden: their
// filenames are metadata too.
func (s *Service) bundleAttachments(ctx context.Context, roomID string, events []Event, keys []BundleKey) ([]WireAttachment, error) {
	allowed := make(map[int]bool, len(keys))
	for _, key := range keys {
		allowed[key.Epoch] = true
	}
	ids := make(map[string]bool)
	for i := range events {
		event := &events[i]
		if event.Type != EventMessagePosted || !allowed[event.Epoch] {
			continue
		}
		key, err := s.roomKeyLocked(ctx, roomID, event.Epoch)
		if err != nil {
			continue
		}
		plain, err := openRoomPayload(key, event.Payload, payloadAAD(roomID, event.Epoch, "event"))
		if err != nil {
			continue
		}
		var body MessagePostedPayload
		if err := json.Unmarshal(plain, &body); err != nil {
			continue
		}
		for _, id := range body.Attachments {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := s.store.ListAttachments(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]WireAttachment, 0, len(ids))
	for i := range all {
		if ids[all[i].ID] {
			out = append(out, all[i].ToWire())
		}
	}
	if len(out) > MaxBundleAttachments {
		out = out[len(out)-MaxBundleAttachments:]
	}
	return out, nil
}

func sortedEpochs(keys map[int][]byte) []int {
	out := make([]int, 0, len(keys))
	for epoch := range keys {
		out = append(out, epoch)
	}
	sort.Ints(out)
	return out
}

func findDevice(devices []RoomDevice, deviceID string) (*RoomDevice, bool) {
	for i := range devices {
		if devices[i].DeviceID == deviceID {
			return &devices[i], true
		}
	}
	return nil, false
}

func limitMembers(members []WireMember) []WireMember {
	if len(members) > MaxBundleMembers {
		members = members[len(members)-MaxBundleMembers:]
	}
	return members
}

func limitDevices(devices []WireDevice) []WireDevice {
	if len(devices) > MaxBundleDevices {
		devices = devices[len(devices)-MaxBundleDevices:]
	}
	return devices
}

// PrepareLeave builds and seals a self-leave event at the next local sequence
// without appending it.
func (s *Service) PrepareLeave(ctx context.Context, roomID string, actor Actor) (*Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	last, err := s.store.LastEvent(ctx, roomID)
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

// AcceptRemoteLeave appends a member's own leave event, marks the member left,
// and rotates the room key so the departing device cannot read what follows.
func (s *Service) AcceptRemoteLeave(ctx context.Context, event *Event) error {
	if event == nil || event.Type != EventMemberLeft || event.AuthorID == "" || event.AuthorDevice == "" {
		return ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, err := s.store.GetMember(ctx, event.RoomID, event.AuthorID)
	if err != nil || member.Status != "active" {
		return ErrNotMember
	}
	device, err := s.store.GetRoomDevice(ctx, event.RoomID, event.AuthorDevice)
	if err != nil || device.AccountID != event.AuthorID || device.RevokedAt != nil {
		return ErrForbidden
	}
	epoch := event.Epoch
	if epoch <= 0 {
		epoch = 1
	}
	key, err := s.roomKeyLocked(ctx, event.RoomID, epoch)
	if err != nil {
		return err
	}
	plain, err := openRoomPayload(key, event.Payload, payloadAAD(event.RoomID, epoch, "event"))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	var body MemberLeftPayload
	if err := json.Unmarshal(plain, &body); err != nil || body.AccountID != event.AuthorID {
		return fmt.Errorf("%w: leave payload does not match its author", ErrInvalidEvent)
	}
	if err := s.appendRemoteLocked(ctx, event); err != nil {
		return err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	member.Status = "left"
	member.LeftAt = &now
	if err := s.store.UpdateMember(ctx, member); err != nil {
		return err
	}
	if _, err := s.rotateEpochLocked(ctx, event.RoomID); err != nil {
		return err
	}
	return nil
}

// ApplyRemoteLeave installs an already accepted leave event on the departing
// device without rotating the local key again. The home node owns the new
// epoch; this device is no longer a member and will not receive it.
func (s *Service) ApplyRemoteLeave(ctx context.Context, event *Event) error {
	if event == nil || event.Type != EventMemberLeft {
		return ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendRemoteLocked(ctx, event); err != nil {
		return err
	}
	member, err := s.store.GetMember(ctx, event.RoomID, event.AuthorID)
	if err != nil {
		return err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	member.Status = "left"
	member.LeftAt = &now
	return s.store.UpdateMember(ctx, member)
}
