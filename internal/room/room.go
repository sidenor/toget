// Package room owns rooms, membership, the append-only event log, and
// per-room ordering.
package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/vault"
)

var (
	// ErrNotFound means the room does not exist.
	ErrNotFound = errors.New("room not found")
	// ErrInvalidName means the room name is empty or too long.
	ErrInvalidName = errors.New("invalid room name")
	// ErrInvalidTopic means the topic is too long.
	ErrInvalidTopic = errors.New("invalid room topic")
	// ErrEmptyMessage means the message has no text.
	ErrEmptyMessage = errors.New("message is empty")
	// ErrMessageTooLong means the message exceeds the local limit.
	ErrMessageTooLong = errors.New("message is too long")
	// ErrCorruptEvent means an event failed its integrity check.
	ErrCorruptEvent = errors.New("event integrity check failed")
	// ErrNotMember means the caller is not an active member of the room.
	ErrNotMember = errors.New("not a room member")
	// ErrForbidden means the caller lacks the room role for this action.
	ErrForbidden = errors.New("room action is not allowed")
	// ErrMemberExists means the account is already a member.
	ErrMemberExists = errors.New("account is already a room member")
	// ErrInviteInvalid means an invitation code is malformed or unknown.
	ErrInviteInvalid = errors.New("invite is invalid")
	// ErrInviteExpired means an invitation has expired.
	ErrInviteExpired = errors.New("invite has expired")
	// ErrInviteUsed means an invitation has already been redeemed.
	ErrInviteUsed = errors.New("invite has already been used")
	// ErrInvalidEvent means an event is structurally invalid.
	ErrInvalidEvent = errors.New("invalid event")
	// ErrMessageNotFound means an edit target does not exist.
	ErrMessageNotFound = errors.New("message not found")

	// ErrKeyUnavailable means this node does not hold the room key for an
	// event's epoch, usually because it joined without prior history.
	ErrKeyUnavailable = errors.New("room key unavailable")
	// ErrRemoteReadOnly means the room is hosted by another node. Messages
	// are forwarded to the home node; local-only mutations are not allowed.
	ErrRemoteReadOnly = errors.New("room is hosted by another node")
)

const (
	MaxNameLength    = 80
	MaxTopicLength   = 280
	MaxMessageLength = 64 * 1024
	defaultPageSize  = 200
	maxPageSize      = 1000
	maxSearchHits    = 200
)

// Room is a private conversation.
type Room struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Topic        string     `json:"topic,omitempty"`
	HomeNode     string     `json:"home_node"`
	HomeServerID string     `json:"home_server_id,omitempty"`
	HomeAddress  string     `json:"home_address,omitempty"`
	CreatedBy    string     `json:"created_by"`
	Epoch        int        `json:"epoch"`
	CreatedAt    time.Time  `json:"created_at"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty"`
}

// Member is an account's membership in a room.
type Member struct {
	RoomID              string     `json:"room_id"`
	AccountID           string     `json:"account_id"`
	DeviceID            string     `json:"device_id,omitempty"`
	DisplayName         string     `json:"display_name"`
	Role                string     `json:"role"`
	Status              string     `json:"status"`
	AccountPublicKey    []byte     `json:"-"`
	EncryptionPublicKey []byte     `json:"-"`
	JoinedAt            time.Time  `json:"joined_at"`
	LeftAt              *time.Time `json:"left_at,omitempty"`
}

// RoomDevice is a device allowed to sign events in a room.
type RoomDevice struct {
	RoomID              string     `json:"room_id"`
	DeviceID            string     `json:"device_id"`
	AccountID           string     `json:"account_id"`
	PublicKey           []byte     `json:"-"`
	EncryptionPublicKey []byte     `json:"-"`
	JoinedAt            time.Time  `json:"joined_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
}

// Actor is the local account and device making a change.
type Actor struct {
	AccountID           string
	DeviceID            string
	DisplayName         string
	SigningKey          ed25519.PrivateKey
	AccountPublicKey    ed25519.PublicKey
	EncryptionPublicKey []byte
}

// Valid reports whether the actor can sign a local event.
func (a Actor) Valid() bool {
	return a.AccountID != "" && a.DeviceID != "" && len(a.SigningKey) == ed25519.PrivateKeySize
}

// Event is one immutable entry in a room log.
type Event struct {
	ID           string          `json:"id"`
	RoomID       string          `json:"room_id"`
	Seq          int64           `json:"seq"`
	Epoch        int             `json:"epoch"`
	Type         string          `json:"type"`
	AuthorID     string          `json:"author_id"`
	AuthorDevice string          `json:"author_device,omitempty"`
	Payload      json.RawMessage `json:"payload"`
	CreatedAt    time.Time       `json:"created_at"`
	PrevID       string          `json:"prev_id,omitempty"`
	PrevHash     string          `json:"prev_hash,omitempty"`
	Hash         string          `json:"hash"`
	Signature    string          `json:"signature,omitempty"`
}

// Event types. Names are past tense and stable.
const (
	EventRoomCreated         = "room.created"
	EventRoomSettingsChanged = "room.settings_changed"
	EventRoomArchived        = "room.archived"
	EventMemberInvited       = "room.member_invited"
	EventMemberJoined        = "room.member_joined"
	EventMemberLeft          = "room.member_left"
	EventEpochRotated        = "room.epoch_rotated"
	EventHistoryAttached     = "room.history_attached"
	EventMessagePosted       = "message.posted"
	EventMessageEdited       = "message.edited"
	EventApprovalRequested   = "approval.requested"
	EventApprovalGranted     = "approval.granted"
	EventApprovalRejected    = "approval.rejected"
	EventApprovalExpired     = "approval.expired"
	EventApprovalConsumed    = "approval.consumed"
	EventToolRequested       = "tool.requested"
	EventToolResult          = "tool.result"
	EventToolFailed          = "tool.failed"
	EventAgentJoined         = "agent.joined"
	EventAgentLeft           = "agent.left"
	EventAgentStatus         = "agent.status"
	EventPolicyUpdated       = "policy.updated"
	EventPolicyDeleted       = "policy.deleted"
)

// Payloads for the event types above.
type RoomCreatedPayload struct {
	Name  string `json:"name"`
	Topic string `json:"topic,omitempty"`
}

type RoomSettingsChangedPayload struct {
	Name  string `json:"name"`
	Topic string `json:"topic"`
}

type MemberInvitedPayload struct {
	InviteID    string `json:"invite_id"`
	AccountID   string `json:"account_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	History     string `json:"history"`
	ExpiresAt   string `json:"expires_at"`
}

