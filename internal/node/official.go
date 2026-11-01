package node

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/room"
)

var (
	// ErrOfficialAccountRequired means the account is not hosted by an
	// official Toget account home.
	ErrOfficialAccountRequired = errors.New("official rooms require an official hosted account")
	// ErrOfficialAccountUnavailable means an official account exists but its
	// device records could not be refreshed with the host.
	ErrOfficialAccountUnavailable = errors.New("official account is unavailable")
)

var errOfficialRetry = errors.New("official room state changed; retry")

// officialSnapshot returns the local account snapshot and pins the official
// account host. The account must already be hosted before an official room can
// be created or joined.
func (s *Service) officialSnapshot(ctx context.Context) (*account.Snapshot, error) {
	if s == nil {
		return nil, errors.New("node transport is disabled")
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(snapshot.Account.HostURL) == "" {
		return nil, ErrOfficialAccountRequired
	}
	return snapshot, nil
}

// ensureOfficialAccount publishes fresh device certificates to the account
// host before creating or joining a room. Room requests are device-signed, so
// the host must know this device first.
func (s *Service) ensureOfficialAccount(ctx context.Context) (*account.Snapshot, error) {
	snapshot, err := s.officialSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.accounts.ExportDeviceRecords(ctx, s.Address(ctx))
	if err != nil {
		return nil, err
	}
	warnings, _, attempted, synced := s.syncHostedAccount(ctx, snapshot, records, true)
	if !attempted || !synced {
		reason := strings.Join(warnings, "; ")
		if reason == "" {
			reason = "the official host could not refresh this device"
		}
		return nil, fmt.Errorf("%w: %s", ErrOfficialAccountUnavailable, reason)
	}
	return s.accounts.Get(ctx)
}

func officialSigner(actor room.Actor) hosted.RoomSigner {
	return hosted.RoomSigner{
		AccountID: actor.AccountID, DeviceID: actor.DeviceID, PrivateKey: actor.SigningKey,
	}
}

func officialDeviceRef(snapshot *account.Snapshot) hosted.RoomDeviceRef {
	return hosted.RoomDeviceRef{
		AccountID:           snapshot.Account.ID,
		DeviceID:            snapshot.Device.ID,
		DisplayName:         snapshot.Account.Name,
		SigningPublicKey:    append([]byte(nil), snapshot.Device.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), snapshot.Device.EncryptionPublicKey...),
		Certificate:         []byte(snapshot.Device.Certificate),
	}
}

func (s *Service) officialClientForRoom(ctx context.Context, roomID string) (*hosted.Client, hosted.RoomSigner, *account.Snapshot, error) {
	record, err := s.rooms.GetRoom(ctx, roomID)
	if err != nil {
		return nil, hosted.RoomSigner{}, nil, err
	}
	if record.HomeKind != room.HomeKindOfficial {
		return nil, hosted.RoomSigner{}, nil, room.ErrNotOfficialRoom
	}
	if record.HomeAddress == "" || record.HomeServerID == "" {
		return nil, hosted.RoomSigner{}, nil, errors.New("official room home is missing")
	}
	actor, err := s.localActor(ctx)
	if err != nil {
		return nil, hosted.RoomSigner{}, nil, err
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, hosted.RoomSigner{}, nil, err
	}
	client, err := hosted.NewClient(record.HomeAddress)
	if err != nil {
		return nil, hosted.RoomSigner{}, nil, err
	}
	return client, officialSigner(actor), snapshot, nil
}

