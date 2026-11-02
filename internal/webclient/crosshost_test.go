package webclient_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/directory"
	"github.com/sidenor/toget/internal/directoryclient"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/webclient"
)

type crossHost struct {
	service *hosted.Service
	server  *httptest.Server
	origin  string
}

type federationFixture struct {
	directoryURL string
	hostA        *crossHost
	hostB        *crossHost
}

func newCrossHost(t *testing.T, name string, lookup func(context.Context, string) (*hosted.DirectoryRecord, error)) *crossHost {
	t.Helper()
	store, err := hosted.OpenStore(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsureHost(context.Background(), name, []identity.Endpoint{
		{Transport: "https", Addr: "127.0.0.1:7722"},
	}); err != nil {
		t.Fatal(err)
	}
	service, err := hosted.New(hosted.Options{
		Store: store, Mailer: &hosted.MemoryMailer{}, DisableRateLimit: true,
		DirectoryLookup: lookup,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)
	return &crossHost{service: service, server: server, origin: server.URL}
}

func newFederationFixture(t *testing.T) *federationFixture {
	t.Helper()
	directoryService, err := directory.New(directory.Options{
		Listen: "127.0.0.1:0", Paths: config.Paths{Home: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryService.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directoryService.Close() })
	directoryURL := directoryService.URL()

	lookup := func(ctx context.Context, code string) (*hosted.DirectoryRecord, error) {
		return directoryclient.Fetch(ctx, directoryURL, code)
	}
	hostA := newCrossHost(t, "Host A", lookup)
	hostB := newCrossHost(t, "Host B", lookup)
	fixture := &federationFixture{directoryURL: directoryURL, hostA: hostA, hostB: hostB}

	// Both browser clients reach both test servers directly, so trust both
	// self-signed certificates for the duration of the test.
	pool := x509.NewCertPool()
	pool.AddCert(hostA.server.Certificate())
	pool.AddCert(hostB.server.Certificate())
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: pool,
		},
		ForceAttemptHTTP2: true,
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		transport.CloseIdleConnections()
	})

	for _, host := range []*crossHost{hostA, hostB} {
		manifest, err := host.service.SignHostManifest(
			host.server.URL, hosted.Fingerprint(host.server.Certificate()), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := directoryclient.PublishHost(context.Background(), directoryURL,
			host.service.Manifest(), manifest); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func registerBrowser(t *testing.T, host *crossHost, name, email string) *webclient.Client {
	t.Helper()
	client, err := webclient.New(host.origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Register(context.Background(), webclient.RegisterInput{
		Name: name, Email: email, Password: "correct horse battery staple",
		DeviceName: name + " 的浏览器",
	}); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestBrowserCrossHostFriendshipAndRoom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newFederationFixture(t)

	alice := registerBrowser(t, f.hostA, "Alice", "alice@example.com")
	bob := registerBrowser(t, f.hostB, "Bob", "bob@example.com")
	aliceID := alice.Profile().AccountID
	bobID := bob.Profile().AccountID
	if aliceID[:8] == bobID[:8] {
		t.Fatal("the two test hosts have the same server code")
	}

	resolved, err := alice.ResolveAccount(ctx, bobID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Kind != "account" || resolved.HostID != f.hostB.service.HostID() ||
		resolved.HostCode != bobID[:8] {
		t.Fatalf("resolved = %+v", resolved)
	}
	if len(resolved.Devices) == 0 {
		t.Fatal("remote lookup returned no devices")
	}

	addBob := func() (*webclient.FriendRequestSent, string) {
		t.Helper()
		devices := make([]webclient.FriendRequestDevice, 0, len(resolved.Devices))
		for _, device := range resolved.Devices {
			devices = append(devices, webclient.FriendRequestDevice{
				ID: device.ID, EncryptionPublicKey: device.EncryptionPublicKey,
			})
		}
		sent, err := alice.AddFriend(ctx, bobID, "Bob", devices)
		if err != nil {
			t.Fatal(err)
		}
		sentList, err := alice.ListSentFriendRequests(ctx)
		if err != nil {
			t.Fatal(err)
		}
		roomID := ""
		for _, item := range sentList {
			if item.ID == sent.ID {
				roomID = item.RoomID
				break
			}
		}
		if roomID == "" {
			t.Fatalf("sent request %s is not listed", sent.ID)
		}
		return sent, roomID
	}

	// A cross-host request reaches the recipient and can be cancelled from the
	// sender before it is answered.
	first, firstRoom := addBob()
	requests, err := bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != first.ID ||
		requests[0].FromAccountID != aliceID || requests[0].RoomID != firstRoom {
		t.Fatalf("incoming requests = %+v", requests)
	}
	if err := alice.CancelFriendRequest(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	requests, err = bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("cancelled request is still listed: %+v", requests)
	}

	// Declining tells the sender's host to drop the outgoing request at once.
	second, _ := addBob()
	requests, err = bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != second.ID {
		t.Fatalf("incoming requests = %+v", requests)
	}
	if err := bob.DeclineFriendRequest(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	sent, err := alice.ListSentFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("declined request is still outgoing: %+v", sent)
	}

	// A third request is accepted, which joins Bob to Alice's room on host A.
	third, _ := addBob()
	requests, err = bob.ListFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != third.ID {
		t.Fatalf("incoming requests = %+v", requests)
	}
	bobRoom, err := bob.AcceptFriendRequest(ctx, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bobRoom.Summary.ID != requests[0].RoomID {
		t.Fatalf("joined room = %s, want %s", bobRoom.Summary.ID, requests[0].RoomID)
	}
	if bobRoom.Summary.PeerAccountID != aliceID {
		t.Fatalf("peer = %+v", bobRoom.Summary)
	}
	// Joining the room answers the request, so it must disappear from the
	// sender's outgoing list too, not only from the recipient's inbox.
	sent, err = alice.ListSentFriendRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("accepted request is still outgoing: %+v", sent)
	}
	// The remote room must survive a room-list refresh even though the account
	// host knows nothing about it.
	refreshed, err := bob.RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, room := range refreshed.Rooms {
		if room.ID == bobRoom.Summary.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("remote room missing after refresh: %+v", refreshed.Rooms)
	}

	// Chat across the two hosts.
	if _, err := bob.SendMessage(ctx, bobRoom.Summary.ID, "你好，我从另一台服务器来"); err != nil {
		t.Fatal(err)
	}
	aliceRoom, err := alice.SyncRoom(ctx, requests[0].RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(aliceRoom, "你好，我从另一台服务器来") {
		t.Fatalf("Alice did not receive Bob's message: %+v", aliceRoom.Messages)
	}
	if !hasMember(aliceRoom, bobID) {
		t.Fatalf("Bob is not an active member: %+v", aliceRoom.Members)
	}
	if _, err := alice.SendMessage(ctx, requests[0].RoomID, "收到，欢迎过来"); err != nil {
		t.Fatal(err)
	}
	bobRoom, err = bob.SyncRoom(ctx, bobRoom.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(bobRoom, "收到，欢迎过来") {
		t.Fatalf("Bob did not receive Alice's message: %+v", bobRoom.Messages)
	}

	// An invite created by a remote member names host A, not Bob's account
	// host, so a third browser on host B can join the group on host A.
	group, err := alice.CreateRoom(ctx, webclient.CreateRoomInput{Name: "跨服群"})
	if err != nil {
		t.Fatal(err)
	}
	groupInvite, err := alice.CreateInvite(ctx, group.Summary.ID, webclient.InviteInput{
		History: "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	bobGroup, err := bob.JoinInvite(ctx, groupInvite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if bobGroup.Summary.ID != group.Summary.ID {
		t.Fatalf("bob joined %s", bobGroup.Summary.ID)
	}
	remoteInvite, err := bob.CreateInvite(ctx, group.Summary.ID, webclient.InviteInput{
		History: "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(remoteInvite.Code, "toget-invite:v1:") {
		t.Fatalf("invite = %q", remoteInvite.Code)
	}
	carol := registerBrowser(t, f.hostB, "Carol", "carol@example.com")
	carolRoom, err := carol.JoinInvite(ctx, remoteInvite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if carolRoom.Summary.ID != group.Summary.ID {
		t.Fatalf("carol joined %s", carolRoom.Summary.ID)
	}
	if !hasMember(carolRoom, bobID) {
		t.Fatalf("carol does not see Bob as a member: %+v", carolRoom.Members)
	}
}

func hasText(room *webclient.RoomView, want string) bool {
	for _, message := range room.Messages {
		if message.Text == want {
			return true
		}
	}
	return false
}

func hasMember(room *webclient.RoomView, accountID string) bool {
	for _, member := range room.Members {
		if member.AccountID == accountID && member.Status == "active" {
			return true
		}
	}
	return false
}