type MemberJoinedPayload struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	History     string `json:"history,omitempty"`
}

type MemberLeftPayload struct {
	AccountID string `json:"account_id"`
	Reason    string `json:"reason,omitempty"`
}

type EpochRotatedPayload struct {
	Epoch  int    `json:"epoch"`
	Reason string `json:"reason,omitempty"`
}

type MessagePostedPayload struct {
	Text        string   `json:"text"`
	Format      string   `json:"format"`
	Attachments []string `json:"attachments,omitempty"`
	Mentions    []string `json:"mentions,omitempty"`
	ReplyTo     string   `json:"reply_to,omitempty"`
	// AgentID marks the message as an Agent's reply. The cryptographic author
	// remains the owning account's device; this field is display and routing
	// metadata, validated by the local daemon at the origin.
	AgentID string `json:"agent_id,omitempty"`
}

type MessageEditedPayload struct {
	TargetID string `json:"target_id"`
	Text     string `json:"text"`
	Format   string `json:"format"`
}

type ApprovalRequestedPayload struct {
	ApprovalID   string `json:"approval_id"`
	AgentID      string `json:"agent_id"`
	ActionDigest string `json:"action_digest"`
	Description  string `json:"description"`
	ExpiresAt    string `json:"expires_at"`
	PolicyID     string `json:"policy_id,omitempty"`
}

type ApprovalDecidedPayload struct {
	ApprovalID string `json:"approval_id"`
	AgentID    string `json:"agent_id,omitempty"`
	Decision   string `json:"decision"`
	By         string `json:"by"`
	Digest     string `json:"digest"`
	PolicyID   string `json:"policy_id,omitempty"`
}

type ApprovalConsumedPayload struct {
	ApprovalID string `json:"approval_id"`
	AgentID    string `json:"agent_id,omitempty"`
	Digest     string `json:"digest"`
}

type ToolEventPayload struct {
	ReceiptID  string          `json:"receipt_id"`
	AgentID    string          `json:"agent_id"`
	Tool       string          `json:"tool"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	PolicyID   string          `json:"policy_id,omitempty"`
}

type AgentJoinedPayload struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	OwnerID     string `json:"owner_id"`
	Runtime     string `json:"runtime"`
	DisplayName string `json:"display_name"`
}

type AgentLeftPayload struct {
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason,omitempty"`
}

type AgentStatusPayload struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
}

