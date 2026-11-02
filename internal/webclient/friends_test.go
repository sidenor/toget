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

func TestBrowserSentFriendRequestsAndCancel(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	bob := register(t, f, "Bob", "bob@example.com")
	bobID := bob.Profile().AccountID

	resolved, err := alice.ResolveAccount(ctx, bobID)
	if err != nil {
		t.Fatal(err)
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

	outgoing, err := alice.ListSentFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(outgoing) != 1 {
		t.Fatalf("alice sent requests = %+v", outgoing)
	}
	if outgoing[0].ToAccountID != bobID || outgoing[0].ToName != "Bob" || outgoing[0].RoomID == "" {
		t.Fatalf("sent request = %+v", outgoing[0])
	}
	if outgoing[0].ExpiresAt.Before(outgoing[0].CreatedAt) {
		t.Fatalf("sent request expires before it was created: %+v", outgoing[0])
	}

	inbox, err := bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("bob inbox = %+v", inbox)
	}

	if err := alice.CancelFriendRequest(ctx, outgoing[0].ID); err != nil {
		t.Fatal(err)
	}
	outgoing, err = alice.ListSentFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(outgoing) != 0 {
		t.Fatalf("cancelled request remained in sent list: %+v", outgoing)
	}
	refresh, err := alice.RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refresh.Rooms) != 0 {
		t.Fatalf("cancelled request left a pending room: %+v", refresh.Rooms)
	}
	inbox, err = bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("cancelled request remained in bob's inbox: %+v", inbox)
	}

	// Cancelling again must report the request as gone.
	if err := alice.CancelFriendRequest(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("cancelling an unknown request succeeded")
	} else if coded := webclient.AsError(err); coded == nil || coded.Code != "friend_request_not_found" {
		t.Fatalf("unknown request error = %v", err)
	}

	// Once the recipient accepts, the request disappears from both sides.
	if _, err := alice.AddFriend(ctx, bobID, "Bob", devices); err != nil {
		t.Fatal(err)
	}
	inbox, err = bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("bob inbox before accept = %+v", inbox)
	}
	if _, err := bob.AcceptFriendRequest(ctx, inbox[0].ID); err != nil {
		t.Fatal(err)
	}
	outgoing, err = alice.ListSentFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(outgoing) != 0 {
		t.Fatalf("accepted request remained in sent list: %+v", outgoing)
	}
}
