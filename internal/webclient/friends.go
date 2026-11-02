package webclient

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/sealbox"
)

// FriendRequestDevice is one target device's public encryption key.
type FriendRequestDevice struct {
	ID                  string `json:"id"`
	EncryptionPublicKey string `json:"encryption_public_key"`
}

// FriendRequestSent confirms that a sealed invite reached the host inbox.
type FriendRequestSent struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// FriendRequestView is one incoming friend request after local decryption.
type FriendRequestView struct {
	ID            string    `json:"id"`
	FromAccountID string    `json:"from_account_id"`
	FromName      string    `json:"from_name,omitempty"`
	RoomID        string    `json:"room_id"`
	RoomName      string    `json:"room_name,omitempty"`
	RoomTopic     string    `json:"room_topic,omitempty"`
	Code          string    `json:"code"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// AddFriend creates a direct room, invites the target, and delivers the
// invite as a sealed friend request. The target must be on this host.
func (c *Client) AddFriend(ctx context.Context, targetAccountID, name string, devices []FriendRequestDevice) (*FriendRequestSent, error) {
	c.mu.Lock()
	selfID := ""
	if c.state.Account != nil {
		selfID = c.state.Account.ID
	}
	c.mu.Unlock()
	if selfID == "" {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	targetAccountID = strings.TrimSpace(targetAccountID)
	if !validAccountID(targetAccountID) {
		return nil, Errorf("invalid_account", "账号 ID 应该是 14 位数字。")
	}
	if targetAccountID == selfID {
		return nil, Errorf("friend_request_self", "不能添加自己为好友。")
	}
	if len(devices) == 0 {
		return nil, Errorf("target_unavailable", "对方暂时没有可用的设备。")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "新的好友"
	}
	view, err := c.CreateRoom(ctx, CreateRoomInput{
		Name: name, Topic: "", Kind: room.RoomKindDirect,
	})
	if err != nil {
		return nil, err
	}
	invite, err := c.CreateInvite(ctx, view.Summary.ID, InviteInput{
		History: room.HistoryAll, TTLHours: 168,
	})
	if err != nil {
		return nil, err
	}
	return c.SendFriendRequest(ctx, targetAccountID, view.Summary.ID, invite.Code, devices)
}

// SendFriendRequest seals an existing direct-room invite once per target
// device. The host stores the ciphertext but can never read the invite.
func (c *Client) SendFriendRequest(ctx context.Context, targetAccountID, roomID, inviteCode string, devices []FriendRequestDevice) (*FriendRequestSent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	targetAccountID = strings.TrimSpace(targetAccountID)
	if !validAccountID(targetAccountID) {
		return nil, Errorf("invalid_account", "账号 ID 应该是 14 位数字。")
	}
	if targetAccountID == c.state.Account.ID {
		return nil, Errorf("friend_request_self", "不能添加自己为好友。")
	}
	roomState := c.roomLocked(roomID)
	if roomState == nil {
		return nil, Errorf("room_not_found", "找不到这个房间。")
	}
	if roomState.Kind != room.RoomKindDirect || roomState.OwnerAccountID != c.state.Account.ID {
		return nil, Errorf("invalid_room", "好友申请只能使用自己创建的 1:1 房间。")
	}
	code, err := decodeOfficialInvite(inviteCode, c.origin, c.state.Account.HostID)
	if err != nil {
		return nil, err
	}
	if code.RoomID != roomID {
		return nil, Errorf("invalid_invite", "邀请码和房间不一致。")
	}
	if len(devices) == 0 || len(devices) > hosted.MaxFriendRequestSeals {
		return nil, Errorf("target_unavailable", "对方暂时没有可用的设备。")
	}
	requestID := identity.NewID()
	context := hosted.FriendRequestSealContext(
		requestID, c.state.Account.ID, targetAccountID, roomID)
	seals := make([]hosted.FriendRequestSeal, 0, len(devices))
	seen := make(map[string]bool, len(devices))
	for _, device := range devices {
		if device.ID == "" || seen[device.ID] {
			return nil, Errorf("invalid_device", "对方的设备信息不完整。")
		}
		key, err := identity.DecodeEncryptionPublicKey(device.EncryptionPublicKey)
		if err != nil || len(key) != 32 {
			return nil, Errorf("invalid_device", "对方的设备密钥无效。")
		}
		sealed, err := sealbox.Seal(key, []byte(inviteCode), context)
		if err != nil {
			return nil, Errorf("internal", "无法封装好友申请。")
		}
		seen[device.ID] = true
		seals = append(seals, hosted.FriendRequestSeal{
			DeviceID: device.ID, Sealed: string(sealed),
		})
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	created, err := client.CreateFriendRequest(ctx, c.signerLocked(), hosted.FriendRequestCreate{
		Protocol: hosted.Protocol, ID: requestID, ToAccountID: targetAccountID,
		RoomID: roomID, Seals: seals,
	})
	if err != nil {
		return nil, mapFriendError(err)
	}
	return &FriendRequestSent{ID: created.ID, ExpiresAt: created.ExpiresAt}, nil
}

// ListFriendRequests returns incoming requests this device can open.
func (c *Client) ListFriendRequests(ctx context.Context) ([]FriendRequestView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	return c.listFriendRequestsLocked(ctx, client)
}

func (c *Client) listFriendRequestsLocked(ctx context.Context, client *hosted.Client) ([]FriendRequestView, error) {
	rows, err := client.ListFriendRequests(ctx, c.signerLocked())
	if err != nil {
		return nil, mapHostError(err)
	}
	requests := make([]FriendRequestView, 0, len(rows))
	for _, row := range rows {
		if row.FromAccountID == c.state.Account.ID || row.Sealed == "" {
			continue
		}
		context := hosted.FriendRequestSealContext(
			row.ID, row.FromAccountID, c.state.Account.ID, row.RoomID)
		plaintext, err := sealbox.Open(
			c.state.Account.Device.EncryptionPrivateKey, []byte(row.Sealed), context)
		if err != nil {
			continue
		}
		code, err := decodeOfficialInvite(string(plaintext), c.origin, c.state.Account.HostID)
		if err != nil || code.RoomID != row.RoomID {
			continue
		}
		requests = append(requests, FriendRequestView{
			ID: row.ID, FromAccountID: row.FromAccountID, FromName: row.FromName,
			RoomID: row.RoomID, RoomName: code.RoomName, RoomTopic: code.RoomTopic,
			Code: string(plaintext), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		})
	}
	return requests, nil
}

// AcceptFriendRequest joins the direct room carried by a sealed request and
// then removes the request from the inbox.
func (c *Client) AcceptFriendRequest(ctx context.Context, requestID string) (*RoomView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	requests, err := c.listFriendRequestsLocked(ctx, client)
	if err != nil {
		return nil, err
	}
	var selected *FriendRequestView
	for i := range requests {
		if requests[i].ID == requestID {
			selected = &requests[i]
			break
		}
	}
	if selected == nil {
		return nil, Errorf("friend_request_not_found", "这个好友申请已经失效了。")
	}
	view, err := c.joinInviteLocked(ctx, selected.Code)
	if err != nil {
		return nil, err
	}
	_, _ = client.DeleteFriendRequest(ctx, c.signerLocked(), requestID)
	return view, nil
}

// DeclineFriendRequest removes an incoming request without joining.
func (c *Client) DeclineFriendRequest(ctx context.Context, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return err
	}
	deleted, err := client.DeleteFriendRequest(ctx, c.signerLocked(), requestID)
	if err != nil {
		return mapHostError(err)
	}
	if !deleted {
		return Errorf("friend_request_not_found", "这个好友申请已经失效了。")
	}
	return nil
}

func mapFriendError(err error) error {
	var apiError *hosted.APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "account_not_found":
			return Errorf("account_not_found", "没有找到这个账号。")
		case "friend_request_self":
			return Errorf("friend_request_self", "不能添加自己为好友。")
		case "target_unavailable":
			return Errorf("target_unavailable", "对方暂时没有可用的设备，暂时不能发送好友申请。")
		case "too_many_requests":
			return Errorf("too_many_requests", "待处理的好友申请太多了，请稍后再试。")
		case "already_member":
			return Errorf("already_member", "你们已经在同一个房间里了。")
		case "invalid_seal", "invalid_request":
			return Errorf("invalid_request", "好友申请格式不正确，请重试。")
		case "forbidden":
			return Errorf("forbidden", "这个房间不能发送好友申请。")
		}
	}
	return mapHostError(err)
}