type PolicyUpdatedPayload struct {
	PolicyID  string `json:"policy_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	RuleCount int    `json:"rule_count,omitempty"`
	Change    string `json:"change,omitempty"`
}

type HistoryAttachedPayload struct {
	AccountID string `json:"account_id"`
	FromSeq   int64  `json:"from_seq"`
	ToSeq     int64  `json:"to_seq"`
	Choice    string `json:"choice"`
}

var (
	// ErrInvalidInviteHistory is returned for an unknown history choice.
	ErrInvalidInviteHistory = errors.New("invalid history choice")
	// ErrInviteSecret means the invite secret is malformed.
	ErrInviteSecret = errors.New("invalid invite secret")
)

// Invite is a single-use room invitation. The secret is returned only at
// creation time; only its hash is stored.
type Invite struct {
	ID        string     `json:"id"`
	RoomID    string     `json:"room_id"`
	Secret    string     `json:"secret,omitempty"`
	Code      string     `json:"code,omitempty"`
	CreatedBy string     `json:"created_by"`
	History   string     `json:"history"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// History choices for invitations.
const (
	HistoryNone   = "none"
	HistoryRecent = "recent"
	HistoryAll    = "all"
)

// Store persists rooms, membership, and events.
type Store interface {
	CreateRoom(ctx context.Context, room *Room, event *Event, member *Member, device *RoomDevice, epoch *RoomEpoch, keys []RoomMemberKey) error
	ImportRoom(ctx context.Context, room *Room, members []Member, devices []RoomDevice, epochs []RoomEpoch, keys []RoomMemberKey, events []Event) error
	MergeRoomSnapshot(ctx context.Context, room *Room, members []Member, devices []RoomDevice, epochs []RoomEpoch, keys []RoomMemberKey) error
	ImportEvents(ctx context.Context, events []Event) error
	ListRooms(ctx context.Context) ([]Room, error)
	GetRoom(ctx context.Context, id string) (*Room, error)
	UpdateRoom(ctx context.Context, room *Room) error
	LastEvent(ctx context.Context, roomID string) (*Event, error)
	AppendEvent(ctx context.Context, event *Event) error
	ListEvents(ctx context.Context, roomID string, after int64, limit int) ([]Event, error)
	FirstEventSeqForEpoch(ctx context.Context, roomID string, epoch int) (int64, error)
	GetEvent(ctx context.Context, eventID string) (*Event, error)
	GetMember(ctx context.Context, roomID, accountID string) (*Member, error)
	ListMembers(ctx context.Context, roomID string) ([]Member, error)
	AddMember(ctx context.Context, member *Member) error
	UpdateMember(ctx context.Context, member *Member) error
	UpsertMember(ctx context.Context, member *Member) error
	GetRoomDevice(ctx context.Context, roomID, deviceID string) (*RoomDevice, error)
	ListRoomDevices(ctx context.Context, roomID string) ([]RoomDevice, error)
	AddRoomDevice(ctx context.Context, device *RoomDevice) error
	UpsertRoomDevice(ctx context.Context, device *RoomDevice) error
	UpdateRoomDevice(ctx context.Context, device *RoomDevice) error
	CreateInvite(ctx context.Context, invite *Invite, secretHash string) error
	GetInviteByHash(ctx context.Context, secretHash string) (*Invite, error)
	MarkInviteUsed(ctx context.Context, id string, at time.Time) error
	SetReadCursor(ctx context.Context, roomID, deviceID string, seq int64) error
	GetReadCursor(ctx context.Context, roomID, deviceID string) (int64, error)
	SaveRoomEpoch(ctx context.Context, e *RoomEpoch) error
	GetRoomEpoch(ctx context.Context, roomID string, epoch int) (*RoomEpoch, error)
	LatestRoomEpoch(ctx context.Context, roomID string) (*RoomEpoch, error)
	ListRoomEpochs(ctx context.Context, roomID string) ([]RoomEpoch, error)
	SaveRoomMemberKey(ctx context.Context, k *RoomMemberKey) error
	GetRoomMemberKey(ctx context.Context, roomID string, epoch int, deviceID string) (*RoomMemberKey, error)
	ListRoomMemberKeys(ctx context.Context, roomID string, epoch int) ([]RoomMemberKey, error)
	CreateAttachment(ctx context.Context, a *Attachment) error
	UpsertAttachment(ctx context.Context, a *Attachment) error
	SetAttachmentPath(ctx context.Context, id, path string) error
	GetAttachment(ctx context.Context, id string) (*Attachment, error)
	ListAttachments(ctx context.Context, roomID string) ([]Attachment, error)
	AttachToEvent(ctx context.Context, eventID string, attachmentIDs []string) error
	// GetRoomAgent returns the home node's view of a remote Agent that joined
	// the room. Local Agents are tracked in the agent store, not here.
	GetRoomAgent(ctx context.Context, roomID, agentID string) (*RoomAgent, error)
	// AppendEventWithRoomAgent atomically appends an event and records the
	// Agent membership it establishes or closes.
	AppendEventWithRoomAgent(ctx context.Context, event *Event, agent *RoomAgent) error
	// ListEventsByTypes returns stored events of the given types, oldest first.
	ListEventsByTypes(ctx context.Context, roomID string, types []string) ([]Event, error)
}

// RoomAgent is the home node's projection of an Agent that belongs to a remote
// room member. It is built only from accepted agent.joined and agent.left
// events, so a member cannot post status, tools, or approvals for an Agent
// another account owns.
type RoomAgent struct {
	RoomID    string `json:"room_id"`
	AgentID   string `json:"agent_id"`
	AccountID string `json:"account_id"`
	Active    bool   `json:"active"`
	JoinedSeq int64  `json:"joined_seq"`
	LeftSeq   int64  `json:"left_seq,omitempty"`
}

// AgentPresence is a read-only projection of Agents that joined a room,
// rebuilt from the event log. It lets every member see and name shared Agents
// without replicating the owner's local Agent registry.
type AgentPresence struct {
	AgentID   string    `json:"agent_id"`
	AccountID string    `json:"account_id"`
	Name      string    `json:"name"`
	Runtime   string    `json:"runtime"`
	Status    string    `json:"status"`
	JoinedAt  time.Time `json:"joined_at"`
}

// RemoteAppendFunc forwards an event to the node that hosts a remote room.
type RemoteAppendFunc func(ctx context.Context, roomID string, actor Actor, eventType string, payload any) (*Event, error)

// Service owns room rules.
type Service struct {
	store        Store
	vault        *vault.Vault
	now          func() time.Time
	mu           sync.Mutex
	dataDir      string
	writeGuard   func(ctx context.Context, roomID, eventType string) error
	remoteAppend RemoteAppendFunc
}

// Option configures a room service.
type Option func(*Service)

// WithWriteGuard installs a hook that can reject local writes to rooms this
// node does not host. Remote messages do not pass through Append, so a guard
// only blocks operations that would fork the home node's log.
func WithWriteGuard(guard func(ctx context.Context, roomID, eventType string) error) Option {
	return func(s *Service) { s.writeGuard = guard }
}

// SetRemoteAppend installs the transport that lets Agent, approval, and tool
// events reach a room hosted by another node. The node service installs it
// after construction; without it, Append keeps returning ErrRemoteReadOnly.
func (s *Service) SetRemoteAppend(fn RemoteAppendFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteAppend = fn
}

func (s *Service) checkWritable(ctx context.Context, roomID, eventType string) error {
	if s.writeGuard == nil {
		return nil
	}
	return s.writeGuard(ctx, roomID, eventType)
}

// New creates a room service.
func New(store Store, v *vault.Vault, opts ...Option) *Service {
	s := &Service{store: store, vault: v, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateRoom creates a room, its room.created event, and the creator's
// membership.
func (s *Service) CreateRoom(ctx context.Context, name, topic string, actor Actor) (*Room, *Event, error) {
	name = strings.TrimSpace(name)
	topic = strings.TrimSpace(topic)
	if name == "" || len(name) > MaxNameLength {
		return nil, nil, ErrInvalidName
	}
	if len(topic) > MaxTopicLength {
		return nil, nil, ErrInvalidTopic
	}
	if !actor.Valid() {
		return nil, nil, ErrForbidden
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC().Truncate(time.Millisecond)
	room := &Room{
		ID:        identity.NewID(),
		Name:      name,
		Topic:     topic,
		HomeNode:  actor.DeviceID,
		CreatedBy: actor.AccountID,
		Epoch:     1,
		CreatedAt: now,
	}
	payload, err := json.Marshal(RoomCreatedPayload{Name: name, Topic: topic})
	if err != nil {
		return nil, nil, fmt.Errorf("encode room.created: %w", err)
	}
	event, err := s.buildEvent(room.ID, 1, nil, EventRoomCreated, actor, payload, now)
	if err != nil {
		return nil, nil, err
	}
	key, err := newRoomKey()
	if err != nil {
		return nil, nil, err
	}
	sealedPayload, err := sealRoomPayload(key, payload, payloadAAD(room.ID, 1, "event"))
	if err != nil {
		return nil, nil, err
	}
	event.Epoch = 1
	member := &Member{
		RoomID:              room.ID,
		AccountID:           actor.AccountID,
		DeviceID:            actor.DeviceID,
		DisplayName:         actor.DisplayName,
		Role:                "owner",
		Status:              "active",
		AccountPublicKey:    actor.AccountPublicKey,
		EncryptionPublicKey: actor.EncryptionPublicKey,
		JoinedAt:            now,
	}
	device := &RoomDevice{
		RoomID:              room.ID,
		DeviceID:            actor.DeviceID,
		AccountID:           actor.AccountID,
		PublicKey:           actor.SigningKey.Public().(ed25519.PublicKey),
		EncryptionPublicKey: actor.EncryptionPublicKey,
		JoinedAt:            now,
	}
	stored := *event
	stored.Payload = sealedPayload
	sealedKey, err := s.vault.Seal(key, roomEpochAAD(room.ID, 1))
	if err != nil {
		return nil, nil, fmt.Errorf("seal room key: %w", err)
	}
	wrapped, err := WrapRoomKey(room.ID, 1, actor.DeviceID, actor.EncryptionPublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	epoch := &RoomEpoch{RoomID: room.ID, Epoch: 1, KeySealed: sealedKey, CreatedAt: now}
	keys := []RoomMemberKey{{RoomID: room.ID, Epoch: 1, DeviceID: actor.DeviceID, Wrapped: wrapped, CreatedAt: now}}
	if err := s.store.CreateRoom(ctx, room, &stored, member, device, epoch, keys); err != nil {
		return nil, nil, err
	}
	return room, event, nil
}

// PostMessage appends a message.posted event to a room.
func (s *Service) PostMessage(ctx context.Context, roomID string, actor Actor, body MessagePostedPayload) (*Event, error) {
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" && len(body.Attachments) == 0 {
		return nil, ErrEmptyMessage
	}
	if len(body.Text) > MaxMessageLength {
		return nil, ErrMessageTooLong
	}
	if body.Format == "" {
		body.Format = "plain"
	}
	if len(body.Attachments) > 0 {
		if err := s.RequireMember(ctx, roomID, actor); err != nil {
			return nil, err
		}
		ids, err := s.validateAttachmentRefs(ctx, roomID, body.Attachments)
		if err != nil {
			return nil, err
		}
		body.Attachments = ids
	}
	event, err := s.Append(ctx, roomID, actor, EventMessagePosted, body)
	if err != nil {
		return nil, err
	}
	if len(body.Attachments) > 0 {
		if err := s.store.AttachToEvent(ctx, event.ID, body.Attachments); err != nil {
			return nil, err
		}
	}
	return event, nil
}

// EditMessage appends a message.edited event. History is never rewritten.
func (s *Service) EditMessage(ctx context.Context, roomID string, actor Actor, targetID, text string) (*Event, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ErrEmptyMessage
	}
	if len(text) > MaxMessageLength {
		return nil, ErrMessageTooLong
	}
	if _, err := s.findEvent(ctx, roomID, targetID); err != nil {
		return nil, err
	}
	return s.Append(ctx, roomID, actor, EventMessageEdited, MessageEditedPayload{
		TargetID: targetID,
		Text:     text,
		Format:   "plain",
	})
}

// Append adds a signed event of any type to a room. It is used by the
// approval, agent, and tool services; room-level authorization is checked by
// those services before calling it.
// RequireMember checks that the actor is an active member of the room and
// that its device is enrolled. It is the common authorization gate for local
// writes.
func (s *Service) RequireMember(ctx context.Context, roomID string, actor Actor) error {
	if !actor.Valid() {
		return ErrForbidden
	}
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return err
	}
	member, err := s.store.GetMember(ctx, roomID, actor.AccountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if member.Status != "active" {
		return ErrNotMember
	}
	if _, err := s.store.GetRoomDevice(ctx, roomID, actor.DeviceID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrForbidden
		}
		return err
	}
	return nil
}

func (s *Service) Append(ctx context.Context, roomID string, actor Actor, eventType string, payload any) (*Event, error) {
	if eventType == "" {
		return nil, ErrInvalidEvent
	}

	s.mu.Lock()
	if err := s.checkWritable(ctx, roomID, eventType); err != nil {
		forward := s.remoteAppend
		s.mu.Unlock()
		if errors.Is(err, ErrRemoteReadOnly) && forward != nil {
			return forward(ctx, roomID, actor, eventType, payload)
		}
		return nil, err
	}
	plain, sealed, err := s.prepareLocalLocked(ctx, roomID, actor, eventType, payload)
	if err == nil {
		err = s.store.AppendEvent(ctx, sealed)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// Prepare builds and seals an event at the next local sequence without
// appending it. A remote node posts the event to the room home and installs
// the accepted copy with AppendRemote.
func (s *Service) Prepare(ctx context.Context, roomID string, actor Actor, eventType string, payload any) (*Event, error) {
	if eventType == "" {
		return nil, ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, sealed, err := s.prepareLocalLocked(ctx, roomID, actor, eventType, payload)
	return sealed, err
}

func (s *Service) prepareLocalLocked(ctx context.Context, roomID string, actor Actor, eventType string, payload any) (*Event, *Event, error) {
	if err := s.RequireMember(ctx, roomID, actor); err != nil {
		return nil, nil, err
	}
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode %s: %w", eventType, err)
	}
	epoch, key, err := s.latestRoomKeyLocked(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	event, err := s.buildEvent(roomID, last.Seq+1, last, eventType, actor, raw, now)
	if err != nil {
		return nil, nil, err
	}
	event.Epoch = epoch
	stored, err := s.sealEvent(event, epoch, key)
	if err != nil {
		return nil, nil, err
	}
	return event, stored, nil
}

// AppendRemote stores an event received from another node. The caller must
// synchronize ordering with the home node; this function verifies the event's
// chain, author device, and signature before persisting it.
func (s *Service) AppendRemote(ctx context.Context, event *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendRemoteLocked(ctx, event, nil)
}

// remoteEventValidator inspects a decrypted event whose signature has already
// been checked. It returns a membership change to commit with the event, or
// nil when the event does not change the Agent projection.
type remoteEventValidator func(ctx context.Context, plain *Event) (*RoomAgent, error)

func (s *Service) appendRemoteLocked(ctx context.Context, event *Event, validate remoteEventValidator) error {
	if event == nil || event.RoomID == "" || event.ID == "" || event.Seq <= 0 || event.Type == "" {
		return ErrInvalidEvent
	}
	if _, err := s.store.GetRoom(ctx, event.RoomID); err != nil {
		return err
	}
	last, err := s.store.LastEvent(ctx, event.RoomID)
	if err != nil {
		return err
	}
	if event.Seq <= last.Seq {
		// Idempotent replay by event ID.
		if existing, err := s.findEvent(ctx, event.RoomID, event.ID); err == nil && existing.Seq == event.Seq {
			return nil
		}
		return fmt.Errorf("%w: sequence is not after the local head", ErrInvalidEvent)
	}
	if event.Seq != last.Seq+1 {
		return fmt.Errorf("%w: gap in room sequence", ErrInvalidEvent)
	}
	if event.PrevID != last.ID || event.PrevHash != last.Hash {
		return fmt.Errorf("%w: chain does not link to the local head", ErrInvalidEvent)
	}
	device, err := s.store.GetRoomDevice(ctx, event.RoomID, event.AuthorDevice)
	if err != nil {
		return fmt.Errorf("%w: unknown author device", ErrInvalidEvent)
	}
	plain := *event
	if isRoomPayload(event.Payload) {
		epoch := event.Epoch
		if epoch <= 0 {
			epoch = 1
		}
		key, err := s.roomKeyLocked(ctx, event.RoomID, epoch)
		if err != nil {
			return err
		}
		opened, err := openRoomPayload(key, event.Payload, payloadAAD(event.RoomID, epoch, "event"))
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
		}
		plain.Payload = opened
		event.Epoch = epoch
	}
	if err := VerifyEvent(&plain, device.PublicKey); err != nil {
		return err
	}
	var binding *RoomAgent
	if validate != nil {
		binding, err = validate(ctx, &plain)
		if err != nil {
			return err
		}
	}
	var attachmentIDs []string
	if event.Type == EventMessagePosted {
		var body MessagePostedPayload
		if err := json.Unmarshal(plain.Payload, &body); err != nil {
			return fmt.Errorf("%w: message payload is invalid", ErrInvalidEvent)
		}
		if len(body.Attachments) > 0 {
			ids, err := s.validateAttachmentRefs(ctx, event.RoomID, body.Attachments)
			if err != nil {
				return err
			}
			attachmentIDs = ids
		}
	}
	stored := event
	if !isRoomPayload(event.Payload) {
		epoch, key, err := s.latestRoomKeyLocked(ctx, event.RoomID)
		if err != nil {
			return err
		}
		storedCopy, err := s.sealEvent(event, epoch, key)
		if err != nil {
			return err
		}
		stored = storedCopy
	}
	if binding != nil {
		if err := s.store.AppendEventWithRoomAgent(ctx, stored, binding); err != nil {
			return err
		}
	} else if err := s.store.AppendEvent(ctx, stored); err != nil {
		return err
	}
	if len(attachmentIDs) > 0 {
		if err := s.store.AttachToEvent(ctx, stored.ID, attachmentIDs); err != nil {
			return err
		}
	}
	return nil
}

// GetRoom returns one room.
func (s *Service) GetRoom(ctx context.Context, id string) (*Room, error) {
	return s.store.GetRoom(ctx, id)
}

// ListRooms returns rooms, newest first.
func (s *Service) ListRooms(ctx context.Context) ([]Room, error) {
	return s.store.ListRooms(ctx)
}

// GetMember returns one room membership.
func (s *Service) GetMember(ctx context.Context, roomID, accountID string) (*Member, error) {
	return s.store.GetMember(ctx, roomID, accountID)
}

// ListMembers returns active and past members of a room.
func (s *Service) ListMembers(ctx context.Context, roomID string) ([]Member, error) {
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return nil, err
	}
	return s.store.ListMembers(ctx, roomID)
}

// GetDevice returns one trusted device in a room.
func (s *Service) GetDevice(ctx context.Context, roomID, deviceID string) (*RoomDevice, error) {
	return s.store.GetRoomDevice(ctx, roomID, deviceID)
}

// ListDevices returns the device keys trusted for a room.
func (s *Service) ListDevices(ctx context.Context, roomID string) ([]RoomDevice, error) {
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return nil, err
	}
	return s.store.ListRoomDevices(ctx, roomID)
}

// ListEvents returns decrypted, verified events after a sequence number.
func (s *Service) ListEvents(ctx context.Context, roomID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return nil, err
	}
	stored, err := s.store.ListEvents(ctx, roomID, after, limit)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(stored))
	for i := range stored {
		event, err := s.openEvent(ctx, &stored[i])
		if errors.Is(err, ErrKeyUnavailable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		events = append(events, *event)
	}
	return events, nil
}

// SetReadCursor records the last sequence read by a device.
func (s *Service) SetReadCursor(ctx context.Context, roomID, deviceID string, seq int64) error {
	if seq < 0 {
		return ErrInvalidEvent
	}
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return err
	}
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if seq != 0 {
				return ErrInvalidEvent
			}
		} else {
			return err
		}
	} else if seq > last.Seq {
		return ErrInvalidEvent
	}
	return s.store.SetReadCursor(ctx, roomID, deviceID, seq)
}

