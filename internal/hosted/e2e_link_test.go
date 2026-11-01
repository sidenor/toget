package hosted_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/hostedlink"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type localStack struct {
	accounts *account.Service
	db       *storage.DB
	vault    *vault.Vault
	dir      string
}

func newLocalStack(t *testing.T) *localStack {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Production starts with a missing key file and wraps the first vault key
	// with the account password. LoadOrCreate would instead seed a legacy
	// plaintext key that cannot be restored after Lock.
	v, err := vault.Load(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &localStack{accounts: account.New(db, v), db: db, vault: v, dir: dir}
}

func newLocalAccountService(t *testing.T) *account.Service {
	return newLocalStack(t).accounts
}

func TestHostedLinkRegisterLoginPasswordAndRecovery(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	const password = "correct horse battery staple"
	const newPassword = "a different password entirely"

	firstStack := newLocalStack(t)
	first := firstStack.accounts
	sessions := hosted.NewSessionCache()
	link := hostedlink.New(first, sessions)
	snapshot, registered, err := link.Register(ctx, hostedlink.RegisterSpec{
		HostURL: h.client.PinnedURL(), Name: "Aiden", Email: "aiden@example.com",
		Password: password, DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Account.ID != registered.AccountID || snapshot.Account.HostURL != registered.HostURL {
		t.Fatalf("local account = %+v", snapshot.Account)
	}
	if !strings.HasSuffix(snapshot.Account.ID, snapshot.Account.Code) {
		t.Fatalf("account code %q does not match id %q", snapshot.Account.Code, snapshot.Account.ID)
	}
	if !account.VerifyPassword(snapshot.Account.PasswordHash, password) {
		t.Fatal("local password verifier does not accept the account password")
	}
	if !first.VaultReady() {
		t.Fatal("registration did not unlock the local vault")
	}
	if registered.RecoveryCredentialRaw == nil {
		t.Fatal("registration did not return a recovery file")
	}
	if token, ok := sessions.Get(snapshot.Account.ID); !ok || token == "" {
		t.Fatal("registration session was not cached")
	}

	// A second device adopts the account with the password and publishes its
	// fresh device identity to the host.
	secondStack := newLocalStack(t)
	second := secondStack.accounts
	link2 := hostedlink.New(second, hosted.NewSessionCache())
	secondSnapshot, login, warnings, err := link2.Login(ctx, hostedlink.LoginSpec{
		HostURL: h.client.PinnedURL(), AccountID: snapshot.Account.ID,
		Password: password, DeviceName: "Desktop",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range warnings {
		t.Logf("login warning: %s", warning)
	}
	if secondSnapshot.Account.ID != snapshot.Account.ID || secondSnapshot.Device.Name != "Desktop" {
		t.Fatalf("second device account = %+v", secondSnapshot)
	}
	if len(login.AccountPrivateKey) == 0 {
		t.Fatal("login did not return the account key")
	}
	hostLogin, err := h.client.Login(ctx, snapshot.Account.ID, password)
	if err != nil {
		t.Fatal(err)
	}
	published := false
	for _, device := range hostLogin.Devices {
		if device.ID == secondSnapshot.Device.ID {
			published = true
		}
	}
	if !published {
		t.Fatal("second device was not published to the host")
	}

	// The first device refreshes its public device set from the host without
	// using the account password: it signs a short-lived device session.
	paths, err := config.Resolve(firstStack.dir)
	if err != nil {
		t.Fatal(err)
	}
	rooms := room.New(firstStack.db, firstStack.vault)
	nodeService, err := node.New(node.Options{
		Accounts: first, Rooms: rooms, Paths: paths, Listen: "127.0.0.1:0",
		HostSessions: sessions,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if nodeService == nil {
		t.Fatal("node service was not created")
	}
	syncResult, err := nodeService.SyncAccountNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !syncResult.Host || !syncResult.HostSynced {
		t.Fatalf("host sync result = %+v, warnings = %v", syncResult, syncResult.Warnings)
	}
	throttled, err := nodeService.SyncAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if throttled.Host {
		t.Fatal("background account sync ignored the official-host interval")
	}
	// An expired or stale bearer session is dropped and replaced by a fresh
	// device-signed session, without asking for the account password.
	sessions.Set(snapshot.Account.ID, "stale-token", time.Now().Add(time.Hour))
	retried, err := nodeService.SyncAccountNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !retried.HostSynced {
		t.Fatalf("session retry did not sync: %+v, warnings = %v", retried, retried.Warnings)
	}
	firstDevices, err := first.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	learned := false
	for _, device := range firstDevices {
		if device.ID == secondSnapshot.Device.ID {
			learned = true
		}
	}
	if !learned {
		t.Fatal("first device did not learn the second device from the host")
	}

	// Changing the official password updates the host and this device's vault.
	if err := link2.ChangePassword(ctx, password, newPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Login(ctx, snapshot.Account.ID, password); err == nil {
		t.Fatal("old password still works on the host")
	}
	if _, err := h.client.Login(ctx, snapshot.Account.ID, newPassword); err != nil {
		t.Fatalf("new password does not work on the host: %v", err)
	}
	second.LockVault()
	if _, err := second.Login(ctx, newPassword); err != nil {
		t.Fatalf("new password does not unlock the local vault: %v", err)
	}

	// Official recovery rotation is independent of the local recovery file.
	credential, raw, err := link2.RotateRecovery(ctx, newPassword)
	if err != nil {
		t.Fatal(err)
	}
	if credential == nil || len(raw) == 0 || !strings.HasPrefix(credential.Filename(), "toget-account-recovery-") {
		t.Fatalf("rotated recovery = %+v", credential)
	}

	// A hosted key rotation publishes a key bundle encrypted under MK, so a
	// later password login on another device can recover the new key.
	rotatedKey, warnings, err := link2.RotateAccountKey(ctx, newPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range warnings {
		t.Logf("rotation warning: %s", warning)
	}
	if rotatedKey.Generation != 2 {
		t.Fatalf("rotated generation = %d", rotatedKey.Generation)
	}
	hostAfterRotation, err := h.client.Login(ctx, snapshot.Account.ID, newPassword)
	if err != nil {
		t.Fatal(err)
	}
	if hostAfterRotation.KeyGeneration != 2 {
		t.Fatalf("host key generation = %d", hostAfterRotation.KeyGeneration)
	}
	if !bytes.Equal(hostAfterRotation.AccountPrivateKey.Public().(ed25519.PublicKey), rotatedKey.PublicKey) {
		t.Fatal("host did not store the rotated account key")
	}
}
