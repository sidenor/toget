package account_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type accountFixture struct {
	dir     string
	vault   *vault.Vault
	db      *storage.DB
	service *account.Service
}

func openAccountFixture(t *testing.T, dir string) *accountFixture {
	t.Helper()
	v, err := vault.Load(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &accountFixture{dir: dir, vault: v, db: db, service: account.New(db, v)}
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	f := openAccountFixture(t, t.TempDir())
	if _, err := f.service.Create(context.Background(), "Aiden", "oldpass123", "test-device"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRecoveryCredentialResetsPasswordAndPreservesData(t *testing.T) {
	ctx := context.Background()
	f := newAccountFixture(t)
	credential, content, err := f.service.CreateRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(credential.Filename(), "toget-recovery-") ||
		!strings.HasSuffix(credential.Filename(), ".togetkey") {
		t.Fatalf("filename = %q", credential.Filename())
	}
	status, err := f.service.Recovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.CreatedAt == nil || status.DeviceID == "" {
		t.Fatalf("status = %#v", status)
	}
	parsed, key, err := account.ParseRecoveryCredential(content)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AccountID == "" || len(key) != vault.RecoveryKeySize {
		t.Fatalf("parsed = %#v, key %d bytes", parsed, len(key))
	}
	info, err := os.Stat(filepath.Join(f.dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("vault mode = %o", info.Mode().Perm())
	}

	// Simulate a daemon restart: the vault is locked and only the recovery
	// file can unlock the old data.
	restarted := openAccountFixture(t, f.dir)
	if restarted.vault.Ready() {
		t.Fatal("vault unlocked without a password")
	}
	if _, _, err := restarted.service.CreateRecovery(ctx); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("create while locked error = %v, want ErrLocked", err)
	}
	reset, err := restarted.service.ResetPasswordWithRecovery(ctx, content, "newpass456")
	if err != nil {
		t.Fatal(err)
	}
	if !account.VerifyPassword(reset.Account.PasswordHash, "newpass456") {
		t.Fatal("new password did not verify after reset")
	}
	if !restarted.vault.Ready() {
		t.Fatal("vault is locked after recovery reset")
	}
	if _, err := restarted.service.UnsealPrivateKey(reset.Account); err != nil {
		t.Fatalf("sealed account key did not survive reset: %v", err)
	}

	after := openAccountFixture(t, f.dir)
	if _, err := after.service.Login(ctx, "oldpass123"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := after.service.Login(ctx, "newpass456"); err != nil {
		t.Fatal(err)
	}
	if _, err := after.service.UnsealPrivateKey(reset.Account); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCredentialRejectsTamperingAndOtherAccounts(t *testing.T) {
	ctx := context.Background()
	f := newAccountFixture(t)
	credential, content, err := f.service.CreateRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.Replace(string(content), credential.Key[:8], flipBase64(credential.Key[:8]), 1))
	if _, _, err := account.ParseRecoveryCredential(tampered); !errors.Is(err, account.ErrInvalidRecoveryCredential) {
		t.Fatalf("tampered parse error = %v", err)
	}
	if _, err := f.service.ResetPasswordWithRecovery(ctx, tampered, "newpass456"); !errors.Is(err, account.ErrInvalidRecoveryCredential) {
		t.Fatalf("tampered reset error = %v", err)
	}

	other := newAccountFixture(t)
	if _, err := other.service.ResetPasswordWithRecovery(ctx, content, "newpass456"); !errors.Is(err, account.ErrInvalidRecoveryCredential) {
		t.Fatalf("other account reset error = %v", err)
	}
	if _, err := f.service.ResetPasswordWithRecovery(ctx, []byte("not json"), "newpass456"); !errors.Is(err, account.ErrInvalidRecoveryCredential) {
		t.Fatalf("malformed reset error = %v", err)
	}
}

func flipBase64(value string) string {
	if value == "" {
		return "A"
	}
	if value[0] == 'A' {
		return "B" + value[1:]
	}
	return "A" + value[1:]
}

func TestChangePasswordPreservesRecovery(t *testing.T) {
	ctx := context.Background()
	f := newAccountFixture(t)
	_, content, err := f.service.CreateRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ChangePassword(ctx, "wrongpass", "newpass456"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("wrong current password error = %v", err)
	}
	if _, err := f.service.ChangePassword(ctx, "oldpass123", "short"); !errors.Is(err, account.ErrInvalidPassword) {
		t.Fatalf("short new password error = %v", err)
	}
	if _, err := f.service.ChangePassword(ctx, "oldpass123", "newpass456"); err != nil {
		t.Fatal(err)
	}
	restarted := openAccountFixture(t, f.dir)
	if _, err := restarted.service.Login(ctx, "oldpass123"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v", err)
	}
	if _, err := restarted.service.Login(ctx, "newpass456"); err != nil {
		t.Fatal(err)
	}
	restarted.service.LockVault()
	if _, err := restarted.service.ResetPasswordWithRecovery(ctx, content, "thirdpass789"); err != nil {
		t.Fatal(err)
	}
}

func TestResetPasswordUnlockedPreservesDataAndRecovery(t *testing.T) {
	ctx := context.Background()
	f := newAccountFixture(t)
	_, content, err := f.service.CreateRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.vault.Seal([]byte("history"), "test-history")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ResetPasswordUnlocked(ctx, "short"); !errors.Is(err, account.ErrInvalidPassword) {
		t.Fatalf("short password error = %v", err)
	}
	if _, err := f.service.ResetPasswordUnlocked(ctx, "newpass456"); err != nil {
		t.Fatal(err)
	}
	f.service.LockVault()
	if _, err := f.service.Login(ctx, "oldpass123"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v", err)
	}
	if _, err := f.service.Login(ctx, "newpass456"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.Open(sealed, "test-history"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ResetPasswordWithRecovery(ctx, content, "thirdpass789"); err != nil {
		t.Fatal(err)
	}
}

func TestResetPasswordUnlockedRequiresUnlockedVault(t *testing.T) {
	ctx := context.Background()
	f := newAccountFixture(t)
	f.service.LockVault()
	if _, err := f.service.ResetPasswordUnlocked(ctx, "newpass456"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("locked reset error = %v, want ErrLocked", err)
	}
}

func TestRemoveRecovery(t *testing.T) {
	ctx := context.Background()
	f := newAccountFixture(t)
	if err := f.service.RemoveRecovery(ctx); !errors.Is(err, account.ErrRecoveryUnavailable) {
		t.Fatalf("remove without recovery error = %v", err)
	}
	if _, _, err := f.service.CreateRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RemoveRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Recovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatal("recovery is still enabled")
	}
}

func TestRecoveryStatusWithoutAccount(t *testing.T) {
	f := openAccountFixture(t, t.TempDir())
	if _, err := f.service.Recovery(context.Background()); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("status error = %v", err)
	}
}
