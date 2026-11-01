// Package webclient implements a browser-hosted Toget account and official
// room client. It reuses the account-host protocol and room cryptography from
// the native client, keeps all keys in the page's local state, and never asks
// the official service to decrypt anything.
package webclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	roompkg "github.com/sidenor/toget/internal/room"
)

// StateVersion is the on-disk browser-state format.
const StateVersion = 1

// Error is a user-facing client error with a stable code for the web UI.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Errorf builds a coded error.
func Errorf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError converts any error into a coded error, preserving a known code.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var clientErr *Error
	if errors.As(err, &clientErr) {
		return clientErr
	}
	return &Error{Code: "internal", Message: friendly(err)}
}

func friendly(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		text = "操作失败，请重试。"
	}
	return text
}

// State is the encrypted-at-rest browser state. The page stores this JSON
// inside its local vault; Go never writes it to disk.
type State struct {
	Version int           `json:"version"`
	Account *AccountState `json:"account,omitempty"`
	Rooms   []*RoomState  `json:"rooms,omitempty"`
}

// AccountState is the account and this browser's device identity.
type AccountState struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	HostURL          string      `json:"host_url"`
	HostID           string      `json:"host_id"`
	HostCode         string      `json:"host_code"`
	PublicKey        []byte      `json:"public_key"`
	PrivateKey       []byte      `json:"private_key"`
	MasterKey        []byte      `json:"master_key"`
	Device           DeviceState `json:"device"`
	SessionToken     string      `json:"session_token,omitempty"`
	SessionExpiresAt time.Time   `json:"session_expires_at,omitempty"`
	EmailVerified    bool        `json:"email_verified,omitempty"`
	CreatedAt        time.Time   `json:"created_at,omitempty"`
	VaultVersion     int64       `json:"vault_version,omitempty"`
	VaultDirty       bool        `json:"vault_dirty,omitempty"`
}

// DeviceState is the browser's account device. Private keys never leave this
// state except as signatures.
type DeviceState struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	PublicKey            []byte    `json:"public_key"`
	PrivateKey           []byte    `json:"private_key"`
	EncryptionPublicKey  []byte    `json:"encryption_public_key"`
	EncryptionPrivateKey []byte    `json:"encryption_private_key"`
	Certificate          string    `json:"certificate"`
	CreatedAt            time.Time `json:"created_at"`
}

// AccountDevice is one device certificate registered to the account.
type AccountDevice struct {
	ID        string     `json:"device_id"`
	Name      string     `json:"name"`
	Current   bool       `json:"current"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// MemberState is a room member as last seen on the host.
type MemberState struct {
	AccountID           string     `json:"account_id"`
	DisplayName         string     `json:"display_name"`
	Role                string     `json:"role"`
	Status              string     `json:"status"`
	EncryptionPublicKey []byte     `json:"encryption_public_key,omitempty"`
	JoinedAt            time.Time  `json:"joined_at"`
	LeftAt              *time.Time `json:"left_at,omitempty"`
}

// DeviceInfo is a trusted room device and its revocation state.
type DeviceInfo struct {
	DeviceID            string     `json:"device_id"`
	AccountID           string     `json:"account_id"`
	SigningPublicKey    []byte     `json:"signing_public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key"`
	JoinedAt            time.Time  `json:"joined_at,omitempty"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
}

// MessageState is one decrypted timeline item.
type MessageState struct {
	ID           string    `json:"id"`
	Seq          int64     `json:"seq"`
	EventType    string    `json:"event_type"`
	AuthorID     string    `json:"author_id"`
	AuthorDevice string    `json:"author_device,omitempty"`
	AuthorName   string    `json:"author_name,omitempty"`
	Text         string    `json:"text"`
	Kind         string    `json:"kind"` // text | system
	At           time.Time `json:"at"`
	Epoch        int       `json:"epoch,omitempty"`
}

