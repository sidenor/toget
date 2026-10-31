package notify

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
)

type memoryStore struct {
	mu       sync.Mutex
	settings map[string]string
	subs     map[string]storage.PushSubscription
	deleted  []string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{settings: map[string]string{}, subs: map[string]storage.PushSubscription{}}
}

func (m *memoryStore) GetSetting(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings[key], nil
}
func (m *memoryStore) SetSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[key] = value
	return nil
}
func (m *memoryStore) ListPushSubscriptions(context.Context) ([]storage.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]storage.PushSubscription, 0, len(m.subs))
	for _, sub := range m.subs {
		out = append(out, sub)
	}
	return out, nil
}
func (m *memoryStore) GetPushSubscription(_ context.Context, id string) (*storage.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.subs[id]
	if !ok {
		return nil, storage.ErrPushSubscriptionNotFound
	}
	return &sub, nil
}
func (m *memoryStore) SavePushSubscription(_ context.Context, sub *storage.PushSubscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, existing := range m.subs {
		if existing.Endpoint == sub.Endpoint {
			sub.ID = id
			sub.CreatedAt = existing.CreatedAt
			break
		}
	}
	sub.UpdatedAt = time.Now().UTC()
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = sub.UpdatedAt
	}
	m.subs[sub.ID] = *sub
	return nil
}
func (m *memoryStore) UpdatePushSubscription(_ context.Context, sub *storage.PushSubscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.subs[sub.ID]; !ok {
		return storage.ErrPushSubscriptionNotFound
	}
	m.subs[sub.ID] = *sub
	return nil
}
func (m *memoryStore) DeletePushSubscription(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.subs[id]; !ok {
		return storage.ErrPushSubscriptionNotFound
	}
	delete(m.subs, id)
	m.deleted = append(m.deleted, id)
	return nil
}
func (m *memoryStore) RecordPushResult(context.Context, string, time.Time, bool) error { return nil }

type browserKeys struct {
	private *ecdh.PrivateKey
	p256dh  string
	auth    string
}

func newBrowserKeys(t *testing.T) browserKeys {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return browserKeys{
		private: private,
		p256dh:  base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
		auth:    base64.RawURLEncoding.EncodeToString(auth),
	}
}

func contains(haystack []byte, needle string) bool { return strings.Contains(string(haystack), needle) }

