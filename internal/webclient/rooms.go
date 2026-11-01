package webclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
)

const (
	invitePrefix       = "toget-invite:v1:"
	inviteProtocol     = 1
	inviteKindOfficial = "official"
	maxRoomNameRunes   = 80
	maxRoomTopicRunes  = 280
	maxMessageRunes    = 6000
)

// RoomView is the complete local view of one room.
type RoomView struct {
	Summary          RoomSummary    `json:"summary"`
	Messages         []MessageState `json:"messages"`
	Members          []MemberState  `json:"members"`
	Devices          []DeviceInfo   `json:"devices"`
	Epoch            int            `json:"epoch"`
	MyRole           string         `json:"my_role,omitempty"`
	CanRename        bool           `json:"can_rename"`
	CanInvite        bool           `json:"can_invite"`
	RotationRequired bool           `json:"rotation_required"`
	LockedCount      int            `json:"locked_count"`
	VaultPending     bool           `json:"vault_pending"`
}

// CreateRoomInput names a new room.
type CreateRoomInput struct {
	Name  string `json:"name"`
	Topic string `json:"topic"`
}

// InviteInput controls an invitation.
type InviteInput struct {
	History  string `json:"history"` // all | recent | none
	TTLHours int    `json:"ttl_hours"`
}