// RoomState is this browser's view of one official room. Epoch keys are
// plaintext here because the whole state is sealed by the page vault.
type RoomState struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Topic            string `json:"topic,omitempty"`
	Kind             string `json:"kind,omitempty"`
	HomeURL          string `json:"home_url"`
	HostID           string `json:"host_id"`
	OwnerAccountID   string `json:"owner_account_id"`
	Epoch            int    `json:"epoch"`
	Head             int64  `json:"head"`
	HeadEventID      string `json:"head_event_id,omitempty"`
	HeadHash         string `json:"head_hash,omitempty"`
	HeadEnvelopeHash string `json:"head_envelope_hash,omitempty"`
	// LastSeq and its hash fields identify the next event's predecessor. They
	// may come from the host head when an invite hid earlier history.
	LastSeq          int64          `json:"last_seq"`
	LastID           string         `json:"last_id,omitempty"`
	LastHash         string         `json:"last_hash,omitempty"`
	LastEnvelopeHash string         `json:"last_envelope_hash,omitempty"`
	Keys             map[int][]byte `json:"keys,omitempty"`
	Members          []MemberState  `json:"members,omitempty"`
	Devices          []DeviceInfo   `json:"devices,omitempty"`
	Messages         []MessageState `json:"messages,omitempty"`
	ReadSeq          int64          `json:"read_seq,omitempty"`
	History          string         `json:"history,omitempty"`
	DeviceRegistered bool           `json:"device_registered,omitempty"`
	RotationRequired bool           `json:"rotation_required,omitempty"`
	JoinedAt         time.Time      `json:"joined_at,omitempty"`
	UpdatedAt        time.Time      `json:"updated_at,omitempty"`
	Left             bool           `json:"left,omitempty"`
}

// Client owns one browser session. Operations are serialized because a
// browser tab advances one room head at a time.
type Client struct {
	mu     sync.Mutex
	origin string
	now    func() time.Time
	state  State
	// recovery is only populated by Register and must be shown and discarded
	// before the page leaves the onboarding flow.
	recovery *RecoveryFile
}

// New creates a client bound to the origin that served the page.
func New(origin string) (*Client, error) {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if origin == "" {
		return nil, Errorf("internal", "页面来源无效。")
	}
	return &Client{origin: origin, now: time.Now, state: State{Version: StateVersion}}, nil
}

// SetClock replaces the clock in tests.
func (c *Client) SetClock(now func() time.Time) {
	if now != nil {
		c.now = now
	}
}

// Restore loads a previously exported state. It does not contact the host.
func (c *Client) Restore(encoded string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(encoded) == "" {
		c.state = State{Version: StateVersion}
		return nil
	}
	var state State
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return Errorf("invalid_state", "本地登录状态已损坏，请重新登录。")
	}
	if state.Version != StateVersion {
		return Errorf("invalid_state", "本地登录状态来自不兼容的版本。")
	}
	if state.Account != nil {
		if err := validateAccountState(state.Account); err != nil {
			return err
		}
	}
	for _, room := range state.Rooms {
		if err := validateRoomState(room); err != nil {
			return err
		}
	}
	c.state = state
	return nil
}

func validateAccountState(a *AccountState) error {
	if a.ID == "" || a.HostID == "" || len(a.PublicKey) != 32 ||
		len(a.PrivateKey) != 64 || len(a.Device.PublicKey) != 32 ||
		len(a.Device.PrivateKey) != 64 || len(a.Device.EncryptionPublicKey) != 32 ||
		len(a.Device.EncryptionPrivateKey) != 32 || a.Device.Certificate == "" {
		return Errorf("invalid_state", "本地账号状态不完整，请重新登录。")
	}
	return nil
}

func validateRoomState(r *RoomState) error {
	if r == nil || r.ID == "" || r.HostID == "" {
		return Errorf("invalid_state", "本地房间状态不完整，请重新登录。")
	}
	if r.Keys == nil {
		r.Keys = map[int][]byte{}
	}
	if r.Epoch < 1 {
		r.Epoch = 1
	}
	return nil
}

// Export serializes the state for the page's encrypted vault.
func (c *Client) Export() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, room := range c.state.Rooms {
		if room != nil {
			sortRoomMessages(room)
		}
	}
	encoded, err := json.Marshal(c.state)
	if err != nil {
		return "", Errorf("internal", "无法保存本地状态。")
	}
	return string(encoded), nil
}