// ReadCursor returns the last sequence a device has read.
func (s *Service) ReadCursor(ctx context.Context, roomID, deviceID string) (int64, error) {
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return 0, err
	}
	seq, err := s.store.GetReadCursor(ctx, roomID, deviceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return seq, nil
}

// Unread returns the number of events after a device's read cursor.
func (s *Service) Unread(ctx context.Context, roomID, deviceID string) (int64, error) {
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	cursor, err := s.store.GetReadCursor(ctx, roomID, deviceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if cursor >= last.Seq {
		return 0, nil
	}
	return last.Seq - cursor, nil
}

// Rename updates a room's name and topic with a settings event.
func (s *Service) Rename(ctx context.Context, roomID string, actor Actor, name, topic string) (*Room, *Event, error) {
	name = strings.TrimSpace(name)
	topic = strings.TrimSpace(topic)
	if name == "" || len(name) > MaxNameLength {
		return nil, nil, ErrInvalidName
	}
	if len(topic) > MaxTopicLength {
		return nil, nil, ErrInvalidTopic
	}
	if err := s.requireOwner(ctx, roomID, actor); err != nil {
		return nil, nil, err
	}
	event, err := s.Append(ctx, roomID, actor, EventRoomSettingsChanged, RoomSettingsChangedPayload{
		Name: name, Topic: topic,
	})
	if err != nil {
		return nil, nil, err
	}
	current, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	current.Name = name
	current.Topic = topic
	if err := s.store.UpdateRoom(ctx, current); err != nil {
		return nil, nil, err
	}
	return current, event, nil
}

// CreateInvite creates a single-use invitation to a room.
func (s *Service) CreateInvite(ctx context.Context, roomID string, actor Actor, history string, ttl time.Duration) (*Invite, *Event, error) {
	switch history {
	case "", HistoryNone:
		history = HistoryNone
	case HistoryRecent, HistoryAll:
	default:
		return nil, nil, ErrInvalidInviteHistory
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if err := s.checkWritable(ctx, roomID, EventMemberInvited); err != nil {
		return nil, nil, err
	}
	if err := s.requireMember(ctx, roomID, actor); err != nil {
		return nil, nil, err
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	invite := &Invite{
		ID:        identity.NewID(),
		RoomID:    roomID,
		Secret:    secret,
		CreatedBy: actor.AccountID,
		History:   history,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}
	if err := s.store.CreateInvite(ctx, invite, HashInviteSecret(secret)); err != nil {
		return nil, nil, err
	}
	event, err := s.Append(ctx, roomID, actor, EventMemberInvited, MemberInvitedPayload{
		InviteID:  invite.ID,
		History:   history,
		ExpiresAt: invite.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return nil, nil, err
	}
	return invite, event, nil
}

// Leave marks the caller's membership as left.
func (s *Service) Leave(ctx context.Context, roomID string, actor Actor) (*Event, error) {
	if err := s.requireMember(ctx, roomID, actor); err != nil {
		return nil, err
	}
	event, err := s.Append(ctx, roomID, actor, EventMemberLeft, MemberLeftPayload{
		AccountID: actor.AccountID,
		Reason:    "left",
	})
	if err != nil {
		return nil, err
	}
	member, err := s.store.GetMember(ctx, roomID, actor.AccountID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	member.Status = "left"
	member.LeftAt = &now
	if err := s.store.UpdateMember(ctx, member); err != nil {
		return nil, err
	}
	if _, err := s.rotateEpochLocked(ctx, roomID); err != nil {
		return nil, err
	}
	return event, nil
}

// Archive marks a room read-only for this node and records the change.
func (s *Service) Archive(ctx context.Context, roomID string, actor Actor) (*Room, *Event, error) {
	if err := s.requireOwner(ctx, roomID, actor); err != nil {
		return nil, nil, err
	}
	current, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	if current.ArchivedAt != nil {
		return current, nil, nil
	}
	event, err := s.Append(ctx, roomID, actor, EventRoomArchived, map[string]string{"archived_by": actor.AccountID})
	if err != nil {
		return nil, nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	current.ArchivedAt = &now
	if err := s.store.UpdateRoom(ctx, current); err != nil {
		return nil, nil, err
	}
	return current, event, nil
}

// ArchiveLocal hides a room on this device without recording an event. Members
// use it to retire a remote room without asking the home node to archive it for
// everyone; the shared log and other devices are unaffected.
func (s *Service) ArchiveLocal(ctx context.Context, roomID string, actor Actor) (*Room, error) {
	if !actor.Valid() {
		return nil, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if current.ArchivedAt != nil {
		return current, nil
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	current.ArchivedAt = &now
	if err := s.store.UpdateRoom(ctx, current); err != nil {
		return nil, err
	}
	return current, nil
}

// Audit returns a verifiable snapshot of a room's history.
type Audit struct {
	Room        Room         `json:"room"`
	Members     []Member     `json:"members"`
	Devices     []RoomDevice `json:"devices"`
	Events      []Event      `json:"events"`
	Head        string       `json:"head"`
	GeneratedAt time.Time    `json:"generated_at"`
}

// ExportAudit builds an audit bundle for a room.
func (s *Service) ExportAudit(ctx context.Context, roomID string) (*Audit, error) {
	room, err := s.store.GetRoom(ctx, roomID)
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
	var events []Event
	for after := int64(0); ; {
		page, err := s.ListEvents(ctx, roomID, after, maxPageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		events = append(events, page...)
		after = page[len(page)-1].Seq
		if len(page) < maxPageSize {
			break
		}
	}
	head := ""
	if len(events) > 0 {
		head = events[len(events)-1].Hash
	}
	return &Audit{
		Room:        *room,
		Members:     members,
		Devices:     devices,
		Events:      events,
		Head:        head,
		GeneratedAt: s.now().UTC(),
	}, nil
}

// VerifyAudit checks an audit bundle's event chain and signatures.
func VerifyAudit(a *Audit) error {
	if a == nil {
		return ErrInvalidEvent
	}
	devices := make(map[string]ed25519.PublicKey, len(a.Devices))
	for _, d := range a.Devices {
		devices[d.DeviceID] = ed25519.PublicKey(d.PublicKey)
	}
	var prev *Event
	for i := range a.Events {
		event := &a.Events[i]
		if event.RoomID != a.Room.ID {
			return fmt.Errorf("%w: event belongs to a different room", ErrCorruptEvent)
		}
		if prev != nil {
			if event.Seq != prev.Seq+1 || event.PrevID != prev.ID || event.PrevHash != prev.Hash {
				return fmt.Errorf("%w: broken chain at sequence %d", ErrCorruptEvent, event.Seq)
			}
		} else if event.Seq != 1 {
			return fmt.Errorf("%w: audit does not start at sequence 1", ErrCorruptEvent)
		}
		key, ok := devices[event.AuthorDevice]
		if !ok {
			return fmt.Errorf("%w: unknown device %s", ErrCorruptEvent, event.AuthorDevice)
		}
		if err := VerifyEvent(event, key); err != nil {
			return err
		}
		prev = event
	}
	if a.Head != prev.Hash && !(prev == nil && a.Head == "") {
		return fmt.Errorf("%w: head does not match the last event", ErrCorruptEvent)
	}
	return nil
}

// SearchHit is a matching event.
type SearchHit struct {
	RoomID   string    `json:"room_id"`
	RoomName string    `json:"room_name"`
	EventID  string    `json:"event_id"`
	Seq      int64     `json:"seq"`
	Type     string    `json:"type"`
	Text     string    `json:"text"`
	Author   string    `json:"author_id"`
	At       time.Time `json:"created_at"`
}

// Search scans decrypted local events for a case-insensitive substring.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > maxSearchHits {
		limit = maxSearchHits
	}
	rooms, err := s.store.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, limit)
	for _, r := range rooms {
		events, err := s.ListEvents(ctx, r.ID, 0, maxPageSize)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if len(hits) >= limit {
				return hits, nil
			}
			text := searchableText(event.Payload)
			if !strings.Contains(strings.ToLower(text), query) {
				continue
			}
			hits = append(hits, SearchHit{
				RoomID: r.ID, RoomName: r.Name, EventID: event.ID, Seq: event.Seq,
				Type: event.Type, Text: text, Author: event.AuthorID, At: event.CreatedAt,
			})
		}
	}
	return hits, nil
}

func searchableText(payload json.RawMessage) string {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return string(payload)
	}
	var parts []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			parts = append(parts, x)
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k])
			}
		}
	}
	walk(value)
	return strings.Join(parts, " ")
}

