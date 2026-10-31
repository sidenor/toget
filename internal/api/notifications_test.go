package api_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/api"
)

func browserSubscriptionKeys(t *testing.T) (string, string) {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(auth)
}

func TestNotificationAPI(t *testing.T) {
	node := newTestNode(t, nil)
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	// The account starts with no push subscriptions.
	resp, body = node.request(t, http.MethodGet, "/api/v1/notifications", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get notifications = %d %s", resp.StatusCode, body)
	}
	status := decode[api.NotificationStatusView](t, body)
	if status.PublicKey == "" || len(status.Subscriptions) != 0 {
		t.Fatalf("initial status = %#v", status)
	}

	received := make(chan *http.Request, 1)
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		received <- r.Clone(r.Context())
		w.WriteHeader(http.StatusCreated)
	}))
	defer push.Close()
	p256dh, auth := browserSubscriptionKeys(t)
	subscribeBody, _ := json.Marshal(map[string]string{
		"endpoint": push.URL + "/push/capability", "p256dh": p256dh, "auth": auth,
		"language": "zh-CN", "user_agent": "api-test", "label": "Test browser",
	})
	resp, body = node.request(t, http.MethodPost, "/api/v1/notifications/subscriptions", testToken, string(subscribeBody), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "capability") || strings.Contains(string(body), p256dh) {
		t.Fatalf("subscription response leaked push capability: %s", body)
	}
	sub := decode[api.NotificationSubscriptionView](t, body)
	if sub.ID == "" || sub.Label != "Test browser" || sub.Language != "zh" || !sub.Enabled ||
		!sub.MessageEvents || !sub.ApprovalEvents || sub.EndpointHost != "127.0.0.1" {
		t.Fatalf("subscription = %#v", sub)
	}

	// Browser keys are still stored even though the API view hides them.
	resp, body = node.request(t, http.MethodGet, "/api/v1/notifications", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list subscriptions = %d %s", resp.StatusCode, body)
	}
	status = decode[api.NotificationStatusView](t, body)
	if len(status.Subscriptions) != 1 || status.Subscriptions[0].ID != sub.ID {
		t.Fatalf("subscriptions = %#v", status.Subscriptions)
	}

	resp, body = node.request(t, http.MethodPatch, "/api/v1/notifications/subscriptions/"+sub.ID, testToken,
		`{"message_events":false,"label":"Desk"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update subscription = %d %s", resp.StatusCode, body)
	}
	updated := decode[api.NotificationSubscriptionView](t, body)
	if updated.MessageEvents || updated.Label != "Desk" || !updated.ApprovalEvents {
		t.Fatalf("updated = %#v", updated)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/notifications/mute", testToken, `{"hours":8}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mute = %d %s", resp.StatusCode, body)
	}
	muted := decode[api.NotificationStatusView](t, body)
	if muted.MutedUntil == nil {
		t.Fatal("mute did not set a deadline")
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/notifications/mute", testToken, `{"hours":0}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unmute = %d %s", resp.StatusCode, body)
	}
	if got := decode[api.NotificationStatusView](t, body); got.MutedUntil != nil {
		t.Fatalf("still muted: %v", got.MutedUntil)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/notifications/subscriptions/"+sub.ID+"/test", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test push = %d %s", resp.StatusCode, body)
	}
	select {
	case req := <-received:
		if !strings.HasPrefix(req.Header.Get("Authorization"), "vapid t=") ||
			req.Header.Get("Content-Encoding") != "aes128gcm" {
			t.Fatalf("push headers = %#v", req.Header)
		}
	default:
		t.Fatal("test push was not delivered")
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/notifications/subscriptions", testToken,
		`{"endpoint":"http://example.test/push","p256dh":"abc","auth":"abc"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid subscribe = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPatch, "/api/v1/notifications/subscriptions/missing", testToken,
		`{"enabled":false}`, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing subscription = %d %s", resp.StatusCode, body)
	}

	resp, _ = node.request(t, http.MethodDelete, "/api/v1/notifications/subscriptions/"+sub.ID, testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete subscription = %d", resp.StatusCode)
	}
	resp, body = node.request(t, http.MethodGet, "/api/v1/notifications", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final status = %d %s", resp.StatusCode, body)
	}
	if got := decode[api.NotificationStatusView](t, body); len(got.Subscriptions) != 0 {
		t.Fatalf("subscription was not deleted: %#v", got.Subscriptions)
	}
}
