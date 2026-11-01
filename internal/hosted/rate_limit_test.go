package hosted_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
)

// A browser reset checks the token, fetches material, and re-registers OPAQUE
// in several calls. The token bucket must be large enough for that flow; the
// separate mail-request bucket stays tight.
func TestRecoveryRateLimitsKeepResetFlowUsable(t *testing.T) {
	store, err := hosted.OpenStore(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsureHost(context.Background(), "Rate Test Host", []identity.Endpoint{
		{Transport: "https", Addr: "127.0.0.1:7722"},
	}); err != nil {
		t.Fatal(err)
	}
	service, err := hosted.New(hosted.Options{
		Store: store, PublicURL: "https://host.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)

	post := func(path string, payload any) int {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}

	tokenRequest := map[string]any{"protocol": 1, "token": "not-a-real-token"}
	for attempt := 1; attempt <= 30; attempt++ {
		if status := post(hosted.RecoverContextPath, tokenRequest); status == http.StatusTooManyRequests {
			t.Fatalf("token request %d was rate limited; a reset needs several calls", attempt)
		}
	}
	if status := post(hosted.RecoverContextPath, tokenRequest); status != http.StatusTooManyRequests {
		t.Fatalf("token request 31 status = %d, want 429", status)
	}

	// Mail requests have their own, tighter bucket and were not consumed by
	// the token calls above.
	mailRequest := map[string]any{"protocol": 1, "user": "00000000000000"}
	for attempt := 1; attempt <= 6; attempt++ {
		if status := post(hosted.RecoverRequestPath, mailRequest); status != http.StatusAccepted {
			t.Fatalf("recovery mail request %d status = %d, want 202", attempt, status)
		}
	}
	if status := post(hosted.RecoverRequestPath, mailRequest); status != http.StatusTooManyRequests {
		t.Fatalf("recovery mail request 7 status = %d, want 429", status)
	}
}