// CreateOfficialRoom creates a blind hosted room and stores it locally only
// after the host accepts its first sealed event.
func (s *Service) CreateOfficialRoom(ctx context.Context, name, topic string) (*room.Room, *room.Event, error) {
	snapshot, err := s.ensureOfficialAccount(ctx)
	if err != nil {
		return nil, nil, err
	}
	actor, err := s.localActor(ctx)
	if err != nil {
		return nil, nil, err
	}
	client, err := hosted.NewClient(snapshot.Account.HostURL)
	if err != nil {
		return nil, nil, err
	}
	info, err := client.Info(ctx)
	if err != nil {
		return nil, nil, err
	}
	prepared, err := s.rooms.PrepareOfficialRoom(name, topic, actor, room.OfficialHome{
		ServerID: info.HostID, Address: snapshot.Account.HostURL,
	})
	if err != nil {
		return nil, nil, err
	}
	createRequest := hosted.RoomCreateRequest{
		Protocol: hosted.Protocol,
		RoomID:   prepared.Room.ID,
		Device:   officialDeviceRef(snapshot),
		Event:    prepared.Event.ToWire(),
		Keys:     []hosted.RoomKeyPackage{prepared.Package},
	}
	createErr := officialRetryRequest(ctx, func() error {
		_, err := client.CreateRoom(ctx, officialSigner(actor), createRequest)
		return err
	})
	if createErr != nil {
		return nil, nil, officialHostError(createErr)
	}
	created, err := s.rooms.CommitOfficialRoom(ctx, prepared, actor)
	if err != nil {
		return nil, nil, err
	}
	opened, err := s.rooms.GetOpenedEvent(ctx, created.ID, prepared.Event.ID)
	if err != nil {
		return created, nil, err
	}
	return created, opened, nil
}

// officialHostError adds context while preserving typed host errors so callers
// can still detect conflicts and rotation requirements.
func officialHostError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *hosted.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return fmt.Errorf("%w: %s", err, apiErr.Message)
	}
	return err
}

// officialMembers converts the host's membership view into the room wire
// shape. Account public keys are not stored by a blind host; device keys are
// still sufficient to verify every event.
func officialMembers(members []hosted.RoomMemberInfo, devices []hosted.RoomDeviceInfo, roomID string) []room.WireMember {
	out := make([]room.WireMember, 0, len(members))
	for i := range members {
		m := &members[i]
		deviceID := ""
		for j := range devices {
			if devices[j].AccountID == m.AccountID && devices[j].RevokedAt == nil {
				deviceID = devices[j].DeviceID
				break
			}
		}
		out = append(out, room.WireMember{
			RoomID: roomID, AccountID: m.AccountID, DeviceID: deviceID,
			DisplayName: m.DisplayName, Role: m.Role, Status: m.Status,
			EncryptionPublicKey: append([]byte(nil), m.EncryptionPublicKey...),
			JoinedAt:            m.JoinedAt, LeftAt: m.LeftAt,
		})
	}
	return out
}

func officialDevices(devices []hosted.RoomDeviceInfo, roomID string) []room.WireDevice {
	out := make([]room.WireDevice, 0, len(devices))
	for i := range devices {
		d := &devices[i]
		out = append(out, room.WireDevice{
			RoomID: roomID, DeviceID: d.DeviceID, AccountID: d.AccountID,
			PublicKey:           append([]byte(nil), d.SigningPublicKey...),
			EncryptionPublicKey: append([]byte(nil), d.EncryptionPublicKey...),
			JoinedAt:            d.JoinedAt, RevokedAt: d.RevokedAt,
		})
	}
	return out
}

func officialBundleKeys(keys []hosted.RoomKeyPackage) []room.BundleKey {
	out := make([]room.BundleKey, 0, len(keys))
	for i := range keys {
		out = append(out, room.BundleKey{
			Epoch: keys[i].Epoch, Wrapped: append([]byte(nil), keys[i].Wrapped...),
			CreatedAt: keys[i].CreatedAt,
		})
	}
	return out
}

func officialAnchor(roomID string, info hosted.RoomInfo) *room.Event {
	if info.Head <= 0 || info.HeadEventID == "" || info.HeadHash == "" {
		return nil
	}
	return &room.Event{
		ID: info.HeadEventID, RoomID: roomID, Seq: info.Head, Hash: info.HeadHash,
		Epoch: info.Epoch, EnvelopeHash: info.HeadEnvelopeHash,
	}
}

// SyncOfficialRoom pulls official room changes and any missing epoch keys,
// rotates after a leave when the host asks for it, and deposits keys that
// peers are missing.
func (s *Service) SyncOfficialRoom(ctx context.Context, roomID string) (int, error) {
	total, _, err := s.syncOfficialRoomState(ctx, roomID)
	return total, err
}