// HashInviteSecret hashes the bearer secret stored in an invitation code.
func HashInviteSecret(secret string) string {
	sum := sha256.Sum256([]byte("toget:invite:v1:" + secret))
	return hex.EncodeToString(sum[:])
}

func randomSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate invite secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Service) requireMember(ctx context.Context, roomID string, actor Actor) error {
	member, err := s.store.GetMember(ctx, roomID, actor.AccountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if member.Status != "active" {
		return ErrNotMember
	}
	return nil
}

func (s *Service) requireOwner(ctx context.Context, roomID string, actor Actor) error {
	member, err := s.store.GetMember(ctx, roomID, actor.AccountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if member.Status != "active" || member.Role != "owner" {
		return ErrForbidden
	}
	return nil
}

func (s *Service) appendUnchecked(ctx context.Context, roomID string, actor Actor, eventType string, payload any, now time.Time) (*Event, error) {
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", eventType, err)
	}
	epoch, key, err := s.latestRoomKeyLocked(ctx, roomID)
	if err != nil {
		return nil, err
	}
	event, err := s.buildEvent(roomID, last.Seq+1, last, eventType, actor, raw, now)
	if err != nil {
		return nil, err
	}
	event.Epoch = epoch
	stored, err := s.sealEvent(event, epoch, key)
	if err != nil {
		return nil, err
	}
	if err := s.store.AppendEvent(ctx, stored); err != nil {
		return nil, err
	}
	return event, nil
}

func (s *Service) buildEvent(roomID string, seq int64, prev *Event, eventType string, actor Actor, payload json.RawMessage, now time.Time) (*Event, error) {
	event := &Event{
		ID:           identity.NewID(),
		RoomID:       roomID,
		Seq:          seq,
		Type:         eventType,
		AuthorID:     actor.AccountID,
		AuthorDevice: actor.DeviceID,
		Payload:      payload,
		CreatedAt:    now,
	}
	if prev != nil {
		event.PrevID = prev.ID
		event.PrevHash = prev.Hash
	}
	event.Hash = ComputeEventHash(event)
	if len(actor.SigningKey) == ed25519.PrivateKeySize {
		event.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(actor.SigningKey, []byte(event.Hash)))
	}
	return event, nil
}

