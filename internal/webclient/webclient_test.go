package webclient_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/webclient"
)

type fixture struct {
	server *httptest.Server
	origin string
	mailer *hosted.MemoryMailer

	mu         sync.Mutex
	joinBodies [][]byte
}

func (f *fixture) capturedJoinBodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.joinBodies...)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := hosted.OpenStore(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsureHost(context.Background(), "Web Test Host", []identity.Endpoint{
		{Transport: "https", Addr: "127.0.0.1:7722"},
	}); err != nil {
		t.Fatal(err)
	}
	mailer := &hosted.MemoryMailer{}
	service, err := hosted.New(hosted.Options{
		Store: store, Mailer: mailer, PublicURL: "https://toget.test",
		DisableRateLimit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{mailer: mailer}
	handler := service.Handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == hosted.RoomJoinPath {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				f.mu.Lock()
				f.joinBodies = append(f.joinBodies, body)
				f.mu.Unlock()
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	// NewBrowserClient uses the process default transport. Trust this test
	// server's self-signed certificate for the duration of the test.
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	f.server = server
	f.origin = server.URL
	return f
}

func register(t *testing.T, f *fixture, name, email string) *webclient.Client {
	t.Helper()
	client, err := webclient.New(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Register(context.Background(), webclient.RegisterInput{
		Name: name, Email: email, Password: "correct horse battery staple", DeviceName: name + " 的浏览器",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovery == nil || result.Recovery.Content == "" {
		t.Fatal("registration did not return a recovery file")
	}
	if result.Profile.Name != name || result.Profile.RoomCount != 0 {
		t.Fatalf("profile = %+v", result.Profile)
	}
	return client
}

func login(t *testing.T, f *fixture, accountID, deviceName string) *webclient.Client {
	t.Helper()
	client, err := webclient.New(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Login(context.Background(), webclient.LoginInput{
		AccountID: accountID, Password: "correct horse battery staple", DeviceName: deviceName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Profile.EmailVerified && result.Profile.AccountID == "" {
		t.Fatal("login did not return a profile")
	}
	return client
}

func TestBrowserAccountRoomAndMultiDevice(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	alice := register(t, f, "Alice", "alice@example.com")
	profile := alice.Profile()
	if profile == nil || len(profile.AccountID) != 14 {
		t.Fatalf("profile = %+v", profile)
	}
	// A browser session is restored without the password.
	restored, err := webclient.New(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := alice.Export()
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if restored.Profile() == nil || restored.Profile().AccountID != profile.AccountID {
		t.Fatal("resume lost the account")
	}

	view, err := alice.CreateRoom(ctx, webclient.CreateRoomInput{Name: "项目讨论", Topic: "周五上线"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Name != "项目讨论" || view.Summary.Topic != "周五上线" || view.MyRole != "owner" {
		t.Fatalf("created room = %+v", view)
	}
	if len(view.Messages) == 0 || view.Messages[0].Kind != "system" {
		t.Fatalf("created room timeline = %+v", view.Messages)
	}

	message, err := alice.SendMessage(ctx, view.Summary.ID, "第一条加密消息")
	if err != nil {
		t.Fatal(err)
	}
	if message == nil || message.Text != "第一条加密消息" || message.AuthorID != profile.AccountID {
		t.Fatalf("message = %+v", message)
	}

	// Signing in on a second browser restores room metadata and keys from the
	// encrypted vault, then syncs the encrypted event log.
	browser := login(t, f, profile.AccountID, "第二台浏览器")

	// The first render after sign-in happens before the room's sealed events
	// arrive. Every collection must still serialize as an array; JSON null
	// used to crash the chat view on a restored vault with no messages yet.
	pending, err := browser.Room(view.Summary.ID)
	if err != nil {
		t.Fatalf("Room before sync: %v", err)
	}
	pendingJSON, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"messages", "members", "devices"} {
		if strings.Contains(string(pendingJSON), `"`+field+`":null`) {
			t.Fatalf("empty room serialized %s as null: %s", field, pendingJSON)
		}
	}

	refresh, err := browser.RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refresh.Rooms) != 1 || refresh.Rooms[0].Name != "项目讨论" || refresh.Rooms[0].Unread != 1 {
		t.Fatalf("refresh = %+v", refresh)
	}
	second, err := browser.Room(refresh.Rooms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.LockedCount != 0 || len(second.Messages) != 2 {
		t.Fatalf("second device view = %+v", second)
	}
	if second.Messages[1].Text != "第一条加密消息" {
		t.Fatalf("second device messages = %+v", second.Messages)
	}
	if err := browser.MarkRead(ctx, second.Summary.ID, second.Summary.LastSeq); err != nil {
		t.Fatal(err)
	}

	reply, err := browser.SendMessage(ctx, second.Summary.ID, "收到，我来看看")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "收到，我来看看" {
		t.Fatalf("reply = %+v", reply)
	}
	aliceView, err := alice.SyncRoom(ctx, view.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceView.LockedCount != 0 || len(aliceView.Messages) != 3 ||
		aliceView.Messages[2].Text != "收到，我来看看" {
		t.Fatalf("alice view = %+v", aliceView)
	}

	// A second account redeems an invite with full history.
	invite, err := alice.CreateInvite(ctx, view.Summary.ID, webclient.InviteInput{
		History: "all", TTLHours: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(invite.Code, "toget-invite:v1:") {
		t.Fatalf("invite = %q", invite.Code)
	}
	bob := register(t, f, "Bob", "bob@example.com")
	joined, err := bob.JoinInvite(ctx, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Summary.Name != "项目讨论" || joined.LockedCount != 0 {
		t.Fatalf("joined = %+v", joined)
	}
	if len(joined.Messages) != 3 {
		t.Fatalf("joined messages = %+v", joined.Messages)
	}
	if _, err := bob.SendMessage(ctx, joined.Summary.ID, "我也能看到历史"); err != nil {
		t.Fatal(err)
	}
	aliceView, err = alice.SyncRoom(ctx, view.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceView.Summary.MemberCount != 2 || len(aliceView.Messages) != 4 {
		t.Fatalf("alice after join = %+v", aliceView)
	}

	// Owner rename is encrypted and reaches the other member.
	renamed, err := alice.RenameRoom(ctx, view.Summary.ID, "项目讨论（正式）", "周五 20:00 上线")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Summary.Name != "项目讨论（正式）" {
		t.Fatalf("renamed = %+v", renamed)
	}
	bobView, err := bob.SyncRoom(ctx, joined.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bobView.Summary.Name != "项目讨论（正式）" || bobView.Summary.Topic != "周五 20:00 上线" {
		t.Fatalf("bob rename = %+v", bobView)
	}

	// Bob leaves. Alice's next sync must rotate the epoch before she can send.
	if err := bob.LeaveRoom(ctx, joined.Summary.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Room(joined.Summary.ID); err == nil {
		t.Fatal("left room is still in the local list")
	}
	aliceView, err = alice.SyncRoom(ctx, view.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceView.Summary.MemberCount != 1 || aliceView.Epoch < 2 {
		t.Fatalf("alice after leave = %+v", aliceView)
	}
	if _, err := alice.SendMessage(ctx, view.Summary.ID, "离开后仍可继续"); err != nil {
		t.Fatal(err)
	}
}

func TestDirectRoomKindAndInviteLimit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	bob := register(t, f, "Bob", "bob@example.com")

	view, err := alice.CreateRoom(ctx, webclient.CreateRoomInput{Name: "小王", Kind: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.Kind != "direct" || view.Summary.PeerName != "" || view.Summary.MemberCount != 1 {
		t.Fatalf("direct room = %+v", view.Summary)
	}
	invite, err := alice.CreateInvite(ctx, view.Summary.ID, webclient.InviteInput{
		History: "all", TTLHours: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := bob.JoinInvite(ctx, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Summary.Kind != "direct" {
		t.Fatalf("joined kind = %q", joined.Summary.Kind)
	}
	if joined.Summary.PeerAccountID != alice.Profile().AccountID || joined.Summary.PeerName != "Alice" {
		t.Fatalf("joined peer = %+v", joined.Summary)
	}

	aliceView, err := alice.SyncRoom(ctx, view.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceView.Summary.Kind != "direct" ||
		aliceView.Summary.PeerAccountID != bob.Profile().AccountID ||
		aliceView.Summary.PeerName != "Bob" {
		t.Fatalf("alice peer = %+v", aliceView.Summary)
	}

	for _, client := range []*webclient.Client{alice, bob} {
		_, err := client.CreateInvite(ctx, view.Summary.ID, webclient.InviteInput{
			History: "all", TTLHours: 24,
		})
		var clientErr *webclient.Error
		if !errors.As(err, &clientErr) || clientErr.Code != "direct_room_full" {
			t.Fatalf("invite in full direct room = %v", err)
		}
	}
}

func TestDeviceListAndRevocation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	accountID := alice.Profile().AccountID
	second := login(t, f, accountID, "待撤销的浏览器")
	third := login(t, f, accountID, "保持登录的浏览器")

	devices, err := alice.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 3 {
		t.Fatalf("devices = %+v", devices)
	}
	if !devices[0].Current || devices[0].ID != alice.Profile().DeviceID {
		t.Fatalf("current device is not first: %+v", devices)
	}
	var targetID string
	for _, device := range devices {
		if device.Name == "待撤销的浏览器" {
			targetID = device.ID
		}
	}
	if targetID == "" {
		t.Fatal("second device is missing")
	}
	if _, err := second.RefreshRooms(ctx); err != nil {
		t.Fatalf("second device cannot use its session: %v", err)
	}
	if _, err := third.RefreshRooms(ctx); err != nil {
		t.Fatalf("third device cannot use its session: %v", err)
	}
	if err := alice.RevokeDevice(ctx, targetID); err != nil {
		t.Fatal(err)
	}
	devices, err = alice.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revoked := 0
	for _, device := range devices {
		if device.ID == targetID {
			if device.RevokedAt == nil {
				t.Fatalf("device was not marked revoked: %+v", device)
			}
			revoked++
		}
	}
	if revoked != 1 {
		t.Fatalf("revoked devices = %d (%+v)", revoked, devices)
	}
	// The old bearer session was deleted, and the revoked certificate cannot
	// mint another one.
	if _, err := second.RefreshRooms(ctx); err == nil {
		t.Fatal("revoked device kept its session")
	}
	// Revoking one device must not sign out the account's other browsers.
	if _, err := third.RefreshRooms(ctx); err != nil {
		t.Fatalf("unrelated device lost its session: %v", err)
	}
	var state struct {
		Account *struct {
			Device struct {
				ID         string `json:"id"`
				PrivateKey []byte `json:"private_key"`
			} `json:"device"`
		} `json:"account"`
	}
	encoded, err := second.Export()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		t.Fatal(err)
	}
	if state.Account == nil || state.Account.Device.ID != targetID ||
		len(state.Account.Device.PrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("second device state = %+v", state.Account)
	}
	host, err := hosted.NewBrowserClient(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.DeviceSession(ctx, accountID, targetID,
		ed25519.PrivateKey(state.Account.Device.PrivateKey)); err == nil {
		t.Fatal("revoked device minted a fresh session")
	}
	// The current device still has its session.
	if _, err := alice.RefreshRooms(ctx); err != nil {
		t.Fatalf("current device lost its session: %v", err)
	}
	if err := alice.RevokeDevice(ctx, alice.Profile().DeviceID); err == nil {
		t.Fatal("revoked the current device")
	}
}

func TestInviteValidation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	view, err := alice.CreateRoom(ctx, webclient.CreateRoomInput{Name: "私密房间"})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.CreateInvite(ctx, view.Summary.ID, webclient.InviteInput{History: "recent"})
	if err != nil {
		t.Fatal(err)
	}
	bob := register(t, f, "Bob", "bob@example.com")
	for _, bad := range []string{"", "https://example.com/invite", "toget-invite:v1:not-base64!"} {
		if _, err := bob.JoinInvite(ctx, bad); err == nil {
			t.Fatalf("accepted invalid invite %q", bad)
		}
	}
	// A second redemption of the same single-use code fails.
	if _, err := bob.JoinInvite(ctx, invite.Code); err != nil {
		t.Fatal(err)
	}
	carol := register(t, f, "Carol", "carol@example.com")
	if _, err := carol.JoinInvite(ctx, invite.Code); err == nil {
		t.Fatal("single-use invite was redeemed twice")
	}
}

func TestStateRejectsTampering(t *testing.T) {
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	encoded, err := alice.Export()
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(encoded, `"version":1`, `"version":99`, 1)
	client, err := webclient.New(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Restore(tampered); err == nil {
		t.Fatal("accepted an unsupported state version")
	}
	if err := client.Restore("{not json"); err == nil {
		t.Fatal("accepted malformed state")
	}
	if err := client.Restore(encoded); err != nil {
		t.Fatal(err)
	}
	if client.Profile() == nil {
		t.Fatal("restore lost the profile")
	}
}

func TestConcurrentSendRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	view, err := alice.CreateRoom(ctx, webclient.CreateRoomInput{Name: "并发"})
	if err != nil {
		t.Fatal(err)
	}
	// A second client exports the same device state, so both share a head and
	// the loser must sync and retry.
	second, err := webclient.New(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := alice.Export()
	if err := second.Restore(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.SendMessage(ctx, view.Summary.ID, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.SendMessage(ctx, view.Summary.ID, "two"); err != nil {
		t.Fatal(err)
	}
	final, err := alice.SyncRoom(ctx, view.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(final.Messages))
	for _, message := range final.Messages {
		if message.Kind == "text" {
			texts = append(texts, message.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "one" || texts[1] != "two" {
		t.Fatalf("concurrent messages = %+v", texts)
	}
}

func TestBrowserResolveAccount(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	profile := alice.Profile()
	if profile == nil || profile.HostCode == "" {
		t.Fatalf("profile = %+v", profile)
	}

	resolved, err := alice.ResolveAccount(ctx, profile.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AccountID != profile.AccountID || resolved.Name != "Alice" ||
		resolved.HostCode != profile.HostCode || resolved.Kind != "account" || !resolved.Self {
		t.Fatalf("resolved = %+v", resolved)
	}

	if _, err := alice.ResolveAccount(ctx, "123"); err == nil ||
		webclient.AsError(err).Code != "invalid_account" {
		t.Fatalf("short id error = %v", err)
	}
	if _, err := alice.ResolveAccount(ctx, profile.HostCode+"999999"); err == nil ||
		webclient.AsError(err).Code != "account_not_found" {
		t.Fatalf("unknown account error = %v", err)
	}
	remoteCode := "00000000"
	if profile.HostCode == remoteCode {
		remoteCode = "00000001"
	}
	if _, err := alice.ResolveAccount(ctx, remoteCode+profile.AccountID[8:]); err == nil ||
		webclient.AsError(err).Code != "directory_disabled" {
		t.Fatalf("remote account error = %v", err)
	}
}

// The host stores invite epoch keys wrapped under the invite secret. If the
// join request carried the secret itself, a malicious or compromised host
// could unwrap them and read the room. Joining must send only the hash.
func TestBrowserJoinSendsSecretHashOnly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := register(t, f, "Alice", "alice@example.com")
	bob := register(t, f, "Bob", "bob@example.com")

	view, err := alice.CreateRoom(ctx, webclient.CreateRoomInput{Name: "加密房间"})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.CreateInvite(ctx, view.Summary.ID, webclient.InviteInput{
		History: "all", TTLHours: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(ctx, invite.Code); err != nil {
		t.Fatal(err)
	}

	bodies := f.capturedJoinBodies()
	if len(bodies) != 1 {
		t.Fatalf("join requests = %d, want 1", len(bodies))
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(bodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	if raw, ok := payload["secret"]; ok {
		t.Fatalf("join request leaks the raw invite secret: %s", raw)
	}
	raw, ok := payload["secret_hash"]
	if !ok {
		t.Fatal("join request has no secret_hash")
	}
	var hash []byte
	if err := json.Unmarshal(raw, &hash); err != nil {
		t.Fatal(err)
	}
	if len(hash) != sha256.Size {
		t.Fatalf("secret_hash length = %d, want %d", len(hash), sha256.Size)
	}
}