// Profile is the signed-in account summary shown by the web UI.
type Profile struct {
	AccountID     string    `json:"account_id"`
	Name          string    `json:"name"`
	HostID        string    `json:"host_id"`
	DeviceID      string    `json:"device_id"`
	DeviceName    string    `json:"device_name"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	RoomCount     int       `json:"room_count"`
	VaultPending  bool      `json:"vault_pending"`
}

// Profile returns the current account summary, or nil when signed out.
func (c *Client) Profile() *Profile {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profileLocked()
}

func (c *Client) profileLocked() *Profile {
	a := c.state.Account
	if a == nil {
		return nil
	}
	return &Profile{
		AccountID: a.ID, Name: a.Name, HostID: a.HostID,
		DeviceID: a.Device.ID, DeviceName: a.Device.Name,
		EmailVerified: a.EmailVerified, CreatedAt: a.CreatedAt,
		RoomCount: len(c.activeRoomsLocked()), VaultPending: a.VaultDirty,
	}
}

// Logout wipes all local account and room key material. The caller should
// clear the page vault only after this returns.
func (c *Client) Logout() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = State{Version: StateVersion}
	c.recovery = nil
	return nil
}

// RecoveryFile is the one-time account recovery credential.
type RecoveryFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// ConsumeRecovery returns the pending recovery file once and clears it.
func (c *Client) ConsumeRecovery() *RecoveryFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	file := c.recovery
	c.recovery = nil
	return file
}

func (c *Client) activeRoomsLocked() []*RoomState {
	out := make([]*RoomState, 0, len(c.state.Rooms))
	for _, room := range c.state.Rooms {
		if room != nil && !room.Left {
			out = append(out, room)
		}
	}
	return out
}

func (c *Client) roomLocked(roomID string) *RoomState {
	for _, room := range c.state.Rooms {
		if room != nil && room.ID == roomID && !room.Left {
			return room
		}
	}
	return nil
}

func (c *Client) findRoomIndexLocked(roomID string) int {
	for i, room := range c.state.Rooms {
		if room != nil && room.ID == roomID {
			return i
		}
	}
	return -1
}

func (c *Client) upsertRoomLocked(room *RoomState) {
	if room == nil {
		return
	}
	if index := c.findRoomIndexLocked(room.ID); index >= 0 {
		c.state.Rooms[index] = room
		return
	}
	c.state.Rooms = append(c.state.Rooms, room)
}

func sortRoomMessages(room *RoomState) {
	if room == nil {
		return
	}
	sort.SliceStable(room.Messages, func(i, j int) bool {
		if room.Messages[i].Seq != room.Messages[j].Seq {
			return room.Messages[i].Seq < room.Messages[j].Seq
		}
		return room.Messages[i].ID < room.Messages[j].ID
	})
	if len(room.Messages) > maxCachedMessages {
		room.Messages = room.Messages[len(room.Messages)-maxCachedMessages:]
	}
}

// RoomSummary is the room-list projection used by the web UI.
type RoomSummary struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Topic          string     `json:"topic,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	PeerAccountID  string     `json:"peer_account_id,omitempty"`
	PeerName       string     `json:"peer_name,omitempty"`
	OwnerAccountID string     `json:"owner_account_id"`
	Epoch          int        `json:"epoch"`
	MemberCount    int        `json:"member_count"`
	Unread         int        `json:"unread"`
	History        string     `json:"history,omitempty"`
	JoinedAt       time.Time  `json:"joined_at,omitempty"`
	ReadSeq        int64      `json:"read_seq,omitempty"`
	LastSeq        int64      `json:"last_seq,omitempty"`
	LastText       string     `json:"last_text,omitempty"`
	LastAuthor     string     `json:"last_author,omitempty"`
	LastAt         *time.Time `json:"last_at,omitempty"`
}

func roomSummary(room *RoomState, accountID, deviceID string) RoomSummary {
	if room == nil {
		return RoomSummary{}
	}
	unread := 0
	for _, message := range room.Messages {
		if message.Seq > room.ReadSeq && message.Kind == "text" && message.AuthorDevice != deviceID {
			unread++
		}
	}
	summary := RoomSummary{
		ID: room.ID, Name: room.Name, Topic: room.Topic, Kind: room.Kind,
		OwnerAccountID: room.OwnerAccountID, Epoch: room.Epoch,
		MemberCount: len(activeMembers(room)), Unread: unread,
		History: room.History, JoinedAt: room.JoinedAt,
		ReadSeq: room.ReadSeq, LastSeq: room.LastSeq,
	}
	if room.Kind == roompkg.RoomKindDirect {
		for _, member := range room.Members {
			if member.Status == "active" && member.AccountID != accountID {
				summary.PeerAccountID = member.AccountID
				summary.PeerName = member.DisplayName
				break
			}
		}
	}
	if len(room.Messages) > 0 {
		last := room.Messages[len(room.Messages)-1]
		summary.LastText = last.Text
		at := last.At
		summary.LastAt = &at
		summary.LastAuthor = last.AuthorName
	}
	return summary
}

func activeMembers(room *RoomState) []MemberState {
	if room == nil {
		return nil
	}
	out := make([]MemberState, 0, len(room.Members))
	for _, member := range room.Members {
		if member.Status == "active" {
			out = append(out, member)
		}
	}
	return out
}

const maxCachedMessages = 1200