func (s *Service) findEvent(ctx context.Context, roomID, eventID string) (*Event, error) {
	event, err := s.store.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if event.RoomID != roomID {
		return nil, ErrMessageNotFound
	}
	opened, err := s.openEvent(ctx, event)
	if errors.Is(err, ErrKeyUnavailable) {
		return nil, ErrMessageNotFound
	}
	return opened, err
}

func (s *Service) sealEvent(event *Event, epoch int, key []byte) (*Event, error) {
	sealed, err := sealRoomPayload(key, event.Payload, payloadAAD(event.RoomID, epoch, "event"))
	if err != nil {
		return nil, fmt.Errorf("seal event payload: %w", err)
	}
	copy := *event
	copy.Payload = sealed
	copy.Epoch = epoch
	return &copy, nil
}

func (s *Service) openEvent(ctx context.Context, event *Event) (*Event, error) {
	plaintext := []byte(nil)
	if isRoomPayload(event.Payload) {
		epoch := event.Epoch
		if epoch <= 0 {
			epoch = 1
		}
		key, err := s.roomKey(ctx, event.RoomID, epoch)
		if errors.Is(err, ErrNotFound) {
			return nil, ErrKeyUnavailable
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptEvent, err)
		}
		plaintext, err = openRoomPayload(key, event.Payload, payloadAAD(event.RoomID, epoch, "event"))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptEvent, err)
		}
	} else {
		var err error
		plaintext, err = s.vault.Open(string(event.Payload), eventPayloadAAD(event.RoomID))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptEvent, err)
		}
	}
	copy := *event
	copy.Payload = plaintext
	if event.PrevHash == "" && event.AuthorDevice == "" && event.Signature == "" {
		if legacyEventHash(&copy) != event.Hash {
			return nil, ErrCorruptEvent
		}
	} else if ComputeEventHash(&copy) != event.Hash {
		return nil, ErrCorruptEvent
	}
	return &copy, nil
}

