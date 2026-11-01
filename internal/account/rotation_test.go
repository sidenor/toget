package account_test

import (
	"bytes"
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

func newRotationService(t *testing.T, dir string) (*account.Service, *storage.DB, *vault.Vault) {
	t.Helper()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return account.New(db, v), db, v
}

func importSecondDevice(t *testing.T, dir string, source *account.Snapshot, accountKey ed25519.PrivateKey, name string) (*account.Service, *account.Snapshot, ed25519.PublicKey, []byte) {
	t.Helper()
	svc, _, _ := newRotationService(t, dir)
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, encPriv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	deviceID := identity.NewID()
	cert, err := identity.SignDeviceCertificate(deviceID, source.Account.ID, devicePub, encPub, accountKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.Import(context.Background(), account.ImportSpec{
		AccountID: source.Account.ID, AccountCode: source.Account.Code, Name: source.Account.Name,
		AccountPublicKey: source.Account.PublicKey, AccountPrivateKey: accountKey,
		PasswordHash: source.Account.PasswordHash, CreatedAt: source.Account.CreatedAt,
		OriginServerID: source.Account.OriginServerID, KeyGeneration: source.Account.KeyGeneration,
		KeyRotations: source.Account.KeyRotations,
		Device: account.Device{
			ID: deviceID, AccountID: source.Account.ID, Name: name,
			PublicKey: devicePub, PrivateKey: devicePriv,
			EncryptionPublicKey: encPub, EncryptionPrivateKey: encPriv,
			Certificate: string(cert), Role: "node", CreatedAt: time.Now().UTC(),
		},
		Siblings: []account.Device{*source.Device},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, snapshot, encPub, encPriv
}

func TestRotateKeyReSignsActiveDevices(t *testing.T) {
	ctx := context.Background()
	source, _, _ := newRotationService(t, t.TempDir())
	created, err := source.Create(ctx, "Aiden", "testpass123", "one")
	if err != nil {
		t.Fatal(err)
	}
	oldAccountPub := append(ed25519.PublicKey(nil), created.Account.PublicKey...)
	result, err := source.RotateKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Generation != 2 || result.Devices != 1 {
		t.Fatalf("rotation result = %#v", result)
	}
	rotated, err := source.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(rotated.Account.PublicKey, oldAccountPub) {
		t.Fatal("account public key did not change")
	}
	if rotated.Account.KeyGeneration != 2 || len(rotated.Account.KeyRotations) != 1 {
		t.Fatalf("generation/history = %d/%d", rotated.Account.KeyGeneration, len(rotated.Account.KeyRotations))
	}
	if _, err := identity.VerifyDeviceCertificate([]byte(rotated.Device.Certificate), oldAccountPub); err == nil {
		t.Fatal("device certificate still verifies under the old account key")
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(rotated.Device.Certificate), ed25519.PublicKey(rotated.Account.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if cert.DeviceID != rotated.Device.ID {
		t.Fatalf("re-signed certificate names %q", cert.DeviceID)
	}
	key, err := source.UnsealPrivateKey(rotated.Account)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key.Public().(ed25519.PublicKey), rotated.Account.PublicKey) {
		t.Fatal("stored private key does not match the rotated public key")
	}
}

func TestInstallAccountKeyFromHostLogin(t *testing.T) {
	ctx := context.Background()
	source, _, _ := newRotationService(t, t.TempDir())
	created, err := source.Create(ctx, "Aiden", "testpass123", "one")
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := source.UnsealPrivateKey(created.Account)
	if err != nil {
		t.Fatal(err)
	}
	newPub, newKey, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := identity.SignAccountKeyRotation(created.Account.ID, 2,
		ed25519.PublicKey(created.Account.PublicKey), oldKey, newPub, newKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, wrongKey, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.InstallAccountKey(ctx, 2, []string{statement}, wrongKey); !errors.Is(err, account.ErrKeyRotationEnvelope) {
		t.Fatalf("mismatched key error = %v", err)
	}
	installed, changed, err := source.InstallAccountKey(ctx, 2, []string{statement}, newKey)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || installed.Account.KeyGeneration != 2 || !bytes.Equal(installed.Account.PublicKey, newPub) {
		t.Fatalf("installed account = %+v, changed = %v", installed.Account, changed)
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(installed.Device.Certificate), newPub)
	if err != nil {
		t.Fatal(err)
	}
	if cert.DeviceID != installed.Device.ID {
		t.Fatalf("certificate device = %q", cert.DeviceID)
	}
	stored, err := source.UnsealPrivateKey(installed.Account)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, newKey) {
		t.Fatal("installed private key does not match")
	}
	if _, changed, err := source.InstallAccountKey(ctx, 2, []string{statement}, newKey); err != nil || changed {
		t.Fatalf("reinstall changed = %v, error = %v", changed, err)
	}
}

func TestApplyAccountKeyRotationsInstallsSealedKey(t *testing.T) {
	ctx := context.Background()
	source, _, _ := newRotationService(t, t.TempDir())
	created, err := source.Create(ctx, "Aiden", "testpass123", "one")
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := source.UnsealPrivateKey(created.Account)
	if err != nil {
		t.Fatal(err)
	}
	other, otherSnapshot, _, _ := importSecondDevice(t, t.TempDir(), created, oldKey, "two")
	if _, err := source.RotateKey(ctx); err != nil {
		t.Fatal(err)
	}
	rotated, err := source.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	generation, envelope, err := source.SealAccountKeyFor(ctx, otherSnapshot.Device.ID, otherSnapshot.Device.EncryptionPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 2 {
		t.Fatalf("envelope generation = %d", generation)
	}
	// An envelope addressed to a different device must not decrypt the key.
	_, wrongEnvelope, err := source.SealAccountKeyFor(ctx, "someone-else", otherSnapshot.Device.EncryptionPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.ApplyAccountKeyRotations(ctx, rotated.Account.KeyRotations, wrongEnvelope); !errors.Is(err, account.ErrKeyRotationEnvelope) {
		t.Fatalf("wrong envelope error = %v", err)
	}
	applied, changed, err := other.ApplyAccountKeyRotations(ctx, rotated.Account.KeyRotations, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || applied.Account.KeyGeneration != 2 ||
		!bytes.Equal(applied.Account.PublicKey, rotated.Account.PublicKey) {
		t.Fatalf("applied snapshot = %#v", applied.Account)
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(applied.Device.Certificate), ed25519.PublicKey(applied.Account.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if cert.DeviceID != otherSnapshot.Device.ID {
		t.Fatalf("local certificate names %q", cert.DeviceID)
	}
	// Applying the same chain again is a no-op.
	if _, changed, err := other.ApplyAccountKeyRotations(ctx, rotated.Account.KeyRotations, envelope); err != nil || changed {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	// An incremental chain still applies, and the local history keeps the
	// earlier statement so this device can vouch for the full chain later.
	if _, err := source.RotateKey(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := source.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Account.KeyRotations) != 2 {
		t.Fatalf("source chain length = %d", len(second.Account.KeyRotations))
	}
	_, thirdEnvelope, err := source.SealAccountKeyFor(ctx, otherSnapshot.Device.ID, otherSnapshot.Device.EncryptionPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	incremental := second.Account.KeyRotations[len(second.Account.KeyRotations)-1:]
	appliedThird, changed, err := other.ApplyAccountKeyRotations(ctx, incremental, thirdEnvelope)
	if err != nil || !changed {
		t.Fatalf("incremental apply changed=%v err=%v", changed, err)
	}
	if appliedThird.Account.KeyGeneration != 3 || len(appliedThird.Account.KeyRotations) != 2 {
		t.Fatalf("incremental chain state = %d/%d",
			appliedThird.Account.KeyGeneration, len(appliedThird.Account.KeyRotations))
	}
}

func TestApplyRejectsForkedAndGappedChains(t *testing.T) {
	ctx := context.Background()
	source, _, _ := newRotationService(t, t.TempDir())
	created, err := source.Create(ctx, "Aiden", "testpass123", "one")
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := source.UnsealPrivateKey(created.Account)
	if err != nil {
		t.Fatal(err)
	}
	other, otherSnapshot, _, _ := importSecondDevice(t, t.TempDir(), created, oldKey, "two")
	if _, err := source.RotateKey(ctx); err != nil {
		t.Fatal(err)
	}
	rotated, err := source.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, envelope, err := source.SealAccountKeyFor(ctx, otherSnapshot.Device.ID, otherSnapshot.Device.EncryptionPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := other.ApplyAccountKeyRotations(ctx, rotated.Account.KeyRotations, envelope); err != nil || !changed {
		t.Fatalf("valid chain changed=%v err=%v", changed, err)
	}
	// A forked declaration at generation 2 signed by the same old key.
	forkPub, forkPriv, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	fork, err := identity.SignAccountKeyRotation(created.Account.ID, 2,
		ed25519.PublicKey(created.Account.PublicKey), oldKey, forkPub, forkPriv, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.ApplyAccountKeyRotations(ctx, []string{fork}, envelope); !errors.Is(err, account.ErrKeyRotationConflict) {
		t.Fatalf("fork error = %v", err)
	}
	// A later chain that skips generation 3 must fail closed.
	currentKey, err := source.UnsealPrivateKey(rotated.Account)
	if err != nil {
		t.Fatal(err)
	}
	gapPub, gapPriv, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	gap, err := identity.SignAccountKeyRotation(created.Account.ID, 4,
		ed25519.PublicKey(rotated.Account.PublicKey), currentKey, gapPub, gapPriv, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.ApplyAccountKeyRotations(ctx, []string{gap}, envelope); !errors.Is(err, account.ErrKeyRotationGap) {
		t.Fatalf("gap error = %v", err)
	}
}

func TestImportInitializesPasswordVault(t *testing.T) {
	ctx := context.Background()
	source, _, _ := newRotationService(t, t.TempDir())
	created, err := source.Create(ctx, "Aiden", "testpass123", "host")
	if err != nil {
		t.Fatal(err)
	}
	accountKey, err := source.UnsealPrivateKey(created.Account)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	v, err := vault.Load(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := account.New(db, v)
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, encPriv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	deviceID := identity.NewID()
	cert, err := identity.SignDeviceCertificate(deviceID, created.Account.ID, devicePub, encPub, accountKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	spec := account.ImportSpec{
		AccountID: created.Account.ID, AccountCode: created.Account.Code, Name: created.Account.Name,
		AccountPublicKey: created.Account.PublicKey, AccountPrivateKey: accountKey,
		PasswordHash: created.Account.PasswordHash, CreatedAt: created.Account.CreatedAt,
		OriginServerID: created.Account.OriginServerID,
		Device: account.Device{
			ID: deviceID, AccountID: created.Account.ID, Name: "laptop",
			PublicKey: devicePub, PrivateKey: devicePriv,
			EncryptionPublicKey: encPub, EncryptionPrivateKey: encPriv,
			Certificate: string(cert), Role: "node", CreatedAt: time.Now().UTC(),
		},
		Siblings: []account.Device{*created.Device},
	}
	if _, err := svc.Import(ctx, spec); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("import without a password error = %v, want ErrLocked", err)
	}
	spec.Password = "wrong-password"
	if _, err := svc.Import(ctx, spec); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("import with a wrong password error = %v", err)
	}
	spec.Password = "testpass123"
	imported, err := svc.Import(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Ready() {
		t.Fatal("vault is not ready after a password import")
	}
	key, err := svc.UnsealPrivateKey(imported.Account)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key.Public().(ed25519.PublicKey), imported.Account.PublicKey) {
		t.Fatal("imported account key does not match")
	}
	reloaded, err := vault.Load(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Ready() {
		t.Fatal("reloaded vault unlocked without the account password")
	}
	if err := reloaded.Unlock("testpass123"); err != nil {
		t.Fatalf("account password did not unlock the imported vault: %v", err)
	}
}

func TestAccountLoginUnlocksPasswordVaultAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")
	v, err := vault.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := account.New(db, v)
	if _, err := svc.Create(ctx, "Aiden", "testpass123", "one"); err != nil {
		t.Fatal(err)
	}
	if !v.Ready() {
		t.Fatal("vault is not ready after account creation")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	restartedVault, err := vault.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if restartedVault.Ready() {
		t.Fatal("vault unlocked at rest")
	}
	db, err = storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	restarted := account.New(db, restartedVault)
	if _, err := restarted.Login(ctx, "wrong-password"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("wrong login error = %v", err)
	}
	if restartedVault.Ready() {
		t.Fatal("a failed login unlocked the vault")
	}
	snapshot, err := restarted.Login(ctx, "testpass123")
	if err != nil {
		t.Fatal(err)
	}
	if !restartedVault.Ready() {
		t.Fatal("a correct login did not unlock the vault")
	}
	if _, err := restarted.UnsealPrivateKey(snapshot.Account); err != nil {
		t.Fatalf("unlock account key: %v", err)
	}
}
