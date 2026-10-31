package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/room"
)

func TestMergeRoomAgents(t *testing.T) {
	joined := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	local := []agent.Agent{{
		ID: "local", AccountID: "me", Name: "Local", Runtime: "echo",
		Status: agent.StatusRunning, Permissions: json.RawMessage(`{"shell":true}`), CreatedAt: joined,
	}}
	presence := []room.AgentPresence{
		{AgentID: "local", AccountID: "me", Name: "Stale", Status: "idle", JoinedAt: joined.Add(time.Hour)},
		{AgentID: "shared", AccountID: "peer", Name: "Shared", Runtime: "claude-code", Status: "waiting_approval", JoinedAt: joined},
		{AgentID: "blank", AccountID: "peer", Name: "Blank", JoinedAt: joined},
		{AgentID: "gone", AccountID: "me", Name: "Revoked", JoinedAt: joined},
	}
	merged := mergeRoomAgents(local, presence, map[string]bool{"gone": true})
	if len(merged) != 3 {
		t.Fatalf("merged = %#v", merged)
	}
	if merged[0].ID != "local" || merged[0].Name != "Local" || merged[0].Status != agent.StatusRunning {
		t.Fatalf("local row was not preserved: %#v", merged[0])
	}
	if merged[1].ID != "shared" || merged[1].AccountID != "peer" || merged[1].Status != "waiting_approval" ||
		string(merged[1].Permissions) != "{}" || !merged[1].CreatedAt.Equal(joined) {
		t.Fatalf("shared row = %#v", merged[1])
	}
	if merged[2].Status != "idle" {
		t.Fatalf("blank status = %q, want idle", merged[2].Status)
	}
	if empty := mergeRoomAgents(nil, nil, nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty merge = %#v, want a non-nil empty list", empty)
	}
}