func (s *Service) syncOfficialRoomState(ctx context.Context, roomID string) (int, *room.Event, error) {
	if s == nil {
		return 0, nil, nil
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	record, err := s.rooms.GetRoom(ctx, roomID)
	if err != nil {
		return 0, nil, err
	}
	if record.HomeKind != room.HomeKindOfficial {
		return 0, nil, nil
	}
	actor, err := s.localActor(ctx)
	if err != nil {
		return 0, nil, err
	}
	client, err := hosted.NewClient(record.HomeAddress)
	if err != nil {
		return 0, nil, err
	}
	signer := officialSigner(actor)
	total := 0
	var anchor *room.Event
	previousNeed := ""
	for batch := 0; batch < 100; batch++ {
		local, err := s.rooms.GetRoom(ctx, roomID)
		if err != nil {
			return total, anchor, err
		}
		after, err := s.rooms.LastSeq(ctx, roomID)
		if err != nil {
			return total, anchor, err
		}
		need, err := s.rooms.MissingOfficialEpochs(ctx, roomID, local.Epoch)
		if err != nil {
			return total, anchor, err
		}
		response, err := client.SyncRoom(ctx, signer, hosted.RoomSyncRequest{
			Protocol: hosted.Protocol, RoomID: roomID, After: after, NeedKeys: need,
		})
		if err != nil {
			return total, anchor, officialHostError(err)
		}
		if response.Room.ID != roomID || response.Room.Epoch < 1 {
			return total, anchor, errors.New("official room sync response does not match the room")
		}
		bundle := &room.SyncBundle{
			Room:    *local,
			Members: officialMembers(response.Members, response.Devices, roomID),
			Devices: officialDevices(response.Devices, roomID),
			Keys:    officialBundleKeys(response.Keys),
			Events:  response.Events,
			After:   after,
			Head:    response.Room.Head, HasMore: response.HasMore,
			ServerTime: time.Now().UTC(),
		}
		bundle.Room.Epoch = response.Room.Epoch
		bundle.Room.CreatedBy = response.Room.OwnerAccountID
		snapshot, err := s.accounts.Get(ctx)
		if err != nil {
			return total, anchor, err
		}
		encryptionKey, err := s.accounts.UnsealDeviceEncryptionKey(snapshot.Device)
		if err != nil {
			return total, anchor, err
		}
		n, err := s.rooms.ImportSync(ctx, bundle, snapshot.Device.ID, encryptionKey)
		if err != nil {
			return total, anchor, err
		}
		total += n
		s.uploadOfficialKeyRequests(ctx, client, signer, roomID, response)
		s.healOfficialSelfKeys(ctx, client, signer, roomID, snapshot, response)

		if current, err := s.rooms.LastSeq(ctx, roomID); err == nil && current == 0 {
			anchor = officialAnchor(roomID, response.Room)
		} else {
			anchor = nil
		}
		if response.RotationRequired {
			if err := s.rotateOfficialRoom(ctx, client, signer, roomID, actor, anchor); err != nil {
				if errors.Is(err, errOfficialRetry) {
					continue
				}
				return total, anchor, err
			}
			continue
		}
		if response.HasMore {
			continue
		}
		remaining, err := s.rooms.MissingOfficialEpochs(ctx, roomID, response.Room.Epoch)
		if err != nil {
			return total, anchor, err
		}
		key := fmt.Sprint(remaining)
		if len(remaining) > 0 && key != previousNeed {
			previousNeed = key
			continue
		}
		return total, anchor, nil
	}
	return total, anchor, errors.New("official room sync did not settle")
}

func (s *Service) uploadOfficialKeyRequests(ctx context.Context, client *hosted.Client, signer hosted.RoomSigner, roomID string, response *hosted.RoomSyncResponse) {
	if len(response.KeyRequests) == 0 {
		return
	}
	requests := make([]room.KeyRequest, 0, len(response.KeyRequests))
	for i := range response.KeyRequests {
		requests = append(requests, room.KeyRequest{
			DeviceID: response.KeyRequests[i].DeviceID, Epochs: response.KeyRequests[i].Epochs,
		})
	}
	targets := make([]room.KeyTarget, 0, len(response.Devices))
	for i := range response.Devices {
		d := &response.Devices[i]
		if d.RevokedAt == nil && len(d.EncryptionPublicKey) == 32 {
			targets = append(targets, room.KeyTarget{
				DeviceID: d.DeviceID, EncryptionPublicKey: d.EncryptionPublicKey,
			})
		}
	}
	packages, err := s.rooms.WrapOfficialKeys(ctx, roomID, requests, targets)
	if err != nil || len(packages) == 0 {
		if err != nil {
			s.logger.Debug("official room: wrap peer keys", "room", roomID, "error", err)
		}
		return
	}
	for start := 0; start < len(packages); start += hosted.MaxRoomPackages {
		end := start + hosted.MaxRoomPackages
		if end > len(packages) {
			end = len(packages)
		}
		if _, err := client.UploadRoomKeys(ctx, signer, hosted.RoomKeyUploadRequest{
			Protocol: hosted.Protocol, RoomID: roomID, Packages: packages[start:end],
		}); err != nil {
			s.logger.Debug("official room: upload peer keys", "room", roomID, "error", err)
			return
		}
	}
}

func hasOfficialEpoch(keys []hosted.RoomKeyPackage, epoch int) bool {
	for i := range keys {
		if keys[i].Epoch == epoch {
			return true
		}
	}
	return false
}

// healOfficialSelfKeys re-deposits this device's own wrapped keys when the
// host has none for the current epoch. This repairs an interrupted join or a
// registration whose key upload failed.
func (s *Service) healOfficialSelfKeys(ctx context.Context, client *hosted.Client, signer hosted.RoomSigner, roomID string, snapshot *account.Snapshot, response *hosted.RoomSyncResponse) {
	if response.Room.Epoch < 1 || hasOfficialEpoch(response.Keys, response.Room.Epoch) {
		return
	}
	packages, err := s.rooms.WrapOfficialKeysForDevice(ctx, roomID, snapshot.Device.ID, snapshot.Device.EncryptionPublicKey)
	if err != nil || len(packages) == 0 {
		return
	}
	for start := 0; start < len(packages); start += hosted.MaxRoomPackages {
		end := start + hosted.MaxRoomPackages
		if end > len(packages) {
			end = len(packages)
		}
		if _, err := client.UploadRoomKeys(ctx, signer, hosted.RoomKeyUploadRequest{
			Protocol: hosted.Protocol, RoomID: roomID, Packages: packages[start:end],
		}); err != nil {
			s.logger.Debug("official room: heal self keys", "room", roomID, "error", err)
			return
		}
	}
}

// rotateOfficialRoom creates the next epoch for every active device after a
// leave. A conflict means another member changed the head, so the caller
// re-syncs and tries again.
func (s *Service) rotateOfficialRoom(ctx context.Context, client *hosted.Client, signer hosted.RoomSigner, roomID string, actor room.Actor, anchor *room.Event) error {
	rotation, err := s.rooms.PrepareOfficialRotation(ctx, roomID, actor, anchor)
	if err != nil {
		return err
	}
	rotateRequest := hosted.RoomRotateRequest{
		Protocol: hosted.Protocol, Epoch: rotation.Epoch,
		Event: rotation.Event.ToWire(), Keys: rotation.Keys,
	}
	err = officialRetryRequest(ctx, func() error {
		_, err := client.RotateRoom(ctx, signer, rotateRequest)
		return err
	})
	if err != nil {
		if hosted.IsRoomConflict(err) || hosted.RoomErrorCode(err) == "key_coverage" {
			return errOfficialRetry
		}
		return officialHostError(err)
	}
	return s.rooms.CommitOfficialRotation(ctx, rotation, actor)
}

// PostOfficialMessage posts a sealed, envelope-signed message and installs the
// host-accepted copy.
func (s *Service) PostOfficialMessage(ctx context.Context, roomID string, actor room.Actor, body room.MessagePostedPayload) (*room.Event, error) {
	return s.postOfficial(ctx, roomID, func(anchor *room.Event) (*room.Event, error) {
		return s.rooms.PrepareOfficialMessage(ctx, roomID, actor, body, anchor)
	})
}

// PostOfficialEvent posts an Agent, approval, tool, or settings event.
func (s *Service) PostOfficialEvent(ctx context.Context, roomID string, actor room.Actor, eventType string, payload any) (*room.Event, error) {
	return s.postOfficial(ctx, roomID, func(anchor *room.Event) (*room.Event, error) {
		return s.rooms.PrepareOfficialEvent(ctx, roomID, actor, eventType, payload, anchor)
	})
}

// RenameOfficialRoom posts an owner-signed settings event to the blind host
// and updates the local name/topic projection after the host accepts it.
func (s *Service) RenameOfficialRoom(ctx context.Context, roomID string, actor room.Actor, name, topic string) (*room.Room, *room.Event, error) {
	name = strings.TrimSpace(name)
	topic = strings.TrimSpace(topic)
	if name == "" || len(name) > room.MaxNameLength {
		return nil, nil, room.ErrInvalidName
	}
	if len(topic) > room.MaxTopicLength {
		return nil, nil, room.ErrInvalidTopic
	}
	event, err := s.PostOfficialEvent(ctx, roomID, actor, room.EventRoomSettingsChanged, room.RoomSettingsChangedPayload{
		Name: name, Topic: topic,
	})
	if err != nil {
		return nil, nil, err
	}
	updated, err := s.rooms.ApplyOfficialRoomMetadata(ctx, roomID, name, topic)
	if err != nil {
		return nil, event, err
	}
	return updated, event, nil
}

func (s *Service) postOfficial(ctx context.Context, roomID string, prepare func(*room.Event) (*room.Event, error)) (*room.Event, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		_, anchor, err := s.syncOfficialRoomState(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("sync before send: %w", err)
		}
		event, err := prepare(anchor)
		if err != nil {
			return nil, err
		}
		client, signer, _, err := s.officialClientForRoom(ctx, roomID)
		if err != nil {
			return nil, err
		}
		err = officialRetryRequest(ctx, func() error {
			_, err := client.PostRoomEvent(ctx, signer, event.ToWire())
			return err
		})
		if err != nil {
			lastErr = err
			if hosted.IsRoomRotationRequired(err) || hosted.IsRoomConflict(err) {
				continue
			}
			return nil, officialHostError(err)
		}
		if err := s.rooms.AppendRemote(ctx, event); err != nil {
			if _, syncErr := s.SyncOfficialRoom(ctx, roomID); syncErr == nil {
				if opened, getErr := s.rooms.GetOpenedEvent(ctx, roomID, event.ID); getErr == nil {
					return opened, nil
				}
			}
			return nil, err
		}
		return s.rooms.GetOpenedEvent(ctx, roomID, event.ID)
	}
	return nil, lastErr
}

