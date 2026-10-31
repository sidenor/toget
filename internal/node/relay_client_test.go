package node_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/relayproto"
)

func TestClientPollsQueuedRelayTicket(t *testing.T) {
	const target = "toget-test-server"
	var forwards, polls int32
	mux := http.NewServeMux()
	mux.HandleFunc(relayproto.ForwardPrefix+target, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&forwards, 1)
		var env relayproto.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Errorf("decode envelope: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if env.Target != target || env.Method != http.MethodGet || env.Path != "/toget/v1/info" {
			t.Errorf("envelope = %#v", env)
		}
		for _, name := range []string{"X-Toget-Timestamp", "X-Toget-Nonce", "X-Toget-Signature"} {
			if env.Headers[name] == "" {
				t.Errorf("inner header %s is missing", name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(relayproto.HeaderRelayQueued, "true")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(relayproto.Queued{
			Queued: true, Ticket: "ticket-1", ExpiresAt: time.Now().Add(time.Minute),
		})
	})
	mux.HandleFunc("/relay/v1/responses/ticket-1", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		if n == 1 {
			w.Header().Set(relayproto.HeaderRelayQueued, "true")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(relayproto.Queued{
				Queued: true, Ticket: "ticket-1", ExpiresAt: time.Now().Add(time.Minute),
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(node.Manifest{
			Format: node.ManifestFormat, Version: 1, ServerID: target,
			AccountID: "account", Endpoint: "https://127.0.0.1:7717",
			TLSFingerprint: "00", Protocol: 1,
		})
	})
	relayServer := httptest.NewTLSServer(mux)
	defer relayServer.Close()
	sum := sha256.Sum256(relayServer.Certificate().Raw)
	fingerprint := hex.EncodeToString(sum[:])
	route, err := relayproto.NodeRouteURL(relayServer.URL, fingerprint, target)
	if err != nil {
		t.Fatal(err)
	}
	direct := "https://127.0.0.1:1#fp=" + fingerprint
	address := relayproto.JoinAddress(direct, route)

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client, err := node.NewClientForURL(address, "account", "device", key)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	manifest, err := client.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ServerID != target {
		t.Fatalf("manifest = %#v", manifest)
	}
	if atomic.LoadInt32(&forwards) != 1 {
		t.Fatalf("forwards = %d", forwards)
	}
	if atomic.LoadInt32(&polls) < 2 {
		t.Fatalf("polls = %d", polls)
	}
}
