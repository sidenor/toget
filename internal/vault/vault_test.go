package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	v, err := LoadOrCreate(filepath.Join(t.TempDir(), "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("a private key"), "test-aad")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, sealPrefix) {
		t.Fatalf("sealed value lacks prefix: %q", sealed)
	}
	plaintext, err := v.Open(sealed, "test-aad")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "a private key" {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestOpenRejectsWrongAADAndTampering(t *testing.T) {
	v, err := LoadOrCreate(filepath.Join(t.TempDir(), "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("secret"), "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(sealed, "wrong"); err == nil {
		t.Fatal("opened with the wrong AAD")
	}
	tampered := sealed[:len(sealed)-2] + "AA"
	if tampered == sealed {
		t.Fatal("test did not modify the sealed value")
	}
	if _, err := v.Open(tampered, "right"); err == nil {
		t.Fatal("opened a tampered value")
	}
}

func TestKeyFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.key")
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("vault key mode = %o, want 600", mode)
	}
}
