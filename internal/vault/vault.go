// Package vault encrypts local secrets and message payloads at rest.
//
// The vault key is a random 32-byte key stored in a file readable only by the
// current user. It protects copied database files and backups. It is not a
// defense against an attacker who can read the whole data directory; OS
// keychain integration and full-database encryption are later milestones.
package vault

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keySize    = chacha20poly1305.KeySize
	nonceSize  = chacha20poly1305.NonceSizeX
	sealPrefix = "enc:v1:"
)

// Vault seals and opens local values.
type Vault struct {
	key []byte
}

// LoadOrCreate reads the key at path, creating it with owner-only permissions
// when it does not exist.
func LoadOrCreate(path string) (*Vault, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != keySize {
			return nil, fmt.Errorf("vault key has %d bytes, want %d", len(key), keySize)
		}
		return &Vault{key: key}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read vault key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create vault directory: %w", err)
	}
	key = make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreate(path)
		}
		return nil, fmt.Errorf("create vault key: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, fmt.Errorf("write vault key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close vault key: %w", err)
	}
	return &Vault{key: key}, nil
}

// Seal encrypts plaintext and returns a self-describing string.
func (v *Vault) Seal(plaintext []byte, aad string) (string, error) {
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return "", fmt.Errorf("create vault cipher: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate vault nonce: %w", err)
	}
	out := make([]byte, nonceSize, nonceSize+len(plaintext)+aead.Overhead())
	copy(out, nonce)
	out = aead.Seal(out, nonce, plaintext, []byte(aad))
	return sealPrefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func (v *Vault) Open(sealed string, aad string) ([]byte, error) {
	if !strings.HasPrefix(sealed, sealPrefix) {
		return nil, errors.New("vault: unsupported value")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil {
		return nil, fmt.Errorf("vault: decode: %w", err)
	}
	if len(raw) < nonceSize {
		return nil, errors.New("vault: value is too short")
	}
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return nil, fmt.Errorf("vault: create cipher: %w", err)
	}
	plaintext, err := aead.Open(nil, raw[:nonceSize], raw[nonceSize:], []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("vault: authentication failed: %w", err)
	}
	return plaintext, nil
}

// SealBytes is a small convenience for binary values.
func (v *Vault) SealBytes(plaintext []byte, aad string) ([]byte, error) {
	sealed, err := v.Seal(plaintext, aad)
	if err != nil {
		return nil, err
	}
	return []byte(sealed), nil
}

// OpenBytes is a small convenience for binary values.
func (v *Vault) OpenBytes(sealed []byte, aad string) ([]byte, error) {
	return v.Open(string(sealed), aad)
}
