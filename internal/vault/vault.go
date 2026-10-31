// Package vault encrypts local secrets and message payloads at rest.
//
// A random 32-byte master key protects every sealed value. On disk that key is
// wrapped with a password-derived KEK (Argon2id + XChaCha20-Poly1305), so a
// copied data directory or backup does not reveal the key material without the
// account password. Older installations stored the master key in plaintext;
// those files are read for compatibility and wrapped on the next successful
// unlock.
package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keySize    = chacha20poly1305.KeySize
	nonceSize  = chacha20poly1305.NonceSizeX
	sealPrefix = "enc:v1:"
	wrapPrefix = "toget-vault:v2:"
	wrapAAD    = "toget:vault-key-wrap:v1"
)

// ErrLocked means the vault key is not in memory yet.
var ErrLocked = errors.New("vault is locked")

// ErrInvalidPassword means the wrapped key did not authenticate.
var ErrInvalidPassword = errors.New("vault password is incorrect")

// ErrNoKey means no vault key file exists.
var ErrNoKey = errors.New("vault key does not exist")

// ErrRecoveryNotConfigured means no recovery key is wrapped in the key file.
var ErrRecoveryNotConfigured = errors.New("vault recovery key is not configured")

// ErrInvalidRecoveryKey means the recovery key did not unwrap the master key.
var ErrInvalidRecoveryKey = errors.New("vault recovery key is invalid")

// RecoveryKeySize is the raw size of a recovery key.
const RecoveryKeySize = 32

// Argon2id parameters for the password-derived KEK. Tests may lower these.
var (
	argonMemory      uint32 = 64 * 1024
	argonIterations  uint32 = 3
	argonParallelism uint8  = 2
	argonSaltLength         = 16
)

// wrappedKey is the on-disk password-wrapped master key.
type wrappedKey struct {
	Version     int          `json:"version"`
	KDF         string       `json:"kdf"`
	Memory      uint32       `json:"m"`
	Iterations  uint32       `json:"t"`
	Parallelism uint8        `json:"p"`
	Salt        string       `json:"salt"`
	Nonce       string       `json:"nonce"`
	Ciphertext  string       `json:"ciphertext"`
	Recovery    *recoveryKey `json:"recovery,omitempty"`
}

