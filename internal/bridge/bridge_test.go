package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridges.json")
	m, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	code, expires, err := m.CreateCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 || !expires.After(time.Now()) {
		t.Fatalf("code = %q, expires = %v", code, expires)
	}
	if _, _, err := m.Pair("000000", "https://toget.vu", "acct"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong code error = %v", err)
	}
	secret, record, err := m.Pair(code, "https://toget.vu", "acct")
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || record.Origin != "https://toget.vu" || record.AccountID != "acct" {
		t.Fatalf("record = %+v", record)
	}
	if _, _, err := m.Pair(code, "https://toget.vu", "acct"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatal("pairing code was reusable")
	}
	got, err := m.Validate(secret, "https://toget.vu")
	if err != nil || got.ID != record.ID {
		t.Fatalf("validate = %+v, %v", got, err)
	}
	if _, err := m.Validate(secret, "https://evil.example"); !errors.Is(err, ErrOriginMismatch) {
		t.Fatalf("wrong origin error = %v", err)
	}
	if _, err := m.Validate("nope", "https://toget.vu"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong token error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %o", info.Mode().Perm())
	}

	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Validate(secret, "https://toget.vu"); err != nil {
		t.Fatalf("token did not survive reload: %v", err)
	}
	if err := reloaded.Revoke(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Validate(secret, "https://toget.vu"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestCodeExpiryAndAttempts(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "bridges.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	code, _, err := m.CreateCode()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(CodeTTL + time.Second)
	if _, _, err := m.Pair(code, "https://toget.vu", ""); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired code error = %v", err)
	}

	code, _, err = m.CreateCode()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxPairFailures; i++ {
		if _, _, err := m.Pair("999999", "https://toget.vu", ""); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("attempt %d error = %v", i, err)
		}
	}
	if _, _, err := m.Pair(code, "https://toget.vu", ""); !errors.Is(err, ErrCodeInvalid) {
		t.Fatal("code survived too many wrong attempts")
	}
}

func TestNewCodeInvalidatesPrevious(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "bridges.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := m.CreateCode()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := m.CreateCode()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Skip("random codes collided; the behavior under test needs distinct values")
	}
	if _, _, err := m.Pair(first, "https://toget.vu", ""); !errors.Is(err, ErrCodeInvalid) {
		t.Fatal("previous code still worked")
	}
	if _, _, err := m.Pair(second, "https://toget.vu", ""); err != nil {
		t.Fatal(err)
	}
}