func eventPayloadAAD(roomID string) string {
	return "toget:event-payload:v1:" + roomID
}

func roomEpochAAD(roomID string, epoch int) string {
	return fmt.Sprintf("toget:room-epoch:v1|%s|%d", roomID, epoch)
}

func isRoomPayload(payload []byte) bool {
	return len(payload) >= len(sealedKeyPrefix) && string(payload[:len(sealedKeyPrefix)]) == sealedKeyPrefix
}

func (s *Service) roomKey(ctx context.Context, roomID string, epoch int) ([]byte, error) {
	stored, err := s.store.GetRoomEpoch(ctx, roomID, epoch)
	if err != nil {
		return nil, err
	}
	key, err := s.vault.Open(stored.KeySealed, roomEpochAAD(roomID, epoch))
	if err != nil {
		return nil, fmt.Errorf("open room key: %w", err)
	}
	if len(key) != roomKeySize {
		return nil, errors.New("room key has the wrong size")
	}
	return key, nil
}

func (s *Service) roomKeyLocked(ctx context.Context, roomID string, epoch int) ([]byte, error) {
	return s.roomKey(ctx, roomID, epoch)
}

func (s *Service) latestRoomKey(ctx context.Context, roomID string) (int, []byte, error) {
	return s.latestRoomKeyLocked(ctx, roomID)
}