// CreateOfficialInvite asks the host to arm a single-use invite and then
// installs the invite event locally.
func (s *Service) CreateOfficialInvite(ctx context.Context, roomID string, actor room.Actor, history string, ttl time.Duration) (*room.Invite, *room.Event, string, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		_, anchor, err := s.syncOfficialRoomState(ctx, roomID)
		if err != nil {
			return nil, nil, "", fmt.Errorf("sync before invite: %w", err)
		}
		prepared, err := s.rooms.PrepareOfficialInvite(ctx, roomID, actor, history, ttl, anchor)
		if err != nil {
			return nil, nil, "", err
		}
		client, signer, _, err := s.officialClientForRoom(ctx, roomID)
		if err != nil {
			return nil, nil, "", err
		}
		hash := sha256.Sum256([]byte(prepared.Secret))
		inviteRequest := hosted.RoomInviteRequest{
			Protocol: hosted.Protocol, RoomID: roomID, InviteID: prepared.ID,
			SecretHash: hash[:], History: prepared.History, ExpiresAt: prepared.ExpiresAt,
			Event: prepared.Event.ToWire(), Keys: prepared.Keys,
		}
		err = officialRetryRequest(ctx, func() error {
			_, err := client.CreateRoomInvite(ctx, signer, inviteRequest)
			return err
		})
		if err != nil {
			lastErr = err
			if hosted.IsRoomRotationRequired(err) || hosted.IsRoomConflict(err) {
				continue
			}
			return nil, nil, "", officialHostError(err)
		}
		if err := s.rooms.AppendRemote(ctx, &prepared.Event); err != nil {
			if _, syncErr := s.SyncOfficialRoom(ctx, roomID); syncErr != nil {
				return nil, nil, "", err
			}
		}
		record, err := s.rooms.GetRoom(ctx, roomID)
		if err != nil {
			return nil, nil, "", err
		}
		invite := &room.Invite{
			ID: prepared.ID, RoomID: roomID, Secret: prepared.Secret,
			CreatedBy: actor.AccountID, History: prepared.History,
			ExpiresAt: prepared.ExpiresAt, CreatedAt: time.Now().UTC(),
		}
		code, err := EncodeInvite(InviteCode{
			Protocol: Protocol, Secret: prepared.Secret,
			URL: record.HomeAddress, ServerID: record.HomeServerID, RoomID: roomID,
			RoomName: record.Name, RoomTopic: record.Topic, HomeKind: InviteKindOfficial,
		})
		if err != nil {
			return nil, nil, "", err
		}
		invite.Code = code
		opened, err := s.rooms.GetOpenedEvent(ctx, roomID, prepared.Event.ID)
		if err != nil {
			return invite, nil, code, nil
		}
		return invite, opened, code, nil
	}
	return nil, nil, "", lastErr
}

