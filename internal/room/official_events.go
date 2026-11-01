package room

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

// BuildOfficialEvent constructs, seals, and envelope-signs one event for a
// blind official room home. The caller must have already synchronized its
// local head and hold the epoch key.
func BuildOfficialEvent(roomID string, seq int64, prev *Event, eventType string, actor Actor, payload any, epoch int, key []byte, now time.Time) (*Event, error) {
	if !actor.Valid() {
		return nil, ErrForbidden
	}
	if roomID == "" || seq < 1 || eventType == "" || epoch < 1 || len(key) != 32 {
		return nil, ErrInvalidEvent
	}
	if prev != nil && (prev.RoomID != roomID || prev.Seq+1 != seq) {
		return nil, ErrInvalidEvent
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	event, err := buildEvent(roomID, seq, prev, eventType, actor, raw, now.UTC().Truncate(time.Millisecond))
	if err != nil {
		return nil, err
	}
	sealed, err := sealEvent(event, epoch, key)
	if err != nil {
		return nil, err
	}
	if err := SignEnvelope(sealed, actor.SigningKey); err != nil {
		return nil, err
	}
	return sealed, nil
}

// OpenOfficialEvent decrypts a sealed official-room event and returns a copy
// with plaintext payload. It does not verify the author signature; callers
// must call VerifyEvent with the author device key afterwards.
func OpenOfficialEvent(event *Event, key []byte) (*Event, error) {
	if event == nil || event.Epoch < 1 || len(key) != 32 {
		return nil, ErrInvalidEvent
	}
	plain, err := openRoomPayload(key, event.Payload, payloadAAD(event.RoomID, event.Epoch, "event"))
	if err != nil {
		return nil, ErrCorruptEvent
	}
	opened := *event
	opened.Payload = plain
	return &opened, nil
}

// OpenOfficialPayload decrypts one sealed official-room payload without
// rebuilding its event envelope.
func OpenOfficialPayload(roomID string, epoch int, key, sealed []byte) ([]byte, error) {
	if roomID == "" || epoch < 1 || len(key) != 32 {
		return nil, ErrInvalidEvent
	}
	return openRoomPayload(key, sealed, payloadAAD(roomID, epoch, "event"))
}

// SignOfficialEventWithDevice is a small helper for callers that build an
// event without BuildOfficialEvent.
func SignOfficialEventWithDevice(event *Event, key []byte) error {
	if len(key) != 64 {
		return errors.New("device signing key is invalid")
	}
	return SignEnvelope(event, key)
}

// NewOfficialRoomID returns an opaque room ID.
func NewOfficialRoomID() string { return identity.NewID() }

// NewRoomKey generates a fresh 32-byte room epoch key.
func NewRoomKey() ([]byte, error) { return newRoomKey() }
