package node_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/directory"
	"github.com/sidenor/toget/internal/directoryproto"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/relay"
	"github.com/sidenor/toget/internal/relayproto"
	"github.com/sidenor/toget/internal/room"
)

func startTestRelay(t *testing.T, parent string, sessionTTL time.Duration) *relay.Service {
	t.Helper()
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	service, err := relay.New(relay.Options{
		Listen: "127.0.0.1:0", Paths: paths, Parent: parent,
		PollWait: 250 * time.Millisecond, WaitTimeout: 5 * time.Second,
		QueueTTL: time.Minute, ResponseTTL: time.Minute, SessionTTL: sessionTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
		cancel()
	})
	return service
}

func startTestDirectory(t *testing.T, relayURL string) (*directory.Service, config.Paths) {
	t.Helper()
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	service, err := directory.New(directory.Options{
		Listen: "127.0.0.1:0", Paths: paths, RelayURL: relayURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
		cancel()
	})
	return service, paths
}

func waitForCondition(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func publishDirectoryRecord(t *testing.T, directoryURL string, identityManifest, nodeManifest []byte) (int, []byte) {
	t.Helper()
	base, fingerprint, err := node.ParsePinnedURL(directoryURL)
	if err != nil {
		t.Fatal(err)
	}
	config, err := node.PinnedTLSConfig(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(directoryproto.PublishRequest{
		Protocol:         directoryproto.Protocol,
		IdentityManifest: json.RawMessage(identityManifest),
		NodeManifest:     json.RawMessage(nodeManifest),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: true},
		Timeout:   10 * time.Second,
	}
	req, err := http.NewRequest(http.MethodPost, base+directoryproto.PublishPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestRelayFallbackDirectoryAndRoomTraffic(t *testing.T) {
	ctx := context.Background()
	relayService := startTestRelay(t, "", time.Minute)
	directoryService, directoryPaths := startTestDirectory(t, relayService.URL())

	host := newNodeWithOptions(t, "Host", func(opts *node.Options) {
		opts.Relay = relayService.URL()
		opts.Directory = directoryService.URL()
	})
	waitForCondition(t, 20*time.Second, "host relay registration", host.node.RelayConnected)

	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roomRecord, _, err := host.rooms.CreateRoom(ctx, "Relay room", "across networks", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, roomRecord.ID); err != nil {
		t.Fatal(err)
	}

	// The node publishes a signed manifest to the directory once its account
	// exists; the earlier attempts retry quickly.
	var record *node.DirectoryRecord
	waitForCondition(t, 20*time.Second, "directory publish", func() bool {
		got, err := host.node.LookupServer(ctx, host.snapshot.Server.Code)
		if err != nil {
			return false
		}
		record = got
		return true
	})
	if record.ServerID != host.snapshot.Server.ID || record.ServerCode != host.snapshot.Server.Code {
		t.Fatalf("directory record = %#v", record)
	}
	if !strings.Contains(record.URL, "&relay=") || record.DirectURL == "" {
		t.Fatalf("directory URL is missing the relay fallback: %q", record.URL)
	}

	// A tampered node manifest is rejected, and an older but validly signed
	// manifest cannot roll the record back.
	hostDeviceKey, err := host.accounts.UnsealDeviceKey(host.snapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	hostClient, err := node.NewClientForURL(host.node.URL(), host.snapshot.Account.ID, host.snapshot.Device.ID, hostDeviceKey)
	if err != nil {
		t.Fatal(err)
	}
	defer hostClient.Close()
	firstManifest, err := hostClient.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	secondManifest, err := hostClient.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, _ := json.Marshal(secondManifest)
	publishStatus, publishData := publishDirectoryRecord(t, directoryService.URL(), []byte(host.snapshot.Server.Manifest), secondJSON)
	if publishStatus != http.StatusOK {
		t.Fatalf("directory accepted manifest = %d %s", publishStatus, publishData)
	}
	tampered := *secondManifest
	tampered.Endpoint = "https://attacker.example:7717"
	tamperedJSON, _ := json.Marshal(&tampered)
	publishStatus, publishData = publishDirectoryRecord(t, directoryService.URL(), []byte(host.snapshot.Server.Manifest), tamperedJSON)
	if publishStatus != http.StatusBadRequest {
		t.Fatalf("directory accepted a tampered manifest = %d %s", publishStatus, publishData)
	}
	firstJSON, _ := json.Marshal(firstManifest)
	publishStatus, publishData = publishDirectoryRecord(t, directoryService.URL(), []byte(host.snapshot.Server.Manifest), firstJSON)
	if publishStatus != http.StatusConflict {
		t.Fatalf("directory accepted a stale manifest = %d %s", publishStatus, publishData)
	}

	// The directory persists verified records across a restart.
	reloaded, err := directory.New(directory.Options{
		Paths: directoryPaths, RelayURL: relayService.URL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reloadedServer := httptest.NewServer(reloaded.Handler())
	defer reloadedServer.Close()
	resp, err := http.Get(reloadedServer.URL + directoryproto.LookupPrefix + host.snapshot.Server.Code)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reloaded directory lookup = %d %s", resp.StatusCode, data)
	}
	var lookup directoryproto.LookupResponse
	if err := json.Unmarshal(data, &lookup); err != nil || len(lookup.NodeManifest) == 0 {
		t.Fatalf("reloaded lookup body = %s", data)
	}

	guest := newNode(t, "Guest")
	invite, _, err := host.rooms.CreateInvite(ctx, roomRecord.ID, hostActor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, roomRecord.ID, invite.Secret, roomRecord.Name)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := guest.node.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}

	// Force the direct path to fail. The signed relay route must carry the
	// room sync even though the stored LAN endpoint is unreachable.
	hostRoute, err := host.node.RelayRoute(ctx)
	if err != nil || hostRoute == "" {
		t.Fatalf("host relay route = %q, %v", hostRoute, err)
	}
	fakeDirect := "https://127.0.0.1:1#fp=" + host.node.Fingerprint()
	composite := relayproto.JoinAddress(fakeDirect, hostRoute)
	if _, err := guest.rooms.SetHomeInfo(ctx, joined.ID, host.snapshot.Server.ID, composite); err != nil {
		t.Fatal(err)
	}

	if _, err := host.rooms.PostMessage(ctx, roomRecord.ID, hostActor, room.MessagePostedPayload{Text: "through the relay"}); err != nil {
		t.Fatal(err)
	}
	if n, err := guest.node.SyncRoom(ctx, joined.ID); err != nil || n != 1 {
		t.Fatalf("relay sync = %d, %v", n, err)
	}
	events, err := guest.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if strings.Contains(string(event.Payload), "through the relay") {
			found = true
		}
	}
	if !found {
		t.Fatalf("guest did not receive the relayed message: %#v", events)
	}

	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := guest.node.PostRemoteMessage(ctx, joined.ID, guestActor, room.MessagePostedPayload{Text: "from the guest over relay"})
	if err != nil {
		t.Fatal(err)
	}
	if posted.AuthorID != guest.snapshot.Account.ID {
		t.Fatalf("posted event = %#v", posted)
	}
	hostEvents, err := host.rooms.ListEvents(ctx, roomRecord.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range hostEvents {
		if strings.Contains(string(event.Payload), "from the guest over relay") {
			found = true
		}
	}
	if !found {
		t.Fatalf("host did not receive the guest message: %#v", hostEvents)
	}

	// A client with a broken direct endpoint but a valid relay route works.
	deviceKey, err := guest.accounts.UnsealDeviceKey(guest.snapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	client, err := node.NewClientForURL(composite, guest.snapshot.Account.ID, guest.snapshot.Device.ID, deviceKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	manifest, err := client.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ServerID != host.snapshot.Server.ID {
		t.Fatalf("relayed manifest = %#v", manifest)
	}

	// Conversely, a broken relay URL does not matter while the direct LAN
	// endpoint is reachable.
	badRelay, err := relayproto.NodeRouteURL("https://127.0.0.1:1", host.node.Fingerprint(), host.snapshot.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	directPreferred := relayproto.JoinAddress(host.node.URL(), badRelay)
	client, err = node.NewClientForURL(directPreferred, guest.snapshot.Account.ID, guest.snapshot.Device.ID, deviceKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Info(ctx); err != nil {
		t.Fatalf("direct-first client: %v", err)
	}
}

func TestMultiHopRelayRoutes(t *testing.T) {
	ctx := context.Background()
	parent := startTestRelay(t, "", time.Minute)
	child := startTestRelay(t, parent.URL(), time.Minute)

	host := newNodeWithOptions(t, "Host", func(opts *node.Options) {
		opts.Relay = child.URL()
	})
	waitForCondition(t, 20*time.Second, "host relay registration", host.node.RelayConnected)

	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roomRecord, _, err := host.rooms.CreateRoom(ctx, "Multi-hop room", "", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, roomRecord.ID); err != nil {
		t.Fatal(err)
	}
	guest := newNode(t, "Guest")
	invite, _, err := host.rooms.CreateInvite(ctx, roomRecord.ID, hostActor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, roomRecord.ID, invite.Secret, roomRecord.Name)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := guest.node.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}

	parentBase, parentFP, err := node.ParsePinnedURL(parent.URL())
	if err != nil {
		t.Fatal(err)
	}
	parentRoute, err := relayproto.NodeRouteURL(parentBase, parentFP, host.snapshot.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	fakeDirect := "https://127.0.0.1:1#fp=" + host.node.Fingerprint()
	if _, err := guest.rooms.SetHomeInfo(ctx, joined.ID, host.snapshot.Server.ID, relayproto.JoinAddress(fakeDirect, parentRoute)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.rooms.PostMessage(ctx, roomRecord.ID, hostActor, room.MessagePostedPayload{Text: "via two relays"}); err != nil {
		t.Fatal(err)
	}

	var lastErr error
	waitForCondition(t, 20*time.Second, "multi-hop route", func() bool {
		n, err := guest.node.SyncRoom(ctx, joined.ID)
		lastErr = err
		return err == nil && n == 1
	})
	if lastErr != nil {
		t.Fatalf("multi-hop sync: %v", lastErr)
	}
	events, err := guest.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if strings.Contains(string(event.Payload), "via two relays") {
			found = true
		}
	}
	if !found {
		t.Fatalf("guest did not receive the multi-hop message: %#v", events)
	}
}
