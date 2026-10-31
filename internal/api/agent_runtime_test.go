package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/room"
)

func createAgentInRoom(t *testing.T, node *testNode, roomID string) agent.Agent {
	t.Helper()
	resp, body := node.request(t, http.MethodPost, "/api/v1/agents", testToken,
		`{"name":"Claude","runtime":"claude-code"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent = %d %s", resp.StatusCode, body)
	}
	created := decode[agent.Agent](t, body)
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/agents", testToken,
		`{"agent_id":"`+created.ID+`"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("join agent = %d %s", resp.StatusCode, body)
	}
	return created
}

func TestAgentMessagesAndSessions(t *testing.T) {
	node, roomID := setupAccountRoom(t)
	created := createAgentInRoom(t, node, roomID)

	resp, body := node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/messages", testToken,
		`{"text":"@Claude please look","agent_id":"`+created.ID+`"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("agent message = %d %s", resp.StatusCode, body)
	}
	event := decode[room.Event](t, body)
	var payload room.MessagePostedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AgentID != created.ID {
		t.Fatalf("agent id = %q", payload.AgentID)
	}
	if len(payload.Mentions) != 1 || payload.Mentions[0] != created.ID {
		t.Fatalf("auto mentions = %#v", payload.Mentions)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/agents/"+created.ID+"/sessions", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions = %d %s", resp.StatusCode, body)
	}
	sessions := decode[struct {
		Sessions []struct {
			AgentID  string `json:"agent_id"`
			RoomID   string `json:"room_id"`
			RoomName string `json:"room_name"`
			Status   string `json:"status"`
		} `json:"sessions"`
	}](t, body)
	if len(sessions.Sessions) != 1 || sessions.Sessions[0].RoomID != roomID || sessions.Sessions[0].RoomName == "" {
		t.Fatalf("sessions = %s", body)
	}

	resp, body = node.request(t, http.MethodDelete, "/api/v1/agents/"+created.ID+"/sessions/"+roomID, testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset session = %d %s", resp.StatusCode, body)
	}
	session := decode[agent.Session](t, body)
	if session.LastSeenSeq < event.Seq || session.LastRunSeq < event.Seq || session.Status != agent.StatusIdle {
		t.Fatalf("reset session = %#v", session)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/agents/"+created.ID+"/stop", testToken, "", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "agent_not_running") {
		t.Fatalf("stop without a run = %d %s", resp.StatusCode, body)
	}

	other := createAgentInRoom(t, node, roomID)
	// Leaving the room makes agent attribution fail.
	resp, body = node.request(t, http.MethodDelete, "/api/v1/rooms/"+roomID+"/agents/"+other.ID, testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("leave agent = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/messages", testToken,
		`{"text":"ghost","agent_id":"`+other.ID+`"}`, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "agent_not_in_room") {
		t.Fatalf("non-member agent message = %d %s", resp.StatusCode, body)
	}
}
