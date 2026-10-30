package session

import (
	"errors"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	m := New(time.Hour)
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }

	if err := m.Require(); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked error = %v", err)
	}
	expires := m.Unlock()
	if !expires.After(now) {
		t.Fatalf("unlock expiry = %v", expires)
	}
	if err := m.Require(); err != nil {
		t.Fatalf("unlock failed: %v", err)
	}
	now = now.Add(30 * time.Minute)
	m.Touch()
	now = now.Add(45 * time.Minute)
	if err := m.Require(); err != nil {
		t.Fatalf("touched session expired: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if err := m.Require(); !errors.Is(err, ErrLocked) {
		t.Fatalf("expired session error = %v", err)
	}
	m.Unlock()
	m.Lock()
	if err := m.Require(); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked session error = %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	m := New(time.Hour)
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }
	for i := 0; i < maxAttempts; i++ {
		if err := m.Allow("local"); err != nil {
			t.Fatalf("attempt %d blocked: %v", i, err)
		}
		m.Failed("local")
	}
	if err := m.Allow("local"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}
	now = now.Add(attemptWindow + time.Second)
	if err := m.Allow("local"); err != nil {
		t.Fatalf("rate limit did not expire: %v", err)
	}
	m.Failed("local")
	m.Succeeded("local")
	if err := m.Allow("local"); err != nil {
		t.Fatalf("success did not clear attempts: %v", err)
	}
}
