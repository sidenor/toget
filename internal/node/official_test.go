package node_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/hostedlink"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type officialHost struct {
	store  *hosted.Store
	server *httptest.Server
	url    string
	dir    string
}

func newOfficialHost(t *testing.T) *officialHost {
	t.Helper()
	return newOfficialHostWithLimits(t, 0, 0)
}

func newOfficialHostWithLimits(t *testing.T, memberLimit, deviceLimit int) *officialHost {
	t.Helper()
	dir := t.TempDir()
	store, err := hosted.OpenStore(filepath.Join(dir, "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsureHost(context.Background(), "Test Host", []identity.Endpoint{
		{Transport: "https", Addr: "127.0.0.1:7722"},
	}); err != nil {
		t.Fatal(err)
	}
	service, err := hosted.New(hosted.Options{
		Store: store, Mailer: &hosted.MemoryMailer{}, PublicURL: "https://host.example",
		ResetFS: fstest.MapFS{"index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><title>reset</title>"),
		}},
		DisableRateLimit: true, RoomMemberLimit: memberLimit, RoomDeviceLimit: deviceLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)
	return &officialHost{
		store: store, server: server, dir: dir,
		url: "https://" + strings.TrimPrefix(server.URL, "https://") + "#fp=" + hosted.Fingerprint(server.Certificate()),
	}
}

