package node_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/node"
)

// pairNow approves the joining node onto the approver's account and waits for
// the join to import the account.
func pairNow(t *testing.T, approver, joiner *testNode, deviceName string) {
	t.Helper()
	ctx := context.Background()
	pairing, err := approver.node.CreatePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := joiner.node.StartPairJoin(ctx, pairing.Code, deviceName)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		list, err := approver.node.ListPairings(ctx)
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
	if _, err := approver.node.ApprovePairing(ctx, pairing.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, func() bool {
		view, err := joiner.node.PairJoinStatus(ctx, joined.ID)
		if err != nil {
			return false
		}
		if view.Status == "ready" {
			return true
		}
		if view.Status == "failed" || view.Status == "canceled" || view.Status == "expired" {
			t.Fatalf("pairing ended %s: %s", view.Status, view.Error)
		}
		return false
	})
}

func deviceIDs(t *testing.T, nodeUnderTest *testNode) []string {
	t.Helper()
	devices, err := nodeUnderTest.accounts.ListDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(devices))
	for i := range devices {
		out = append(out, devices[i].ID)
	}
	return out
}

func hasDevice(t *testing.T, nodeUnderTest *testNode, deviceID string) bool {
	t.Helper()
	for _, id := range deviceIDs(t, nodeUnderTest) {
		if id == deviceID {
			return true
		}
	}
	return false
}

func TestPairingTriggersAccountSyncAcrossSiblings(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Aiden")
	guest, guestAccounts, _ := newEmptyNode(t)
	pairNow(t, host, guest, "Laptop")

	third, thirdAccounts, _ := newEmptyNode(t)
	pairNow(t, guest, third, "Tablet")
	thirdSnapshot, err := thirdAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	guestSnapshot, err := guestAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Pairing through a sibling still records the original account host, not
	// the sibling's local server.
	for name, snapshot := range map[string]*account.Snapshot{
		"guest": guestSnapshot, "third": thirdSnapshot,
	} {
		if snapshot.Account.OriginServerID != host.snapshot.Server.ID {
			t.Fatalf("%s origin server = %q, want %q", name,
				snapshot.Account.OriginServerID, host.snapshot.Server.ID)
		}
		if snapshot.Account.ServerID == host.snapshot.Server.ID {
			t.Fatalf("%s local server was not kept separate from the origin", name)
		}
	}

	// Pairing the third device triggers an account sync from its approver, so
	// the host learns it without waiting for the periodic round.
	waitFor(t, 15*time.Second, func() bool {
		return hasDevice(t, host, thirdSnapshot.Device.ID)
	})
	devices, err := host.accounts.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range devices {
		if devices[i].ID == thirdSnapshot.Device.ID && devices[i].NodeURL == "" {
			t.Fatal("third device was learned without its node address")
		}
	}

	// An explicit sync is still idempotent and reaches the peer.
	result, err := host.node.SyncAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Synced == 0 {
		t.Fatalf("account sync did not reach the paired device: %+v", result)
	}
}

