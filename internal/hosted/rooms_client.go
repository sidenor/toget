package hosted

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/sidenor/toget/internal/room"
)

// RoomSigner identifies the local account device that signs room requests.
type RoomSigner struct {
	AccountID  string
	DeviceID   string
	PrivateKey ed25519.PrivateKey
	// Certificate is the account-signed device certificate. It is sent when
	// the room host is not the device's account host, so that host can bind
	// the remote device to its account.
	Certificate string
}

func (s RoomSigner) valid() bool {
	return s.AccountID != "" && s.DeviceID != "" && len(s.PrivateKey) == ed25519.PrivateKeySize
}

func (c *Client) doRoom(ctx context.Context, path string, signer RoomSigner, request, response any) error {
	if !signer.valid() {
		return errors.New("room signer is incomplete")
	}
	return c.doDeviceSigned(ctx, path, signer.AccountID, signer.DeviceID,
		signer.PrivateKey, signer.Certificate, request, response)
}

// CreateRoom creates a blind hosted room.
func (c *Client) CreateRoom(ctx context.Context, signer RoomSigner, request RoomCreateRequest) (*RoomCreateResponse, error) {
	var response RoomCreateResponse
	if err := c.doRoom(ctx, RoomCreatePath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.Room.ID != request.RoomID {
		return nil, fmt.Errorf("%w: malformed room creation response", ErrInvalidHost)
	}
	return &response, nil
}

// SyncRoom fetches room state and events after a cursor.
func (c *Client) SyncRoom(ctx context.Context, signer RoomSigner, request RoomSyncRequest) (*RoomSyncResponse, error) {
	var response RoomSyncResponse
	if err := c.doRoom(ctx, RoomSyncPath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.Room.ID != request.RoomID {
		return nil, fmt.Errorf("%w: malformed room sync response", ErrInvalidHost)
	}
	return &response, nil
}

// PostRoomEvent appends one signed, sealed event.
func (c *Client) PostRoomEvent(ctx context.Context, signer RoomSigner, event room.WireEvent) (*RoomEventResponse, error) {
	var response RoomEventResponse
	if err := c.doRoom(ctx, RoomEventPath, signer, RoomEventRequest{Protocol: Protocol, Event: event}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed room event response", ErrInvalidHost)
	}
	return &response, nil
}

// CreateRoomInvite creates a single-use invitation and its event.
func (c *Client) CreateRoomInvite(ctx context.Context, signer RoomSigner, request RoomInviteRequest) (*RoomInviteResponse, error) {
	var response RoomInviteResponse
	if err := c.doRoom(ctx, RoomInvitePath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.InviteID != request.InviteID {
		return nil, fmt.Errorf("%w: malformed invite response", ErrInvalidHost)
	}
	return &response, nil
}

// JoinRoom redeems an invitation.
func (c *Client) JoinRoom(ctx context.Context, signer RoomSigner, request RoomJoinRequest) (*RoomJoinResponse, error) {
	var response RoomJoinResponse
	if err := c.doRoom(ctx, RoomJoinPath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.Room.ID != request.RoomID {
		return nil, fmt.Errorf("%w: malformed join response", ErrInvalidHost)
	}
	return &response, nil
}

// UploadRoomKeys deposits wrapped epoch keys for peers.
func (c *Client) UploadRoomKeys(ctx context.Context, signer RoomSigner, request RoomKeyUploadRequest) (*RoomKeyUploadResponse, error) {
	var response RoomKeyUploadResponse
	if err := c.doRoom(ctx, RoomKeysPath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed key response", ErrInvalidHost)
	}
	return &response, nil
}

// LeaveRoom records this account's departure.
func (c *Client) LeaveRoom(ctx context.Context, signer RoomSigner, event room.WireEvent) (*RoomEventResponse, error) {
	var response RoomEventResponse
	if err := c.doRoom(ctx, RoomLeavePath, signer, RoomLeaveRequest{Protocol: Protocol, Event: event}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed leave response", ErrInvalidHost)
	}
	return &response, nil
}

// RotateRoom installs a new epoch with keys for every active device.
func (c *Client) RotateRoom(ctx context.Context, signer RoomSigner, request RoomRotateRequest) (*RoomCreateResponse, error) {
	var response RoomCreateResponse
	if err := c.doRoom(ctx, RoomRotatePath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed rotation response", ErrInvalidHost)
	}
	return &response, nil
}

// RegisterRoomDevice adds another device of an existing member.
func (c *Client) RegisterRoomDevice(ctx context.Context, signer RoomSigner, request RoomDeviceRegisterRequest) (*RoomDeviceRegisterResponse, error) {
	var response RoomDeviceRegisterResponse
	if err := c.doRoom(ctx, RoomDevicesPath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed device response", ErrInvalidHost)
	}
	return &response, nil
}

// RoomErrorCode returns the host's structured code for an error.
func RoomErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// IsRoomConflict reports a stale room head or epoch that should be retried
// after a sync.
func IsRoomConflict(err error) bool {
	code := RoomErrorCode(err)
	return code == "conflict" || code == "epoch_conflict"
}

// IsRoomRotationRequired reports that the caller must rotate the epoch before
// posting again.
func IsRoomRotationRequired(err error) bool {
	return RoomErrorCode(err) == "rotation_required"
}

// ListRooms returns the account's active room memberships.
func (c *Client) ListRooms(ctx context.Context, signer RoomSigner) (*RoomListResponse, error) {
	var response RoomListResponse
	if err := c.doRoom(ctx, RoomListPath, signer, RoomListRequest{Protocol: Protocol}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed room list response", ErrInvalidHost)
	}
	return &response, nil
}
