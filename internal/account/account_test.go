package account_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func TestCreateAndLoadAccount(t *testing.T) {
	dir := t.TempDir()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := account.New(db, v)

	snapshot, err := svc.Create(context.Background(), "Aiden", "testpass123", "test-device")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Account.ID) != 14 {
		t.Fatalf("account ID %q is not 14 digits", snapshot.Account.ID)
	}
	if snapshot.Account.ID[:8] != snapshot.Server.Code {
		t.Fatalf("account ID prefix %q does not match server code %q", snapshot.Account.ID[:8], snapshot.Server.Code)
	}
	if snapshot.Account.Name != "Aiden" {
		t.Fatalf("name = %q", snapshot.Account.Name)
	}
	if !account.VerifyPassword(snapshot.Account.PasswordHash, "testpass123") {
		t.Fatal("correct password was rejected")
	}
	if account.VerifyPassword(snapshot.Account.PasswordHash, "wrong-password") {
		t.Fatal("wrong password was accepted")
	}

	cert, err := identity.VerifyDeviceCertificate([]byte(snapshot.Device.Certificate), ed25519.PublicKey(snapshot.Account.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if cert.DeviceID != snapshot.Device.ID || cert.AccountID != snapshot.Account.ID {
		t.Fatalf("unexpected device certificate: %#v", cert)
	}

	key, err := svc.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != ed25519.PrivateKeySize {
		t.Fatalf("account key has %d bytes", len(key))
	}
}

func TestCreateRejectsSecondAccount(t *testing.T) {
	dir := t.TempDir()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := account.New(db, v)

	if _, err := svc.Create(context.Background(), "Aiden", "testpass123", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), "Aiden", "testpass123", "two"); !errors.Is(err, account.ErrAlreadyExists) {
		t.Fatalf("second create error = %v, want ErrAlreadyExists", err)
	}
}

func TestAccountPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")
	dbPath := filepath.Join(dir, "toget.db")

	v, err := vault.LoadOrCreate(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := account.New(db, v)
	created, err := svc.Create(context.Background(), "Aiden", "testpass123", "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	v2, err := vault.LoadOrCreate(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	db2, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	loaded, err := account.New(db2, v2).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Account.ID != created.Account.ID {
		t.Fatalf("account ID changed: %q != %q", loaded.Account.ID, created.Account.ID)
	}
	if loaded.Device.ID != created.Device.ID {
		t.Fatalf("device ID changed: %q != %q", loaded.Device.ID, created.Device.ID)
	}
}