// newOfficialNode builds a node whose only account is registered on the shared
// official host. It mirrors newNode but must start the transport before the
// account exists so the host can record a reachable node address.
func newOfficialNode(t *testing.T, name, hostURL string) *testNode {
	t.Helper()
	home := t.TempDir()
	paths, err := config.Resolve(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	v, err := vault.LoadOrCreate(paths.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	accounts := account.New(db, v)
	var rooms *room.Service
	rooms = room.New(db, v, room.WithDataDir(paths.Home), room.WithWriteGuard(
		func(ctx context.Context, roomID, _ string) error {
			record, err := rooms.GetRoom(ctx, roomID)
			if err != nil {
				return err
			}
			if record.HomeKind == room.HomeKindOfficial {
				return room.ErrRemoteReadOnly
			}
			snapshot, err := accounts.Get(ctx)
			if err != nil {
				return nil
			}
			if record.HomeServerID != "" && record.HomeServerID != snapshot.Account.ServerID {
				return room.ErrRemoteReadOnly
			}
			return nil
		},
	))
	service, err := node.New(node.Options{
		Accounts: accounts, Rooms: rooms, Paths: paths,
		Listen: "127.0.0.1:0", SyncInterval: time.Hour, AccountSyncInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		cancel()
		db.Close()
	})
	link := hostedlink.New(accounts, nil)
	snapshot, _, err := link.Register(ctx, hostedlink.RegisterSpec{
		HostURL: hostURL, Name: name, Email: strings.ToLower(name) + "@example.com",
		Password: "password-12345", DeviceName: name + " laptop", NodeURL: service.URL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testNode{node: service, accounts: accounts, rooms: rooms, snapshot: snapshot, db: db}
}

func plainTexts(t *testing.T, service *room.Service, roomID string) []string {
	t.Helper()
	events, err := service.ListEvents(context.Background(), roomID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(events))
	for i := range events {
		if events[i].Type == room.EventMessagePosted {
			var body room.MessagePostedPayload
			if err := json.Unmarshal(events[i].Payload, &body); err != nil {
				t.Fatalf("decode message: %v", err)
			}
			out = append(out, body.Text)
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestOfficialRoomBlindEndToEnd(t *testing.T) {
	ctx := context.Background()
	host := newOfficialHost(t)
	alice := newOfficialNode(t, "Alice", host.url)
	bob := newOfficialNode(t, "Bob", host.url)
	carol := newOfficialNode(t, "Carol", host.url)
	dave := newOfficialNode(t, "Dave", host.url)

	created, createdEvent, err := alice.node.CreateOfficialRoom(ctx, "Blind room", "off the record")
	if err != nil {
		t.Fatal(err)
	}
	if created.HomeKind != room.HomeKindOfficial || created.HomeAddress != host.url {
		t.Fatalf("created room = %+v", created)
	}
	if createdEvent == nil || createdEvent.Type != room.EventRoomCreated {
		t.Fatalf("created event = %+v", createdEvent)
	}

	aliceActor, err := alice.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.PostRemoteMessage(ctx, created.ID, aliceActor, room.MessagePostedPayload{Text: "first secret"}); err != nil {
		t.Fatal(err)
	}

	invite, _, code, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryAll, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if invite.Secret == "" || code == "" {
		t.Fatal("invite was not encoded")
	}
	joined, err := bob.node.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Name != "Blind room" || joined.Topic != "off the record" {
		t.Fatalf("joined room metadata = %q / %q", joined.Name, joined.Topic)
	}
	if got := plainTexts(t, bob.rooms, joined.ID); !containsString(got, "first secret") {
		t.Fatalf("bob sees %v", got)
	}

	bobActor, err := bob.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.node.PostRemoteMessage(ctx, joined.ID, bobActor, room.MessagePostedPayload{Text: "bob says hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.SyncOfficialRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, alice.rooms, created.ID); !containsString(got, "bob says hi") {
		t.Fatalf("alice sees %v", got)
	}

	// A history-none invitee gets no earlier plaintext, but can still post to
	// the hidden chain using the host-provided head anchor.
	inviteNone, _, codeNone, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if inviteNone.History != room.HistoryNone {
		t.Fatalf("invite history = %q", inviteNone.History)
	}
	carolRoom, err := carol.node.Join(ctx, codeNone)
	if err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, carol.rooms, carolRoom.ID); len(got) != 0 {
		t.Fatalf("carol sees hidden history: %v", got)
	}
	carolActor, err := carol.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := carol.node.PostRemoteMessage(ctx, carolRoom.ID, carolActor, room.MessagePostedPayload{Text: "carol after none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.SyncOfficialRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, alice.rooms, created.ID); !containsString(got, "carol after none") {
		t.Fatalf("alice does not see carol: %v", got)
	}
	if _, err := bob.node.SyncOfficialRoom(ctx, joined.ID); err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, bob.rooms, joined.ID); !containsString(got, "carol after none") {
		t.Fatalf("bob does not see carol: %v", got)
	}

	// Leaving revokes Carol's keys and forces a rotation. Alice (next active
	// device to sync) rotates; Bob follows the new epoch.
	if _, err := carol.node.LeaveRoom(ctx, carolRoom.ID, carolActor); err != nil {
		t.Fatal(err)
	}
	if archived, err := carol.rooms.GetRoom(ctx, carolRoom.ID); err != nil || archived.ArchivedAt == nil {
		t.Fatalf("carol room after leave = %+v, %v", archived, err)
	}
	if _, err := alice.node.SyncOfficialRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.node.SyncOfficialRoom(ctx, joined.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.PostRemoteMessage(ctx, created.ID, aliceActor, room.MessagePostedPayload{Text: "after rotation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.node.SyncOfficialRoom(ctx, joined.ID); err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, bob.rooms, joined.ID); !containsString(got, "after rotation") {
		t.Fatalf("bob cannot read after rotation: %v", got)
	}
	if _, err := alice.node.SyncOfficialRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, alice.rooms, created.ID); !containsString(got, "after rotation") {
		t.Fatalf("alice cannot read after rotation: %v", got)
	}

	// An invite created before a rename still carries the old name in its
	// code. A history-all join replays the visible settings event and shows
	// the current name.
	_, _, lateCode, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryAll, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Only the owner may rename, and the new name travels as a sealed event.
	renamed, renameEvent, err := alice.node.RenameOfficialRoom(ctx, created.ID, aliceActor, "Blind room v2", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renameEvent.Type != room.EventRoomSettingsChanged || renamed.Name != "Blind room v2" || renamed.Topic != "renamed" {
		t.Fatalf("renamed room = %+v / %+v", renamed, renameEvent)
	}
	if _, err := bob.node.SyncOfficialRoom(ctx, joined.ID); err != nil {
		t.Fatal(err)
	}
	if bobRoom, err := bob.rooms.GetRoom(ctx, joined.ID); err != nil || bobRoom.Name != "Blind room v2" || bobRoom.Topic != "renamed" {
		t.Fatalf("bob renamed room = %+v, %v", bobRoom, err)
	}
	if _, _, err := bob.node.RenameOfficialRoom(ctx, joined.ID, bobActor, "Not allowed", ""); err == nil {
		t.Fatal("non-owner rename was accepted")
	} else {
		var apiErr *hosted.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
			t.Fatalf("non-owner rename error = %v", err)
		}
	}
	daveRoom, err := dave.node.Join(ctx, lateCode)
	if err != nil {
		t.Fatal(err)
	}
	if daveRoom.Name != "Blind room v2" || daveRoom.Topic != "renamed" {
		t.Fatalf("dave joined stale invite name = %q / %q", daveRoom.Name, daveRoom.Topic)
	}

	// Archiving a room locally only hides it. Redeeming a fresh invite for a
	// room this account still belongs to restores the existing copy.
	daveActor, err := dave.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dave.node.ArchiveRoom(ctx, daveRoom.ID, daveActor); err != nil {
		t.Fatal(err)
	}
	_, _, daveCode, err := bob.node.CreateOfficialInvite(ctx, joined.ID, bobActor, room.HistoryRecent, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := dave.node.Join(ctx, daveCode)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ArchivedAt != nil || restored.ID != daveRoom.ID {
		t.Fatalf("restored room = %+v", restored)
	}

	// When the owner leaves, the host promotes the oldest remaining member.
	// Bob's sync must follow the new owner so owner-only actions keep working.
	if _, err := alice.node.LeaveRoom(ctx, created.ID, aliceActor); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.node.SyncOfficialRoom(ctx, joined.ID); err != nil {
		t.Fatal(err)
	}
	bobRoom, err := bob.rooms.GetRoom(ctx, joined.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bobRoom.CreatedBy != bob.snapshot.Account.ID {
		t.Fatalf("bob room owner = %q, want %q", bobRoom.CreatedBy, bob.snapshot.Account.ID)
	}
	if renamed, _, err := bob.node.RenameOfficialRoom(ctx, joined.ID, bobActor, "Bob's room", ""); err != nil {
		t.Fatal(err)
	} else if renamed.Name != "Bob's room" {
		t.Fatalf("bob rename = %+v", renamed)
	}

	// A device that left cannot redeem its way back in: the host revoked its
	// membership and keys. The client reports that clearly instead of returning
	// the archived room as if the join had worked.
	_, _, aliceCode, err := bob.node.CreateOfficialInvite(ctx, joined.ID, bobActor, room.HistoryNone, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.Join(ctx, aliceCode); !errors.Is(err, room.ErrRoomLeft) {
		t.Fatalf("left device rejoin error = %v, want room.ErrRoomLeft", err)
	}

	// The host must not retain the room name, topic, or message plaintext.
	needles := [][]byte{[]byte("Blind room"), []byte("Blind room v2"), []byte("Bob's room"), []byte("off the record"), []byte("renamed"), []byte("first secret"), []byte("bob says hi"), []byte("carol after none"), []byte("after rotation")}
	err = filepath.WalkDir(host.dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, needle := range needles {
			if bytes.Contains(data, needle) {
				t.Errorf("host file %s contains plaintext %q", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func officialDeviceRefFor(t *testing.T, n *testNode) hosted.RoomDeviceRef {
	t.Helper()
	snapshot, err := n.accounts.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return hosted.RoomDeviceRef{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		DisplayName: snapshot.Account.Name, SigningPublicKey: snapshot.Device.PublicKey,
		EncryptionPublicKey: snapshot.Device.EncryptionPublicKey,
		Certificate:         []byte(snapshot.Device.Certificate),
	}
}

func officialSignerFor(t *testing.T, n *testNode) hosted.RoomSigner {
	t.Helper()
	snapshot, err := n.accounts.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key, err := n.accounts.UnsealDeviceKey(snapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	return hosted.RoomSigner{
		AccountID: snapshot.Account.ID, DeviceID: snapshot.Device.ID,
		PrivateKey: key,
	}
}

func TestOfficialRoomHostRejectsInvalidAccess(t *testing.T) {
	ctx := context.Background()
	host := newOfficialHost(t)
	alice := newOfficialNode(t, "Alice", host.url)
	bob := newOfficialNode(t, "Bob", host.url)
	carol := newOfficialNode(t, "Carol", host.url)
	dave := newOfficialNode(t, "Dave", host.url)
	erin := newOfficialNode(t, "Erin", host.url)

	created, _, err := alice.node.CreateOfficialRoom(ctx, "Guarded room", "metadata")
	if err != nil {
		t.Fatal(err)
	}
	aliceActor, err := alice.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.PostRemoteMessage(ctx, created.ID, aliceActor, room.MessagePostedPayload{Text: "anchor"}); err != nil {
		t.Fatal(err)
	}
	_, _, codeAll, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryAll, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bobRoom, err := bob.node.Join(ctx, codeAll)
	if err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, bob.rooms, bobRoom.ID); !containsString(got, "anchor") {
		t.Fatalf("bob sees %v", got)
	}
	// The same device may replay a consumed invite. This is the recovery path
	// for a join response that was lost after the host committed membership.
	decoded, err := node.DecodeInvite(codeAll)
	if err != nil {
		t.Fatal(err)
	}
	bobClient, err := hosted.NewClient(host.url)
	if err != nil {
		t.Fatal(err)
	}
	replaySecretHash := sha256.Sum256([]byte(decoded.Secret))
	joinRequest := hosted.RoomJoinRequest{
		Protocol: hosted.Protocol, RoomID: decoded.RoomID, SecretHash: replaySecretHash[:],
		Device: officialDeviceRefFor(t, bob),
	}
	replayedJoin, err := bobClient.JoinRoom(ctx, officialSignerFor(t, bob), joinRequest)
	if err != nil {
		t.Fatalf("idempotent join replay: %v", err)
	}
	if replayedJoin.Room.ID != bobRoom.ID {
		t.Fatalf("replayed join room = %q", replayedJoin.Room.ID)
	}

	_, _, codeNone, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	carolRoom, err := carol.node.Join(ctx, codeNone)
	if err != nil {
		t.Fatal(err)
	}
	if got := plainTexts(t, carol.rooms, carolRoom.ID); len(got) != 0 {
		t.Fatalf("carol sees hidden history: %v", got)
	}
	carolActor, err := carol.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := carol.node.LeaveRoom(ctx, carolRoom.ID, carolActor); err != nil {
		t.Fatal(err)
	}

	// The host must replay a committed leave instead of rejecting the
	// now-left device. This is what makes a lost leave response recoverable.
	storedEvents, err := carol.db.ListEvents(ctx, carolRoom.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var leaveEvent *room.Event
	for i := range storedEvents {
		if storedEvents[i].Type == room.EventMemberLeft {
			leaveEvent = &storedEvents[i]
			break
		}
	}
	if leaveEvent == nil {
		t.Fatal("local leave event was not stored")
	}
	carolClient, err := hosted.NewClient(host.url)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := carolClient.LeaveRoom(ctx, officialSignerFor(t, carol), leaveEvent.ToWire())
	if err != nil {
		t.Fatalf("idempotent leave replay: %v", err)
	}
	if replayed.Head < leaveEvent.Seq {
		t.Fatalf("replayed head = %d, leave seq = %d", replayed.Head, leaveEvent.Seq)
	}

	// Alice rotates after Carol's departure.
	if _, err := alice.node.SyncOfficialRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if created, err = alice.rooms.GetRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if created.Epoch < 2 {
		t.Fatalf("rotation did not advance the epoch: %+v", created)
	}
	_, _, codeAfter, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	daveRoom, err := dave.node.Join(ctx, codeAfter)
	if err != nil {
		t.Fatal(err)
	}
	daveClient, err := hosted.NewClient(host.url)
	if err != nil {
		t.Fatal(err)
	}
	keyView, err := daveClient.SyncRoom(ctx, officialSignerFor(t, dave), hosted.RoomSyncRequest{
		Protocol: hosted.Protocol, RoomID: daveRoom.ID, After: 0, NeedKeys: []int{1, created.Epoch},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keyView.Keys) == 0 {
		t.Fatal("the current epoch key was not delivered")
	}
	for _, key := range keyView.Keys {
		if key.Epoch != created.Epoch {
			t.Fatalf("history-none device received key for epoch %d", key.Epoch)
		}
	}
	if len(keyView.Events) != 0 {
		t.Fatalf("history-none device received %d hidden events", len(keyView.Events))
	}

	// A device outside the room cannot sync, even with valid account keys.
	erinClient, err := hosted.NewClient(host.url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = erinClient.SyncRoom(ctx, officialSignerFor(t, erin), hosted.RoomSyncRequest{
		Protocol: hosted.Protocol, RoomID: created.ID,
	})
	var apiErr *hosted.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("non-member sync error = %v", err)
	}

	// A signed request with the wrong device key is rejected before room
	// access is considered.
	_, wrongKey, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	badSigner := officialSignerFor(t, dave)
	badSigner.PrivateKey = wrongKey
	_, err = daveClient.SyncRoom(ctx, badSigner, hosted.RoomSyncRequest{
		Protocol: hosted.Protocol, RoomID: created.ID,
	})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("wrong-key sync error = %v", err)
	}

	// A consumed invite cannot be redeemed by another account.
	_, err = erin.node.Join(ctx, codeAfter)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "invite_used" {
		t.Fatalf("reused invite error = %v", err)
	}

	// An unauthenticated HTTP request never reaches room lookup.
	request, err := http.NewRequest(http.MethodPost, host.server.URL+hosted.RoomSyncPath,
		strings.NewReader(`{"protocol":1}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := host.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated sync status = %d", response.StatusCode)
	}

	// The host verifies size, type, and epoch even for a valid member.
	if _, err := alice.node.SyncOfficialRoom(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	aliceClient, err := hosted.NewClient(host.url)
	if err != nil {
		t.Fatal(err)
	}
	large, err := alice.rooms.PrepareOfficialEvent(ctx, created.ID, aliceActor, room.EventToolResult,
		map[string]string{"output": strings.Repeat("x", hosted.MaxRoomEventSize+1024)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aliceClient.PostRoomEvent(ctx, officialSignerFor(t, alice), large.ToWire())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_event" {
		t.Fatalf("oversized event error = %v", err)
	}
	badType, err := alice.rooms.PrepareOfficialEvent(ctx, created.ID, aliceActor, "not.allowed",
		map[string]string{"value": "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aliceClient.PostRoomEvent(ctx, officialSignerFor(t, alice), badType.ToWire())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != "forbidden" {
		t.Fatalf("disallowed event type error = %v", err)
	}
	stale, err := alice.rooms.PrepareOfficialEvent(ctx, created.ID, aliceActor, room.EventToolResult,
		map[string]string{"value": "stale"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stale.Epoch = 1
	_, err = aliceClient.PostRoomEvent(ctx, officialSignerFor(t, alice), stale.ToWire())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "epoch_conflict" {
		t.Fatalf("stale epoch error = %v", err)
	}
	// The host cannot recompute the plaintext hash, but it can verify that the
	// author signed the hash the envelope commits to.
	tampered, err := alice.rooms.PrepareOfficialEvent(ctx, created.ID, aliceActor, room.EventToolResult,
		map[string]string{"value": "tampered"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tampered.Signature = "AAAA"
	_, err = aliceClient.PostRoomEvent(ctx, officialSignerFor(t, alice), tampered.ToWire())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_event" {
		t.Fatalf("tampered author signature error = %v", err)
	}

	// Repeating the same signed creation is idempotent, so a lost response
	// does not leave a retrying client with a conflict.
	hostInfo, err := aliceClient.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := alice.rooms.PrepareOfficialRoom("Retry room", "", aliceActor, room.OfficialHome{
		ServerID: hostInfo.HostID, Address: host.url,
	})
	if err != nil {
		t.Fatal(err)
	}
	createRequest := hosted.RoomCreateRequest{
		Protocol: hosted.Protocol, RoomID: prepared.Room.ID,
		Device: officialDeviceRefFor(t, alice),
		Event:  prepared.Event.ToWire(),
		Keys:   []hosted.RoomKeyPackage{prepared.Package},
	}
	first, err := aliceClient.CreateRoom(ctx, officialSignerFor(t, alice), createRequest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := aliceClient.CreateRoom(ctx, officialSignerFor(t, alice), createRequest)
	if err != nil {
		t.Fatalf("idempotent room creation: %v", err)
	}
	if first.Room.ID != second.Room.ID || second.Room.Head != 1 {
		t.Fatalf("replayed room = %+v, first = %+v", second.Room, first.Room)
	}

	// The same is true for an invite: a lost acknowledgement must not force
	// the caller to burn another invite or leave a dangling invite event.
	preparedInvite, err := alice.rooms.PrepareOfficialInvite(ctx, created.ID, aliceActor,
		room.HistoryNone, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	secretHash := sha256.Sum256([]byte(preparedInvite.Secret))
	inviteRequest := hosted.RoomInviteRequest{
		Protocol: hosted.Protocol, RoomID: created.ID, InviteID: preparedInvite.ID,
		SecretHash: secretHash[:], History: preparedInvite.History,
		ExpiresAt: preparedInvite.ExpiresAt, Event: preparedInvite.Event.ToWire(),
		Keys: preparedInvite.Keys,
	}
	if _, err := aliceClient.CreateRoomInvite(ctx, officialSignerFor(t, alice), inviteRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := aliceClient.CreateRoomInvite(ctx, officialSignerFor(t, alice), inviteRequest); err != nil {
		t.Fatalf("idempotent invite creation: %v", err)
	}
}

func TestOfficialRoomLastMemberLeaveRevokesInvites(t *testing.T) {
	ctx := context.Background()
	host := newOfficialHost(t)
	alice := newOfficialNode(t, "Alice", host.url)
	carol := newOfficialNode(t, "Carol", host.url)
	created, _, err := alice.node.CreateOfficialRoom(ctx, "Closing room", "")
	if err != nil {
		t.Fatal(err)
	}
	aliceActor, err := alice.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, code, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.node.LeaveRoom(ctx, created.ID, aliceActor); err != nil {
		t.Fatal(err)
	}
	_, err = carol.node.Join(ctx, code)
	var apiErr *hosted.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != "invite_invalid" {
		t.Fatalf("join after last member left = %v", err)
	}
}

func TestOfficialRoomMemberAndDeviceLimits(t *testing.T) {
	ctx := context.Background()
	var apiErr *hosted.APIError

	t.Run("member limit", func(t *testing.T) {
		host := newOfficialHostWithLimits(t, 2, 10)
		alice := newOfficialNode(t, "Alice", host.url)
		bob := newOfficialNode(t, "Bob", host.url)
		carol := newOfficialNode(t, "Carol", host.url)
		created, _, err := alice.node.CreateOfficialRoom(ctx, "Two seats", "")
		if err != nil {
			t.Fatal(err)
		}
		aliceActor, err := alice.node.LocalActor(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _, code, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.node.Join(ctx, code); err != nil {
			t.Fatal(err)
		}
		_, _, second, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		_, err = carol.node.Join(ctx, second)
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "room_full" {
			t.Fatalf("join over member limit = %v", err)
		}
	})

	t.Run("device limit", func(t *testing.T) {
		host := newOfficialHostWithLimits(t, 10, 2)
		alice := newOfficialNode(t, "Alice", host.url)
		bob := newOfficialNode(t, "Bob", host.url)
		carol := newOfficialNode(t, "Carol", host.url)
		created, _, err := alice.node.CreateOfficialRoom(ctx, "Two devices", "")
		if err != nil {
			t.Fatal(err)
		}
		aliceActor, err := alice.node.LocalActor(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _, code, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.node.Join(ctx, code); err != nil {
			t.Fatal(err)
		}
		_, _, second, err := alice.node.CreateOfficialInvite(ctx, created.ID, aliceActor, room.HistoryNone, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		_, err = carol.node.Join(ctx, second)
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "device_limit" {
			t.Fatalf("join over device limit = %v", err)
		}
	})
}
