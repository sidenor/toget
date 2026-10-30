package room

import (
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	roomKeySize     = 32
	wrapVersion     = byte(1)
	sealedKeyPrefix = "rke1:"
)

// RoomEpoch is one generation of a room's symmetric key. The key itself is
// sealed with the node vault at rest; devices get wrapped copies.
type RoomEpoch struct {
	RoomID    string    `json:"room_id"`
	Epoch     int       `json:"epoch"`
	KeySealed string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// RoomMemberKey is a room key wrapped to one device's X25519 key.
type RoomMemberKey struct {
	RoomID    string    `json:"room_id"`
	Epoch     int       `json:"epoch"`
	DeviceID  string    `json:"device_id"`
	Wrapped   []byte    `json:"wrapped_key"`
	CreatedAt time.Time `json:"created_at"`
}

func newRoomKey() ([]byte, error) {
	key := make([]byte, roomKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate room key: %w", err)
	}
	return key, nil
}

func keyWrapInfo(roomID string, epoch int, deviceID string) []byte {
	return []byte(fmt.Sprintf("toget:room-key-wrap:v1|%s|%d|%s", roomID, epoch, deviceID))
}

func payloadAAD(roomID string, epoch int, purpose string) []byte {
	if purpose == "" {
		purpose = "event"
	}
	return []byte(fmt.Sprintf("toget:room-payload:v1|%s|%d|%s", roomID, epoch, purpose))
}

// WrapRoomKey encrypts a room key to a recipient device's X25519 public key.
// The format is version || ephemeral public key || nonce || ciphertext.
func WrapRoomKey(roomID string, epoch int, deviceID string, recipientPub, key []byte) ([]byte, error) {
	if len(recipientPub) != 32 {
		return nil, errors.New("recipient encryption key must be 32 bytes")
	}
	if len(key) != roomKeySize {
		return nil, errors.New("room key must be 32 bytes")
	}
	curve := ecdh.X25519()
	remote, err := curve.NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("parse recipient key: %w", err)
	}
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(remote)
	if err != nil {
		return nil, fmt.Errorf("derive room key secret: %w", err)
	}
	info := keyWrapInfo(roomID, epoch, deviceID)
	aead, err := deriveWrapCipher(shared, info)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate room key nonce: %w", err)
	}
	out := make([]byte, 0, 1+32+len(nonce)+len(key)+aead.Overhead())
	out = append(out, wrapVersion)
	out = append(out, ephemeral.PublicKey().Bytes()...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, key, info)
	return out, nil
}

// UnwrapRoomKey decrypts a wrapped room key with a device X25519 private key.
func UnwrapRoomKey(roomID string, epoch int, deviceID string, devicePriv, wrapped []byte) ([]byte, error) {
	if len(wrapped) < 1+32+chacha20poly1305.NonceSizeX {
		return nil, errors.New("wrapped room key is too short")
	}
	if wrapped[0] != wrapVersion {
		return nil, errors.New("unsupported room key wrap version")
	}
	curve := ecdh.X25519()
	ephemeralPub, err := curve.NewPublicKey(wrapped[1:33])
	if err != nil {
		return nil, fmt.Errorf("parse ephemeral key: %w", err)
	}
	priv, err := curve.NewPrivateKey(devicePriv)
	if err != nil {
		return nil, fmt.Errorf("parse device key: %w", err)
	}
	shared, err := priv.ECDH(ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("derive room key secret: %w", err)
	}
	info := keyWrapInfo(roomID, epoch, deviceID)
	aead, err := deriveWrapCipher(shared, info)
	if err != nil {
		return nil, err
	}
	nonce := wrapped[33 : 33+chacha20poly1305.NonceSizeX]
	key, err := aead.Open(nil, nonce, wrapped[33+chacha20poly1305.NonceSizeX:], info)
	if err != nil {
		return nil, errors.New("room key authentication failed")
	}
	if len(key) != roomKeySize {
		return nil, errors.New("unwrapped room key has the wrong size")
	}
	return key, nil
}

func deriveWrapCipher(shared, info []byte) (cipher.AEAD, error) {
	reader := hkdf.New(sha256.New, shared, nil, info)
	derived := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(reader, derived); err != nil {
		return nil, fmt.Errorf("derive room key: %w", err)
	}
	aead, err := chacha20poly1305.NewX(derived)
	if err != nil {
		return nil, fmt.Errorf("create room key cipher: %w", err)
	}
	return aead, nil
}

func sealRoomPayload(key, plaintext []byte, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create room cipher: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate room nonce: %w", err)
	}
	out := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(plaintext)+aead.Overhead())
	copy(out, nonce)
	out = aead.Seal(out, nonce, plaintext, aad)
	return []byte(sealedKeyPrefix + base64.RawStdEncoding.EncodeToString(out)), nil
}

func openRoomPayload(key, sealed []byte, aad []byte) ([]byte, error) {
	const prefix = sealedKeyPrefix
	if len(sealed) <= len(prefix) || string(sealed[:len(prefix)]) != prefix {
		return nil, errors.New("unsupported room payload")
	}
	raw, err := base64.RawStdEncoding.DecodeString(string(sealed[len(prefix):]))
	if err != nil {
		return nil, fmt.Errorf("decode room payload: %w", err)
	}
	if len(raw) < chacha20poly1305.NonceSizeX {
		return nil, errors.New("room payload is too short")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create room cipher: %w", err)
	}
	plaintext, err := aead.Open(nil, raw[:chacha20poly1305.NonceSizeX], raw[chacha20poly1305.NonceSizeX:], aad)
	if err != nil {
		return nil, errors.New("room payload authentication failed")
	}
	return plaintext, nil
}

// SealBlob encrypts a local blob (for example an attachment) under the room's
// current epoch key.
func (s *Service) SealBlob(ctx context.Context, roomID, purpose string, plaintext []byte) (int, []byte, error) {
	epoch, key, err := s.latestRoomKey(ctx, roomID)
	if err != nil {
		return 0, nil, err
	}
	sealed, err := sealRoomPayload(key, plaintext, payloadAAD(roomID, epoch, purpose))
	if err != nil {
		return 0, nil, err
	}
	return epoch, sealed, nil
}

// OpenBlob decrypts a blob produced by SealBlob.
func (s *Service) OpenBlob(ctx context.Context, roomID string, epoch int, purpose string, sealed []byte) ([]byte, error) {
	key, err := s.roomKey(ctx, roomID, epoch)
	if err != nil {
		return nil, err
	}
	return openRoomPayload(key, sealed, payloadAAD(roomID, epoch, purpose))
}