// InviteResult is a portable single-use invite.
type InviteResult struct {
	Code      string    `json:"code"`
	History   string    `json:"history"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RefreshResult reports a room-list refresh and non-fatal room warnings.
type RefreshResult struct {
	Rooms    []RoomSummary `json:"rooms"`
	Warnings []string      `json:"warnings,omitempty"`
}

// Rooms returns the locally cached room list, newest activity first.
func (c *Client) Rooms() []RoomSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	deviceID := ""
	if c.state.Account != nil {
		deviceID = c.state.Account.Device.ID
	}
	rooms := c.activeRoomsLocked()
	summaries := make([]RoomSummary, 0, len(rooms))
	for _, room := range rooms {
		summaries = append(summaries, roomSummary(room, deviceID))
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		left, right := summaries[i], summaries[j]
		if left.LastAt != nil && right.LastAt != nil && !left.LastAt.Equal(*right.LastAt) {
			return left.LastAt.After(*right.LastAt)
		}
		if left.LastAt != nil && right.LastAt == nil {
			return true
		}
		if left.LastAt == nil && right.LastAt != nil {
			return false
		}
		return left.JoinedAt.After(right.JoinedAt)
	})
	return summaries
}

// Room returns a locally cached room view without contacting the host.
func (c *Client) Room(roomID string) (*RoomView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return nil, Errorf("room_not_found", "找不到这个房间。")
	}
	return c.viewLocked(roomState), nil
}

// RefreshRooms reconciles the account's room memberships with the host. A
// browser that signed in on a new device adds itself to each active room and
// then syncs with the keys restored from the encrypted vault.
func (c *Client) RefreshRooms(ctx context.Context) (*RefreshResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.refreshSessionLocked(ctx, client); err != nil {
		return nil, err
	}
	if _, err := client.Info(ctx); err != nil {
		return nil, mapHostError(err)
	}
	if err := c.refreshAccountStatusLocked(ctx, client); err != nil {
		return nil, err
	}
	list, err := client.ListRooms(ctx, c.signerLocked())
	if err != nil {
		return nil, mapHostError(err)
	}
	active := make(map[string]hosted.RoomListEntry, len(list.Rooms))
	for _, entry := range list.Rooms {
		if entry.Room.ID != "" && entry.Status == "active" {
			active[entry.Room.ID] = entry
		}
	}
	// Drop rooms the account is no longer an active member of.
	kept := c.state.Rooms[:0]
	for _, roomState := range c.state.Rooms {
		if roomState == nil {
			continue
		}
		if _, ok := active[roomState.ID]; ok || roomState.Left {
			kept = append(kept, roomState)
		}
	}
	c.state.Rooms = kept
	result := &RefreshResult{}
	rooms := make([]*hosted.RoomListEntry, 0, len(list.Rooms))
	for i := range list.Rooms {
		if list.Rooms[i].Status == "active" {
			rooms = append(rooms, &list.Rooms[i])
		}
	}
	for _, entry := range rooms {
		roomState := c.roomLocked(entry.Room.ID)
		if roomState == nil {
			roomState = &RoomState{
				ID: entry.Room.ID, HomeURL: c.origin, HostID: c.state.Account.HostID,
				OwnerAccountID: entry.Room.OwnerAccountID, Epoch: maxInt(entry.Room.Epoch, 1),
				History: entry.History, JoinedAt: entry.JoinedAt,
				Head: entry.Room.Head,
			}
			c.upsertRoomLocked(roomState)
		}
		roomState.Head = entry.Room.Head
		if entry.Room.Epoch > roomState.Epoch {
			roomState.Epoch = entry.Room.Epoch
		}
		if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
			result.Warnings = append(result.Warnings, c.roomWarningLocked(roomState, err))
			continue
		}
		if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
			result.Warnings = append(result.Warnings, c.roomWarningLocked(roomState, err))
		}
	}
	result.Rooms = c.summariesLocked()
	return result, nil
}

// SyncRoom fetches new events and key packages for one room.
func (c *Client) SyncRoom(ctx context.Context, roomID string) (*RoomView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return nil, Errorf("room_not_found", "找不到这个房间。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
		return nil, err
	}
	if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
		return nil, err
	}
	return c.viewLocked(roomState), nil
}

// CreateRoom creates a room on the account host. The host stores only sealed
// events and device-wrapped keys.
func (c *Client) CreateRoom(ctx context.Context, input CreateRoomInput) (*RoomView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	name := strings.TrimSpace(input.Name)
	topic := strings.TrimSpace(input.Topic)
	if name == "" {
		return nil, Errorf("invalid_name", "请填写房间名称。")
	}
	if len([]rune(name)) > maxRoomNameRunes {
		return nil, Errorf("invalid_name", "房间名称最多 80 个字。")
	}
	if len([]rune(topic)) > maxRoomTopicRunes {
		return nil, Errorf("invalid_topic", "房间说明最多 280 个字。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	info, err := client.Info(ctx)
	if err != nil {
		return nil, mapHostError(err)
	}
	if info.HostID != c.state.Account.HostID {
		return nil, Errorf("invalid_host", "服务器身份校验失败。")
	}
	now := c.now().UTC().Truncate(time.Millisecond)
	roomID := room.NewOfficialRoomID()
	key, err := room.NewRoomKey()
	if err != nil {
		return nil, Errorf("internal", "无法生成房间密钥。")
	}
	actor := c.actorLocked()
	event, err := room.BuildOfficialEvent(roomID, 1, nil, room.EventRoomCreated, actor,
		room.RoomCreatedPayload{Name: name, Topic: topic}, 1, key, now)
	if err != nil {
		return nil, Errorf("internal", "无法创建房间事件。")
	}
	wrapped, err := room.WrapRoomKey(roomID, 1, actor.DeviceID, actor.EncryptionPublicKey, key)
	if err != nil {
		return nil, Errorf("internal", "无法封装房间密钥。")
	}
	response, err := client.CreateRoom(ctx, c.signerLocked(), hosted.RoomCreateRequest{
		Protocol: hosted.Protocol, RoomID: roomID, Device: c.deviceRefLocked(),
		Event: event.ToWire(), Keys: []hosted.RoomKeyPackage{{
			Epoch: 1, DeviceID: actor.DeviceID, Wrapped: wrapped, CreatedAt: now,
		}},
	})
	if err != nil {
		return nil, mapRoomError(err)
	}
	roomState := &RoomState{
		ID: roomID, Name: name, Topic: topic, HomeURL: c.origin, HostID: c.state.Account.HostID,
		OwnerAccountID: c.state.Account.ID, Epoch: 1, Head: response.Room.Head,
		LastSeq: 1, LastID: event.ID, LastHash: event.Hash, LastEnvelopeHash: event.EnvelopeHash,
		Keys: map[int][]byte{1: key}, DeviceRegistered: true,
		Members: []MemberState{{
			AccountID: c.state.Account.ID, DisplayName: c.state.Account.Name,
			Role: "owner", Status: "active", EncryptionPublicKey: actor.EncryptionPublicKey,
			JoinedAt: now,
		}},
		Devices: []DeviceInfo{{
			DeviceID: actor.DeviceID, AccountID: c.state.Account.ID,
			SigningPublicKey:    append([]byte(nil), c.state.Account.Device.PublicKey...),
			EncryptionPublicKey: append([]byte(nil), actor.EncryptionPublicKey...),
			JoinedAt:            now,
		}},
		History: room.HistoryAll, JoinedAt: now, ReadSeq: 1, UpdatedAt: now,
	}
	c.upsertRoomLocked(roomState)
	c.applyOpenedEventLocked(roomState, event, actor.DisplayName)
	if err := c.saveVaultLocked(ctx, client); err != nil {
		c.state.Account.VaultDirty = true
	}
	return c.viewLocked(roomState), nil
}

// SendMessage appends an end-to-end encrypted message to a room.
func (c *Client) SendMessage(ctx context.Context, roomID, text string) (*MessageState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, Errorf("empty_message", "消息不能为空。")
	}
	if len([]rune(text)) > maxMessageRunes {
		return nil, Errorf("message_too_long", "消息最多 6000 个字。")
	}
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return nil, Errorf("room_not_found", "找不到这个房间。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		vaultWanted, err := c.syncRoomLocked(ctx, client, roomState)
		if err != nil {
			return nil, err
		}
		key := roomState.Keys[roomState.Epoch]
		if len(key) != 32 {
			return nil, Errorf("key_missing", "这台设备还没有这个房间的密钥，请先用原设备同步。")
		}
		actor := c.actorLocked()
		anchor := anchorFromRoom(roomState)
		now := c.now().UTC().Truncate(time.Millisecond)
		event, err := room.BuildOfficialEvent(roomState.ID, roomState.LastSeq+1, anchor,
			room.EventMessagePosted, actor, room.MessagePostedPayload{
				Text: text, Format: "plain",
			}, roomState.Epoch, key, now)
		if err != nil {
			return nil, Errorf("internal", "无法加密消息。")
		}
		response, err := client.PostRoomEvent(ctx, c.signerLocked(), event.ToWire())
		if err == nil {
			c.applyOpenedEventLocked(roomState, event, actor.DisplayName)
			c.advanceAnchorLocked(roomState, *event)
			roomState.Head = response.Head
			roomState.UpdatedAt = now
			if err := c.saveVaultLocked(ctx, client); err != nil {
				c.state.Account.VaultDirty = true
			}
			return c.findMessageLocked(roomState, event.ID), nil
		}
		lastErr = err
		if hosted.IsRoomRotationRequired(err) {
			if _, rotationErr := c.rotateRoomLocked(ctx, client, roomState, "device_change"); rotationErr != nil {
				return nil, mapRoomError(rotationErr)
			}
			continue
		}
		if !hosted.IsRoomConflict(err) && !retryableHostError(err) {
			return nil, mapRoomError(err)
		}
		// A lost response may mean the host already accepted this event.
		if _, syncErr := c.syncRoomLocked(ctx, client, roomState); syncErr == nil {
			if existing := c.findMessageLocked(roomState, event.ID); existing != nil {
				return existing, nil
			}
		}
		if vaultWanted {
			_ = c.saveVaultLocked(ctx, client)
		}
	}
	return nil, mapRoomError(lastErr)
}

// CreateInvite creates a single-use invite whose keys are wrapped under the
// invite secret, so no existing member has to be online when it is redeemed.
func (c *Client) CreateInvite(ctx context.Context, roomID string, input InviteInput) (*InviteResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return nil, Errorf("room_not_found", "找不到这个房间。")
	}
	history := strings.TrimSpace(input.History)
	switch history {
	case "", room.HistoryNone:
		history = room.HistoryNone
	case room.HistoryRecent, room.HistoryAll:
	default:
		return nil, Errorf("invalid_invite", "邀请的历史范围无效。")
	}
	ttl := time.Duration(input.TTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if ttl > hosted.RoomInviteTTL {
		ttl = hosted.RoomInviteTTL
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
			return nil, err
		}
		key := roomState.Keys[roomState.Epoch]
		if len(key) != 32 {
			return nil, Errorf("key_missing", "这台设备还没有这个房间的密钥，暂时不能邀请。")
		}
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return nil, Errorf("internal", "无法生成邀请密钥。")
		}
		secret := base64.RawURLEncoding.EncodeToString(secretBytes)
		inviteID := identity.NewID()
		now := c.now().UTC().Truncate(time.Millisecond)
		expires := now.Add(ttl)
		actor := c.actorLocked()
		event, err := room.BuildOfficialEvent(roomState.ID, roomState.LastSeq+1, anchorFromRoom(roomState),
			room.EventMemberInvited, actor, room.MemberInvitedPayload{
				InviteID: inviteID, History: history, ExpiresAt: expires.Format(time.RFC3339),
			}, roomState.Epoch, key, now)
		if err != nil {
			return nil, Errorf("internal", "无法创建邀请事件。")
		}
		epochs := sortedEpochs(roomState.Keys)
		packages := make([]hosted.RoomKeyPackage, 0, len(epochs))
		for _, epoch := range epochs {
			if history != room.HistoryAll && epoch != roomState.Epoch {
				continue
			}
			wrapped, err := room.WrapInviteRoomKey(secret, roomState.ID, epoch, roomState.Keys[epoch])
			if err != nil {
				return nil, Errorf("internal", "无法封装邀请密钥。")
			}
			packages = append(packages, hosted.RoomKeyPackage{Epoch: epoch, Wrapped: wrapped, CreatedAt: now})
		}
		hash := sha256Bytes([]byte(secret))
		response, err := client.CreateRoomInvite(ctx, c.signerLocked(), hosted.RoomInviteRequest{
			Protocol: hosted.Protocol, RoomID: roomState.ID, InviteID: inviteID,
			SecretHash: hash, History: history, ExpiresAt: expires,
			Event: event.ToWire(), Keys: packages,
		})
		if err == nil {
			c.applyOpenedEventLocked(roomState, event, actor.DisplayName)
			c.advanceAnchorLocked(roomState, *event)
			roomState.Head = event.Seq
			roomState.UpdatedAt = now
			if err := c.saveVaultLocked(ctx, client); err != nil {
				c.state.Account.VaultDirty = true
			}
			code, err := encodeOfficialInvite(inviteCode{
				Secret: secret, URL: c.hostURLWithIDLocked(), ServerID: c.state.Account.HostID,
				RoomID: roomState.ID, RoomName: roomState.Name, RoomTopic: roomState.Topic,
			})
			if err != nil {
				return nil, Errorf("internal", "无法生成邀请码。")
			}
			return &InviteResult{Code: code, History: history, ExpiresAt: response.ExpiresAt}, nil
		}
		lastErr = err
		if hosted.IsRoomConflict(err) || hosted.IsRoomRotationRequired(err) {
			continue
		}
		return nil, mapRoomError(err)
	}
	return nil, mapRoomError(lastErr)
}

// JoinInvite redeems a portable invite and imports the invite-wrapped keys.
func (c *Client) JoinInvite(ctx context.Context, rawCode string) (*RoomView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	code, err := decodeOfficialInvite(rawCode, c.origin, c.state.Account.HostID)
	if err != nil {
		return nil, err
	}
	if existing := c.roomLocked(code.RoomID); existing != nil {
		client, clientErr := c.hostedClient()
		if clientErr == nil {
			_, _ = c.syncRoomLocked(ctx, client, existing)
			return c.viewLocked(existing), nil
		}
		return c.viewLocked(existing), nil
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	response, err := client.JoinRoom(ctx, c.signerLocked(), hosted.RoomJoinRequest{
		Protocol: hosted.Protocol, RoomID: code.RoomID, Secret: code.Secret,
		Device: c.deviceRefLocked(),
	})
	if err != nil {
		if hosted.RoomErrorCode(err) == "already_member" {
			return c.attachExistingRoomLocked(ctx, client, code)
		}
		return nil, mapRoomError(err)
	}
	roomState := &RoomState{
		ID: code.RoomID, Name: code.RoomName, Topic: code.RoomTopic,
		HomeURL: c.origin, HostID: c.state.Account.HostID,
		OwnerAccountID: response.Room.OwnerAccountID, Epoch: maxInt(response.Room.Epoch, 1),
		Head: response.Room.Head, History: response.History, JoinedAt: response.JoinedAt,
		Keys: map[int][]byte{}, DeviceRegistered: true,
		Members:   membersFromHosted(response.Members),
		Devices:   devicesFromHosted(response.Devices),
		UpdatedAt: c.now().UTC(),
	}
	for _, pkg := range response.InviteKeys {
		key, err := room.UnwrapInviteRoomKey(code.Secret, code.RoomID, pkg.Epoch, pkg.Wrapped)
		if err == nil && len(key) == 32 {
			roomState.Keys[pkg.Epoch] = key
		}
	}
	c.upsertRoomLocked(roomState)
	if err := c.uploadSelfKeysLocked(ctx, client, roomState); err != nil {
		// The room still works locally; a later sync retries the upload.
		_ = err
	}
	if _, err := c.applyWireEventsLocked(roomState, response.Events); err != nil {
		c.state.Rooms = removeRoom(c.state.Rooms, roomState.ID)
		return nil, err
	}
	if response.HasMore {
		if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
			return nil, err
		}
	} else {
		c.anchorAtHeadLocked(roomState, response.Room)
	}
	roomState.ReadSeq = roomState.LastSeq
	if err := c.saveVaultLocked(ctx, client); err != nil {
		c.state.Account.VaultDirty = true
	}
	return c.viewLocked(roomState), nil
}

// RenameRoom changes the encrypted name and topic. Only the owner may.
func (c *Client) RenameRoom(ctx context.Context, roomID, name, topic string) (*RoomView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return nil, Errorf("room_not_found", "找不到这个房间。")
	}
	name = strings.TrimSpace(name)
	topic = strings.TrimSpace(topic)
	if name == "" || len([]rune(name)) > maxRoomNameRunes || len([]rune(topic)) > maxRoomTopicRunes {
		return nil, Errorf("invalid_name", "房间名称或说明格式不正确。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
		return nil, err
	}
	if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
		return nil, err
	}
	key := roomState.Keys[roomState.Epoch]
	if len(key) != 32 {
		return nil, Errorf("key_missing", "这台设备缺少房间密钥。")
	}
	actor := c.actorLocked()
	now := c.now().UTC().Truncate(time.Millisecond)
	event, err := room.BuildOfficialEvent(roomState.ID, roomState.LastSeq+1, anchorFromRoom(roomState),
		room.EventRoomSettingsChanged, actor, room.RoomSettingsChangedPayload{Name: name, Topic: topic},
		roomState.Epoch, key, now)
	if err != nil {
		return nil, Errorf("internal", "无法创建房间设置事件。")
	}
	response, err := client.PostRoomEvent(ctx, c.signerLocked(), event.ToWire())
	if err != nil {
		return nil, mapRoomError(err)
	}
	c.applyOpenedEventLocked(roomState, event, actor.DisplayName)
	c.advanceAnchorLocked(roomState, *event)
	roomState.Head = response.Head
	roomState.UpdatedAt = now
	if err := c.saveVaultLocked(ctx, client); err != nil {
		c.state.Account.VaultDirty = true
	}
	return c.viewLocked(roomState), nil
}

// LeaveRoom records a departure and removes the room from this browser.
func (c *Client) LeaveRoom(ctx context.Context, roomID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return Errorf("room_not_found", "找不到这个房间。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return err
	}
	if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
		return err
	}
	if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
		return err
	}
	key := roomState.Keys[roomState.Epoch]
	if len(key) != 32 {
		return Errorf("key_missing", "这台设备缺少房间密钥，无法离开。")
	}
	actor := c.actorLocked()
	now := c.now().UTC().Truncate(time.Millisecond)
	event, err := room.BuildOfficialEvent(roomState.ID, roomState.LastSeq+1, anchorFromRoom(roomState),
		room.EventMemberLeft, actor, room.MemberLeftPayload{AccountID: actor.AccountID, Reason: "left"},
		roomState.Epoch, key, now)
	if err != nil {
		return Errorf("internal", "无法创建离开事件。")
	}
	if _, err := client.LeaveRoom(ctx, c.signerLocked(), event.ToWire()); err != nil {
		if hosted.RoomErrorCode(err) != "forbidden" {
			return mapRoomError(err)
		}
	}
	c.state.Rooms = removeRoom(c.state.Rooms, roomID)
	if err := c.saveVaultLocked(ctx, client); err != nil {
		c.state.Account.VaultDirty = true
	}
	return nil
}

// MarkRead advances the local read cursor.
func (c *Client) MarkRead(ctx context.Context, roomID string, seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return Errorf("room_not_found", "找不到这个房间。")
	}
	if seq > roomState.LastSeq {
		seq = roomState.LastSeq
	}
	if seq <= roomState.ReadSeq {
		return nil
	}
	roomState.ReadSeq = seq
	roomState.UpdatedAt = c.now().UTC()
	client, err := c.hostedClient()
	if err != nil {
		return nil
	}
	if err := c.saveVaultLocked(ctx, client); err != nil {
		c.state.Account.VaultDirty = true
	}
	return nil
}

func (c *Client) attachExistingRoomLocked(ctx context.Context, client *hosted.Client, code *inviteCode) (*RoomView, error) {
	roomState := c.roomLocked(code.RoomID)
	if roomState == nil {
		roomState = &RoomState{
			ID: code.RoomID, Name: code.RoomName, Topic: code.RoomTopic,
			HomeURL: c.origin, HostID: c.state.Account.HostID,
			History: room.HistoryNone, Keys: map[int][]byte{}, JoinedAt: c.now().UTC(),
		}
		c.upsertRoomLocked(roomState)
	}
	if err := c.ensureRoomDeviceLocked(ctx, client, roomState); err != nil {
		c.state.Rooms = removeRoom(c.state.Rooms, roomState.ID)
		return nil, err
	}
	if _, err := c.syncRoomLocked(ctx, client, roomState); err != nil {
		c.state.Rooms = removeRoom(c.state.Rooms, roomState.ID)
		return nil, err
	}
	if len(roomState.Keys) == 0 {
		return c.viewLocked(roomState), nil
	}
	return c.viewLocked(roomState), nil
}

func (c *Client) ensureRoomDeviceLocked(ctx context.Context, client *hosted.Client, roomState *RoomState) error {
	if roomState.DeviceRegistered {
		return nil
	}
	response, err := client.RegisterRoomDevice(ctx, c.signerLocked(), hosted.RoomDeviceRegisterRequest{
		Protocol: hosted.Protocol, RoomID: roomState.ID, Device: c.deviceRefLocked(),
	})
	if err != nil {
		return mapRoomError(err)
	}
	roomState.DeviceRegistered = true
	if response.Room.Epoch > roomState.Epoch {
		roomState.Epoch = response.Room.Epoch
	}
	roomState.Head = response.Room.Head
	return nil
}

// syncRoomLocked advances one room until the host has no more visible events,
// heals keys, and rotates the epoch when the host requires it.
func (c *Client) syncRoomLocked(ctx context.Context, client *hosted.Client, roomState *RoomState) (bool, error) {
	if roomState.Keys == nil {
		roomState.Keys = map[int][]byte{}
	}
	vaultWanted := false
	requestsSent := false
	for attempt := 0; attempt < 30; attempt++ {
		needKeys := missingEpochs(roomState)
		response, err := client.SyncRoom(ctx, c.signerLocked(), hosted.RoomSyncRequest{
			Protocol: hosted.Protocol, RoomID: roomState.ID, After: roomState.LastSeq,
			Limit: hosted.MaxRoomEvents, NeedKeys: needKeys,
		})
		if err != nil {
			return vaultWanted, mapRoomError(err)
		}
		roomState.Members = membersFromHosted(response.Members)
		roomState.Devices = devicesFromHosted(response.Devices)
		roomState.Head = response.Room.Head
		roomState.RotationRequired = response.Room.RotationRequired || response.RotationRequired
		if response.Room.Epoch > roomState.Epoch {
			roomState.Epoch = response.Room.Epoch
		}
		for _, pkg := range response.Keys {
			if pkg.DeviceID != "" && pkg.DeviceID != c.state.Account.Device.ID {
				continue
			}
			key, err := room.UnwrapRoomKey(roomState.ID, pkg.Epoch, c.state.Account.Device.ID,
				c.state.Account.Device.EncryptionPrivateKey, pkg.Wrapped)
			if err != nil || len(key) != 32 {
				continue
			}
			if existing := roomState.Keys[pkg.Epoch]; len(existing) != 32 {
				vaultWanted = true
			}
			roomState.Keys[pkg.Epoch] = key
			if pkg.Epoch > roomState.Epoch {
				roomState.Epoch = pkg.Epoch
			}
		}
		changed, err := c.applyWireEventsLocked(roomState, response.Events)
		if err != nil {
			return vaultWanted, err
		}
		if changed {
			vaultWanted = true
		}
		requestsSent = c.uploadPeerKeysLocked(ctx, client, roomState, response.KeyRequests) || requestsSent
		if !requestsSent && len(roomState.Keys) > 0 && !responseHasKey(response.Keys, c.state.Account.Device.ID, roomState.Epoch) {
			// Our local vault has keys the host did not return; make them
			// available to this device on the next sync.
			if err := c.uploadSelfKeysLocked(ctx, client, roomState); err == nil {
				requestsSent = true
			}
		}
		if response.HasMore {
			if len(response.Events) == 0 {
				break
			}
			continue
		}
		if roomState.LastSeq < response.Room.Head {
			c.anchorAtHeadLocked(roomState, response.Room)
		}
		if roomState.RotationRequired {
			if _, err := c.rotateRoomLocked(ctx, client, roomState, "device_change"); err != nil {
				return vaultWanted, err
			}
			vaultWanted = true
			roomState.RotationRequired = false
			continue
		}
		break
	}
	roomState.UpdatedAt = c.now().UTC()
	if vaultWanted {
		if err := c.saveVaultLocked(ctx, client); err != nil {
			c.state.Account.VaultDirty = true
		}
	}
	return vaultWanted, nil
}

func (c *Client) rotateRoomLocked(ctx context.Context, client *hosted.Client, roomState *RoomState, reason string) (*room.Event, error) {
	if len(roomState.Keys[roomState.Epoch]) != 32 {
		return nil, Errorf("key_missing", "这台设备还没有当前房间密钥，需要原设备更新。")
	}
	epoch := roomState.Epoch + 1
	key, err := room.NewRoomKey()
	if err != nil {
		return nil, Errorf("internal", "无法生成新的房间密钥。")
	}
	actor := c.actorLocked()
	now := c.now().UTC().Truncate(time.Millisecond)
	event, err := room.BuildOfficialEvent(roomState.ID, roomState.LastSeq+1, anchorFromRoom(roomState),
		room.EventEpochRotated, actor, room.EpochRotatedPayload{Epoch: epoch, Reason: reason},
		epoch, key, now)
	if err != nil {
		return nil, Errorf("internal", "无法创建密钥更新事件。")
	}
	var packages []hosted.RoomKeyPackage
	active := activeMemberIDs(roomState)
	for _, device := range roomState.Devices {
		if device.RevokedAt != nil || len(device.EncryptionPublicKey) != 32 {
			continue
		}
		if !active[device.AccountID] {
			continue
		}
		wrapped, err := room.WrapRoomKey(roomState.ID, epoch, device.DeviceID, device.EncryptionPublicKey, key)
		if err != nil {
			continue
		}
		packages = append(packages, hosted.RoomKeyPackage{
			Epoch: epoch, DeviceID: device.DeviceID, Wrapped: wrapped, CreatedAt: now,
		})
	}
	selfFound := false
	for _, pkg := range packages {
		if pkg.DeviceID == c.state.Account.Device.ID {
			selfFound = true
			break
		}
	}
	if !selfFound {
		return nil, Errorf("device_revoked", "这台设备已不在房间中。")
	}
	response, err := client.RotateRoom(ctx, c.signerLocked(), hosted.RoomRotateRequest{
		Protocol: hosted.Protocol, Epoch: epoch, Event: event.ToWire(), Keys: packages,
	})
	if err != nil {
		return nil, mapRoomError(err)
	}
	c.applyOpenedEventLocked(roomState, event, actor.DisplayName)
	c.advanceAnchorLocked(roomState, *event)
	roomState.Epoch = epoch
	roomState.Keys[epoch] = key
	roomState.Head = response.Room.Head
	roomState.RotationRequired = false
	roomState.UpdatedAt = now
	if err := c.saveVaultLocked(ctx, client); err != nil {
		c.state.Account.VaultDirty = true
	}
	return event, nil
}

func (c *Client) uploadPeerKeysLocked(ctx context.Context, client *hosted.Client, roomState *RoomState, requests []hosted.RoomKeyRequest) bool {
	if len(requests) == 0 {
		return false
	}
	devices := make(map[string]DeviceInfo, len(roomState.Devices))
	for _, device := range roomState.Devices {
		devices[device.DeviceID] = device
	}
	var packages []hosted.RoomKeyPackage
	now := c.now().UTC().Truncate(time.Millisecond)
	for _, request := range requests {
		device, ok := devices[request.DeviceID]
		if !ok || device.RevokedAt != nil || len(device.EncryptionPublicKey) != 32 {
			continue
		}
		for _, epoch := range request.Epochs {
			key := roomState.Keys[epoch]
			if len(key) != 32 {
				continue
			}
			wrapped, err := room.WrapRoomKey(roomState.ID, epoch, device.DeviceID, device.EncryptionPublicKey, key)
			if err != nil {
				continue
			}
			packages = append(packages, hosted.RoomKeyPackage{
				Epoch: epoch, DeviceID: device.DeviceID, Wrapped: wrapped, CreatedAt: now,
			})
			if len(packages) >= hosted.MaxRoomPackages {
				break
			}
		}
		if len(packages) >= hosted.MaxRoomPackages {
			break
		}
	}
	if len(packages) == 0 {
		return false
	}
	_, err := client.UploadRoomKeys(ctx, c.signerLocked(), hosted.RoomKeyUploadRequest{
		Protocol: hosted.Protocol, RoomID: roomState.ID, Packages: packages,
	})
	return err == nil
}

func (c *Client) uploadSelfKeysLocked(ctx context.Context, client *hosted.Client, roomState *RoomState) error {
	epochs := sortedEpochs(roomState.Keys)
	if len(epochs) == 0 {
		return nil
	}
	now := c.now().UTC().Truncate(time.Millisecond)
	packages := make([]hosted.RoomKeyPackage, 0, len(epochs))
	for _, epoch := range epochs {
		wrapped, err := room.WrapRoomKey(roomState.ID, epoch, c.state.Account.Device.ID,
			c.state.Account.Device.EncryptionPublicKey, roomState.Keys[epoch])
		if err != nil {
			continue
		}
		packages = append(packages, hosted.RoomKeyPackage{
			Epoch: epoch, DeviceID: c.state.Account.Device.ID, Wrapped: wrapped, CreatedAt: now,
		})
	}
	if len(packages) == 0 {
		return nil
	}
	_, err := client.UploadRoomKeys(ctx, c.signerLocked(), hosted.RoomKeyUploadRequest{
		Protocol: hosted.Protocol, RoomID: roomState.ID, Packages: packages,
	})
	return err
}

// applyWireEventsLocked verifies and projects events. It returns whether room
// metadata or keys changed in a way worth persisting.
func (c *Client) applyWireEventsLocked(roomState *RoomState, events []room.WireEvent) (bool, error) {
	changed := false
	devices := make(map[string]DeviceInfo, len(roomState.Devices))
	for _, device := range roomState.Devices {
		devices[device.DeviceID] = device
	}
	for i := range events {
		event := events[i].ToEvent()
		if event.RoomID != roomState.ID {
			return changed, Errorf("corrupt_room", "房间事件不属于这个房间。")
		}
		if roomState.LastSeq > 0 {
			if event.Seq != roomState.LastSeq+1 || event.PrevID != roomState.LastID ||
				event.PrevHash != roomState.LastHash || event.EnvelopePrevHash != roomState.LastEnvelopeHash {
				return changed, Errorf("room_conflict", "房间事件链不连续，请重新同步。")
			}
		} else if event.Seq == 1 {
			if event.PrevID != "" || event.PrevHash != "" || event.EnvelopePrevHash != "" {
				return changed, Errorf("corrupt_room", "房间的第一个事件无效。")
			}
		}
		device, ok := devices[event.AuthorDevice]
		if !ok {
			return changed, Errorf("corrupt_room", "房间事件来自未知设备。")
		}
		if device.AccountID != event.AuthorID {
			return changed, Errorf("corrupt_room", "房间事件的作者与设备不匹配。")
		}
		publicKey := device.SigningPublicKey
		if err := room.VerifyEnvelope(&event, publicKey); err != nil {
			return changed, Errorf("corrupt_room", "房间事件签名校验失败。")
		}
		key := roomState.Keys[event.Epoch]
		if len(key) == 32 {
			opened, err := room.OpenOfficialEvent(&event, key)
			if err != nil {
				return changed, Errorf("corrupt_room", "房间事件无法解密。")
			}
			if err := room.VerifyEvent(opened, publicKey); err != nil {
				return changed, Errorf("corrupt_room", "房间事件内容校验失败。")
			}
			if c.projectEventLocked(roomState, opened, device) {
				changed = true
			}
		} else if event.Type == room.EventMessagePosted {
			c.appendMessageLocked(roomState, MessageState{
				ID: event.ID, Seq: event.Seq, EventType: event.Type,
				AuthorID: event.AuthorID, AuthorDevice: event.AuthorDevice,
				AuthorName: memberName(roomState, event.AuthorID),
				Text:       "此消息需要房间密钥", Kind: "locked", At: event.CreatedAt, Epoch: event.Epoch,
			})
		}
		c.advanceAnchorLocked(roomState, event)
	}
	return changed, nil
}

func (c *Client) projectEventLocked(roomState *RoomState, event *room.Event, device DeviceInfo) bool {
	author := displayNameFor(roomState, event.AuthorID, device.DeviceID)
	at := event.CreatedAt
	switch event.Type {
	case room.EventRoomCreated:
		var payload room.RoomCreatedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return false
		}
		roomState.Name = payload.Name
		roomState.Topic = payload.Topic
		roomState.UpdatedAt = at
		c.appendSystemLocked(roomState, event, author, "创建了房间")
		return true
	case room.EventRoomSettingsChanged:
		var payload room.RoomSettingsChangedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return false
		}
		roomState.Name = payload.Name
		roomState.Topic = payload.Topic
		roomState.UpdatedAt = at
		c.appendSystemLocked(roomState, event, author, "更新了房间信息")
		return true
	case room.EventMessagePosted:
		var payload room.MessagePostedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return false
		}
		text := strings.TrimSpace(payload.Text)
		if text == "" {
			return false
		}
		c.appendMessageLocked(roomState, MessageState{
			ID: event.ID, Seq: event.Seq, EventType: event.Type,
			AuthorID: event.AuthorID, AuthorDevice: event.AuthorDevice,
			AuthorName: author, Text: text, Kind: "text", At: at, Epoch: event.Epoch,
		})
		return true
	case room.EventMessageEdited:
		var payload room.MessageEditedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return false
		}
		for i := len(roomState.Messages) - 1; i >= 0; i-- {
			message := &roomState.Messages[i]
			if message.ID == payload.TargetID && message.AuthorID == event.AuthorID {
				if text := strings.TrimSpace(payload.Text); text != "" {
					message.Text = text
					return true
				}
			}
		}
		return false
	case room.EventMemberJoined:
		var payload room.MemberJoinedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		name := strings.TrimSpace(payload.DisplayName)
		if name == "" {
			name = author
		}
		c.appendSystemLocked(roomState, event, name, "加入了房间")
		return true
	case room.EventMemberLeft:
		c.appendSystemLocked(roomState, event, author, "离开了房间")
		return true
	case room.EventMemberInvited:
		// Invite creation is administrative; the timeline starts for the
		// invitee at their join, not at an invite they never saw.
		return false
	case room.EventEpochRotated:
		c.appendSystemLocked(roomState, event, author, "更新了房间密钥")
		return false
	case room.EventApprovalRequested:
		var payload room.ApprovalRequestedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		text := "请求审批"
		if strings.TrimSpace(payload.Description) != "" {
			text = "请求审批：" + strings.TrimSpace(payload.Description)
		}
		c.appendSystemLocked(roomState, event, author, text)
		return false
	case room.EventApprovalGranted, room.EventApprovalRejected, room.EventApprovalExpired, room.EventApprovalConsumed:
		c.appendSystemLocked(roomState, event, author, approvalText(event.Type))
		return false
	case room.EventToolRequested, room.EventToolResult, room.EventToolFailed:
		var payload room.ToolEventPayload
		_ = json.Unmarshal(event.Payload, &payload)
		tool := strings.TrimSpace(payload.Tool)
		if tool == "" {
			tool = "工具"
		}
		c.appendSystemLocked(roomState, event, author, "Agent 使用了 "+tool)
		return false
	case room.EventAgentJoined:
		var payload room.AgentJoinedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		name := strings.TrimSpace(payload.DisplayName)
		if name == "" {
			name = strings.TrimSpace(payload.Name)
		}
		if name == "" {
			name = "Agent"
		}
		c.appendSystemLocked(roomState, event, name, "加入了房间")
		return false
	case room.EventAgentLeft:
		c.appendSystemLocked(roomState, event, author, "Agent 离开了房间")
		return false
	}
	return false
}

func approvalText(eventType string) string {
	switch eventType {
	case room.EventApprovalGranted:
		return "审批已通过"
	case room.EventApprovalRejected:
		return "审批已拒绝"
	case room.EventApprovalExpired:
		return "审批已过期"
	default:
		return "审批已完成"
	}
}

func (c *Client) applyOpenedEventLocked(roomState *RoomState, event *room.Event, fallbackAuthor string) {
	if event == nil || c.state.Account == nil {
		return
	}
	opened := event
	if key := roomState.Keys[event.Epoch]; len(key) == 32 {
		if plain, err := room.OpenOfficialEvent(event, key); err == nil {
			opened = plain
		}
	}
	device := DeviceInfo{
		DeviceID: event.AuthorDevice, AccountID: event.AuthorID,
		SigningPublicKey:    append([]byte(nil), c.state.Account.Device.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), c.state.Account.Device.EncryptionPublicKey...),
	}
	_ = fallbackAuthor
	c.projectEventLocked(roomState, opened, device)
}

func (c *Client) appendMessageLocked(roomState *RoomState, message MessageState) {
	if message.ID != "" {
		for i := len(roomState.Messages) - 1; i >= 0; i-- {
			if roomState.Messages[i].ID == message.ID {
				return
			}
		}
	}
	roomState.Messages = append(roomState.Messages, message)
	sortRoomMessages(roomState)
}

func (c *Client) appendSystemLocked(roomState *RoomState, event *room.Event, author, text string) {
	c.appendMessageLocked(roomState, MessageState{
		ID: event.ID, Seq: event.Seq, EventType: event.Type,
		AuthorID: event.AuthorID, AuthorDevice: event.AuthorDevice, AuthorName: author,
		Text: text, Kind: "system", At: event.CreatedAt, Epoch: event.Epoch,
	})
}

func (c *Client) advanceAnchorLocked(roomState *RoomState, event room.Event) {
	roomState.LastSeq = event.Seq
	roomState.LastID = event.ID
	roomState.LastHash = event.Hash
	roomState.LastEnvelopeHash = event.EnvelopeHash
	if roomState.Head < event.Seq {
		roomState.Head = event.Seq
	}
}

func anchorFromRoom(roomState *RoomState) *room.Event {
	if roomState.LastSeq < 1 {
		return nil
	}
	return &room.Event{
		ID: roomState.LastID, RoomID: roomState.ID, Seq: roomState.LastSeq,
		Hash: roomState.LastHash, EnvelopeHash: roomState.LastEnvelopeHash,
	}
}

func (c *Client) anchorAtHeadLocked(roomState *RoomState, info hosted.RoomInfo) {
	if info.Head < 1 || info.HeadEventID == "" || info.HeadHash == "" {
		return
	}
	if roomState.LastSeq >= info.Head {
		return
	}
	roomState.LastSeq = info.Head
	roomState.LastID = info.HeadEventID
	roomState.LastHash = info.HeadHash
	roomState.LastEnvelopeHash = info.HeadEnvelopeHash
}

func (c *Client) viewLocked(roomState *RoomState) *RoomView {
	if roomState.Keys == nil {
		roomState.Keys = map[int][]byte{}
	}
	accountID := ""
	deviceID := ""
	role := ""
	if c.state.Account != nil {
		accountID = c.state.Account.ID
		deviceID = c.state.Account.Device.ID
		for _, member := range roomState.Members {
			if member.AccountID == accountID && member.Status == "active" {
				role = member.Role
				break
			}
		}
	}
	locked := 0
	for _, message := range roomState.Messages {
		if message.Kind == "locked" {
			locked++
		}
	}
	// Keep every collection non-nil. A freshly restored vault can briefly
	// have no messages or members, and JSON null would crash the first render
	// of the room before the first sync completes.
	messages := make([]MessageState, len(roomState.Messages))
	copy(messages, roomState.Messages)
	members := activeMembers(roomState)
	if members == nil {
		members = []MemberState{}
	}
	devices := make([]DeviceInfo, len(roomState.Devices))
	copy(devices, roomState.Devices)
	return &RoomView{
		Summary: roomSummary(roomState, deviceID), Messages: messages,
		Members: members, Devices: devices,
		Epoch: roomState.Epoch, MyRole: role, CanRename: role == "owner",
		CanInvite:        len(roomState.Keys[roomState.Epoch]) == 32,
		RotationRequired: roomState.RotationRequired, LockedCount: locked,
		VaultPending: c.state.Account != nil && c.state.Account.VaultDirty,
	}
}

func (c *Client) summariesLocked() []RoomSummary {
	deviceID := ""
	if c.state.Account != nil {
		deviceID = c.state.Account.Device.ID
	}
	rooms := c.activeRoomsLocked()
	summaries := make([]RoomSummary, 0, len(rooms))
	for _, roomState := range rooms {
		summaries = append(summaries, roomSummary(roomState, deviceID))
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].LastAt != nil && summaries[j].LastAt != nil && !summaries[i].LastAt.Equal(*summaries[j].LastAt) {
			return summaries[i].LastAt.After(*summaries[j].LastAt)
		}
		if summaries[i].LastAt != nil && summaries[j].LastAt == nil {
			return true
		}
		if summaries[i].LastAt == nil && summaries[j].LastAt != nil {
			return false
		}
		return summaries[i].JoinedAt.After(summaries[j].JoinedAt)
	})
	return summaries
}

func (c *Client) signerLocked() hosted.RoomSigner {
	return hosted.RoomSigner{
		AccountID: c.state.Account.ID, DeviceID: c.state.Account.Device.ID,
		PrivateKey: c.state.Account.Device.PrivateKey,
	}
}

func (c *Client) actorLocked() room.Actor {
	return room.Actor{
		AccountID: c.state.Account.ID, DeviceID: c.state.Account.Device.ID,
		DisplayName:         c.state.Account.Name,
		SigningKey:          c.state.Account.Device.PrivateKey,
		AccountPublicKey:    c.state.Account.PublicKey,
		EncryptionPublicKey: c.state.Account.Device.EncryptionPublicKey,
	}
}

func (c *Client) deviceRefLocked() hosted.RoomDeviceRef {
	return hosted.RoomDeviceRef{
		AccountID: c.state.Account.ID, DeviceID: c.state.Account.Device.ID,
		DisplayName:         c.state.Account.Name,
		SigningPublicKey:    c.state.Account.Device.PublicKey,
		EncryptionPublicKey: c.state.Account.Device.EncryptionPublicKey,
		Certificate:         []byte(c.state.Account.Device.Certificate),
	}
}

func (c *Client) hostURLWithIDLocked() string {
	return c.origin + "#id=" + c.state.Account.HostID
}

func (c *Client) roomWarningLocked(roomState *RoomState, err error) string {
	name := strings.TrimSpace(roomState.Name)
	if name == "" {
		name = "未命名房间"
	}
	return name + "：" + AsError(err).Message
}

func (c *Client) findMessageLocked(roomState *RoomState, eventID string) *MessageState {
	for i := len(roomState.Messages) - 1; i >= 0; i-- {
		if roomState.Messages[i].ID == eventID {
			message := roomState.Messages[i]
			return &message
		}
	}
	return nil
}

func membersFromHosted(entries []hosted.RoomMemberInfo) []MemberState {
	out := make([]MemberState, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.DisplayName)
		if name == "" {
			name = "成员"
		}
		out = append(out, MemberState{
			AccountID: entry.AccountID, DisplayName: name, Role: entry.Role,
			Status: entry.Status, EncryptionPublicKey: append([]byte(nil), entry.EncryptionPublicKey...),
			JoinedAt: entry.JoinedAt, LeftAt: entry.LeftAt,
		})
	}
	return out
}

func devicesFromHosted(entries []hosted.RoomDeviceInfo) []DeviceInfo {
	out := make([]DeviceInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, DeviceInfo{
			DeviceID: entry.DeviceID, AccountID: entry.AccountID,
			SigningPublicKey:    append([]byte(nil), entry.SigningPublicKey...),
			EncryptionPublicKey: append([]byte(nil), entry.EncryptionPublicKey...),
			RevokedAt:           entry.RevokedAt,
		})
	}
	return out
}

func activeMemberIDs(roomState *RoomState) map[string]bool {
	ids := make(map[string]bool, len(roomState.Members))
	for _, member := range roomState.Members {
		if member.Status == "active" {
			ids[member.AccountID] = true
		}
	}
	return ids
}

func memberName(roomState *RoomState, accountID string) string {
	for _, member := range roomState.Members {
		if member.AccountID == accountID && strings.TrimSpace(member.DisplayName) != "" {
			return member.DisplayName
		}
	}
	if len(accountID) > 6 {
		return accountID[len(accountID)-6:]
	}
	return "成员"
}

func displayNameFor(roomState *RoomState, accountID, deviceID string) string {
	for _, device := range roomState.Devices {
		if device.DeviceID == deviceID {
			for _, member := range roomState.Members {
				if member.AccountID == accountID && strings.TrimSpace(member.DisplayName) != "" {
					return member.DisplayName
				}
			}
		}
	}
	return memberName(roomState, accountID)
}

func missingEpochs(roomState *RoomState) []int {
	if roomState.Epoch < 1 {
		return nil
	}
	var missing []int
	for epoch := 1; epoch <= roomState.Epoch; epoch++ {
		if len(roomState.Keys[epoch]) != 32 {
			missing = append(missing, epoch)
		}
	}
	if len(missing) > hosted.MaxRoomEvents {
		missing = missing[len(missing)-hosted.MaxRoomEvents:]
	}
	return missing
}

func sortedEpochs(keys map[int][]byte) []int {
	epochs := make([]int, 0, len(keys))
	for epoch, key := range keys {
		if epoch >= 1 && len(key) == 32 {
			epochs = append(epochs, epoch)
		}
	}
	sort.Ints(epochs)
	return epochs
}

func responseHasKey(packages []hosted.RoomKeyPackage, deviceID string, epoch int) bool {
	for _, pkg := range packages {
		if pkg.Epoch == epoch && (pkg.DeviceID == "" || pkg.DeviceID == deviceID) {
			return true
		}
	}
	return false
}

func removeRoom(rooms []*RoomState, roomID string) []*RoomState {
	out := rooms[:0]
	for _, roomState := range rooms {
		if roomState != nil && roomState.ID != roomID {
			out = append(out, roomState)
		}
	}
	return out
}

func retryableHostError(err error) bool {
	var apiError *hosted.APIError
	if errors.As(err, &apiError) {
		return apiError.Status == 408 || apiError.Status == 429 || apiError.Status >= 500
	}
	return true
}

func mapRoomError(err error) error {
	if err == nil {
		return nil
	}
	var apiError *hosted.APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "already_member":
			return Errorf("already_member", "这个账号已经加入了房间。")
		case "invite_invalid":
			return Errorf("invite_invalid", "邀请码无效。")
		case "invite_used":
			return Errorf("invite_used", "这个邀请已经被使用过了。")
		case "invite_expired":
			return Errorf("invite_expired", "这个邀请已经过期了。")
		case "forbidden":
			return Errorf("forbidden", "你没有权限进行这个操作。")
		case "device_revoked":
			return Errorf("device_revoked", "这台设备已被移出房间。")
		case "rotation_required":
			return Errorf("rotation_required", "房间密钥需要更新，请稍后重试。")
		case "key_coverage":
			return Errorf("key_coverage", "房间密钥没有覆盖所有设备，请稍后重试。")
		case "room_full":
			return Errorf("room_full", "房间人数已满。")
		case "device_limit":
			return Errorf("device_limit", "房间设备数量已满。")
		case "conflict", "epoch_conflict":
			return Errorf("room_conflict", "房间有新消息，请重试。")
		}
	}
	return mapHostError(err)
}

// inviteCode mirrors the native portable invite format so nodes and the web
// app can exchange the same codes.
type inviteCode struct {
	Protocol  int    `json:"protocol"`
	Secret    string `json:"secret"`
	URL       string `json:"url"`
	ServerID  string `json:"server_id"`
	RoomID    string `json:"room_id"`
	RoomName  string `json:"room_name,omitempty"`
	HomeKind  string `json:"home_kind,omitempty"`
	RoomTopic string `json:"room_topic,omitempty"`
}

func encodeOfficialInvite(code inviteCode) (string, error) {
	code.Protocol = inviteProtocol
	code.HomeKind = inviteKindOfficial
	if len(code.Secret) < 16 || code.RoomID == "" || code.ServerID == "" {
		return "", Errorf("invalid_invite", "邀请信息不完整。")
	}
	parsed, err := account.ParseHostURL(code.URL)
	if err != nil || parsed.HostID != code.ServerID {
		return "", Errorf("invalid_invite", "邀请地址无效。")
	}
	data, err := json.Marshal(code)
	if err != nil {
		return "", Errorf("internal", "无法生成邀请码。")
	}
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeOfficialInvite(raw, origin, hostID string) (*inviteCode, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, invitePrefix) {
		return nil, Errorf("invalid_invite", "这不是有效的 Toget 邀请码。")
	}
	if len(raw) > 64*1024 {
		return nil, Errorf("invalid_invite", "邀请码过大。")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw[len(invitePrefix):])
	if err != nil {
		return nil, Errorf("invalid_invite", "邀请码格式不正确。")
	}
	var code inviteCode
	if err := json.Unmarshal(data, &code); err != nil {
		return nil, Errorf("invalid_invite", "邀请码内容损坏。")
	}
	if code.Protocol != inviteProtocol || code.HomeKind != inviteKindOfficial ||
		len(code.Secret) < 16 || code.RoomID == "" || code.ServerID == "" {
		return nil, Errorf("invalid_invite", "邀请码版本不受支持。")
	}
	if code.ServerID != hostID {
		return nil, Errorf("wrong_host", "这个邀请来自另一个服务器。")
	}
	parsed, err := account.ParseHostURL(code.URL)
	if err != nil || parsed.HostID != code.ServerID {
		return nil, Errorf("invalid_invite", "邀请地址无效。")
	}
	if parsed.Origin != origin {
		return nil, Errorf("wrong_host", "这个邀请来自另一个服务器。")
	}
	if len([]rune(code.RoomName)) > maxRoomNameRunes || len([]rune(code.RoomTopic)) > maxRoomTopicRunes {
		return nil, Errorf("invalid_invite", "邀请中的房间信息无效。")
	}
	return &code, nil
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func maxInt(value, min int) int {
	if value < min {
		return min
	}
	return value
}