func TestAccountSyncPropagatesRevocationToRoomHomeAndRevokedDevice(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Aiden")
	guest, guestAccounts, _ := newEmptyNode(t)

	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := host.rooms.CreateRoom(ctx, "Sync room", "shared", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	pairNow(t, host, guest, "Laptop")

	third, thirdAccounts, _ := newEmptyNode(t)
	pairNow(t, guest, third, "Tablet")
	thirdSnapshot, err := thirdAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The third device joins the host's room through its paired room snapshot.
	waitFor(t, 15*time.Second, func() bool {
		devices, err := host.rooms.ListDevices(ctx, record.ID)
		if err != nil {
			return false
		}
		for i := range devices {
			if devices[i].DeviceID == thirdSnapshot.Device.ID {
				return true
			}
		}
		return false
	})
	// The revoking device learns the room membership through a room sync;
	// account records alone do not tell it who belongs to which room.
	if _, err := guest.node.SyncRoom(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		devices, err := guest.rooms.ListDevices(ctx, record.ID)
		if err != nil {
			return false
		}
		for i := range devices {
			if devices[i].DeviceID == thirdSnapshot.Device.ID {
				return true
			}
		}
		return false
	})
	before, err := host.rooms.GetRoom(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}

	guestSnapshot, err := guestAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := guest.node.RevokeAccountDevice(ctx, thirdSnapshot.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("revocation warnings: %v", warnings)
	}
	after, err := host.rooms.GetRoom(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Epoch <= before.Epoch {
		t.Fatalf("room epoch did not rotate: %d -> %d", before.Epoch, after.Epoch)
	}
	devices, err := host.rooms.ListDevices(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range devices {
		if devices[i].DeviceID == thirdSnapshot.Device.ID && devices[i].RevokedAt == nil {
			t.Fatal("third device is still active in the room")
		}
	}

	// The revoked device still reaches a sibling read-only and discovers its
	// own revocation. The revoking device must be a different device.
	thirdDeviceID := thirdSnapshot.Device.ID
	thirdKey, err := third.accounts.UnsealDeviceKey(thirdSnapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	thirdRecords, err := third.accounts.ExportDeviceRecords(ctx, third.node.URL())
	if err != nil {
		t.Fatal(err)
	}
	thirdClient, err := node.NewClientForURL(guest.node.URL(), thirdSnapshot.Account.ID, thirdDeviceID, thirdKey)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := thirdClient.SyncAccount(ctx, node.AccountSyncRequest{
		AccountID: thirdSnapshot.Account.ID, DeviceID: thirdDeviceID, Records: thirdRecords,
	})
	thirdClient.Close()
	if err != nil {
		t.Fatalf("revoked device read-only sync: %v", err)
	}
	if !readOnly.ReadOnly {
		t.Fatal("revoked device was not told its session is read-only")
	}

	result, err := third.node.SyncAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Synced == 0 {
		t.Fatalf("revoked device could not reach a sibling: %+v", result)
	}
	thirdSnapshot, err = thirdAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if thirdSnapshot.Device.RevokedAt == nil {
		t.Fatal("revoked device did not learn its own revocation")
	}
	if _, err := third.node.LocalActor(ctx); err == nil {
		t.Fatal("revoked device can still act as a room signer")
	}
	if guestSnapshot.Device.ID == thirdDeviceID {
		t.Fatal("test revoked the wrong device")
	}
}

func TestAccountSyncBootstrapsThroughSavedCard(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest, guestAccounts, _ := newEmptyNode(t)
	pairNow(t, host, guest, "Laptop")

	// Simulate a device paired before node addresses were stored: the only
	// sibling has no reachable address. The account host is still resolvable
	// from the account code through a saved card.
	if err := guestAccounts.SetDeviceNodeURL(ctx, host.snapshot.Device.ID, ""); err != nil {
		t.Fatal(err)
	}
	card, err := host.node.OwnCard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.AddCard(ctx, card.Card, "Host card"); err != nil {
		t.Fatal(err)
	}

	result, err := guest.node.SyncAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Synced == 0 {
		t.Fatalf("bootstrap sync did not reach the account host: %+v", result)
	}
	hostDevice, err := guestAccounts.GetDeviceByID(ctx, host.snapshot.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hostDevice.NodeURL == "" {
		t.Fatal("bootstrap sync did not learn the account host address")
	}
	guestSnapshot, err := guestAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	remoteGuest, err := host.accounts.GetDeviceByID(ctx, guestSnapshot.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if remoteGuest.NodeURL == "" {
		t.Fatal("account host did not learn the bootstrapping device address")
	}
}

func TestAccountSyncRerevokesLateRoomRegistration(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest, _, _ := newEmptyNode(t)
	pairNow(t, host, guest, "Laptop")

	third, thirdAccounts, _ := newEmptyNode(t)
	pairNow(t, guest, third, "Tablet")
	thirdSnapshot, err := thirdAccounts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, func() bool {
		return hasDevice(t, host, thirdSnapshot.Device.ID)
	})

	// The room is created after the third device was paired, so it is not in
	// the room yet. Revocation propagation therefore has nothing to do there
	// on its first pass.
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := host.rooms.CreateRoom(ctx, "Late room", "late registration", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.RevokeAccountDevice(ctx, thirdSnapshot.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.SyncAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.SyncAccount(ctx); err != nil {
		t.Fatal(err)
	}
	thirdDevice, err := host.accounts.GetDeviceByID(ctx, thirdSnapshot.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thirdDevice.RevokedAt == nil {
		t.Fatal("account host did not learn the revocation")
	}
	before, err := host.rooms.GetRoom(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The revoked device registers with the room after the first propagation
	// pass, which can happen while a room home is still learning account
	// revocations. The next account-sync round must revoke it again.
	if _, _, err := host.rooms.RegisterDevice(ctx, record.ID, []byte(thirdSnapshot.Device.Certificate)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.SyncAccount(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := host.rooms.GetRoom(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Epoch <= before.Epoch {
		t.Fatalf("room epoch did not rotate after late registration: %d -> %d", before.Epoch, after.Epoch)
	}
	devices, err := host.rooms.ListDevices(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range devices {
		if devices[i].DeviceID == thirdSnapshot.Device.ID && devices[i].RevokedAt == nil {
			t.Fatal("late-registered revoked device is still active")
		}
	}
}

func TestAccountSyncRejectsImpostorAndForeignAccount(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	hostRecords, err := host.accounts.ExportDeviceRecords(ctx, host.node.URL())
	if err != nil {
		t.Fatal(err)
	}

	_, impostorKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	impostor, err := node.NewClientForURL(host.node.URL(), host.snapshot.Account.ID, host.snapshot.Device.ID, impostorKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = impostor.SyncAccount(ctx, node.AccountSyncRequest{
		AccountID: host.snapshot.Account.ID, DeviceID: host.snapshot.Device.ID, Records: hostRecords,
	})
	impostor.Close()
	var remoteErr *node.RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.Status != http.StatusUnauthorized {
		t.Fatalf("impostor sync error = %v, want 401", err)
	}

	other := newNode(t, "Other")
	otherKey, err := other.accounts.UnsealDeviceKey(other.snapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	otherRecords, err := other.accounts.ExportDeviceRecords(ctx, other.node.URL())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := node.NewClientForURL(host.node.URL(), other.snapshot.Account.ID, other.snapshot.Device.ID, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreign.SyncAccount(ctx, node.AccountSyncRequest{
		AccountID: other.snapshot.Account.ID, DeviceID: other.snapshot.Device.ID, Records: otherRecords,
	})
	foreign.Close()
	if !errors.As(err, &remoteErr) || remoteErr.Status != http.StatusForbidden {
		t.Fatalf("foreign account sync error = %v, want 403", err)
	}
}
