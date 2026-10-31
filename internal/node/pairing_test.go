package node_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func newEmptyNode(t *testing.T) (*testNode, *account.Service, *room.Service) {
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
		Listen: "127.0.0.1:0", SyncInterval: time.Hour,
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
	return &testNode{node: service, accounts: accounts, rooms: rooms, db: db}, accounts, rooms
}

func TestPairCodeRoundTripAndTamper(t *testing.T) {
	code := node.PairCode{
		PairingID: "pair-1", Secret: strings.Repeat("s", 43),
		URL:      "https://192.168.1.5:7717#fp=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ServerID: "srv", AccountID: "12345678901234",
		AccountName: "Aiden", DeviceName: "Studio",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Second),
	}
	encoded, err := node.EncodePairCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, node.PairPrefix) {
		t.Fatalf("code prefix = %q", encoded)
	}
	decoded, err := node.DecodePairCode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PairingID != code.PairingID || decoded.AccountID != code.AccountID || decoded.URL != code.URL {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, err := node.DecodePairCode(encoded[:len(encoded)-4] + "AAAA"); err == nil {
		t.Fatal("tampered pairing code decoded")
	}
	if _, err := node.DecodePairCode("https://example.com"); err == nil {
		t.Fatal("foreign URL decoded as pairing code")
	}
}