func (s *Service) latestRoomKeyLocked(ctx context.Context, roomID string) (int, []byte, error) {
	stored, err := s.store.LatestRoomEpoch(ctx, roomID)
	if err != nil {
		return 0, nil, err
	}
	key, err := s.vault.Open(stored.KeySealed, roomEpochAAD(roomID, stored.Epoch))
	if err != nil {
		return 0, nil, fmt.Errorf("open room key: %w", err)
	}
	if len(key) != roomKeySize {
		return 0, nil, errors.New("room key has the wrong size")
	}
	return stored.Epoch, key, nil
}

func (s *Service) rotateEpochLocked(ctx context.Context, roomID string) (int, error) {
	next := 1
	latest, err := s.store.LatestRoomEpoch(ctx, roomID)
	if err == nil {
		next = latest.Epoch + 1
	} else if !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	key, err := newRoomKey()
	if err != nil {
		return 0, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	sealed, err := s.vault.Seal(key, roomEpochAAD(roomID, next))
	if err != nil {
		return 0, err
	}
	if err := s.store.SaveRoomEpoch(ctx, &RoomEpoch{RoomID: roomID, Epoch: next, KeySealed: sealed, CreatedAt: now}); err != nil {
		return 0, err
	}
	devices, err := s.store.ListRoomDevices(ctx, roomID)
	if err != nil {
		return 0, err
	}
	for i := range devices {
		device := &devices[i]
		if device.RevokedAt != nil || len(device.EncryptionPublicKey) != 32 {
			continue
		}
		member, err := s.store.GetMember(ctx, roomID, device.AccountID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return 0, err
		}
		if member.Status != "active" {
			continue
		}
		wrapped, err := WrapRoomKey(roomID, next, device.DeviceID, device.EncryptionPublicKey, key)
		if err != nil {
			return 0, err
		}
		if err := s.store.SaveRoomMemberKey(ctx, &RoomMemberKey{
			RoomID: roomID, Epoch: next, DeviceID: device.DeviceID, Wrapped: wrapped, CreatedAt: now,
		}); err != nil {
			return 0, err
		}
	}
	current, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return 0, err
	}
	current.Epoch = next
	if err := s.store.UpdateRoom(ctx, current); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Service) rotateEpoch(ctx context.Context, roomID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateEpochLocked(ctx, roomID)
}

// shareEpochWithDeviceLocked wraps one existing epoch key for a device. The
// device must already be registered in the room.
func (s *Service) shareEpochWithDeviceLocked(ctx context.Context, roomID string, epoch int, deviceID string, encryptionPub []byte) error {
	if len(encryptionPub) != 32 {
		return errors.New("device has no encryption key")
	}
	key, err := s.roomKeyLocked(ctx, roomID, epoch)
	if err != nil {
		return err
	}
	wrapped, err := WrapRoomKey(roomID, epoch, deviceID, encryptionPub, key)
	if err != nil {
		return err
	}
	return s.store.SaveRoomMemberKey(ctx, &RoomMemberKey{
		RoomID: roomID, Epoch: epoch, DeviceID: deviceID, Wrapped: wrapped, CreatedAt: s.now().UTC().Truncate(time.Millisecond),
	})
}

// shareHistoryLocked gives a newly joined device earlier room keys. History
// choices are enforced at epoch granularity: "all" shares every epoch and
// "recent" shares only the current epoch.
func (s *Service) shareHistoryLocked(ctx context.Context, roomID, deviceID string, encryptionPub []byte, history string) error {
	epochs, err := s.store.ListRoomEpochs(ctx, roomID)
	if err != nil {
		return err
	}
	for i := range epochs {
		if history == HistoryRecent && i != len(epochs)-1 {
			continue
		}
		if err := s.shareEpochWithDeviceLocked(ctx, roomID, epochs[i].Epoch, deviceID, encryptionPub); err != nil {
			return err
		}
	}
	return nil
}

// sharedHistoryRangeLocked reports which existing sequences a history choice
// exposes. "recent" starts at the first event of the current epoch; an epoch
// with no events yet yields an empty range above the current head.
func (s *Service) sharedHistoryRangeLocked(ctx context.Context, roomID, history string, currentEpoch int) (int64, int64, error) {
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return 0, 0, err
	}
	if history == HistoryAll {
		return 1, last.Seq, nil
	}
	first, err := s.store.FirstEventSeqForEpoch(ctx, roomID, currentEpoch)
	if errors.Is(err, ErrNotFound) {
		return last.Seq + 1, last.Seq, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return first, last.Seq, nil
}

// ComputeEventHash returns the canonical hash of an event's immutable fields.
func ComputeEventHash(event *Event) string {
	h := sha256.New()
	fmt.Fprintf(h, "toget:event:v2\n%s\n%s\n%d\n%s\n%s\n%s\n%d\n%s\n%s\n",
		event.RoomID,
		event.ID,
		event.Seq,
		event.Type,
		event.AuthorID,
		event.AuthorDevice,
		event.CreatedAt.UnixMilli(),
		event.PrevID,
		event.PrevHash,
	)
	h.Write(event.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyEvent checks the event hash and, when present, its author signature.
func VerifyEvent(event *Event, publicKey ed25519.PublicKey) error {
	if ComputeEventHash(event) != event.Hash {
		return fmt.Errorf("%w: hash mismatch", ErrCorruptEvent)
	}
	if event.Signature == "" {
		return fmt.Errorf("%w: missing signature", ErrCorruptEvent)
	}
	sig, err := base64.RawStdEncoding.DecodeString(event.Signature)
	if err != nil {
		return fmt.Errorf("%w: bad signature encoding", ErrCorruptEvent)
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, []byte(event.Hash), sig) {
		return fmt.Errorf("%w: signature is invalid", ErrCorruptEvent)
	}
	return nil
}

func legacyEventHash(event *Event) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%d\n%s\n%s\n%d\n%s\n",
		event.RoomID,
		event.ID,
		event.Seq,
		event.Type,
		event.AuthorID,
		event.CreatedAt.UnixMilli(),
		event.PrevID,
	)
	h.Write(event.Payload)
	return hex.EncodeToString(h.Sum(nil))
}
