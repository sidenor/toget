package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

func setup(t *testing.T) (*agent.Service, *room.Service, *storage.DB) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rooms := room.New(db, v)
	return agent.New(db, rooms), rooms, db
}

func actor(t *testing.T) room.Actor {
	t.Helper()
	_, priv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, _, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	return room.Actor{AccountID: "account-1", DeviceID: "device-1", DisplayName: "Aiden",
		SigningKey: priv, AccountPublicKey: pub, EncryptionPublicKey: enc}
}

func TestAgentLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, rooms, _ := setup(t)
	a := actor(t)
	r, _, err := rooms.CreateRoom(ctx, "General", "", a)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := svc.Register(ctx, a.AccountID, "Claude", "claude-code", json.RawMessage(`{"shell":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if ag.Status != agent.StatusIdle || ag.ID == "" {
		t.Fatalf("unexpected agent: %#v", ag)
	}
	if _, _, err := svc.JoinRoom(ctx, ag.ID, r.ID, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.JoinRoom(ctx, ag.ID, r.ID, a); !errors.Is(err, agent.ErrAlreadyInRoom) {
		t.Fatalf("second join error = %v", err)
	}
	inRoom, err := svc.ListInRoom(ctx, r.ID)
	if err != nil || len(inRoom) != 1 || inRoom[0].ID != ag.ID {
		t.Fatalf("room agents = %#v, err=%v", inRoom, err)
	}
	updated, event, err := svc.SetStatus(ctx, ag.ID, agent.StatusRunning, "working", r.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != agent.StatusRunning || event.Type != room.EventAgentStatus {
		t.Fatalf("unexpected status update: %#v %#v", updated, event)
	}
	if _, _, err := svc.SetStatus(ctx, ag.ID, "bogus", "", "", a); err == nil {
		t.Fatal("invalid status accepted")
	}
	foreign := a
	foreign.AccountID = "account-2"
	if err := svc.Revoke(ctx, ag.ID, foreign); !errors.Is(err, agent.ErrNotAgentOwner) {
		t.Fatalf("foreign revoke error = %v", err)
	}
	if err := svc.Revoke(ctx, ag.ID, a); err != nil {
		t.Fatal(err)
	}
	if inRoom, err := svc.ListInRoom(ctx, r.ID); err != nil || len(inRoom) != 0 {
		t.Fatalf("room agents after revoke = %#v, err=%v", inRoom, err)
	}
	events, err := rooms.ListEvents(ctx, r.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != room.EventAgentLeft {
		t.Fatalf("last event after revoke = %#v", last)
	}
	var left room.AgentLeftPayload
	if err := json.Unmarshal(last.Payload, &left); err != nil || left.AgentID != ag.ID || left.Reason != "revoked" {
		t.Fatalf("revoke leave payload = %#v, %v", left, err)
	}
	if err := svc.Revoke(ctx, ag.ID, a); err != nil {
		t.Fatalf("second revoke = %v", err)
	}
	if _, _, err := svc.JoinRoom(ctx, ag.ID, r.ID, a); !errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("revoked join error = %v", err)
	}
}

func TestValidationAndMentions(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setup(t)
	if _, err := svc.Register(ctx, "a", " ", "cli", nil); !errors.Is(err, agent.ErrInvalidName) {
		t.Fatalf("empty name error = %v", err)
	}
	if _, err := svc.Register(ctx, "a", "A", " ", nil); !errors.Is(err, agent.ErrInvalidRuntime) {
		t.Fatalf("empty runtime error = %v", err)
	}
	if _, err := svc.Register(ctx, "a", "A", "cli", json.RawMessage(`nope`)); err == nil {
		t.Fatal("invalid permissions accepted")
	}
	agents := []agent.Agent{{ID: "1", Name: "Claude"}, {ID: "2", Name: "Researcher"}}
	got := agent.DetectMentions("hey @claude please, and @Researcher.", agents)
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("mentions = %#v", got)
	}
	if got := agent.DetectMentions("@claudette", agents); len(got) != 0 {
		t.Fatalf("partial name matched: %#v", got)
	}
}
