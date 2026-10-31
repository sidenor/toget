package backup_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
