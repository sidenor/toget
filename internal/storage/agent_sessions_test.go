package storage

import (
	"context"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
)

func TestAgentSessionsPersistAndOrder(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedRoom(t, db)
	if err := db.CreateAgent(ctx, &agent.Agent{
		ID: "agent-1", AccountID: "account-1", Name: "Claude", Runtime: "echo",
		Status: agent.StatusIdle, Permissions: []byte(`{}`), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.GetAgentSession(ctx, "agent-1", "room-1"); err != agent.ErrSessionNotFound {
		t.Fatalf("missing session error = %v", err)
	}

	base := time.Now().UTC().Truncate(time.Millisecond)
	first := &agent.Session{
		AgentID: "agent-1", RoomID: "room-1", LastSeenSeq: 3, LastRunSeq: 2,
		RuntimeSession: "runtime-1", RunCount: 1, Status: agent.StatusIdle, UpdatedAt: base,
	}
	if err := db.SaveAgentSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAgentSession(ctx, "agent-1", "room-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenSeq != 3 || got.LastRunSeq != 2 || got.RuntimeSession != "runtime-1" || got.RunCount != 1 {
		t.Fatalf("session = %#v", got)
	}

	first.Status = agent.StatusRunning
	first.LastSeenSeq = 5
	first.ActiveEventID = "event-5"
	first.ActiveSeq = 5
	first.UpdatedAt = base.Add(time.Second)
	if err := db.SaveAgentSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetAgentSession(ctx, "agent-1", "room-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != agent.StatusRunning || got.LastSeenSeq != 5 || got.ActiveEventID != "event-5" || got.ActiveSeq != 5 {
		t.Fatalf("upserted session = %#v", got)
	}

	finished := *first
	finished.Status = agent.StatusIdle
	finished.LastRunSeq = 5
	finished.RunCount = 2
	finished.ActiveEventID = ""
	finished.ActiveSeq = 0
	if ok, err := db.SaveAgentSessionIfActive(ctx, &finished, "some-other-event"); err != nil || ok {
		t.Fatalf("conditional save with a stale trigger = %v, %v", ok, err)
	}
	if ok, err := db.SaveAgentSessionIfActive(ctx, &finished, "event-5"); err != nil || !ok {
		t.Fatalf("conditional save = %v, %v", ok, err)
	}
	if ok, err := db.SaveAgentSessionIfActive(ctx, &finished, "event-5"); err != nil || ok {
		t.Fatalf("conditional save reused a completed trigger = %v, %v", ok, err)
	}
	got, err = db.GetAgentSession(ctx, "agent-1", "room-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != agent.StatusIdle || got.LastRunSeq != 5 || got.RunCount != 2 || got.ActiveEventID != "" {
		t.Fatalf("conditionally saved session = %#v", got)
	}

	sessions, err := db.ListAgentSessions(ctx, "agent-1")
	if err != nil || len(sessions) != 1 || sessions[0].RoomID != "room-1" {
		t.Fatalf("sessions = %#v, err = %v", sessions, err)
	}

	if err := db.DeleteAgentSession(ctx, "agent-1", "room-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAgentSession(ctx, "agent-1", "room-1"); err != agent.ErrSessionNotFound {
		t.Fatalf("after delete error = %v", err)
	}
	if err := db.DeleteAgentSession(ctx, "agent-1", "room-1"); err != nil {
		t.Fatalf("idempotent delete error = %v", err)
	}
}
