package account_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	if snapshot.Account.ServerID != snapshot.Server.ID ||
		snapshot.Account.OriginServerID != snapshot.Server.ID {
		t.Fatalf("account servers = local %q / origin %q, want %q",
			snapshot.Account.ServerID, snapshot.Account.OriginServerID, snapshot.Server.ID)
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

func TestRefreshCertificatesRenewsNearExpiry(t *testing.T) {
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
	ctx := context.Background()
	snapshot, err := svc.Create(ctx, "Aiden", "testpass123", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	accountKey, err := svc.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		t.Fatal(err)
	}
	// A certificate that expires in 25 days is inside the renewal window.
	old, err := identity.SignDeviceCertificate(snapshot.Device.ID, snapshot.Account.ID,
		ed25519.PublicKey(snapshot.Device.PublicKey), snapshot.Device.EncryptionPublicKey,
		accountKey, time.Now().UTC().Add(-340*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Device.Certificate = string(old)
	if err := svc.UpdateDevice(ctx, snapshot.Device); err != nil {
		t.Fatal(err)
	}
	if err := svc.RefreshCertificates(ctx); err != nil {
		t.Fatal(err)
	}
	renewed, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Device.Certificate == string(old) {
		t.Fatal("near-expiry certificate was not renewed")
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(renewed.Device.Certificate), ed25519.PublicKey(snapshot.Account.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if !cert.ExpiresAt.After(time.Now().Add(300 * 24 * time.Hour)) {
		t.Fatalf("renewed certificate expires too soon: %s", cert.ExpiresAt)
	}
}

func TestImportPreservesOriginServerID(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceVault, err := vault.LoadOrCreate(filepath.Join(sourceDir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	sourceDB, err := storage.Open(filepath.Join(sourceDir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sourceDB.Close() })
	source, err := account.New(sourceDB, sourceVault).Create(ctx, "Aiden", "testpass123", "host")
	if err != nil {
		t.Fatal(err)
	}
	accountKey, err := account.New(sourceDB, sourceVault).UnsealPrivateKey(source.Account)
	if err != nil {
		t.Fatal(err)
	}
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptionPub, encryptionPriv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	deviceID := identity.NewID()
	cert, err := identity.SignDeviceCertificate(deviceID, source.Account.ID, devicePub,
		encryptionPub, accountKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	spec := account.ImportSpec{
		AccountID: source.Account.ID, AccountCode: source.Account.Code, Name: source.Account.Name,
		AccountPublicKey: source.Account.PublicKey, AccountPrivateKey: accountKey,
		PasswordHash: source.Account.PasswordHash, CreatedAt: source.Account.CreatedAt,
		OriginServerID: source.Server.ID,
		Device: account.Device{
			ID: deviceID, Name: "laptop", PublicKey: devicePub, PrivateKey: devicePriv,
			EncryptionPublicKey: encryptionPub, EncryptionPrivateKey: encryptionPriv,
			Certificate: string(cert), Role: "node",
		},
		Siblings: []account.Device{*source.Device},
	}

	destDir := t.TempDir()
	destVault, err := vault.LoadOrCreate(filepath.Join(destDir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	destDB, err := storage.Open(filepath.Join(destDir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { destDB.Close() })
	dest := account.New(destDB, destVault)
	bad := spec
	bad.OriginServerID = "not-a-server"
	if _, err := dest.Import(ctx, bad); !errors.Is(err, account.ErrInvalidImport) {
		t.Fatalf("invalid origin import error = %v, want ErrInvalidImport", err)
	}
	imported, err := dest.Import(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Account.ServerID != imported.Server.ID {
		t.Fatalf("local server id = %q, want %q", imported.Account.ServerID, imported.Server.ID)
	}
	if imported.Account.OriginServerID != source.Server.ID {
		t.Fatalf("origin server id = %q, want %q", imported.Account.OriginServerID, source.Server.ID)
	}
	loaded, err := dest.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Account.OriginServerID != source.Server.ID {
		t.Fatalf("persisted origin server id = %q, want %q", loaded.Account.OriginServerID, source.Server.ID)
	}
}
