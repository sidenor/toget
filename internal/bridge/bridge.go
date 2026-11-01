// Package bridge issues and verifies the short-lived pairing codes and
// long-lived browser tokens that let the official website manage Agents on a
// local Toget node.
//
// A pairing code is shown only by the local daemon. Redeeming it requires
// local access to the code and produces a bearer token bound to one browser
// origin. The token is stored as a SHA-256 hash; the local database never
// contains a usable copy.
package bridge

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// CodeTTL bounds how long a pairing code can be redeemed.
	CodeTTL = 5 * time.Minute
	// TokenTTL is the default lifetime of a browser token.
	TokenTTL = 180 * 24 * time.Hour
	// MaxPairFailures blocks pairing after repeated wrong guesses.
	MaxPairFailures = 20
	// pairFailureWindow is the window used for MaxPairFailures.
	pairFailureWindow = 5 * time.Minute
	// pairBlockDuration is how long pairing stays disabled after abuse.
	pairBlockDuration = 5 * time.Minute
	// maxTokens bounds the stored browser tokens.
	maxTokens = 32
)

var (
	// ErrCodeInvalid means the pairing code is unknown, expired, or used.
	ErrCodeInvalid = errors.New("pairing code is invalid or expired")
	// ErrTokenInvalid means the browser token is unknown or expired.
	ErrTokenInvalid = errors.New("browser token is invalid or expired")
	// ErrOriginMismatch means the token belongs to a different website origin.
	ErrOriginMismatch = errors.New("browser token belongs to a different origin")
	// ErrTooManyTokens means the token list is full.
	ErrTooManyTokens = errors.New("too many paired browsers; revoke one first")
)

// Token is the public description of one paired browser. Hash is never
// written to a client response; it is persisted so the daemon can verify a
// presented token without keeping the token itself.
type Token struct {
	ID         string     `json:"id"`
	Hash       string     `json:"hash"`
	Origin     string     `json:"origin"`
	AccountID  string     `json:"account_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

// Public is the client-safe view of a token.
type Public struct {
	ID         string     `json:"id"`
	Origin     string     `json:"origin"`
	AccountID  string     `json:"account_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

// Manager owns pairing codes and browser tokens for one local node.
type Manager struct {
	path   string
	mu     sync.Mutex
	tokens []Token
	codes  map[string]pendingCode
	now    func() time.Time
	rand   io.Reader

	failures     int
	firstFailure time.Time
	blockedUntil time.Time
}

type pendingCode struct {
	hash     [32]byte
	expires  time.Time
	attempts int
}

type stored struct {
	Tokens []Token `json:"tokens"`
}

// New opens the token file at path and creates a manager. A missing file is
// not an error. The file is created with mode 0600 on first write.
func New(path string) (*Manager, error) {
	m := &Manager{path: path, codes: map[string]pendingCode{}, now: time.Now, rand: rand.Reader}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, fmt.Errorf("bridge: read tokens: %w", err)
	}
	if len(data) == 0 {
		return m, nil
	}
	var file stored
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("bridge: parse tokens: %w", err)
	}
	now := m.now()
	for _, token := range file.Tokens {
		if token.ID == "" || token.Hash == "" || !token.ExpiresAt.After(now) {
			continue
		}
		m.tokens = append(m.tokens, token)
	}
	return m, nil
}

// CreateCode returns a fresh six-digit pairing code and its expiry. Generating
// a new code invalidates the previous one.
func (m *Manager) CreateCode() (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	code, err := randomDigits(m.rand, 6)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := m.now().UTC().Add(CodeTTL)
	m.codes = map[string]pendingCode{
		code: {hash: sha256.Sum256([]byte(code)), expires: expires},
	}
	return code, expires, nil
}

