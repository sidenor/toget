// Package session tracks whether the local node has been unlocked with the
// account password. The local API token authenticates a process; the session
// gate adds a human login step after a restart.
package session

import (
	"errors"
	"sync"
	"time"
)

// ErrLocked means the node has not been unlocked or the session expired.
var ErrLocked = errors.New("the local node is locked")

// ErrRateLimited means too many failed password attempts were made.
var ErrRateLimited = errors.New("too many failed login attempts")

const (
	// DefaultTTL is how long an unlocked session remains valid.
	DefaultTTL    = 12 * time.Hour
	maxAttempts   = 5
	attemptWindow = 5 * time.Minute
)

type attempt struct {
	count int
	until time.Time
}

// Manager holds the local unlock state. A node has one account, so one
// process-wide session is the right granularity.
type Manager struct {
	mu        sync.Mutex
	ttl       time.Duration
	now       func() time.Time
	unlocked  bool
	expiresAt time.Time
	attempts  map[string]attempt
}

// New creates a locked manager.
func New(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Manager{ttl: ttl, now: time.Now, attempts: make(map[string]attempt)}
}

// State reports the current state and expiry.
func (m *Manager) State() (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	return m.unlocked, m.expiresAt
}

// Unlock starts a new session.
func (m *Manager) Unlock() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unlocked = true
	m.expiresAt = m.now().UTC().Add(m.ttl)
	return m.expiresAt
}

// Lock ends the session.
func (m *Manager) Lock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unlocked = false
	m.expiresAt = time.Time{}
}

// Touch extends an active session after a request.
func (m *Manager) Touch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlocked {
		m.expiresAt = m.now().UTC().Add(m.ttl)
	}
}

// Require returns ErrLocked when the node is locked or the session expired.
func (m *Manager) Require() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	if !m.unlocked {
		return ErrLocked
	}
	return nil
}

// Allow records a login attempt for a key (usually the remote address).
func (m *Manager) Allow(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.attempts[key]
	if !ok || m.now().After(a.until) {
		return nil
	}
	if a.count >= maxAttempts {
		return ErrRateLimited
	}
	return nil
}

// Failed records a failed login attempt.
func (m *Manager) Failed(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	a := m.attempts[key]
	if a.until.IsZero() || now.After(a.until) {
		a = attempt{until: now.Add(attemptWindow)}
	}
	a.count++
	m.attempts[key] = a
}

// Succeeded clears failed attempts for a key.
func (m *Manager) Succeeded(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attempts, key)
}

func (m *Manager) expireLocked() {
	if m.unlocked && !m.expiresAt.IsZero() && !m.now().UTC().Before(m.expiresAt) {
		m.unlocked = false
		m.expiresAt = time.Time{}
	}
}