func TestPairSecondDeviceRecoversAccountAndRooms(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Aiden")
	guest, guestAccounts, guestRooms := newEmptyNode(t)

	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := host.rooms.CreateRoom(ctx, "Pair room", "shared", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.rooms.PostMessage(ctx, record.ID, hostActor, room.MessagePostedPayload{Text: "before pairing"}); err != nil {
		t.Fatal(err)
	}

	pairing, err := host.node.CreatePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pairing.Code == "" || pairing.Status != "waiting" {
		t.Fatalf("pairing = %#v", pairing)
	}
	joined, err := guest.node.StartPairJoin(ctx, pairing.Code, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	if joined.Status != "waiting" {
		t.Fatalf("start join status = %q", joined.Status)
	}

	waitFor(t, 10*time.Second, func() bool {
		list, err := host.node.ListPairings(ctx)
		if err != nil {
			return false
		}
		for _, item := range list {
			if item.ID == pairing.ID {
				return item.Status == "requested" && item.DeviceName == "Laptop" &&
					item.Fingerprint == joined.Fingerprint
			}
		}
		return false
	})
	list, err := host.node.ListPairings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID == pairing.ID && item.Code != "" {
			t.Fatal("pairing code is still exposed after a device requested the pairing")
		}
	}
	approved, err := host.node.ApprovePairing(ctx, pairing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" {
		t.Fatalf("approve status = %q", approved.Status)
	}

	var ready *node.PairJoinView
	waitFor(t, 15*time.Second, func() bool {
		view, err := guest.node.PairJoinStatus(ctx, joined.ID)
		if err != nil {
			return false
		}
		if view.Status == "ready" {
			ready = view
			return true
		}
		if view.Status == "failed" || view.Status == "canceled" || view.Status == "expired" {
			t.Fatalf("join ended %s: %s", view.Status, view.Error)
		}
		return false
	})
	if ready == nil || ready.Fingerprint != joined.Fingerprint {
		t.Fatalf("ready = %#v", ready)
	}

	snapshot, err := guestAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Account.ID != host.snapshot.Account.ID || snapshot.Account.Name != "Aiden" {
		t.Fatalf("recovered account = %#v", snapshot.Account)
	}
	if snapshot.Account.Code != host.snapshot.Account.Code || snapshot.Account.PasswordHash != host.snapshot.Account.PasswordHash {
		t.Fatal("account code or password hash changed during pairing")
	}
	if !snapshot.Device.Local {
		t.Fatal("recovered device is not marked local")
	}
	if len(snapshot.Device.PrivateKey) == 0 || snapshot.Device.ID == host.snapshot.Device.ID {
		t.Fatalf("recovered device = %#v", snapshot.Device)
	}
	devices, err := guestAccounts.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices))
	}
	if _, err := guestAccounts.Login(ctx, "password-12345"); err != nil {
		t.Fatalf("password was not preserved: %v", err)
	}

	guestRoomsList, err := guestRooms.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(guestRoomsList) != 1 || guestRoomsList[0].ID != record.ID {
		t.Fatalf("recovered rooms = %#v", guestRoomsList)
	}
	events, err := guestRooms.ListEvents(ctx, record.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !strings.Contains(string(events[1].Payload), "before pairing") {
		t.Fatalf("recovered events = %#v", events)
	}

	// Both directions work after pairing.
	if _, err := host.rooms.PostMessage(ctx, record.ID, hostActor, room.MessagePostedPayload{Text: "after pairing"}); err != nil {
		t.Fatal(err)
	}
	if n, err := guest.node.SyncRoom(ctx, record.ID); err != nil || n != 1 {
		t.Fatalf("guest sync = %d, %v", n, err)
	}
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.PostRemoteMessage(ctx, record.ID, guestActor, room.MessagePostedPayload{Text: "from laptop"}); err != nil {
		t.Fatal(err)
	}
	events, err = host.rooms.ListEvents(ctx, record.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(events, "from laptop") {
		t.Fatalf("host events = %#v", events)
	}

	// Revoking the paired device rotates the room epoch and marks it revoked.
	roomDevices, err := host.rooms.ListDevices(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	var guestDeviceID string
	for i := range roomDevices {
		if roomDevices[i].DeviceID != host.snapshot.Device.ID {
			guestDeviceID = roomDevices[i].DeviceID
		}
	}
	if guestDeviceID == "" {
		t.Fatal("paired device was not registered in the room")
	}
	warnings, err := host.node.RevokeAccountDevice(ctx, guestDeviceID)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("revoke = %v, %v", warnings, err)
	}
	roomDevices, err = host.rooms.ListDevices(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range roomDevices {
		if roomDevices[i].DeviceID == guestDeviceID && roomDevices[i].RevokedAt == nil {
			t.Fatal("paired device still active in room")
		}
	}
	if _, err := guestAccounts.Get(ctx); err != nil {
		t.Fatalf("account snapshot after revoke: %v", err)
	}
}

func TestPairedDevicePairsAThirdDevice(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	first, firstAccounts, firstRooms := newEmptyNode(t)
	second, secondAccounts, secondRooms := newEmptyNode(t)

	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hostRoom, _, err := host.rooms.CreateRoom(ctx, "Host room", "", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, hostRoom.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.rooms.PostMessage(ctx, hostRoom.ID, hostActor, room.MessagePostedPayload{Text: "from host"}); err != nil {
		t.Fatal(err)
	}

	pairDevice := func(from, to *testNode, name string) *node.PairJoinView {
		t.Helper()
		pairing, err := from.node.CreatePairing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		joined, err := to.node.StartPairJoin(ctx, pairing.Code, name)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, 10*time.Second, func() bool {
			list, err := from.node.ListPairings(ctx)
			if err != nil {
				return false
			}
			for _, item := range list {
				if item.ID == pairing.ID {
					return item.Status == "requested"
				}
			}
			return false
		})
		if _, err := from.node.ApprovePairing(ctx, pairing.ID); err != nil {
			t.Fatal(err)
		}
		var ready *node.PairJoinView
		waitFor(t, 30*time.Second, func() bool {
			view, err := to.node.PairJoinStatus(ctx, joined.ID)
			if err != nil {
				return false
			}
			if view.Status == "failed" {
				t.Fatalf("join failed: %s", view.Error)
			}
			if view.Status == "ready" {
				ready = view
				return true
			}
			return false
		})
		return ready
	}

	pairDevice(host, first, "First")
	firstActor, err := first.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	localRoom, _, err := firstRooms.CreateRoom(ctx, "First room", "", firstActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.node.EnsureRoomHome(ctx, localRoom.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := firstRooms.PostMessage(ctx, localRoom.ID, firstActor, room.MessagePostedPayload{Text: "from first"}); err != nil {
		t.Fatal(err)
	}

	pairDevice(first, second, "Second")

	secondSnapshot, err := secondAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if secondSnapshot.Account.ID != host.snapshot.Account.ID {
		t.Fatalf("third device account = %s", secondSnapshot.Account.ID)
	}
	rooms, err := secondRooms.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 {
		t.Fatalf("third device rooms = %d, want 2", len(rooms))
	}
	homes := map[string]string{}
	for _, record := range rooms {
		homes[record.ID] = record.HomeServerID
	}
	if homes[hostRoom.ID] != host.snapshot.Server.ID {
		t.Fatalf("host room home = %q, want %q", homes[hostRoom.ID], host.snapshot.Server.ID)
	}
	firstSnapshot, err := firstAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if homes[localRoom.ID] != firstSnapshot.Account.ServerID {
		t.Fatalf("first room home = %q, want %q", homes[localRoom.ID], firstSnapshot.Account.ServerID)
	}

	// The third device syncs both homes and can write to the room hosted by
	// the second device.
	for _, roomID := range []string{hostRoom.ID, localRoom.ID} {
		if _, err := second.node.SyncRoom(ctx, roomID); err != nil {
			t.Fatalf("third device sync %s: %v", roomID, err)
		}
	}
	events, err := secondRooms.ListEvents(ctx, localRoom.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(events, "from first") {
		t.Fatalf("third device events = %#v", events)
	}
	secondActor, err := second.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.node.PostRemoteMessage(ctx, localRoom.ID, secondActor, room.MessagePostedPayload{Text: "from second"}); err != nil {
		t.Fatal(err)
	}
	events, err = firstRooms.ListEvents(ctx, localRoom.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(events, "from second") {
		t.Fatalf("first device events = %#v", events)
	}
}

func TestPairRejectsBadSecretAndExistingAccount(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest, _, _ := newEmptyNode(t)
	pairing, err := host.node.CreatePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, err := node.DecodePairCode(pairing.Code)
	if err != nil {
		t.Fatal(err)
	}
	code.Secret = strings.Repeat("x", len(code.Secret))
	tampered, err := node.EncodePairCode(*code)
	if err != nil {
		t.Fatal(err)
	}
	badJoin, err := guest.node.StartPairJoin(ctx, tampered, "Bad")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		view, err := guest.node.PairJoinStatus(ctx, badJoin.ID)
		return err == nil && view.Status == "failed"
	})

	other := newNode(t, "Other")
	if _, err := other.node.StartPairJoin(ctx, pairing.Code, "Other"); !errors.Is(err, account.ErrAlreadyExists) {
		t.Fatalf("join on existing account = %v, want ErrAlreadyExists", err)
	}
	if err := host.node.CancelPairing(ctx, pairing.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPairNeedsNodeTransport(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	empty, _, _ := newEmptyNode(t)
	// Both nodes are running; a nil service is the disabled case.
	var disabled *node.Service
	if _, err := disabled.CreatePairing(ctx); !errors.Is(err, node.ErrPairingDisabled) {
		t.Fatalf("CreatePairing on nil = %v", err)
	}
	if _, err := disabled.StartPairJoin(ctx, "x", "y"); !errors.Is(err, node.ErrPairingDisabled) {
		t.Fatalf("StartPairJoin on nil = %v", err)
	}
	if _, err := empty.node.StartPairJoin(ctx, "nonsense", "y"); err == nil {
		t.Fatal("invalid code accepted")
	}
	_ = host
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func containsText(events []room.Event, text string) bool {
	for i := range events {
		if strings.Contains(string(events[i].Payload), text) {
			return true
		}
	}
	return false
}

func TestPairCopiesRoomsInPages(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest, _, guestRooms := newEmptyNode(t)
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const roomCount = 65
	for i := 0; i < roomCount; i++ {
		created, _, err := host.rooms.CreateRoom(ctx, fmt.Sprintf("Room %02d", i), "", hostActor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := host.node.EnsureRoomHome(ctx, created.ID); err != nil {
			t.Fatal(err)
		}
	}
	pairing, err := host.node.CreatePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := guest.node.StartPairJoin(ctx, pairing.Code, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		list, err := host.node.ListPairings(ctx)
		if err != nil {
			return false
		}
		for _, item := range list {
			if item.ID == pairing.ID {
				return item.Status == "requested"
			}
		}
		return false
	})
	if _, err := host.node.ApprovePairing(ctx, pairing.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		view, err := guest.node.PairJoinStatus(ctx, joined.ID)
		if err != nil {
			return false
		}
		if view.Status == "failed" {
			t.Fatalf("join failed: %s", view.Error)
		}
		return view.Status == "ready"
	})
	rooms, err := guestRooms.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != roomCount {
		t.Fatalf("paired rooms = %d, want %d", len(rooms), roomCount)
	}
}
