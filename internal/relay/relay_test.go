package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/relayproto"
)

func testRelay(t *testing.T, configure func(*Options)) (*Service, *httptest.Server) {
	t.Helper()
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Listen: "127.0.0.1:0", Paths: paths,
		QueueTTL: time.Minute, ResponseTTL: time.Minute,
		WaitTimeout: 2 * time.Second, PollWait: 200 * time.Millisecond,
	}
	if configure != nil {
		configure(&opts)
	}
	service, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)
	return service, server
}

func addTestSession(s *Service, id, kind, token string) *session {
	sess := &session{
		id: id, kind: kind, token: token,
		frames: make(chan relayproto.Envelope, 16), lastSeen: time.Now().UTC(),
		expires: time.Now().UTC().Add(time.Hour),
		routes:  make(map[string]relayproto.RouteAnnouncement),
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess
}

func registerFakeRelay(t *testing.T, server *httptest.Server) (string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := relayproto.RegisterRequest{
		Kind: "relay", RelayID: relayproto.RelayID(pub),
		PublicKey: base64.RawStdEncoding.EncodeToString(pub),
	}
	if err := relayproto.SignRegistration(&req, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(server.URL+"/relay/v1/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register: %d %s", resp.StatusCode, data)
	}
	var out relayproto.RegisterResponse
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		t.Fatalf("register response: %v %s", err, data)
	}
	if out.RelayID == "" {
		t.Fatal("relay did not identify itself")
	}
	return req.RelayID, out.Token
}

func forward(t *testing.T, server *httptest.Server, env relayproto.Envelope) (*http.Response, []byte) {
	t.Helper()
	if env.Protocol == 0 {
		env.Protocol = relayproto.Protocol
	}
	body, _ := json.Marshal(env)
	req, _ := http.NewRequest(http.MethodPost, server.URL+relayproto.ForwardPrefix+env.Target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func poll(t *testing.T, server *httptest.Server, id, token string) []relayproto.Envelope {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/relay/v1/poll?id="+id, bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: %d %s", resp.StatusCode, data)
	}
	var out relayproto.PollResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out.Frames
}

func postResult(t *testing.T, server *httptest.Server, token, id string, status int, body []byte) {
	t.Helper()
	result := relayproto.Result{
		Protocol: relayproto.Protocol, RequestID: id,
		Status: status, Body: relayproto.EncodeBody(body),
	}
	payload, _ := json.Marshal(result)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/relay/v1/results/"+id, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("result: %d %s", resp.StatusCode, data)
	}
}

