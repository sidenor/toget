// Package sealbox encrypts small payloads to an X25519 public key. It is used
// when one device hands private account material to another during pairing.
package sealbox

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	prefix       = "toget-sealed:v1:"
	sealVersion  = byte(1)
	maxSealedLen = 16 << 20
)

// Seal encrypts plaintext to a 32-byte X25519 public key. context binds the
// ciphertext to its purpose and must be reproduced when opening.
func Seal(recipientPublicKey, plaintext []byte, context string) ([]byte, error) {
	if len(recipientPublicKey) != 32 {
		return nil, errors.New("sealbox: recipient key must be 32 bytes")
	}
	if strings.TrimSpace(context) == "" {
		return nil, errors.New("sealbox: context is required")
	}
	curve := ecdh.X25519()
	recipient, err := curve.NewPublicKey(recipientPublicKey)
	if err != nil {
		return nil, fmt.Errorf("sealbox: parse recipient key: %w", err)
	}
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sealbox: generate ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("sealbox: derive shared secret: %w", err)
	}
	aead, err := derive(shared, context)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("sealbox: generate nonce: %w", err)
	}
	out := make([]byte, 0, 1+32+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, sealVersion)
	out = append(out, ephemeral.PublicKey().Bytes()...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, aad(context))
	return []byte(prefix + base64.RawStdEncoding.EncodeToString(out)), nil
}

// Open decrypts a payload produced by Seal with the recipient private key.
func Open(recipientPrivateKey, sealed []byte, context string) ([]byte, error) {
	if len(recipientPrivateKey) != 32 {
		return nil, errors.New("sealbox: recipient private key must be 32 bytes")
	}
	if !strings.HasPrefix(string(sealed), prefix) {
		return nil, errors.New("sealbox: unsupported sealed payload")
	}
	if len(sealed) > maxSealedLen {
		return nil, errors.New("sealbox: sealed payload is too large")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(string(sealed), prefix))
	if err != nil {
		return nil, fmt.Errorf("sealbox: decode payload: %w", err)
	}
	if len(raw) < 1+32+chacha20poly1305.NonceSizeX {
		return nil, errors.New("sealbox: sealed payload is too short")
	}
	if raw[0] != sealVersion {
		return nil, errors.New("sealbox: unsupported sealed payload version")
	}
	curve := ecdh.X25519()
	ephemeral, err := curve.NewPublicKey(raw[1:33])
	if err != nil {
		return nil, fmt.Errorf("sealbox: parse ephemeral key: %w", err)
	}
	private, err := curve.NewPrivateKey(recipientPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sealbox: parse recipient key: %w", err)
	}
	shared, err := private.ECDH(ephemeral)
	if err != nil {
		return nil, fmt.Errorf("sealbox: derive shared secret: %w", err)
	}
	aead, err := derive(shared, context)
	if err != nil {
		return nil, err
	}
	nonce := raw[33 : 33+chacha20poly1305.NonceSizeX]
	plaintext, err := aead.Open(nil, nonce, raw[33+chacha20poly1305.NonceSizeX:], aad(context))
	if err != nil {
		return nil, errors.New("sealbox: authentication failed")
	}
	return plaintext, nil
}

func derive(shared []byte, context string) (cipher.AEAD, error) {
	reader := hkdf.New(sha256.New, shared, nil, []byte("toget:sealbox:v1|"+context))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("sealbox: derive key: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("sealbox: create cipher: %w", err)
	}
	return aead, nil
}

func aad(context string) []byte {
	return []byte("toget:sealbox:v1|" + context)
}