// LeaveOfficialRoom records a leave on the host, applies it locally, and
// archives the room on this device.
func (s *Service) LeaveOfficialRoom(ctx context.Context, roomID string, actor room.Actor) (*room.Event, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_, anchor, err := s.syncOfficialRoomState(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("sync before leave: %w", err)
		}
		event, err := s.rooms.PrepareOfficialLeave(ctx, roomID, actor, anchor)
		if err != nil {
			return nil, err
		}
		client, signer, _, err := s.officialClientForRoom(ctx, roomID)
		if err != nil {
			return nil, err
		}
		// Keep the same event across retries: the host treats a replay of an
		// already accepted leave as success, so a lost response is recoverable.
		leaveErr := officialRetryRequest(ctx, func() error {
			_, err := client.LeaveRoom(ctx, signer, event.ToWire())
			return err
		})
		if leaveErr != nil {
			lastErr = leaveErr
			if hosted.IsRoomConflict(leaveErr) {
				continue
			}
			return nil, officialHostError(leaveErr)
		}
		if err := s.rooms.ApplyRemoteLeave(ctx, event); err != nil {
			return nil, err
		}
		s.archiveAfterLeave(ctx, roomID, actor)
		return s.rooms.GetOpenedEvent(ctx, roomID, event.ID)
	}
	return nil, lastErr
}

