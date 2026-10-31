package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/room"
)

type fakeStreamProvider struct {
	mu      sync.Mutex
	streams []agent.RunStream
	rooms   []string
}

func (f *fakeStreamProvider) Streams(roomID string) []agent.RunStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms = append(f.rooms, roomID)
	out := make([]agent.RunStream, 0)
	for _, stream := range f.streams {
		if stream.RoomID == roomID {
			out = append(out, stream)
		}
	}
	return out
}

func (f *fakeStreamProvider) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rooms...)
}

func TestListAgentStreams(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	provider := &fakeStreamProvider{}
	node := newTestNodeWithOptions(t, nil, func(opts *api.Options) {
		opts.AgentStreams = provider
	})
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"Streams"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}
	var created struct {
		Room room.Room `json:"room"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	provider.streams = []agent.RunStream{
		{
			RunID: "run-1", AgentID: "agent-1", AgentName: "Claude", RoomID: created.Room.ID,
			TriggerEventID: "event-1", TriggerSeq: 4, Text: "partial", Truncated: true,
			StartedAt: now, UpdatedAt: now.Add(time.Second),
		},
		{
			RunID: "run-2", AgentID: "agent-2", AgentName: "Other", RoomID: created.Room.ID + "-other",
			Text: "not this room", StartedAt: now, UpdatedAt: now,
		},
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/rooms/"+created.Room.ID+"/agent-streams", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list streams = %d %s", resp.StatusCode, body)
	}
	result := decode[struct {
		Streams []agent.RunStream `json:"streams"`
	}](t, body)
	if len(result.Streams) != 1 {
		t.Fatalf("streams = %s", body)
	}
	stream := result.Streams[0]
	if stream.RunID != "run-1" || stream.AgentName != "Claude" || stream.Text != "partial" ||
		!stream.Truncated || stream.TriggerSeq != 4 {
		t.Fatalf("stream = %s", body)
	}
	if calls := provider.calls(); len(calls) != 1 || calls[0] != created.Room.ID {
		t.Fatalf("provider calls = %#v", calls)
	}

	resp, _ = node.request(t, http.MethodGet, "/api/v1/rooms/missing/agent-streams", testToken, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing room = %d", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodGet, "/api/v1/rooms/"+created.Room.ID+"/agent-streams", "", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", resp.StatusCode)
	}
	if calls := provider.calls(); len(calls) != 1 {
		t.Fatalf("provider was called for a rejected request: %#v", calls)
	}
}

func TestListAgentStreamsWithoutRunnerIsEmptyArray(t *testing.T) {
	node, roomID := setupAccountRoom(t)
	resp, body := node.request(t, http.MethodGet, "/api/v1/rooms/"+roomID+"/agent-streams", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list streams = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"streams":[]`) {
		t.Fatalf("streams should be an empty array, got %s", body)
	}
}