func decryptBody(t *testing.T, keys browserKeys, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatal("push body is too short")
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != recordSize {
		t.Fatalf("record size = %d", rs)
	}
	idLen := int(body[20])
	if idLen != 65 || len(body) < 21+idLen {
		t.Fatalf("key id length = %d", idLen)
	}
	asPublic, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := keys.private.ECDH(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	authSecret, _ := base64.RawURLEncoding.DecodeString(keys.auth)
	uaPublic, _ := base64.RawURLEncoding.DecodeString(keys.p256dh)
	keyInfo := append(append([]byte{}, hkdfInfoWebPush...), uaPublic...)
	keyInfo = append(keyInfo, body[21:21+idLen]...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, authSecret, keyInfo), ikm); err != nil {
		t.Fatal(err)
	}
	cek := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, hkdfInfoCEK), cek); err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, hkdfInfoNonce), nonce); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(cek)
	aead, _ := cipher.NewGCM(block)
	plain, err := aead.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestEncryptPayloadRoundTrip(t *testing.T) {
	keys := newBrowserKeys(t)
	payload := []byte(`{"v":1,"type":"approval","lang":"zh"}`)
	body, err := encryptPayload(keys.p256dh, keys.auth, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptBody(t, keys, body); string(got) != string(payload) {
		t.Fatalf("decrypted payload = %q", got)
	}
	if contains(body, "approval") || contains(body, "toget") {
		t.Fatal("ciphertext must not contain payload plaintext")
	}
}

func TestVAPIDAuthorizationVerifies(t *testing.T) {
	keys, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	header, err := vapidAuthorization(*keys, "https://push.example.test/send/abc", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("authorization = %q", header)
	}
	parts := strings.Split(strings.TrimPrefix(header, "vapid t="), ", k=")
	if len(parts) != 2 || parts[1] != keys.Public {
		t.Fatalf("authorization parts = %v", parts)
	}
	jwtParts := strings.Split(parts[0], ".")
	if len(jwtParts) != 3 {
		t.Fatalf("jwt = %q", parts[0])
	}
	claims, err := base64.RawURLEncoding.DecodeString(jwtParts[1])
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(claims, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["aud"] != "https://push.example.test" {
		t.Fatalf("aud = %v", parsed["aud"])
	}
	signature, err := base64.RawURLEncoding.DecodeString(jwtParts[2])
	if err != nil || len(signature) != 64 {
		t.Fatalf("signature length = %d (%v)", len(signature), err)
	}
	digest := sha256.Sum256([]byte(jwtParts[0] + "." + jwtParts[1]))
	pub, _ := base64.RawURLEncoding.DecodeString(keys.Public)
	x, y := elliptic.Unmarshal(elliptic.P256(), pub)
	key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	if !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("VAPID signature did not verify")
	}
}

func TestEndpointAudience(t *testing.T) {
	cases := []struct {
		endpoint string
		ok       bool
		aud      string
	}{
		{"https://push.example.test/send/1", true, "https://push.example.test"},
		{"http://127.0.0.1:8080/push", true, "http://127.0.0.1:8080"},
		{"http://[::1]:8080/push", true, "http://[::1]:8080"},
		{"http://localhost:9000/x", true, "http://localhost:9000"},
		{"http://push.example.test/x", false, ""},
		{"ftp://push.example.test/x", false, ""},
		{"https://user:pass@push.example.test/x", false, ""},
		{"https://push.example.test/x#fragment", false, ""},
	}
	for _, tc := range cases {
		aud, err := endpointAudience(tc.endpoint)
		if tc.ok && (err != nil || aud != tc.aud) {
			t.Errorf("endpointAudience(%q) = %q, %v; want %q", tc.endpoint, aud, err, tc.aud)
		}
		if !tc.ok && err == nil {
			t.Errorf("endpointAudience(%q) succeeded; want error", tc.endpoint)
		}
	}
}

func TestHandleRoomEventSendsContentFreePayload(t *testing.T) {
	store := newMemoryStore()
	keys := newBrowserKeys(t)
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			t.Errorf("missing VAPID authorization")
		}
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	now := time.Now()
	svc, err := New(context.Background(), store, nil, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := svc.Subscribe(context.Background(), SubscriptionInput{
		Endpoint: server.URL + "/push/secret-endpoint", P256DH: keys.p256dh, Auth: keys.auth,
		Language: "zh-CN", UserAgent: "test browser", Label: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Language != "zh" || sub.Label != "Laptop" || !sub.Enabled {
		t.Fatalf("stored subscription = %+v", sub)
	}
	svc.Start(context.Background())
	svc.HandleRoomEvent(context.Background(), &room.Event{
		ID: "event-1", Type: room.EventMessagePosted, AuthorID: "someone-else",
		CreatedAt: now.Add(-time.Second),
		Payload:   json.RawMessage(`{"text":"TOP SECRET MESSAGE","agent_id":""}`),
	})
	select {
	case body := <-received:
		plain := decryptBody(t, keys, body)
		if string(plain) != `{"v":1,"type":"message","lang":"zh"}` {
			t.Fatalf("push payload = %s", plain)
		}
		if contains(plain, "SECRET") || contains(plain, "event-1") || contains(plain, "someone") {
			t.Fatal("push payload leaked event content")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no push was delivered")
	}
}

func TestSelfMessagesAndOldEventsAreSkipped(t *testing.T) {
	store := newMemoryStore()
	svc, err := New(context.Background(), store, nil, WithSelfAccount(func(context.Context) string { return "local" }))
	if err != nil {
		t.Fatal(err)
	}
	svc.HandleRoomEvent(context.Background(), &room.Event{Type: room.EventMessagePosted, AuthorID: "local", CreatedAt: time.Now()})
	svc.HandleRoomEvent(context.Background(), &room.Event{Type: room.EventMessagePosted, AuthorID: "remote", CreatedAt: time.Now().Add(-time.Hour)})
	select {
	case n := <-svc.queue:
		t.Fatalf("unexpected queued notification: %+v", n)
	default:
	}
	svc.HandleRoomEvent(context.Background(), &room.Event{Type: room.EventApprovalRequested, AuthorID: "local", CreatedAt: time.Now()})
	if n := <-svc.queue; n.kind != KindApproval {
		t.Fatalf("queued kind = %q", n.kind)
	}
	svc.HandleRoomEvent(context.Background(), &room.Event{
		Type: room.EventMessagePosted, AuthorID: "local", CreatedAt: time.Now(),
		Payload: json.RawMessage(`{"text":"agent reply","agent_id":"agent-1"}`),
	})
	if n := <-svc.queue; n.kind != KindMessage {
		t.Fatalf("agent message kind = %q", n.kind)
	}
}

func TestGoneEndpointRemovesSubscription(t *testing.T) {
	store := newMemoryStore()
	keys := newBrowserKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer server.Close()
	svc, err := New(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := svc.Subscribe(context.Background(), SubscriptionInput{
		Endpoint: server.URL + "/push", P256DH: keys.p256dh, Auth: keys.auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SendTest(context.Background(), sub.ID); err == nil {
		t.Fatal("SendTest succeeded; want error")
	}
	if err := svc.DeleteSubscription(context.Background(), sub.ID); err != ErrSubscriptionNotFound {
		t.Fatalf("subscription still exists after 410: %v", err)
	}
}

func TestMuteAndPreferences(t *testing.T) {
	store := newMemoryStore()
	svc, err := New(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	muted := time.Now().Add(time.Hour)
	if err := svc.SetMute(ctx, &muted); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.MutedUntil(ctx); err != nil || got == nil {
		t.Fatalf("MutedUntil = %v, %v", got, err)
	}
	if !svc.muted(ctx) {
		t.Fatal("service should be muted")
	}
	if err := svc.SetMute(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if svc.muted(ctx) {
		t.Fatal("service should be unmuted")
	}
}

func TestSubscribeValidation(t *testing.T) {
	store := newMemoryStore()
	svc, err := New(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := newBrowserKeys(t)
	base := SubscriptionInput{Endpoint: "https://push.example.test/x", P256DH: keys.p256dh, Auth: keys.auth}
	for name, mutate := range map[string]func(*SubscriptionInput){
		"plain http":  func(in *SubscriptionInput) { in.Endpoint = "http://push.example.test/x" },
		"bad p256dh":  func(in *SubscriptionInput) { in.P256DH = "abc" },
		"bad auth":    func(in *SubscriptionInput) { in.Auth = "abc" },
		"credentials": func(in *SubscriptionInput) { in.Endpoint = "https://u:p@push.example.test/x" },
	} {
		in := base
		mutate(&in)
		if _, err := svc.Subscribe(context.Background(), in); err != ErrInvalidSubscription {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
