package room

import (
	"encoding/json"
	"strconv"
	"time"
)

// WireEvent is the JSON-safe transport form of an Event. Stored room payloads
// are sealed ciphertext ("rke1:...") and are not JSON, so RawMessage cannot
// carry them through encoding/json. The string form keeps ciphertext intact
// without base64-encoding it a second time.
type WireEvent struct {
	ID           string    `json:"id"`
	RoomID       string    `json:"room_id"`
	Seq          int64     `json:"seq"`
	Epoch        int       `json:"epoch"`
	Type         string    `json:"type"`
	AuthorID     string    `json:"author_id"`
	AuthorDevice string    `json:"author_device,omitempty"`
	Payload      string    `json:"payload"`
	CreatedAt    time.Time `json:"created_at"`
	PrevID       string    `json:"prev_id,omitempty"`
	PrevHash     string    `json:"prev_hash,omitempty"`
	Hash         string    `json:"hash"`
	Signature    string    `json:"signature,omitempty"`
	// Envelope fields let a blind home verify order and authorship without
	// the room key. Node-hosted rooms leave them empty.
	EnvelopeHash      string `json:"envelope_hash,omitempty"`
	EnvelopePrevHash  string `json:"envelope_prev_hash,omitempty"`
	EnvelopeSignature string `json:"envelope_signature,omitempty"`
}

// ToWire converts an event for transport.
func (e Event) ToWire() WireEvent {
	return WireEvent{
		ID: e.ID, RoomID: e.RoomID, Seq: e.Seq, Epoch: e.Epoch, Type: e.Type,
		AuthorID: e.AuthorID, AuthorDevice: e.AuthorDevice,
		Payload: string(e.Payload), CreatedAt: e.CreatedAt,
		PrevID: e.PrevID, PrevHash: e.PrevHash, Hash: e.Hash, Signature: e.Signature,
		EnvelopeHash: e.EnvelopeHash, EnvelopePrevHash: e.EnvelopePrevHash,
		EnvelopeSignature: e.EnvelopeSignature,
	}
}

// ToEvent converts a transported event back to its internal form.
func (w WireEvent) ToEvent() Event {
	return Event{
		ID: w.ID, RoomID: w.RoomID, Seq: w.Seq, Epoch: w.Epoch, Type: w.Type,
		AuthorID: w.AuthorID, AuthorDevice: w.AuthorDevice,
		Payload: json.RawMessage(w.Payload), CreatedAt: w.CreatedAt,
		PrevID: w.PrevID, PrevHash: w.PrevHash, Hash: w.Hash, Signature: w.Signature,
		EnvelopeHash: w.EnvelopeHash, EnvelopePrevHash: w.EnvelopePrevHash,
		EnvelopeSignature: w.EnvelopeSignature,
	}
}

// WireEvents converts a batch for transport.
func WireEvents(events []Event) []WireEvent {
	out := make([]WireEvent, len(events))
	for i := range events {
		out[i] = events[i].ToWire()
	}
	return out
}

// EventsFromWire converts a transported batch.
func EventsFromWire(events []WireEvent) []Event {
	out := make([]Event, len(events))
	for i := range events {
		out[i] = events[i].ToEvent()
	}
	return out
}

// WireMember is the transport form of a Member. Member hides public keys
// from local API responses, but peers need them to verify room events.
type WireMember struct {
	RoomID              string     `json:"room_id"`
	AccountID           string     `json:"account_id"`
	DeviceID            string     `json:"device_id,omitempty"`
	DisplayName         string     `json:"display_name"`
	Role                string     `json:"role"`
	Status              string     `json:"status"`
	AccountPublicKey    []byte     `json:"account_public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key"`
	JoinedAt            time.Time  `json:"joined_at"`
	LeftAt              *time.Time `json:"left_at,omitempty"`
}

// ToWire converts a member for transport.
func (m Member) ToWire() WireMember {
	return WireMember{
		RoomID: m.RoomID, AccountID: m.AccountID, DeviceID: m.DeviceID,
		DisplayName: m.DisplayName, Role: m.Role, Status: m.Status,
		AccountPublicKey:    append([]byte(nil), m.AccountPublicKey...),
		EncryptionPublicKey: append([]byte(nil), m.EncryptionPublicKey...),
		JoinedAt:            m.JoinedAt, LeftAt: m.LeftAt,
	}
}

