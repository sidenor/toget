package vault

import (
	"crypto/rand"
	"encoding/base64"
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
	i := len(sealed) / 2
	replacement := byte('A')
	if sealed[i] == 'A' {
		replacement = 'B'
	}
	tampered := sealed[:i] + string(replacement) + sealed[i+1:]
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

func lowerArgonForTest(t *testing.T) {
	t.Helper()
	memory, iterations, parallelism := argonMemory, argonIterations, argonParallelism
	argonMemory, argonIterations, argonParallelism = 8*1024, 1, 1
	t.Cleanup(func() {
		argonMemory, argonIterations, argonParallelism = memory, iterations, parallelism
	})
}

func TestPasswordWrappedVaultLifecycle(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.Ready() {
		t.Fatal("a missing key file produced an unlocked vault")
	}
	if _, err := v.Seal([]byte("secret"), "aad"); err != ErrLocked {
		t.Fatalf("seal while locked error = %v, want ErrLocked", err)
	}
	if err := v.Initialize("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if !v.Ready() {
		t.Fatal("vault is not ready after Initialize")
	}
	sealed, err := v.Seal([]byte("account key"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), wrapPrefix) {
		t.Fatalf("vault file is not password-wrapped: %q", raw[:min(16, len(raw))])
	}

	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Ready() {
		t.Fatal("a restarted vault unlocked without the password")
	}
	if err := restarted.Unlock("wrong password"); err != ErrInvalidPassword {
		t.Fatalf("wrong password error = %v, want ErrInvalidPassword", err)
	}
	if err := restarted.Unlock("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	plaintext, err := restarted.Open(sealed, "aad")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "account key" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	restarted.Lock()
	if restarted.Ready() {
		t.Fatal("vault stayed unlocked after Lock")
	}
	if _, err := restarted.Open(sealed, "aad"); err != ErrLocked {
		t.Fatalf("open after lock error = %v, want ErrLocked", err)
	}
}

func TestLegacyVaultWrapsAndMigrates(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	legacy, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Legacy() {
		t.Fatal("LoadOrCreate did not create a legacy key")
	}
	sealed, err := legacy.Seal([]byte("old room key"), "room")
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.WrapWithPassword("migration password"); err != nil {
		t.Fatal(err)
	}
	if legacy.Legacy() {
		t.Fatal("vault still reports legacy after wrapping")
	}

	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Ready() {
		t.Fatal("migrated vault unlocked without the password")
	}
	if err := restarted.Unlock("migration password"); err != nil {
		t.Fatal(err)
	}
	plaintext, err := restarted.Open(sealed, "room")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "old room key" {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestUnlockWithoutKeyFile(t *testing.T) {
	v, err := Load(filepath.Join(t.TempDir(), "missing.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock("anything"); err != ErrNoKey {
		t.Fatalf("unlock without key error = %v, want ErrNoKey", err)
	}
	if err := v.Initialize(""); err == nil {
		t.Fatal("Initialize accepted an empty password")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func testRecoveryKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, RecoveryKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRecoveryKeyResetsPasswordWhileLocked(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize("old password"); err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("history"), "history")
	if err != nil {
		t.Fatal(err)
	}
	recovery := testRecoveryKey(t)
	created, err := v.SetRecoveryKey(recovery, "account-device")
	if err != nil {
		t.Fatal(err)
	}
	if created.IsZero() || !v.RecoveryEnabled() || !v.RecoveryCreatedAt().Equal(created) {
		t.Fatalf("recovery metadata = %v / %v", created, v.RecoveryCreatedAt())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(string(raw), wrapPrefix))
	if err != nil || !strings.Contains(string(payload), `"recovery"`) {
		t.Fatalf("vault file does not contain a recovery wrap: %s", raw)
	}

	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Ready() {
		t.Fatal("restarted vault unlocked without a password")
	}
	if err := restarted.ResetPasswordWithRecoveryKey(testRecoveryKey(t), "account-device", "new password"); err != ErrInvalidRecoveryKey {
		t.Fatalf("wrong recovery key error = %v, want ErrInvalidRecoveryKey", err)
	}
	if err := restarted.ResetPasswordWithRecoveryKey(recovery, "wrong-aad", "new password"); err != ErrInvalidRecoveryKey {
		t.Fatalf("wrong AAD error = %v, want ErrInvalidRecoveryKey", err)
	}
	if err := restarted.ResetPasswordWithRecoveryKey(recovery, "account-device", "new password"); err != nil {
		t.Fatal(err)
	}
	if !restarted.Ready() {
		t.Fatal("reset did not leave the vault unlocked")
	}
	plaintext, err := restarted.Open(sealed, "history")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "history" {
		t.Fatalf("plaintext = %q", plaintext)
	}

	after, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := after.Unlock("old password"); err != ErrInvalidPassword {
		t.Fatalf("old password error = %v, want ErrInvalidPassword", err)
	}
	if err := after.Unlock("new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := after.Open(sealed, "history"); err != nil {
		t.Fatal(err)
	}
	if !after.RecoveryEnabled() {
		t.Fatal("password reset removed the recovery wrap")
	}
	if err := after.ResetPasswordWithRecoveryKey(recovery, "account-device", "third password"); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordPreservesRecoveryKey(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize("old password"); err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("secret"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	recovery := testRecoveryKey(t)
	if _, err := v.SetRecoveryKey(recovery, "bound"); err != nil {
		t.Fatal(err)
	}
	if err := v.ChangePassword("wrong password", "new password"); err != ErrInvalidPassword {
		t.Fatalf("wrong old password error = %v, want ErrInvalidPassword", err)
	}
	if err := v.ChangePassword("old password", "new password"); err != nil {
		t.Fatal(err)
	}
	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Unlock("old password"); err != ErrInvalidPassword {
		t.Fatalf("old password error = %v", err)
	}
	if err := restarted.Unlock("new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Open(sealed, "aad"); err != nil {
		t.Fatal(err)
	}
	if !restarted.RecoveryEnabled() {
		t.Fatal("password change removed the recovery wrap")
	}
}

func TestResetPasswordUnlockedPreservesDataAndRecovery(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize("old password"); err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("secret"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	recovery := testRecoveryKey(t)
	if _, err := v.SetRecoveryKey(recovery, "bound"); err != nil {
		t.Fatal(err)
	}
	if err := v.ResetPasswordUnlocked("new password"); err != nil {
		t.Fatal(err)
	}
	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Unlock("old password"); err != ErrInvalidPassword {
		t.Fatalf("old password error = %v", err)
	}
	if err := restarted.Unlock("new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Open(sealed, "aad"); err != nil {
		t.Fatal(err)
	}
	if !restarted.RecoveryEnabled() {
		t.Fatal("local reset removed the recovery wrap")
	}
}

func TestResetPasswordUnlockedRequiresUnlockedVault(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize("old password"); err != nil {
		t.Fatal(err)
	}
	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ResetPasswordUnlocked("new password"); err != ErrLocked {
		t.Fatalf("locked reset error = %v, want ErrLocked", err)
	}
}

func TestRemoveRecoveryKey(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize("password"); err != nil {
		t.Fatal(err)
	}
	recovery := testRecoveryKey(t)
	if _, err := v.SetRecoveryKey(recovery, "aad"); err != nil {
		t.Fatal(err)
	}
	if err := v.RemoveRecoveryKey(); err != nil {
		t.Fatal(err)
	}
	if v.RecoveryEnabled() {
		t.Fatal("recovery is still enabled")
	}
	if err := v.RemoveRecoveryKey(); err != ErrRecoveryNotConfigured {
		t.Fatalf("second remove error = %v", err)
	}
	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RecoveryEnabled() {
		t.Fatal("removed recovery wrap persisted")
	}
	if err := restarted.ResetPasswordWithRecoveryKey(recovery, "aad", "new"); err != ErrRecoveryNotConfigured {
		t.Fatalf("reset without recovery error = %v", err)
	}
}

func TestRecoveryKeyRequiresUnlockedWrappedVault(t *testing.T) {
	lowerArgonForTest(t)
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetRecoveryKey(testRecoveryKey(t), "aad"); err == nil {
		t.Fatal("set recovery on a missing vault key")
	}
	if err := v.Initialize("password"); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if _, err := v.SetRecoveryKey(testRecoveryKey(t), "aad"); err != ErrLocked {
		t.Fatalf("set recovery while locked error = %v, want ErrLocked", err)
	}
}
