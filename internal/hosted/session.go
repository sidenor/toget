package hosted

import (
	"strings"
	"sync"
	"time"
)

// SessionCache keeps short-lived host bearer tokens in process memory. Tokens
// are never written to disk: a restart simply obtains a new session with the
// device key, or asks the user to sign in again after a password reset.
type SessionCache struct {
	mu       sync.Mutex
	sessions map[string]sessionEntry
	now      func() time.Time
}

type sessionEntry struct {
	token   string
	expires time.Time
}

// NewSessionCache creates an empty session cache.
func NewSessionCache() *SessionCache {
	return &SessionCache{sessions: make(map[string]sessionEntry), now: time.Now}
}

// Set records a token for one account.
func (c *SessionCache) Set(accountID, token string, expires time.Time) {
	if c == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(token) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions == nil {
		c.sessions = make(map[string]sessionEntry)
	}
	c.sessions[accountID] = sessionEntry{token: token, expires: expires}
}

// Get returns a token that is still comfortably valid.
func (c *SessionCache) Get(accountID string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.sessions[accountID]
	if !ok {
		return "", false
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if entry.expires.IsZero() || !now().Add(30*time.Second).Before(entry.expires) {
		delete(c.sessions, accountID)
		return "", false
	}
	return entry.token, true
}

// Clear forgets one account's token after the host rejects it.
func (c *SessionCache) Clear(accountID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, accountID)
}
