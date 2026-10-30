package room_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
)

type testIdentity struct {
	actor       room.Actor
	encryptPriv []byte
	certificate []byte
}

func newIdentity(t *testing.T, accountID, deviceID, name string) testIdentity {
	t.Helper()
	accountPub, accountPriv, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, encPriv, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := identity.SignDeviceCertificate(deviceID, accountID, devicePub, encPub, accountPriv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return testIdentity{
		actor: room.Actor{
			AccountID: accountID, DeviceID: deviceID, DisplayName: name,
			SigningKey: devicePriv, AccountPublicKey: accountPub, EncryptionPublicKey: encPub,
		},
		encryptPriv: encPriv,
		certificate: cert,
	}
}

func (i testIdentity) joiner() room.RemoteJoiner {
	return room.RemoteJoiner{
		AccountID:           i.actor.AccountID,
		DeviceID:            i.actor.DeviceID,
		DisplayName:         i.actor.DisplayName,
		AccountPublicKey:    i.actor.AccountPublicKey,
		SigningPublicKey:    i.actor.SigningKey.Public().(ed25519.PublicKey),
		EncryptionPublicKey: i.actor.EncryptionPublicKey,
		DeviceCertificate:   i.certificate,
	}
}

func TestRemoteJoinHistoryAndSync(t *testing.T) {
	ctx := context.Background()
	hostSvc, _ := newService(t)
	guestSvc, _ := newService(t)
	host := newIdentity(t, "11111111111111", "host-device", "Host")
	guest := newIdentity(t, "22222222222222", "guest-device", "Guest")

	created, createdEvent, err := hostSvc.CreateRoom(ctx, "Remote", "", host.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostSvc.SetHomeInfo(ctx, created.ID, "server-host", "https://127.0.0.1:7717"); err != nil {
		t.Fatal(err)
	}
	created.HomeServerID = "server-host"
	before, err := hostSvc.PostMessage(ctx, created.ID, host.actor, room.MessagePostedPayload{Text: "before join"})
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := hostSvc.CreateInvite(ctx, created.ID, host.actor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := hostSvc.RedeemInviteFor(ctx, invite.Secret, guest.joiner(), host.actor); err != nil {
		t.Fatal(err)
	}
	bundle, err := hostSvc.ExportJoinBundle(ctx, created.ID, guest.actor.DeviceID, room.HistoryNone)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Head < 4 || bundle.Room.Epoch != 2 {
		t.Fatalf("unexpected bundle head/epoch: %d/%d", bundle.Head, bundle.Room.Epoch)
	}
	imported, err := guestSvc.ImportJoinBundle(ctx, bundle, guest.actor.DeviceID, guest.encryptPriv)
	if err != nil {
		t.Fatal(err)
	}
	if imported.HomeServerID != "server-host" {
		t.Fatalf("unexpected home server: %q", imported.HomeServerID)
	}
	events, err := guestSvc.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != room.EventMemberJoined {
		t.Fatalf("history none should expose only the join event, got %#v", events)
	}
	if events[0].Seq != 4 {
		t.Fatalf("join event seq = %d", events[0].Seq)
	}

	after, err := hostSvc.PostMessage(ctx, created.ID, host.actor, room.MessagePostedPayload{Text: "after join"})
	if err != nil {
		t.Fatal(err)
	}
	sync, err := hostSvc.ExportSync(ctx, created.ID, guest.actor.DeviceID, events[0].Seq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sync.Events) != 1 || sync.Events[0].ID != after.ID {
		t.Fatalf("sync events = %#v", sync.Events)
	}
	if n, err := guestSvc.ImportSync(ctx, sync, guest.actor.DeviceID, guest.encryptPriv); err != nil || n != 1 {
		t.Fatalf("import sync = %d, %v", n, err)
	}
	events, err = guestSvc.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("guest events = %d, want 2", len(events))
	}
	var payload room.MessagePostedPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil || payload.Text != "after join" {
		t.Fatalf("guest payload = %#v, %v", payload, err)
	}

	// Guest writes through the home node.
	prepared, err := guestSvc.PrepareMessage(ctx, created.ID, guest.actor, room.MessagePostedPayload{Text: "from guest"})
	if err != nil {
		t.Fatal(err)
	}
	forged := *prepared
	forged.Type = room.EventMemberLeft
	if err := hostSvc.AcceptRemoteEvent(ctx, &forged); !errors.Is(err, room.ErrForbidden) {
		t.Fatalf("remote membership event = %v, want ErrForbidden", err)
	}
	if prepared.Seq != after.Seq+1 {
		t.Fatalf("prepared seq = %d", prepared.Seq)
	}
	if err := hostSvc.AcceptRemoteEvent(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if err := guestSvc.AppendRemote(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	reply, err := guestSvc.GetOpenedEvent(ctx, created.ID, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reply.Payload, &payload); err != nil || payload.Text != "from guest" {
		t.Fatalf("reply payload = %#v, %v", payload, err)
	}
	hostEvents, err := hostSvc.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostEvents) != 6 || hostEvents[5].AuthorID != guest.actor.AccountID {
		t.Fatalf("host events = %#v", hostEvents)
	}
	_ = createdEvent
	_ = before

	// A second sync is idempotent.
	empty, err := hostSvc.ExportSync(ctx, created.ID, guest.actor.DeviceID, hostEvents[len(hostEvents)-1].Seq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Events) != 0 {
		t.Fatalf("expected empty sync, got %d", len(empty.Events))
	}
	if n, err := guestSvc.ImportSync(ctx, empty, guest.actor.DeviceID, guest.encryptPriv); err != nil || n != 0 {
		t.Fatalf("empty sync import = %d, %v", n, err)
	}
}

func TestRemoteHistoryAllRevealsEarlierEpochs(t *testing.T) {
	ctx := context.Background()
	hostSvc, _ := newService(t)
	guestSvc, _ := newService(t)
	host := newIdentity(t, "11111111111111", "host-device", "Host")
	guest := newIdentity(t, "33333333333333", "guest-device", "Guest")
	created, _, err := hostSvc.CreateRoom(ctx, "History", "", host.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostSvc.SetHomeInfo(ctx, created.ID, "server-host", "https://127.0.0.1:7717"); err != nil {
		t.Fatal(err)
	}
	if _, err := hostSvc.PostMessage(ctx, created.ID, host.actor, room.MessagePostedPayload{Text: "old visible"}); err != nil {
		t.Fatal(err)
	}
	invite, _, err := hostSvc.CreateInvite(ctx, created.ID, host.actor, room.HistoryAll, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := hostSvc.RedeemInviteFor(ctx, invite.Secret, guest.joiner(), host.actor); err != nil {
		t.Fatal(err)
	}
	bundle, err := hostSvc.ExportJoinBundle(ctx, created.ID, guest.actor.DeviceID, room.HistoryAll)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guestSvc.ImportJoinBundle(ctx, bundle, guest.actor.DeviceID, guest.encryptPriv); err != nil {
		t.Fatal(err)
	}
	events, err := guestSvc.ListEvents(ctx, created.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("all-history events = %d, want 5", len(events))
	}
	if events[3].Type != room.EventHistoryAttached {
		t.Fatalf("history event = %q, want %q", events[3].Type, room.EventHistoryAttached)
	}
	var attached room.HistoryAttachedPayload
	if err := json.Unmarshal(events[3].Payload, &attached); err != nil || attached.Choice != room.HistoryAll || attached.FromSeq != 1 || attached.ToSeq != 3 {
		t.Fatalf("history payload = %#v, %v", attached, err)
	}
	var payload room.MessagePostedPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil || payload.Text != "old visible" {
		t.Fatalf("old payload = %#v, %v", payload, err)
	}
}

func TestJoinBundleRejectsTruncatedAndTamperedEvents(t *testing.T) {
	ctx := context.Background()
	hostSvc, _ := newService(t)
	guestSvc, _ := newService(t)
	host := newIdentity(t, "11111111111111", "host-device", "Host")
	guest := newIdentity(t, "44444444444444", "guest-device", "Guest")

	created, _, err := hostSvc.CreateRoom(ctx, "Bundle", "", host.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostSvc.SetHomeInfo(ctx, created.ID, "server-host", "https://127.0.0.1:7717"); err != nil {
		t.Fatal(err)
	}
	if _, err := hostSvc.PostMessage(ctx, created.ID, host.actor, room.MessagePostedPayload{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	invite, _, err := hostSvc.CreateInvite(ctx, created.ID, host.actor, room.HistoryAll, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := hostSvc.RedeemInviteFor(ctx, invite.Secret, guest.joiner(), host.actor); err != nil {
		t.Fatal(err)
	}
	bundle, err := hostSvc.ExportJoinBundle(ctx, created.ID, guest.actor.DeviceID, room.HistoryAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Events) < 2 {
		t.Fatalf("bundle has %d events", len(bundle.Events))
	}

	truncated := *bundle
	truncated.Events = append([]room.WireEvent(nil), bundle.Events[1:]...)
	if _, err := guestSvc.ImportJoinBundle(ctx, &truncated, guest.actor.DeviceID, guest.encryptPriv); !errors.Is(err, room.ErrInvalidEvent) {
		t.Fatalf("truncated bundle error = %v, want ErrInvalidEvent", err)
	}

	tampered := *bundle
	tampered.Events = append([]room.WireEvent(nil), bundle.Events...)
	tampered.Events[1].Hash = strings.Repeat("0", 64)
	if _, err := guestSvc.ImportJoinBundle(ctx, &tampered, guest.actor.DeviceID, guest.encryptPriv); !errors.Is(err, room.ErrInvalidEvent) {
		t.Fatalf("tampered bundle error = %v, want ErrInvalidEvent", err)
	}

	if _, err := guestSvc.GetRoom(ctx, created.ID); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("failed import left a partial room: %v", err)
	}
}
