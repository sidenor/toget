package hosted

import (
	"time"

	"github.com/sidenor/toget/internal/room"
)

// Official room-home paths. Like the account API, every room request is
// device-signed and carries the account and device identity headers. Room
// payloads are opaque to the host.
const (
	// RoomCreatePath creates a hosted room and its first event.
	RoomCreatePath = "/host/v1/room/create"
	// RoomSyncPath returns room state and events after a cursor.
	RoomSyncPath = "/host/v1/room/sync"
	// RoomEventPath appends a normal room event.
	RoomEventPath = "/host/v1/room/event"
	// RoomInvitePath creates a single-use invitation.
	RoomInvitePath = "/host/v1/room/invite"
	// RoomJoinPath redeems an invitation.
	RoomJoinPath = "/host/v1/room/join"
	// RoomKeysPath uploads wrapped epoch keys for other devices.
	RoomKeysPath = "/host/v1/room/keys"
	// RoomLeavePath records a member leaving and revokes their devices.
	RoomLeavePath = "/host/v1/room/leave"
	// RoomRotatePath installs a new epoch and its wrapped keys.
	RoomRotatePath = "/host/v1/room/rotate"
	// RoomDevicesPath registers another device of an existing member.
	RoomDevicesPath = "/host/v1/room/devices"

	// MaxRoomEvents bounds one sync or join response.
	MaxRoomEvents = 1000
	// MaxRoomEventSize bounds one sealed event payload.
	MaxRoomEventSize = 256 << 10
	// MaxRoomWrappedKey bounds one wrapped epoch key.
	MaxRoomWrappedKey = 4 << 10
	// MaxRoomPackages bounds one key upload.
	MaxRoomPackages = 4096
	// MaxRoomInviteKeys bounds wrapped keys attached to one invite.
	MaxRoomInviteKeys = 512
	// MaxRoomMembers bounds membership in one hosted room.
	MaxRoomMembers = 5000
	// MaxRoomDevices bounds devices in one hosted room.
	MaxRoomDevices = 20000
	// RoomInviteTTL bounds how far ahead an invitation may expire.
	RoomInviteTTL = 30 * 24 * time.Hour
)

// RoomDeviceRef is a device identity presented to the room host. The
// certificate is account-signed; the host verifies it against the account
// before trusting the signing key.
type RoomDeviceRef struct {
	AccountID           string `json:"account_id"`
	DeviceID            string `json:"device_id"`
	DisplayName         string `json:"display_name,omitempty"`
	SigningPublicKey    []byte `json:"signing_public_key"`
	EncryptionPublicKey []byte `json:"encryption_public_key"`
	Certificate         []byte `json:"device_certificate"`
}

// RoomKeyPackage is one room epoch key wrapped to one device. The host cannot
// open it. The concrete type lives in the room package so node code can use
// one representation end to end.
type RoomKeyPackage = room.KeyPackage

// RoomKeyRequest tells a syncing device which peers still need wrapped keys.
// Any active member that holds the key may upload the packages.
type RoomKeyRequest struct {
	DeviceID string `json:"device_id"`
	Epochs   []int  `json:"epochs"`
}

// RoomInfo is public room state. It deliberately carries no room name or
// topic: those live only in encrypted events and local room records.
type RoomInfo struct {
	ID               string `json:"id"`
	OwnerAccountID   string `json:"owner_account_id"`
	Epoch            int    `json:"epoch"`
	Head             int64  `json:"head"`
	HeadEnvelopeHash string `json:"head_envelope_hash,omitempty"`
	// HeadEventID and HeadHash are the plaintext chain anchor for the current
	// head. A device whose invite hid earlier history needs them to build the
	// first event it can see without receiving that hidden plaintext.
	HeadEventID      string    `json:"head_event_id,omitempty"`
	HeadHash         string    `json:"head_hash,omitempty"`
	RotationRequired bool      `json:"rotation_required,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// RoomMemberInfo is one hosted membership.
type RoomMemberInfo struct {
	AccountID           string     `json:"account_id"`
	DeviceID            string     `json:"device_id,omitempty"`
	DisplayName         string     `json:"display_name"`
	Role                string     `json:"role"`
	Status              string     `json:"status"`
	History             string     `json:"history,omitempty"`
	EncryptionPublicKey []byte     `json:"encryption_public_key,omitempty"`
	JoinedAt            time.Time  `json:"joined_at"`
	LeftAt              *time.Time `json:"left_at,omitempty"`
}

// RoomDeviceInfo is one trusted room device and its current revocation state.
type RoomDeviceInfo struct {
	DeviceID            string     `json:"device_id"`
	AccountID           string     `json:"account_id"`
	SigningPublicKey    []byte     `json:"signing_public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key"`
	History             string     `json:"history,omitempty"`
	JoinedAt            time.Time  `json:"joined_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
}

// RoomCreateRequest creates a hosted room. Keys must include a wrapping of
// epoch one for the creator's device.
type RoomCreateRequest struct {
	Protocol int              `json:"protocol"`
	RoomID   string           `json:"room_id"`
	Device   RoomDeviceRef    `json:"device"`
	Event    room.WireEvent   `json:"event"`
	Keys     []RoomKeyPackage `json:"keys"`
}

