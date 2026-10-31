package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/relayproto"
)

func relayForwardRequest(t *testing.T, serverURL string, env relayproto.Envelope) (*http.Response, []byte) {
	t.Helper()
	body, err := json.Marshal(&env)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost,
		serverURL+relayproto.RelayForwardPrefix+env.Target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestSignedRelayForwarding(t *testing.T) {
	service, server := testRelay(t, nil)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relayID := relayproto.RelayID(pub)
	child := addTestSession(service, relayID, "relay", "child-token")
	target := "signed-target"
	service.mu.Lock()
	service.routes[target] = &routeEntry{
		session: child, kind: "node", hops: 1, via: []string{relayID},
		expires: time.Now().Add(time.Hour),
	}
	service.mu.Unlock()

	env := relayproto.Envelope{
		Protocol: relayproto.Protocol, Target: target,
		Method: http.MethodGet, Path: "/toget/v1/health",
		Hops: []string{relayID},
	}
	if err := relayproto.SignForward(&env, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		status int
		body   []byte
	}
	done := make(chan answer, 1)
	go func() {
		resp, data := relayForwardRequest(t, server.URL, env)
		done <- answer{status: resp.StatusCode, body: data}
	}()
	var frame relayproto.Envelope
	select {
	case frame = <-child.frames:
	case <-time.After(3 * time.Second):
		t.Fatal("child relay did not receive the signed frame")
	}
	if frame.Target != target || !relayproto.HasHop(&frame, service.relayID) {
		t.Fatalf("frame = %#v", frame)
	}
	postResult(t, server, "child-token", frame.RequestID, http.StatusOK, []byte("signed"))
	got := <-done
	if got.status != http.StatusOK || string(got.body) != "signed" {
		t.Fatalf("signed forward = %d %q", got.status, got.body)
	}

	// Replaying the same signed envelope is rejected before it reaches a node.
	resp, data := relayForwardRequest(t, server.URL, env)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("replayed forward = %d %s", resp.StatusCode, data)
	}
}

func TestSignedRelayForwardingRejectsTampering(t *testing.T) {
	service, server := testRelay(t, nil)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relayID := relayproto.RelayID(pub)
	addTestSession(service, relayID, "relay", "child-token")
	target := "tamper-target"
	service.mu.Lock()
	service.routes[target] = &routeEntry{
		session: service.sessions[relayID], kind: "node", hops: 1,
		via: []string{relayID}, expires: time.Now().Add(time.Hour),
	}
	service.mu.Unlock()

	env := relayproto.Envelope{
		Protocol: relayproto.Protocol, Target: target,
		Method: http.MethodGet, Path: "/toget/v1/health", Hops: []string{relayID},
	}
	if err := relayproto.SignForward(&env, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.Path = "/toget/v1/info"
	resp, data := relayForwardRequest(t, server.URL, env)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered forward = %d %s", resp.StatusCode, data)
	}

	// A signature from an unrelated relay cannot claim the child's route.
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	other := relayproto.Envelope{
		Protocol: relayproto.Protocol, Target: target,
		Method: http.MethodGet, Path: "/toget/v1/health", Hops: []string{relayID},
	}
	if err := relayproto.SignForward(&other, otherKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Restore the claimed hop so only the key identity is wrong.
	other.Hops = []string{other.ForwarderID}
	if err := relayproto.SignForward(&other, otherKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, data = relayForwardRequest(t, server.URL, other)
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unrouted forwarder = %d %s", resp.StatusCode, data)
	}
}

func TestRelaySenderByteQuota(t *testing.T) {
	_, server := testRelay(t, func(opts *Options) {
		opts.MaxSenderBytesPerMinute = 512
	})
	env := relayproto.Envelope{
		Target: "quota-target", Method: http.MethodGet, Path: "/toget/v1/health",
		Body: relayproto.EncodeBody(make([]byte, 1024)),
	}
	body, _ := json.Marshal(env)
	req, _ := http.NewRequest(http.MethodPost, server.URL+relayproto.ForwardPrefix+env.Target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("quota forward = %d %s", resp.StatusCode, data)
	}
	var envelope relayproto.ErrorBody
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "quota_exceeded" {
		t.Fatalf("quota error = %s", data)
	}
}

func TestRelayToRelayByteQuota(t *testing.T) {
	service, server := testRelay(t, func(opts *Options) {
		opts.MaxRelayBytesPerMinute = 256
	})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relayID := relayproto.RelayID(pub)
	child := addTestSession(service, relayID, "relay", "child-token")
	target := "relay-quota-target"
	service.mu.Lock()
	service.routes[target] = &routeEntry{
		session: child, kind: "node", hops: 1, via: []string{relayID},
		expires: time.Now().Add(time.Hour),
	}
	service.mu.Unlock()
	env := relayproto.Envelope{
		Protocol: relayproto.Protocol, Target: target,
		Method: http.MethodGet, Path: "/toget/v1/health", Hops: []string{relayID},
		Body: relayproto.EncodeBody(make([]byte, 1024)),
	}
	if err := relayproto.SignForward(&env, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, data := relayForwardRequest(t, server.URL, env)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("relay quota = %d %s", resp.StatusCode, data)
	}
}
