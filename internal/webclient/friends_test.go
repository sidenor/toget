package webclient_test

import (
	"context"
	"testing"

	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/webclient"
)

func TestBrowserFriendRequestByID(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	bob := register(t, f, "Bob", "bob@example.com")
	bobID := bob.Profile().AccountID

	resolved, err := alice.ResolveAccount(ctx, bobID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "Bob" || resolved.Self || resolved.Kind != "account" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if len(resolved.Devices) == 0 {
		t.Fatal("local lookup did not return target devices")
	}
	devices := make([]webclient.FriendRequestDevice, 0, len(resolved.Devices))
	for _, device := range resolved.Devices {
		devices = append(devices, webclient.FriendRequestDevice{
			ID: device.ID, EncryptionPublicKey: device.EncryptionPublicKey,
		})
	}

	if _, err := alice.AddFriend(ctx, bobID, "Bob", devices); err != nil {
		t.Fatal(err)
	}
	requests, err := bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("bob requests = %+v", requests)
	}
	request := requests[0]
	if request.FromAccountID != alice.Profile().AccountID || request.FromName != "Alice" ||
		request.RoomName != "Bob" || request.RoomTopic != "" || request.Code == "" {
		t.Fatalf("request = %+v", request)
	}
	view, err := bob.AcceptFriendRequest(ctx, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Kind != room.RoomKindDirect || view.Summary.MemberCount != 2 {
		t.Fatalf("accepted room = %+v", view.Summary)
	}
	requests, err = bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("accepted request remained in inbox: %+v", requests)
	}

	refresh, err := alice.RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refresh.Rooms) != 1 || refresh.Rooms[0].Kind != room.RoomKindDirect ||
		refresh.Rooms[0].MemberCount != 2 || refresh.Rooms[0].PeerName != "Bob" {
		t.Fatalf("alice rooms = %+v", refresh.Rooms)
	}

	// A second request can be declined without joining the room.
	aliceID := alice.Profile().AccountID
	aliceResolved, err := bob.ResolveAccount(ctx, aliceID)
	if err != nil {
		t.Fatal(err)
	}
	aliceDevices := make([]webclient.FriendRequestDevice, 0, len(aliceResolved.Devices))
	for _, device := range aliceResolved.Devices {
		aliceDevices = append(aliceDevices, webclient.FriendRequestDevice{
			ID: device.ID, EncryptionPublicKey: device.EncryptionPublicKey,
		})
	}
	if _, err := bob.AddFriend(ctx, aliceID, "Alice", aliceDevices); err != nil {
		t.Fatal(err)
	}
	requests, err = alice.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].FromName != "Bob" {
		t.Fatalf("alice requests = %+v", requests)
	}
	if err := alice.DeclineFriendRequest(ctx, requests[0].ID); err != nil {
		t.Fatal(err)
	}
	requests, err = alice.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("declined request remained in inbox: %+v", requests)
	}
}
