package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/relayproto"
	"github.com/sidenor/toget/internal/relaytunnel"
)

func dialTestTunnel(t *testing.T, service *Service, serverURL, id, token string) *relaytunnel.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := relaytunnel.Dial(ctx, serverURL, token, id, http.DefaultClient)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	t.Cleanup(func() { client.CloseNow() })
	return client
}

func TestWebSocketTunnelForwardsAndReturnsResult(t *testing.T) {
	service, server := testRelay(t, nil)
	addTestSession(service, "node-ws", "node", "token-ws")
	tunnel := dialTestTunnel(t, service, server.URL, "node-ws", "token-ws")

	type answer struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		resp, data := forward(t, server, relayproto.Envelope{
			Protocol: relayproto.Protocol, Target: "node-ws",
			Method: http.MethodGet, Path: "/toget/v1/health",
		})
		done <- answer{status: resp.StatusCode, body: data}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := tunnel.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != relayproto.TunnelFrame || msg.Envelope == nil {
		t.Fatalf("tunnel message = %#v", msg)
	}
	frame := msg.Envelope
	if frame.Target != "node-ws" || !relayproto.HasHop(frame, service.relayID) {
		t.Fatalf("frame = %#v", frame)
	}
	result := relayproto.Result{
		Protocol: relayproto.Protocol, RequestID: frame.RequestID,
		Status: http.StatusOK, Body: relayproto.EncodeBody([]byte("tunneled")),
	}
	if err := tunnel.Write(ctx, relayproto.TunnelMessage{
		Protocol: relayproto.Protocol, Type: relayproto.TunnelResult, Result: &result,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.status != http.StatusOK || string(got.body) != "tunneled" {
			t.Fatalf("forward answer = %d %q", got.status, got.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forward did not finish")
	}
}

func TestWebSocketTunnelDeliversQueuedFrames(t *testing.T) {
	service, server := testRelay(t, nil)
	target := "node-ws-queued"
	service.mu.Lock()
	service.targets[target] = &targetEntry{kind: "node", registeredAt: time.Now(), expires: time.Now().Add(time.Hour)}
	service.mu.Unlock()
	resp, data := forward(t, server, relayproto.Envelope{
		Target: target, Method: http.MethodGet, Path: "/toget/v1/health",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("offline forward = %d %s", resp.StatusCode, data)
	}
	addTestSession(service, target, "node", "token-queued")
	tunnel := dialTestTunnel(t, service, server.URL, target, "token-queued")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := tunnel.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != relayproto.TunnelFrame || msg.Envelope == nil || msg.Envelope.Target != target {
		t.Fatalf("queued tunnel message = %#v", msg)
	}
}

func TestWebSocketTunnelCanBeDisabled(t *testing.T) {
	service, server := testRelay(t, func(opts *Options) { opts.WebSocket = "off" })
	addTestSession(service, "node-http", "node", "token-http")
	if service.opts.WebSocket != "off" {
		t.Fatal("WebSocket option was not preserved")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := relaytunnel.Dial(ctx, server.URL, "token-http", "node-http", http.DefaultClient); err == nil {
		t.Fatal("tunnel dial succeeded while disabled")
	}
	frames := poll(t, server, "node-http", "token-http")
	if len(frames) != 0 {
		t.Fatalf("poll frames = %#v", frames)
	}
}

func TestWebSocketTunnelRejectsAnotherTunnel(t *testing.T) {
	service, server := testRelay(t, nil)
	addTestSession(service, "node-one", "node", "token-one")
	first := dialTestTunnel(t, service, server.URL, "node-one", "token-one")
	_ = first
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	second, err := relaytunnel.Dial(ctx, server.URL, "token-one", "node-one", http.DefaultClient)
	if err == nil {
		second.CloseNow()
		t.Fatal("a second tunnel was accepted")
	}
}

// Ensure the tunnel protocol stays JSON-decodable independently of the
// WebSocket framing helper.
func TestTunnelMessageRoundTrip(t *testing.T) {
	msg := relayproto.TunnelMessage{
		Protocol: relayproto.Protocol, Type: relayproto.TunnelFrame,
		Envelope: &relayproto.Envelope{Target: "n", Path: "/toget/v1/health", Method: "GET"},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var out relayproto.TunnelMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != msg.Type || out.Envelope == nil || out.Envelope.Target != "n" {
		t.Fatalf("round trip = %#v", out)
	}
}