// ToMember converts a transported member.
func (w WireMember) ToMember() Member {
	return Member{
		RoomID: w.RoomID, AccountID: w.AccountID, DeviceID: w.DeviceID,
		DisplayName: w.DisplayName, Role: w.Role, Status: w.Status,
		AccountPublicKey:    append([]byte(nil), w.AccountPublicKey...),
		EncryptionPublicKey: append([]byte(nil), w.EncryptionPublicKey...),
		JoinedAt:            w.JoinedAt, LeftAt: w.LeftAt,
	}
}

// WireDevice is the transport form of a RoomDevice, including public keys.
type WireDevice struct {
	RoomID              string     `json:"room_id"`
	DeviceID            string     `json:"device_id"`
	AccountID           string     `json:"account_id"`
	PublicKey           []byte     `json:"public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key"`
	JoinedAt            time.Time  `json:"joined_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
}

// ToWire converts a room device for transport.
func (d RoomDevice) ToWire() WireDevice {
	return WireDevice{
		RoomID: d.RoomID, DeviceID: d.DeviceID, AccountID: d.AccountID,
		PublicKey:           append([]byte(nil), d.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), d.EncryptionPublicKey...),
		JoinedAt:            d.JoinedAt, RevokedAt: d.RevokedAt,
	}
}

// ToDevice converts a transported room device.
func (w WireDevice) ToDevice() RoomDevice {
	return RoomDevice{
		RoomID: w.RoomID, DeviceID: w.DeviceID, AccountID: w.AccountID,
		PublicKey:           append([]byte(nil), w.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), w.EncryptionPublicKey...),
		JoinedAt:            w.JoinedAt, RevokedAt: w.RevokedAt,
	}
}

// WireMembers converts members for transport.
func WireMembers(members []Member) []WireMember {
	out := make([]WireMember, len(members))
	for i := range members {
		out[i] = members[i].ToWire()
	}
	return out
}

// MembersFromWire converts transported members.
func MembersFromWire(members []WireMember) []Member {
	out := make([]Member, len(members))
	for i := range members {
		out[i] = members[i].ToMember()
	}
	return out
}

// WireDevices converts room devices for transport.
func WireDevices(devices []RoomDevice) []WireDevice {
	out := make([]WireDevice, len(devices))
	for i := range devices {
		out[i] = devices[i].ToWire()
	}
	return out
}

// DevicesFromWire converts transported room devices.
func DevicesFromWire(devices []WireDevice) []RoomDevice {
	out := make([]RoomDevice, len(devices))
	for i := range devices {
		out[i] = devices[i].ToDevice()
	}
	return out
}

// WireAttachment is attachment metadata without a local filesystem path. The
// ciphertext stays on the home node; members download it on demand and verify
// the plaintext digest after decrypting it with the room epoch key.
type WireAttachment struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	EventID   string    `json:"event_id,omitempty"`
	Name      string    `json:"name"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Epoch     int       `json:"epoch"`
	CreatedAt time.Time `json:"created_at"`
}

// ToWire converts attachment metadata for transport.
func (a Attachment) ToWire() WireAttachment {
	epoch, _ := strconv.Atoi(a.KeySealed)
	return WireAttachment{
		ID: a.ID, RoomID: a.RoomID, EventID: a.EventID, Name: a.Name,
		MIME: a.MIME, Size: a.Size, SHA256: a.SHA256, Epoch: epoch,
		CreatedAt: a.CreatedAt,
	}
}

// ToAttachment converts transported metadata back to its local form. The path
// is intentionally empty until the ciphertext is fetched.
func (w WireAttachment) ToAttachment() Attachment {
	return Attachment{
		ID: w.ID, RoomID: w.RoomID, EventID: w.EventID, Name: w.Name,
		MIME: w.MIME, Size: w.Size, SHA256: w.SHA256,
		KeySealed: strconv.Itoa(w.Epoch), CreatedAt: w.CreatedAt,
	}
}

// WireAttachments converts metadata for transport.
func WireAttachments(items []Attachment) []WireAttachment {
	out := make([]WireAttachment, len(items))
	for i := range items {
		out[i] = items[i].ToWire()
	}
	return out
}
