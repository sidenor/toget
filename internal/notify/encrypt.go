package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const recordSize = 4096

var (
	hkdfInfoWebPush = []byte("WebPush: info\x00")
	hkdfInfoCEK     = []byte("Content-Encoding: aes128gcm\x00")
	hkdfInfoNonce   = []byte("Content-Encoding: nonce\x00")
)

// encryptPayload implements RFC 8291 aes128gcm for one push subscription.
func encryptPayload(p256dh, auth string, payload []byte) ([]byte, error) {
	if len(payload) > recordSize-17 {
		return nil, errors.New("notify: push payload is too large")
	}
	uaPublicBytes, err := base64.RawURLEncoding.DecodeString(p256dh)
	if err != nil {
		return nil, errors.New("notify: invalid p256dh key")
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("notify: invalid auth secret")
	}
	uaPublic, err := ecdh.P256().NewPublicKey(uaPublicBytes)
	if err != nil {
		return nil, errors.New("notify: invalid p256dh point")
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("notify: generate ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("notify: derive shared secret: %w", err)
	}
	asPublic := ephemeral.PublicKey().Bytes()

	keyInfo := append(append([]byte{}, hkdfInfoWebPush...), uaPublicBytes...)
	keyInfo = append(keyInfo, asPublic...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256New, shared, authSecret, keyInfo), ikm); err != nil {
		return nil, fmt.Errorf("notify: derive input key: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("notify: generate salt: %w", err)
	}
	cek := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256New, ikm, salt, hkdfInfoCEK), cek); err != nil {
		return nil, fmt.Errorf("notify: derive content key: %w", err)
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.New(sha256New, ikm, salt, hkdfInfoNonce), nonce); err != nil {
		return nil, fmt.Errorf("notify: derive nonce: %w", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// 0x02 is the last-record padding delimiter from RFC 8188.
	plaintext := append(append([]byte{}, payload...), 0x02)
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	body := make([]byte, 0, 16+4+1+len(asPublic)+len(ciphertext))
	body = append(body, salt...)
	var rs [4]byte
	binary.BigEndian.PutUint32(rs[:], recordSize)
	body = append(body, rs[:]...)
	body = append(body, byte(len(asPublic)))
	body = append(body, asPublic...)
	body = append(body, ciphertext...)
	return body, nil
}
