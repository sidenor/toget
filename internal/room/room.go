// Package room owns rooms, the append-only event log, and per-room ordering.
package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// ErrEmptyMessage means the message has no text.
	ErrEmptyMessage = errors.New("message is empty")
	// ErrMessageTooLong means the message exceeds the local limit.
	ErrMessageTooLong = errors.New("message is too long")
	// ErrCorruptEvent means an event failed its integrity check.
	ErrCorruptEvent = errors.New("event integrity check failed")
)

const (
	MaxNameLength    = 80
	MaxMessageLength = 64 * 1024
	defaultPageSize  = 200
	maxPageSize      = 1000
)

// Room is a private conversation.
type Room struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	HomeNode  string    `json:"home_node"`
	CreatedBy string    `json:"created_by"`
	Epoch     int       `json:"epoch"`
	CreatedAt time.Time `json:"created_at"`
}

// Event is one immutable entry in a room log.
type Event struct {
	ID        string          `json:"id"`
	RoomID    string          `json:"room_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	AuthorID  string          `json:"author_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
	PrevID    string          `json:"prev_id,omitempty"`
	Hash      string          `json:"hash"`
}

// Event types used by the local loop.
const (
	EventRoomCreated   = "room.created"
	EventMessagePosted = "message.posted"
)

// RoomCreatedPayload is the payload of room.created.
type RoomCreatedPayload struct {
	Name string `json:"name"`
}

// MessagePostedPayload is the payload of message.posted.
type MessagePostedPayload struct {
	Text   string `json:"text"`
	Format string `json:"format"`
}

// Store persists rooms and events.
type Store interface {
	CreateRoom(ctx context.Context, room *Room, event *Event) error
	ListRooms(ctx context.Context) ([]Room, error)
	GetRoom(ctx context.Context, id string) (*Room, error)
	LastEvent(ctx context.Context, roomID string) (*Event, error)
	AppendEvent(ctx context.Context, event *Event) error
	ListEvents(ctx context.Context, roomID string, after int64, limit int) ([]Event, error)
}

// Service owns room rules.
type Service struct {
	store Store
	vault *vault.Vault
	now   func() time.Time
	mu    sync.Mutex
}

// New creates a room service.
func New(store Store, v *vault.Vault) *Service {
	return &Service{store: store, vault: v, now: time.Now}
}

// CreateRoom creates a room and its room.created event.
func (s *Service) CreateRoom(ctx context.Context, name, authorID, deviceID string) (*Room, *Event, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameLength {
		return nil, nil, ErrInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC().Truncate(time.Millisecond)
	room := &Room{
		ID:        identity.NewID(),
		Name:      name,
		HomeNode:  deviceID,
		CreatedBy: authorID,
		Epoch:     1,
		CreatedAt: now,
	}
	payload, err := json.Marshal(RoomCreatedPayload{Name: name})
	if err != nil {
		return nil, nil, fmt.Errorf("encode room.created: %w", err)
	}
	event := &Event{
		ID:        identity.NewID(),
		RoomID:    room.ID,
		Seq:       1,
		Type:      EventRoomCreated,
		AuthorID:  authorID,
		Payload:   payload,
		CreatedAt: now,
	}
	event.Hash = eventHash(event)

	stored, err := s.sealEvent(event)
	if err != nil {
		return nil, nil, err
	}
	if err := s.store.CreateRoom(ctx, room, stored); err != nil {
		return nil, nil, err
	}
	return room, event, nil
}

// PostMessage appends a message.posted event to a room.
func (s *Service) PostMessage(ctx context.Context, roomID, authorID, text string) (*Event, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ErrEmptyMessage
	}
	if len(text) > MaxMessageLength {
		return nil, ErrMessageTooLong
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		return nil, err
	}
	last, err := s.store.LastEvent(ctx, roomID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	payload, err := json.Marshal(MessagePostedPayload{Text: text, Format: "plain"})
	if err != nil {
		return nil, fmt.Errorf("encode message.posted: %w", err)
	}
	event := &Event{
		ID:        identity.NewID(),
		RoomID:    roomID,
		Seq:       last.Seq + 1,
		Type:      EventMessagePosted,
		AuthorID:  authorID,
		Payload:   payload,
		CreatedAt: now,
		PrevID:    last.ID,
	}
	event.Hash = eventHash(event)

	stored, err := s.sealEvent(event)
	if err != nil {
		return nil, err
	}
	if err := s.store.AppendEvent(ctx, stored); err != nil {
		return nil, err
	}
	return event, nil
}

// ListRooms returns rooms, newest first.
func (s *Service) ListRooms(ctx context.Context) ([]Room, error) {
	return s.store.ListRooms(ctx)
}

// GetRoom returns one room.
func (s *Service) GetRoom(ctx context.Context, id string) (*Room, error) {
	return s.store.GetRoom(ctx, id)
}

// ListEvents returns decrypted events after a sequence number.
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
		event, err := s.openEvent(&stored[i])
		if err != nil {
			return nil, err
		}
		events = append(events, *event)
	}
	return events, nil
}

func (s *Service) sealEvent(event *Event) (*Event, error) {
	sealed, err := s.vault.Seal(event.Payload, payloadAAD(event.RoomID))
	if err != nil {
		return nil, fmt.Errorf("seal event payload: %w", err)
	}
	copy := *event
	copy.Payload = []byte(sealed)
	return &copy, nil
}

func (s *Service) openEvent(event *Event) (*Event, error) {
	plaintext, err := s.vault.Open(string(event.Payload), payloadAAD(event.RoomID))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptEvent, err)
	}
	copy := *event
	copy.Payload = plaintext
	if eventHash(&copy) != event.Hash {
		return nil, ErrCorruptEvent
	}
	return &copy, nil
}

func payloadAAD(roomID string) string {
	return "toget:event-payload:v1:" + roomID
}

func eventHash(event *Event) string {
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
