package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/backup"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func newKeyedActor(t *testing.T, accountID, deviceID, name string) room.Actor {
	t.Helper()
	_, signing, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	accountPub, _, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, _, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	return room.Actor{AccountID: accountID, DeviceID: deviceID, DisplayName: name,
		SigningKey: signing, AccountPublicKey: accountPub, EncryptionPublicKey: encPub}
}

func TestBackupCreateAndRestore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	paths, err := config.Resolve(home)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.LoadOrCreate(paths.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	rooms := room.New(db, v, room.WithDataDir(paths.Home))
	actor := newKeyedActor(t, "account-1", "device-1", "Aiden")
	created, _, err := rooms.CreateRoom(ctx, "Backed up", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.PostMessage(ctx, created.ID, actor, room.MessagePostedPayload{Text: "survives"}); err != nil {
		t.Fatal(err)
	}
	data := []byte("attachment survives too")
	attachment, err := rooms.UploadAttachment(ctx, created.ID, actor, "note.txt", "text/plain", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.PostMessage(ctx, created.ID, actor, room.MessagePostedPayload{Text: "with file", Attachments: []string{attachment.ID}}); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "node.toget-backup")
	manifest, err := backup.Create(ctx, db, paths, archive)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Format != backup.Format || manifest.SchemaVersion != storage.CurrentSchemaVersion || len(manifest.Files) < 3 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if got, err := backup.ReadManifest(archive); err != nil || got.Format != backup.Format {
		t.Fatalf("read manifest = %#v, %v", got, err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("archive mode = %o", info.Mode().Perm())
	}
	db.Close()

	home2 := t.TempDir()
	paths2, err := config.Resolve(home2)
	if err != nil {
		t.Fatal(err)
	}
	restored, rollback, err := backup.Restore(ctx, paths2, archive)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Format != backup.Format || rollback == "" {
		t.Fatalf("restore = %#v, %q", restored, rollback)
	}
	db2, err := storage.Open(paths2.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	v2, err := vault.LoadOrCreate(paths2.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	rooms2 := room.New(db2, v2, room.WithDataDir(paths2.Home))
	roomsList, err := rooms2.ListRooms(ctx)
	if err != nil || len(roomsList) != 1 || roomsList[0].Name != "Backed up" {
		t.Fatalf("restored rooms = %#v, %v", roomsList, err)
	}
	events, err := rooms2.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		var payload room.MessagePostedPayload
		if event.Type == room.EventMessagePosted && json.Unmarshal(event.Payload, &payload) == nil && payload.Text == "survives" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored events = %#v", events)
	}
	actor2 := newKeyedActor(t, "account-1", "device-1", "Aiden")
	_, plaintext, err := rooms2.OpenAttachment(ctx, attachment.ID, actor2)
	if err != nil || string(plaintext) != string(data) {
		t.Fatalf("restored attachment = %q, %v", plaintext, err)
	}
	if _, err := os.Stat(filepath.Join(rollback, "toget.db")); !os.IsNotExist(err) {
		// The destination was empty, so no old database was moved.
		t.Logf("rollback dir: %s", rollback)
	}
}

func TestBackupRestorePreservesRecoveryReset(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	paths, err := config.Resolve(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Load(paths.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	accounts := account.New(db, v)
	if _, err := accounts.Create(ctx, "Aiden", "oldpass123", "backup-device"); err != nil {
		t.Fatal(err)
	}
	_, recovery, err := accounts.CreateRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("sealed history"), "backup-test")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "recovery.toget-backup")
	if _, err := backup.Create(ctx, db, paths, archive); err != nil {
		t.Fatal(err)
	}
	db.Close()

	home2 := t.TempDir()
	paths2, err := config.Resolve(home2)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backup.Restore(ctx, paths2, archive); err != nil {
		t.Fatal(err)
	}
	db2, err := storage.Open(paths2.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	v2, err := vault.Load(paths2.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	accounts2 := account.New(db2, v2)
	if v2.Ready() {
		t.Fatal("restored vault was already unlocked")
	}
	if _, err := accounts2.ResetPasswordWithRecovery(ctx, []byte(recovery), "newpass456"); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Open(sealed, "backup-test"); err != nil {
		t.Fatalf("sealed history after restore: %v", err)
	}
	accounts2.LockVault()
	if _, err := accounts2.Login(ctx, "oldpass123"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("old password after restore = %v", err)
	}
	if _, err := accounts2.Login(ctx, "newpass456"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsUnsafeAndInvalidArchives(t *testing.T) {
	ctx := context.Background()
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := backup.Restore(ctx, paths, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing archive restored")
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.ReadManifest(bad); err == nil {
		t.Fatal("invalid archive read")
	}
	if _, err := backup.Create(ctx, nil, paths, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("nil database backup succeeded")
	}
}

func makeTestBackup(t *testing.T) (config.Paths, string) {
	t.Helper()
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := vault.LoadOrCreate(paths.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	rooms := room.New(db, v, room.WithDataDir(paths.Home))
	actor := newKeyedActor(t, "account-verify", "device-verify", "Verifier")
	created, _, err := rooms.CreateRoom(context.Background(), "Verify room", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.PostMessage(context.Background(), created.ID, actor, room.MessagePostedPayload{Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "source.toget-backup")
	if _, err := backup.Create(context.Background(), db, paths, archive); err != nil {
		t.Fatal(err)
	}
	return paths, archive
}

func TestVerifyArchive(t *testing.T) {
	_, archive := makeTestBackup(t)
	manifest, err := backup.Verify(archive)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != storage.CurrentSchemaVersion || len(manifest.Files) < 2 {
		t.Fatalf("manifest = %#v", manifest)
	}

	truncated := filepath.Join(t.TempDir(), "truncated.toget-backup")
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(truncated, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Verify(truncated); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Fatalf("truncated verify = %v", err)
	}

	unsafe := filepath.Join(t.TempDir(), "unsafe.toget-backup")
	sum := sha256.Sum256([]byte("x"))
	evil := backup.Manifest{
		Format: backup.Format, Version: backup.Version, SchemaVersion: storage.CurrentSchemaVersion,
		Files: []backup.File{{Path: "../evil", Size: 1, SHA256: hex.EncodeToString(sum[:])}},
	}
	out, err := os.Create(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	entry, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(entry).Encode(evil); err != nil {
		t.Fatal(err)
	}
	body, err := zw.Create("data/../evil")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := body.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Verify(unsafe); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Fatalf("unsafe verify = %v", err)
	}
}

func TestStageUploadAndApply(t *testing.T) {
	_, archive := makeTestBackup(t)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}

	// Stage on a fresh destination.
	dest, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(dest); err != nil {
		t.Fatal(err)
	}
	service := backup.NewService(nil, dest)
	upload, err := service.SaveUpload(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if upload.Size != int64(len(data)) || upload.Manifest.Format != backup.Format {
		t.Fatalf("upload = %#v", upload)
	}
	pending, err := service.StageUpload(upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Manifest.SchemaVersion != storage.CurrentSchemaVersion {
		t.Fatalf("staged = %#v", pending)
	}
	if _, err := service.UploadPath(upload.ID); !errors.Is(err, backup.ErrBackupNotFound) {
		t.Fatalf("upload should be gone after staging: %v", err)
	}
	if current, err := service.Pending(); err != nil || !current.StagedAt.Equal(pending.StagedAt) {
		t.Fatalf("pending = %#v, %v", current, err)
	}
	manifest, _, err := backup.ApplyPending(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest == nil || manifest.Format != backup.Format {
		t.Fatalf("applied manifest = %#v", manifest)
	}
	if _, err := service.Pending(); !errors.Is(err, backup.ErrNoPendingRestore) {
		t.Fatalf("pending after apply = %v", err)
	}
	db, err := storage.Open(dest.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rooms, err := room.New(db, nil).ListRooms(context.Background())
	if err != nil || len(rooms) != 1 || rooms[0].Name != "Verify room" {
		t.Fatalf("restored rooms = %#v, %v", rooms, err)
	}
}

func TestStageBackupAndCancel(t *testing.T) {
	source, archive := makeTestBackup(t)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest.BackupsDir, "local.toget-backup"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	service := backup.NewService(nil, dest)
	list, err := service.List()
	if err != nil || len(list) != 1 || list[0].Name != "local.toget-backup" {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if _, err := service.StageBackup("local.toget-backup"); err != nil {
		t.Fatal(err)
	}
	if err := service.CancelPending(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pending(); !errors.Is(err, backup.ErrNoPendingRestore) {
		t.Fatalf("pending after cancel = %v", err)
	}
	_ = source
}

func TestApplyPendingQuarantinesCorruptArchive(t *testing.T) {
	_, archive := makeTestBackup(t)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := backup.NewService(nil, dest)
	upload, err := service.SaveUpload(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageUpload(upload.ID); err != nil {
		t.Fatal(err)
	}
	// Corrupt the staged copy after validation, simulating disk damage.
	pendingPath := filepath.Join(dest.RestoreDir, "pending.toget-backup")
	if err := os.WriteFile(pendingPath, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backup.ApplyPending(context.Background(), dest); err == nil {
		t.Fatal("corrupt pending restore was applied")
	}
	if _, err := service.Pending(); !errors.Is(err, backup.ErrNoPendingRestore) {
		t.Fatalf("corrupt pending was not quarantined: %v", err)
	}
	entries, err := os.ReadDir(dest.RestoreDir)
	if err != nil {
		t.Fatal(err)
	}
	var quarantined bool
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "failed-") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatalf("no quarantined archive in %#v", entries)
	}
}
