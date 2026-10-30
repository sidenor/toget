package room_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func newAttachmentService(t *testing.T) (*room.Service, *storage.DB, string) {
	t.Helper()
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
	return room.New(db, v, room.WithDataDir(dir)), db, dir
}

func TestAttachmentRoundTripAndBinding(t *testing.T) {
	ctx := context.Background()
	svc, _, dir := newAttachmentService(t)
	a := newKeyedActor(t, "account-1", "device-1", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "Files", "", a.actor)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("confidential bytes\n")
	attachment, err := svc.UploadAttachment(ctx, created.ID, a.actor, "../../report.txt", "text/plain", data)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "report.txt" || attachment.Size != int64(len(data)) || attachment.SHA256 == "" {
		t.Fatalf("attachment = %#v", attachment)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "attachments", created.ID, attachment.ID+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, data) {
		t.Fatal("attachment plaintext leaked to disk")
	}
	got, plaintext, err := svc.OpenAttachment(ctx, attachment.ID, a.actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != attachment.ID || !bytes.Equal(plaintext, data) {
		t.Fatalf("round trip = %#v %q", got, plaintext)
	}
	event, err := svc.PostMessage(ctx, created.ID, a.actor, room.MessagePostedPayload{
		Text: "see file", Attachments: []string{attachment.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := svc.GetAttachment(ctx, attachment.ID, a.actor)
	if err != nil || bound.EventID != event.ID {
		t.Fatalf("bound attachment = %#v, err=%v", bound, err)
	}
	if _, err := svc.PostMessage(ctx, created.ID, a.actor, room.MessagePostedPayload{
		Text: "again", Attachments: []string{attachment.ID},
	}); !errors.Is(err, room.ErrAttachmentUsed) {
		t.Fatalf("reused attachment error = %v", err)
	}
	list, err := svc.ListAttachments(ctx, created.ID, a.actor)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %#v, err=%v", list, err)
	}
}

func TestAttachmentTamperAndLimits(t *testing.T) {
	ctx := context.Background()
	svc, _, dir := newAttachmentService(t)
	a := newKeyedActor(t, "account-1", "device-1", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "Files", "", a.actor)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := svc.UploadAttachment(ctx, created.ID, a.actor, "a.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "attachments", created.ID, attachment.ID+".bin")
	if err := os.WriteFile(path, []byte("rke1:tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.OpenAttachment(ctx, attachment.ID, a.actor); err == nil {
		t.Fatal("tampered attachment opened")
	}
	if _, err := svc.UploadAttachment(ctx, created.ID, a.actor, "empty.txt", "text/plain", nil); !errors.Is(err, room.ErrInvalidAttachment) {
		t.Fatalf("empty upload = %v", err)
	}
	big := bytes.Repeat([]byte{1}, room.MaxAttachmentSize+1)
	if _, err := svc.UploadAttachment(ctx, created.ID, a.actor, "big.bin", "application/octet-stream", big); !errors.Is(err, room.ErrAttachmentTooLarge) {
		t.Fatalf("oversize upload = %v", err)
	}
	guest := newKeyedActor(t, "account-2", "device-2", "Guest")
	if _, err := svc.UploadAttachment(ctx, created.ID, guest.actor, "x.txt", "text/plain", []byte("x")); !errors.Is(err, room.ErrNotMember) {
		t.Fatalf("non-member upload = %v", err)
	}
	if _, _, err := svc.OpenAttachment(ctx, attachment.ID, guest.actor); !errors.Is(err, room.ErrNotMember) {
		t.Fatalf("non-member download = %v", err)
	}
}

func TestAttachmentDisabledWithoutDataDir(t *testing.T) {
	ctx := context.Background()
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
	svc := room.New(db, v)
	a := newKeyedActor(t, "account-1", "device-1", "Aiden")
	created, _, err := svc.CreateRoom(ctx, "Files", "", a.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadAttachment(ctx, created.ID, a.actor, "a.txt", "text/plain", []byte("x")); !errors.Is(err, room.ErrAttachmentsDisabled) {
		t.Fatalf("upload without data dir = %v", err)
	}
}