func TestOfflineQueueDeliversAndReturnsResult(t *testing.T) {
	service, server := testRelay(t, nil)
	target := "toget-test-target"
	service.mu.Lock()
	service.targets[target] = &targetEntry{kind: "node", registeredAt: time.Now(), expires: time.Now().Add(time.Hour)}
	service.mu.Unlock()

	resp, data := forward(t, server, relayproto.Envelope{
		Target: target, Method: http.MethodGet, Path: "/toget/v1/health",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("offline forward = %d %s", resp.StatusCode, data)
	}
	var queued relayproto.Queued
	if err := json.Unmarshal(data, &queued); err != nil || queued.Ticket == "" {
		t.Fatalf("queue response = %s", data)
	}
	if resp.Header.Get(relayproto.HeaderRelayQueued) != "true" {
		t.Fatal("missing queued header")
	}

	ticket := queued.Ticket

	// A ticket before the node comes back is still pending.
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/relay/v1/responses/"+ticket, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("pending ticket = %d", resp.StatusCode)
	}

	addTestSession(service, target, "node", "node-token")
	frames := poll(t, server, target, "node-token")
	if len(frames) != 1 || frames[0].RequestID != ticket || frames[0].Target != target {
		t.Fatalf("poll frames = %#v", frames)
	}
	postResult(t, server, "node-token", ticket, http.StatusOK, []byte("ok\n"))

	req, _ = http.NewRequest(http.MethodGet, server.URL+"/relay/v1/responses/"+ticket, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(data) != "ok\n" {
		t.Fatalf("queued result = %d %q", resp.StatusCode, data)
	}
	if resp.Header.Get(relayproto.HeaderRelayedBy) != service.relayID {
		t.Fatalf("relayed-by = %q", resp.Header.Get(relayproto.HeaderRelayedBy))
	}
}

func TestRouteForwardingAndLoopDetection(t *testing.T) {
	service, server := testRelay(t, nil)
	childID, childToken := registerFakeRelay(t, server)
	child := addTestSession(service, childID, "relay", childToken)
	target := "toget-route-target"

	routes, _ := json.Marshal(relayproto.RoutesRequest{
		Protocol: relayproto.Protocol,
		Routes: []relayproto.RouteAnnouncement{{
			Target: target, Kind: "node", Hops: 1, Via: []string{childID},
		}},
		UpdatedAt: time.Now().UTC(),
	})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/relay/v1/routes", bytes.NewReader(routes))
	req.Header.Set("Authorization", "Bearer "+childToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("routes = %d", resp.StatusCode)
	}

	type answer struct {
		resp *http.Response
		data []byte
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		env := relayproto.Envelope{
			Protocol: relayproto.Protocol, Target: target,
			Method: http.MethodGet, Path: "/toget/v1/health",
		}
		body, _ := json.Marshal(env)
		req, _ := http.NewRequest(http.MethodPost, server.URL+relayproto.ForwardPrefix+target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			done <- answer{err: err}
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		done <- answer{resp: resp, data: data}
	}()
	select {
	case frame := <-child.frames:
		if frame.Target != target || !relayproto.HasHop(&frame, service.relayID) {
			t.Fatalf("child frame = %#v", frame)
		}
		postResult(t, server, childToken, frame.RequestID, http.StatusOK, []byte("routed"))
	case <-time.After(3 * time.Second):
		t.Fatal("child relay did not receive the frame")
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.resp.StatusCode != http.StatusOK || string(result.data) != "routed" {
		t.Fatalf("routed forward = %d %q", result.resp.StatusCode, result.data)
	}

	// A directly registered target detects its own relay ID in the path.
	addTestSession(service, "node-loop", "node", "loop-token")
	resp, data := forward(t, server, relayproto.Envelope{
		Target: "node-loop", Method: http.MethodGet, Path: "/toget/v1/health",
		Hops: []string{service.relayID},
	})
	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("loop forward = %d %s", resp.StatusCode, data)
	}

	hops := make([]string, relayproto.MaxHops)
	for i := range hops {
		hops[i] = "toget-hop-" + string(rune('a'+i))
	}
	resp, data = forward(t, server, relayproto.Envelope{
		Target: "node-loop", Method: http.MethodGet, Path: "/toget/v1/health", Hops: hops,
	})
	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("hop-limit forward = %d %s", resp.StatusCode, data)
	}
}

func TestRouteAnnouncementRejectsLoopsAndDirectWins(t *testing.T) {
	service, server := testRelay(t, nil)
	childID, childToken := registerFakeRelay(t, server)

	announce := func(target string, via []string) {
		t.Helper()
		payload, _ := json.Marshal(relayproto.RoutesRequest{
			Protocol: relayproto.Protocol,
			Routes: []relayproto.RouteAnnouncement{{
				Target: target, Kind: "node", Hops: len(via), Via: via,
			}},
		})
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/relay/v1/routes", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+childToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	announce("node-loop-route", []string{childID, service.relayID})
	addTestSession(service, "node-direct-wins", "node", "direct-token")
	announce("node-direct-wins", []string{childID})

	resp, data := forward(t, server, relayproto.Envelope{
		Target: "node-loop-route", Method: http.MethodGet, Path: "/toget/v1/health",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("looping route was accepted: %d %s", resp.StatusCode, data)
	}
	service.mu.RLock()
	_, stored := service.routes["node-loop-route"]
	_, directRoute := service.routes["node-direct-wins"]
	service.mu.RUnlock()
	if stored || directRoute {
		t.Fatalf("unexpected routes: loop=%v direct=%v", stored, directRoute)
	}
}

func TestOfflineQueueQuota(t *testing.T) {
	service, server := testRelay(t, func(opts *Options) { opts.MaxQueued = 1 })
	target := "toget-quota-target"
	service.mu.Lock()
	service.targets[target] = &targetEntry{kind: "node", registeredAt: time.Now(), expires: time.Now().Add(time.Hour)}
	service.mu.Unlock()
	resp, data := forward(t, server, relayproto.Envelope{
		Target: target, Method: http.MethodGet, Path: "/toget/v1/health",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first queue = %d %s", resp.StatusCode, data)
	}
	resp, data = forward(t, server, relayproto.Envelope{
		Target: target, Method: http.MethodGet, Path: "/toget/v1/health",
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second queue = %d %s", resp.StatusCode, data)
	}
}