// RoomCreateResponse acknowledges a room and its ordered first event.
type RoomCreateResponse struct {
	Protocol int      `json:"protocol"`
	Room     RoomInfo `json:"room"`
}

// RoomSyncRequest asks for room state after a sequence cursor.
type RoomSyncRequest struct {
	Protocol int    `json:"protocol"`
	RoomID   string `json:"room_id"`
	After    int64  `json:"after"`
	Limit    int    `json:"limit,omitempty"`
	// NeedKeys names older epochs this device wants peers to rewrap. The
	// current epoch is always requested automatically.
	NeedKeys []int `json:"need_keys,omitempty"`
}

// RoomSyncResponse is an incremental room view. Events are ciphertext; the
// host has checked their envelope chain and device signature. Keys contains
// only packages wrapped to the caller.
type RoomSyncResponse struct {
	Protocol         int              `json:"protocol"`
	Room             RoomInfo         `json:"room"`
	Members          []RoomMemberInfo `json:"members"`
	Devices          []RoomDeviceInfo `json:"devices"`
	Keys             []RoomKeyPackage `json:"keys,omitempty"`
	Events           []room.WireEvent `json:"events"`
	HasMore          bool             `json:"has_more,omitempty"`
	KeyRequests      []RoomKeyRequest `json:"key_requests,omitempty"`
	RotationRequired bool             `json:"rotation_required,omitempty"`
}

// RoomEventRequest appends one signed, sealed event.
type RoomEventRequest struct {
	Protocol int            `json:"protocol"`
	Event    room.WireEvent `json:"event"`
}

// RoomEventResponse reports the accepted head.
type RoomEventResponse struct {
	Protocol         int    `json:"protocol"`
	Head             int64  `json:"head"`
	HeadEnvelopeHash string `json:"head_envelope_hash"`
	RotationRequired bool   `json:"rotation_required,omitempty"`
}

// RoomInviteRequest creates a single-use invite. SecretHash is the SHA-256 of
// the invite secret; the host never sees the secret itself.
type RoomInviteRequest struct {
	Protocol   int              `json:"protocol"`
	RoomID     string           `json:"room_id"`
	InviteID   string           `json:"invite_id"`
	SecretHash []byte           `json:"secret_hash"`
	History    string           `json:"history"`
	ExpiresAt  time.Time        `json:"expires_at"`
	Event      room.WireEvent   `json:"event"`
	Keys       []RoomKeyPackage `json:"keys,omitempty"`
}

// RoomInviteResponse acknowledges an invitation.
type RoomInviteResponse struct {
	Protocol  int       `json:"protocol"`
	InviteID  string    `json:"invite_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RoomJoinRequest redeems an invitation. The caller is not yet a room device,
// so the host authenticates its hosted-account device certificate directly.
type RoomJoinRequest struct {
	Protocol int           `json:"protocol"`
	RoomID   string        `json:"room_id"`
	Secret   string        `json:"secret"`
	Device   RoomDeviceRef `json:"device"`
}

// RoomJoinResponse is the snapshot a new member needs. InviteKeys are wrapped
// under the invite secret, so no existing member must be online to admit the
// joiner. Events may be opaque when their epoch was not shared.
type RoomJoinResponse struct {
	Protocol   int              `json:"protocol"`
	Room       RoomInfo         `json:"room"`
	Members    []RoomMemberInfo `json:"members"`
	Devices    []RoomDeviceInfo `json:"devices"`
	InviteKeys []RoomKeyPackage `json:"invite_keys,omitempty"`
	Events     []room.WireEvent `json:"events"`
	History    string           `json:"history"`
	InviteID   string           `json:"invite_id"`
	JoinedAt   time.Time        `json:"joined_at"`
	HasMore    bool             `json:"has_more,omitempty"`
}

// RoomKeyUploadRequest deposits wrapped epoch keys for other devices.
type RoomKeyUploadRequest struct {
	Protocol int              `json:"protocol"`
	RoomID   string           `json:"room_id"`
	Packages []RoomKeyPackage `json:"packages"`
}

// RoomKeyUploadResponse reports how many packages were stored or already
// present.
type RoomKeyUploadResponse struct {
	Protocol int `json:"protocol"`
	Stored   int `json:"stored"`
	Skipped  int `json:"skipped"`
}

// RoomLeaveRequest records a member's own departure.
type RoomLeaveRequest struct {
	Protocol int            `json:"protocol"`
	Event    room.WireEvent `json:"event"`
}

// RoomRotateRequest installs a new epoch. Every active device must have a
// wrapped key, and the event must be sealed with the new epoch.
type RoomRotateRequest struct {
	Protocol int              `json:"protocol"`
	Epoch    int              `json:"epoch"`
	Event    room.WireEvent   `json:"event"`
	Keys     []RoomKeyPackage `json:"keys"`
}

// RoomDeviceRegisterRequest adds another device to an account that is already
// an active member.
type RoomDeviceRegisterRequest struct {
	Protocol int           `json:"protocol"`
	RoomID   string        `json:"room_id"`
	Device   RoomDeviceRef `json:"device"`
}

// RoomDeviceRegisterResponse acknowledges the device.
type RoomDeviceRegisterResponse struct {
	Protocol int      `json:"protocol"`
	Room     RoomInfo `json:"room"`
}