// officialRetryRequest repeats an idempotent signed request after a transport
// failure or a server-side transient error. The exact same bytes, including
// the event ID, are sent each time.
func officialRetryRequest(ctx context.Context, send func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = send()
		if err == nil || !officialSendRetryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
		}
	}
	return err
}

// officialSendRetryable reports whether repeating the same signed request is
// safe and useful. Permanent 4xx rejections are not retried.
func officialSendRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *hosted.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 408 || apiErr.Status == 429 || apiErr.Status >= 500
	}
	return true
}

// JoinOfficial redeems an official invite. The invite secret unlocks the
// current epoch without requiring an existing member to be online; the new
// device rewraps those keys for itself and uploads them.
func (s *Service) JoinOfficial(ctx context.Context, code *InviteCode) (*room.Room, error) {
	if code == nil || code.HomeKind != InviteKindOfficial {
		return nil, ErrInvalidInviteCode
	}
	snapshot, err := s.ensureOfficialAccount(ctx)
	if err != nil {
		return nil, err
	}
	actor, err := s.localActor(ctx)
	if err != nil {
		return nil, err
	}
	client, err := hosted.NewClient(code.URL)
	if err != nil {
		return nil, err
	}
	info, err := client.Info(ctx)
	if err != nil {
		return nil, officialHostError(err)
	}
	if info.HostID != code.ServerID {
		return nil, errors.New("invite host does not match its signed identity")
	}
	if existing, err := s.rooms.GetRoom(ctx, code.RoomID); err == nil {
		if existing.ArchivedAt == nil {
			return existing, nil
		}
		// The room may have been hidden locally without leaving. An active
		// membership can be restored safely; a departed device cannot, because
		// the host has already revoked its room keys and device record.
		members, listErr := s.rooms.ListMembers(ctx, code.RoomID)
		if listErr == nil {
			for i := range members {
				if members[i].AccountID == actor.AccountID && members[i].Status == "active" {
					return s.rooms.RestoreLocal(ctx, code.RoomID, actor)
				}
			}
		}
		return nil, fmt.Errorf("%w: join from a new device with a fresh invite", room.ErrRoomLeft)
	}
	joinRequest := hosted.RoomJoinRequest{
		Protocol: hosted.Protocol, RoomID: code.RoomID, Secret: code.Secret,
		Device: officialDeviceRef(snapshot),
	}
	var response *hosted.RoomJoinResponse
	err = officialRetryRequest(ctx, func() error {
		var joinErr error
		response, joinErr = client.JoinRoom(ctx, officialSigner(actor), joinRequest)
		return joinErr
	})
	if err != nil {
		return nil, officialHostError(err)
	}
	if response.Room.ID != code.RoomID || response.Room.Epoch < 1 {
		return nil, errors.New("invite response does not match the invite code")
	}
	encryptionKey, err := s.accounts.UnsealDeviceEncryptionKey(snapshot.Device)
	if err != nil {
		return nil, err
	}
	keys := make([]room.BundleKey, 0, len(response.InviteKeys))
	for i := range response.InviteKeys {
		pkg := &response.InviteKeys[i]
		plain, err := room.UnwrapInviteRoomKey(code.Secret, code.RoomID, pkg.Epoch, pkg.Wrapped)
		if err != nil {
			return nil, fmt.Errorf("open invite key for epoch %d: %w", pkg.Epoch, err)
		}
		wrapped, err := room.WrapRoomKey(code.RoomID, pkg.Epoch, snapshot.Device.ID, snapshot.Device.EncryptionPublicKey, plain)
		if err != nil {
			return nil, err
		}
		keys = append(keys, room.BundleKey{Epoch: pkg.Epoch, Wrapped: wrapped, CreatedAt: pkg.CreatedAt})
	}
	events := response.Events
	from := response.Room.Head + 1
	if len(events) > 0 {
		from = events[0].Seq
	}
	head := response.Room.Head
	if response.HasMore && len(events) > 0 {
		head = events[len(events)-1].Seq
	}
	metadata := room.Room{
		ID: code.RoomID, Name: code.RoomName, Topic: code.RoomTopic,
		HomeNode: code.ServerID, HomeServerID: code.ServerID, HomeAddress: code.URL,
		HomeKind: room.HomeKindOfficial, CreatedBy: response.Room.OwnerAccountID,
		Epoch: response.Room.Epoch, CreatedAt: response.Room.CreatedAt,
	}
	members := officialMembers(response.Members, response.Devices, code.RoomID)
	devices := officialDevices(response.Devices, code.RoomID)
	joined := room.Member{
		RoomID: code.RoomID, AccountID: actor.AccountID, DeviceID: actor.DeviceID,
		DisplayName: actor.DisplayName, Role: "member", Status: "active",
		AccountPublicKey:    actor.AccountPublicKey,
		EncryptionPublicKey: actor.EncryptionPublicKey,
		JoinedAt:            response.JoinedAt,
	}
	for i := range members {
		if members[i].AccountID == actor.AccountID {
			joined = members[i].ToMember()
			joined.DeviceID = actor.DeviceID
			joined.AccountPublicKey = actor.AccountPublicKey
			joined.EncryptionPublicKey = actor.EncryptionPublicKey
			joined.Role = "member"
			joined.Status = "active"
			break
		}
	}
	bundle := &room.JoinBundle{
		Room: metadata, Member: joined, Members: members, Devices: devices,
		Keys: keys, Events: events, FromSeq: from, Head: head,
		History: response.History, InviteID: response.InviteID, JoinedAt: response.JoinedAt,
		ServerTime: time.Now().UTC(),
	}
	imported, err := s.rooms.ImportJoinBundle(ctx, bundle, snapshot.Device.ID, encryptionKey)
	if err != nil {
		return nil, err
	}
	if packages, err := s.rooms.WrapOfficialKeysForDevice(ctx, code.RoomID, snapshot.Device.ID, snapshot.Device.EncryptionPublicKey); err == nil && len(packages) > 0 {
		if _, err := client.UploadRoomKeys(ctx, officialSigner(actor), hosted.RoomKeyUploadRequest{
			Protocol: hosted.Protocol, RoomID: code.RoomID, Packages: packages,
		}); err != nil {
			s.logger.Debug("official room: upload join keys", "room", code.RoomID, "error", err)
		}
	}
	s.TriggerAccountSync()
	return imported, nil
}