// Pair redeems a code and returns a bearer token for origin. The code is
// single-use. accountID is recorded for display and diagnostics.
func (m *Manager) Pair(code, origin, accountID string) (string, Public, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if now.Before(m.blockedUntil) {
		return "", Public{}, ErrCodeInvalid
	}
	entry, ok := m.codes[code]
	if !ok {
		m.failLocked(now)
		return "", Public{}, ErrCodeInvalid
	}
	if !now.Before(entry.expires) {
		delete(m.codes, code)
		m.failLocked(now)
		return "", Public{}, ErrCodeInvalid
	}
	digest := hashString(code)
	if subtle.ConstantTimeCompare(entry.hash[:], digest[:]) != 1 {
		m.failLocked(now)
		return "", Public{}, ErrCodeInvalid
	}
	// Valid code: consume it before doing anything that can fail.
	delete(m.codes, code)
	m.failures = 0

	now = now.UTC()
	pruned := m.tokens[:0]
	for _, token := range m.tokens {
		if token.ExpiresAt.After(now) {
			pruned = append(pruned, token)
		}
	}
	m.tokens = pruned
	if len(m.tokens) >= maxTokens {
		return "", Public{}, ErrTooManyTokens
	}

	raw := make([]byte, 32)
	if _, err := io.ReadFull(m.rand, raw); err != nil {
		return "", Public{}, fmt.Errorf("bridge: generate token: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	id, err := randomHex(m.rand, 8)
	if err != nil {
		return "", Public{}, err
	}
	tokenDigest := hashString(secret)
	token := Token{
		ID: id, Hash: hex.EncodeToString(tokenDigest[:]), Origin: origin,
		AccountID: accountID, CreatedAt: now, ExpiresAt: now.Add(TokenTTL),
	}
	m.tokens = append(m.tokens, token)
	if err := m.saveLocked(); err != nil {
		m.tokens = m.tokens[:len(m.tokens)-1]
		return "", Public{}, err
	}
	return secret, public(token), nil
}

// Validate checks a presented token and origin. It returns the stored token
// metadata on success.
func (m *Manager) Validate(secret, origin string) (*Public, error) {
	if secret == "" {
		return nil, ErrTokenInvalid
	}
	digest := hashString(secret)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	for i := range m.tokens {
		token := &m.tokens[i]
		storedHash, err := hex.DecodeString(token.Hash)
		if err != nil || subtle.ConstantTimeCompare(storedHash, digest[:]) != 1 {
			continue
		}
		if !token.ExpiresAt.After(now) {
			return nil, ErrTokenInvalid
		}
		if token.Origin != origin {
			return nil, ErrOriginMismatch
		}
		// Persist last use at most once per minute to avoid a disk write on
		// every API call.
		if token.LastUsedAt == nil || now.Sub(*token.LastUsedAt) > time.Minute {
			token.LastUsedAt = &now
			if err := m.saveLocked(); err != nil {
				// A failed timestamp write must not invalidate a valid token.
				_ = err
			}
		}
		view := public(*token)
		return &view, nil
	}
	return nil, ErrTokenInvalid
}

// List returns paired browsers without their hashes.
func (m *Manager) List() []Public {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]Public, 0, len(m.tokens))
	for _, token := range m.tokens {
		if token.ExpiresAt.After(now) {
			out = append(out, public(token))
		}
	}
	return out
}

// Revoke removes one paired browser.
func (m *Manager) Revoke(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tokens {
		if m.tokens[i].ID != id {
			continue
		}
		m.tokens = append(m.tokens[:i], m.tokens[i+1:]...)
		return m.saveLocked()
	}
	return ErrTokenInvalid
}

// RevokeAll removes every paired browser.
func (m *Manager) RevokeAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = nil
	return m.saveLocked()
}

func (m *Manager) failLocked(now time.Time) {
	if m.failures == 0 || now.Sub(m.firstFailure) > pairFailureWindow {
		m.failures = 0
		m.firstFailure = now
	}
	m.failures++
	if m.failures >= MaxPairFailures {
		m.blockedUntil = now.Add(pairBlockDuration)
		m.failures = 0
	}
}

func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(stored{Tokens: m.tokens}, "", "  ")
	if err != nil {
		return fmt.Errorf("bridge: encode tokens: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("bridge: create token directory: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("bridge: write tokens: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("bridge: secure tokens: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("bridge: replace tokens: %w", err)
	}
	return nil
}

func public(token Token) Public {
	return Public{
		ID: token.ID, Origin: token.Origin, AccountID: token.AccountID,
		CreatedAt: token.CreatedAt, LastUsedAt: token.LastUsedAt, ExpiresAt: token.ExpiresAt,
	}
}

func hashString(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

func randomDigits(reader io.Reader, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	out := make([]byte, n)
	buf := make([]byte, n)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf("bridge: random code: %w", err)
	}
	for i := range out {
		out[i] = '0' + buf[i]%10
	}
	return string(out), nil
}

func randomHex(reader io.Reader, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf("bridge: random id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