// recoveryKey wraps the master key with a high-entropy recovery key. It is
// independent of the password wrap, so a forgotten password can be reset
// without losing sealed data.
type recoveryKey struct {
	Version    int       `json:"version"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
	CreatedAt  time.Time `json:"created_at"`
}

// Vault seals and opens local values. Its master key can be locked and
// unlocked while the process keeps running.
type Vault struct {
	mu      sync.RWMutex
	key     []byte
	path    string
	wrapped bool
	legacy  bool
	wrap    *wrappedKey
}

// Load reads the key file without unlocking a password-wrapped vault. A legacy
// plaintext key is accepted unlocked so existing installations can migrate on
// their next login. A missing file returns a locked, empty vault.
func Load(path string) (*Vault, error) {
	v := &Vault{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read vault key: %w", err)
	}
	if err := v.loadData(data); err != nil {
		return nil, err
	}
	return v, nil
}

// LoadOrCreate reads the key at path, creating a legacy plaintext key when it
// does not exist. New code should use Load plus Initialize so the key is
// wrapped from the start; LoadOrCreate keeps test fixtures and recovery tools
// compatible.
func LoadOrCreate(path string) (*Vault, error) {
	v, err := Load(path)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == keySize {
		return v, nil
	}
	if v.wrapped {
		return v, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create vault directory: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	if err := writeKeyFile(path, key); err != nil {
		return nil, err
	}
	v.key = key
	v.legacy = true
	return v, nil
}

// Initialize prepares the vault for a new account, or unlocks an existing
// wrapped vault. A legacy plaintext key is wrapped with the password.
func (v *Vault) Initialize(password string) error {
	if len(password) == 0 {
		return errors.New("vault password is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.path == "" {
		return errors.New("vault has no key path")
	}
	if v.wrapped {
		if len(v.key) == keySize {
			return nil
		}
		key, err := v.unwrap(password)
		if err != nil {
			return err
		}
		v.key = key
		return nil
	}
	if len(v.key) != keySize {
		key, err := v.readOrCreateKey()
		if err != nil {
			return err
		}
		v.key = key
	}
	return v.wrapLocked(password)
}

// Unlock decrypts a wrapped vault key or accepts an already-unlocked legacy
// key. It is safe to call when the vault is already unlocked.
func (v *Vault) Unlock(password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == keySize {
		return nil
	}
	if !v.wrapped {
		return ErrNoKey
	}
	key, err := v.unwrap(password)
	if err != nil {
		return err
	}
	v.key = key
	return nil
}

// WrapWithPassword wraps a legacy plaintext key. It is a no-op for a vault
// that is already wrapped and unlocked.
func (v *Vault) WrapWithPassword(password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.wrapped {
		if len(v.key) == keySize {
			return nil
		}
		key, err := v.unwrap(password)
		if err != nil {
			return err
		}
		v.key = key
		return nil
	}
	if len(v.key) != keySize {
		return ErrNoKey
	}
	return v.wrapLocked(password)
}

// Lock removes the master key from memory. Sealed values cannot be opened
// until Unlock succeeds again.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.key {
		v.key[i] = 0
	}
	v.key = nil
}

// Ready reports whether the master key is currently in memory.
func (v *Vault) Ready() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.key) == keySize
}

// Legacy reports whether the key file is a plaintext legacy key.
func (v *Vault) Legacy() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.legacy
}

// Seal encrypts plaintext and returns a self-describing string.
func (v *Vault) Seal(plaintext []byte, aad string) (string, error) {
	key, err := v.currentKey()
	if err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.NewX(key)
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
	key, err := v.currentKey()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
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

func (v *Vault) currentKey() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.key) != keySize {
		return nil, ErrLocked
	}
	return v.key, nil
}

func (v *Vault) loadData(data []byte) error {
	if strings.HasPrefix(string(data), wrapPrefix) {
		wrapped, err := decodeWrapped(data)
		if err != nil {
			return err
		}
		v.wrapped = true
		v.wrap = wrapped
		return nil
	}
	if len(data) != keySize {
		return fmt.Errorf("vault key has %d bytes, want %d", len(data), keySize)
	}
	v.key = append([]byte(nil), data...)
	v.legacy = true
	return nil
}

func decodeWrapped(data []byte) (*wrappedKey, error) {
	if !strings.HasPrefix(string(data), wrapPrefix) {
		return nil, errors.New("vault: key is not wrapped")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(string(data), wrapPrefix))
	if err != nil {
		return nil, fmt.Errorf("vault: decode wrapped key: %w", err)
	}
	var wrapped wrappedKey
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("vault: decode wrapped key: %w", err)
	}
	if wrapped.Version != 1 || wrapped.KDF != "argon2id" {
		return nil, errors.New("vault: unsupported wrapped key format")
	}
	return &wrapped, nil
}

func (v *Vault) readWrapped() (*wrappedKey, error) {
	data, err := os.ReadFile(v.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("read vault key: %w", err)
	}
	return decodeWrapped(data)
}

func (v *Vault) readOrCreateKey() ([]byte, error) {
	data, err := os.ReadFile(v.path)
	if err == nil {
		if len(data) != keySize {
			return nil, fmt.Errorf("vault key has %d bytes, want %d", len(data), keySize)
		}
		return append([]byte(nil), data...), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read vault key: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	return key, nil
}

// wrapLocked encrypts the in-memory key under the password and atomically
// replaces the key file. v.mu must be held.
func (v *Vault) wrapLocked(password string) error {
	if len(v.key) != keySize {
		return ErrNoKey
	}
	var recovery *recoveryKey
	if v.wrap != nil {
		recovery = v.wrap.Recovery
	}
	wrapped, err := v.buildWrappedLocked(v.key, password, recovery)
	if err != nil {
		return err
	}
	v.wrap = wrapped
	v.wrapped = true
	v.legacy = false
	return nil
}

// buildWrappedLocked derives a fresh password KEK, encrypts master, writes the
// key file, and returns the new on-disk envelope. v.mu must be held.
func (v *Vault) buildWrappedLocked(master []byte, password string, recovery *recoveryKey) (*wrappedKey, error) {
	if len(master) != keySize {
		return nil, ErrNoKey
	}
	if len(password) == 0 {
		return nil, errors.New("vault password is required")
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return nil, fmt.Errorf("create vault directory: %w", err)
	}
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate vault salt: %w", err)
	}
	kek := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, keySize)
	defer zero(kek)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("create vault key cipher: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate vault key nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, master, []byte(wrapAAD))
	wrapped := &wrappedKey{
		Version: 1, KDF: "argon2id", Memory: argonMemory, Iterations: argonIterations,
		Parallelism: argonParallelism, Salt: base64.RawStdEncoding.EncodeToString(salt),
		Nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
		Recovery:   recovery,
	}
	if err := v.writeWrappedLocked(wrapped); err != nil {
		return nil, err
	}
	return wrapped, nil
}

func (v *Vault) writeWrappedLocked(wrapped *wrappedKey) error {
	data, err := json.Marshal(wrapped)
	if err != nil {
		return fmt.Errorf("encode wrapped vault key: %w", err)
	}
	file := wrapPrefix + base64.RawStdEncoding.EncodeToString(data)
	return writeWrappedKeyFile(v.path, file)
}

// SetRecoveryKey wraps the in-memory master key with a high-entropy recovery
// key and records it in the key file. The vault must be unlocked and already
// password-wrapped. Replacing an existing recovery key voids the old one.
func (v *Vault) SetRecoveryKey(key []byte, aad string) (time.Time, error) {
	if len(key) != RecoveryKeySize {
		return time.Time{}, errors.New("vault: recovery key must be 32 bytes")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) != keySize {
		return time.Time{}, ErrLocked
	}
	if !v.wrapped || v.wrap == nil {
		return time.Time{}, errors.New("vault: password wrapping is not initialized")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return time.Time{}, fmt.Errorf("create recovery cipher: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return time.Time{}, fmt.Errorf("generate recovery nonce: %w", err)
	}
	created := time.Now().UTC().Truncate(time.Second)
	ciphertext := aead.Seal(nil, nonce, v.key, []byte(aad))
	updated := *v.wrap
	updated.Recovery = &recoveryKey{
		Version: 1, Nonce: base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext), CreatedAt: created,
	}
	if err := v.writeWrappedLocked(&updated); err != nil {
		return time.Time{}, err
	}
	v.wrap = &updated
	return created, nil
}

// RemoveRecoveryKey deletes the recovery wrap. The password wrap is unchanged.
func (v *Vault) RemoveRecoveryKey() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.wrap == nil || v.wrap.Recovery == nil {
		return ErrRecoveryNotConfigured
	}
	updated := *v.wrap
	updated.Recovery = nil
	if err := v.writeWrappedLocked(&updated); err != nil {
		return err
	}
	v.wrap = &updated
	return nil
}

// RecoveryEnabled reports whether a recovery key is wrapped in the key file.
func (v *Vault) RecoveryEnabled() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.wrap != nil && v.wrap.Recovery != nil
}

// RecoveryCreatedAt returns when the current recovery key was created.
func (v *Vault) RecoveryCreatedAt() time.Time {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.wrap == nil || v.wrap.Recovery == nil {
		return time.Time{}
	}
	return v.wrap.Recovery.CreatedAt
}

// ResetPasswordWithRecoveryKey re-wraps the master key under a new password
// using a recovery key. It works while the vault is locked, so it is the
// forgotten-password path. The recovery wrap is preserved.
func (v *Vault) ResetPasswordWithRecoveryKey(key []byte, aad, newPassword string) error {
	if len(key) != RecoveryKeySize {
		return ErrInvalidRecoveryKey
	}
	if len(newPassword) == 0 {
		return errors.New("vault password is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	wrapped, err := v.readWrapped()
	if err != nil {
		return err
	}
	if wrapped.Recovery == nil {
		return ErrRecoveryNotConfigured
	}
	master, err := openRecovery(wrapped.Recovery, key, aad)
	if err != nil {
		return err
	}
	updated, err := v.buildWrappedLocked(master, newPassword, wrapped.Recovery)
	if err != nil {
		zero(master)
		return err
	}
	zero(v.key)
	v.key = master
	v.wrap = updated
	v.wrapped = true
	v.legacy = false
	return nil
}

// ResetPasswordUnlocked re-wraps the in-memory master key under a new
// password without asking for the old one. It is the local-administrator
// reset path: possession of the unlocked process is already enough to read
// every sealed value. The recovery wrap, if any, is preserved.
func (v *Vault) ResetPasswordUnlocked(newPassword string) error {
	if len(newPassword) == 0 {
		return errors.New("vault password is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) != keySize {
		return ErrLocked
	}
	var recovery *recoveryKey
	if v.wrap != nil {
		recovery = v.wrap.Recovery
	}
	updated, err := v.buildWrappedLocked(v.key, newPassword, recovery)
	if err != nil {
		return err
	}
	v.wrap = updated
	v.wrapped = true
	v.legacy = false
	return nil
}

// ChangePassword re-wraps the master key from the old password to the new one.
// It works whether the vault is currently locked or unlocked. The recovery
// wrap, if any, is preserved.
func (v *Vault) ChangePassword(oldPassword, newPassword string) error {
	if len(newPassword) == 0 {
		return errors.New("vault password is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.wrapped {
		if len(v.key) != keySize {
			return ErrNoKey
		}
		updated, err := v.buildWrappedLocked(v.key, newPassword, nil)
		if err != nil {
			return err
		}
		v.wrap = updated
		v.wrapped = true
		v.legacy = false
		return nil
	}
	wrapped, err := v.readWrapped()
	if err != nil {
		return err
	}
	master, err := openPassword(wrapped, oldPassword)
	if err != nil {
		return err
	}
	updated, err := v.buildWrappedLocked(master, newPassword, wrapped.Recovery)
	if err != nil {
		zero(master)
		return err
	}
	zero(v.key)
	v.key = master
	v.wrap = updated
	return nil
}

// openPassword decrypts a password-wrapped master key.
func openPassword(wrapped *wrappedKey, password string) ([]byte, error) {
	salt, err := base64.RawStdEncoding.DecodeString(wrapped.Salt)
	if err != nil || len(salt) < 8 {
		return nil, errors.New("vault: wrapped key salt is invalid")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(wrapped.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return nil, errors.New("vault: wrapped key nonce is invalid")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(wrapped.Ciphertext)
	if err != nil || len(ciphertext) < keySize+16 {
		return nil, errors.New("vault: wrapped key ciphertext is invalid")
	}
	kek := argon2.IDKey([]byte(password), salt, wrapped.Iterations, wrapped.Memory, wrapped.Parallelism, keySize)
	defer zero(kek)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("vault: create key cipher: %w", err)
	}
	key, err := aead.Open(nil, nonce, ciphertext, []byte(wrapAAD))
	if err != nil {
		return nil, ErrInvalidPassword
	}
	if len(key) != keySize {
		return nil, errors.New("vault: unwrapped key has the wrong size")
	}
	return key, nil
}

// openRecovery decrypts a recovery-wrapped master key.
func openRecovery(wrapped *recoveryKey, key []byte, aad string) ([]byte, error) {
	if wrapped.Version != 1 {
		return nil, ErrInvalidRecoveryKey
	}
	nonce, err := base64.RawStdEncoding.DecodeString(wrapped.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return nil, ErrInvalidRecoveryKey
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(wrapped.Ciphertext)
	if err != nil || len(ciphertext) < keySize+16 {
		return nil, ErrInvalidRecoveryKey
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, ErrInvalidRecoveryKey
	}
	master, err := aead.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, ErrInvalidRecoveryKey
	}
	if len(master) != keySize {
		zero(master)
		return nil, ErrInvalidRecoveryKey
	}
	return master, nil
}

func (v *Vault) unwrap(password string) ([]byte, error) {
	wrapped, err := v.readWrapped()
	if err != nil {
		return nil, err
	}
	return openPassword(wrapped, password)
}

func writeKeyFile(path string, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create vault directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create vault key: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return fmt.Errorf("write vault key: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close vault key: %w", err)
	}
	return nil
}

func writeWrappedKeyFile(path, data string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create vault directory: %w", err)
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".vault-key-*")
	if err != nil {
		return fmt.Errorf("create vault key: %w", err)
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("set vault key permissions: %w", err)
	}
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		return fmt.Errorf("write vault key: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync vault key: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close vault key: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace vault key: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func zero(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
