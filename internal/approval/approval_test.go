package approval_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/policy"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type fixture struct {
	approvals *approval.Service
	policies  *policy.Service
	agents    *agent.Service
	rooms     *room.Service
	db        *storage.DB
	actor     room.Actor
	room      *room.Room
	agent     *agent.Agent
}

func setup(t *testing.T) *fixture {
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
	agents := agent.New(db, rooms)
	policies := policy.New(db, rooms, agents)

	devPub, devPriv, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	acctPub, _, err := identity.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, _, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	_ = devPub
	actor := room.Actor{AccountID: "account-1", DeviceID: "device-1", DisplayName: "Aiden",
		SigningKey: devPriv, AccountPublicKey: acctPub, EncryptionPublicKey: encPub}
	r, _, err := rooms.CreateRoom(context.Background(), "General", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agents.Register(context.Background(), actor.AccountID, "Claude", "claude-code", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := agents.JoinRoom(context.Background(), ag.ID, r.ID, actor); err != nil {
		t.Fatal(err)
	}
	return &fixture{
		approvals: approval.New(db, rooms, policies), policies: policies, agents: agents, rooms: rooms, db: db,
		actor: actor, room: r, agent: ag,
	}
}

func TestApprovalLifecycle(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	action := approval.Action{
		Kind: "external.send", Tool: "slack", Target: "#general", Recipient: "team",
		Parameters: map[string]any{"text": "build finished", "priority": float64(1)},
	}
	digest, err := approval.Digest(action)
	if err != nil {
		t.Fatal(err)
	}
	a, event, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, action, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != approval.StatusPending || a.ActionDigest != digest || event.Type != room.EventApprovalRequested {
		t.Fatalf("unexpected approval: %#v %#v", a, event)
	}
	if !strings.Contains(a.Description, "external.send") || !strings.Contains(a.Description, "text=\"build finished\"") {
		t.Fatalf("description = %q", a.Description)
	}
	if _, _, err := f.approvals.Consume(ctx, a.ID, digest, f.actor); !errors.Is(err, approval.ErrNotGranted) {
		t.Fatalf("consume before grant = %v", err)
	}
	if _, _, err := f.approvals.Decide(ctx, a.ID, f.actor, approval.DecisionGrant); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Consume(ctx, a.ID, "sha256:wrong", f.actor); !errors.Is(err, approval.ErrDigestMismatch) {
		t.Fatalf("wrong digest = %v", err)
	}
	if _, event, err := f.approvals.Consume(ctx, a.ID, digest, f.actor); err != nil || event.Type != room.EventApprovalConsumed {
		t.Fatalf("consume = %v %#v", err, event)
	}
	if _, _, err := f.approvals.Consume(ctx, a.ID, digest, f.actor); !errors.Is(err, approval.ErrConsumed) {
		t.Fatalf("second consume = %v", err)
	}
}

func TestRejectAndExpiry(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	action := approval.Action{Kind: "money.spend", Amount: "10", Currency: "USD", Target: "api"}
	a, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, action, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Decide(ctx, a.ID, f.actor, approval.DecisionReject); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Consume(ctx, a.ID, a.ActionDigest, f.actor); !errors.Is(err, approval.ErrNotGranted) {
		t.Fatalf("consume rejected = %v", err)
	}
	b, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, action, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := f.approvals.Decide(ctx, b.ID, f.actor, approval.DecisionGrant); !errors.Is(err, approval.ErrExpired) {
		t.Fatalf("decide expired = %v", err)
	}
	got, err := f.approvals.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != approval.StatusExpired {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestExpireDueAndValidation(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	action := approval.Action{Kind: "file.delete", Target: "/tmp/x"}
	a, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, action, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	expired, err := f.approvals.ExpireDue(ctx, f.room.ID, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != a.ID {
		t.Fatalf("expired = %#v", expired)
	}
	if _, err := f.approvals.ExpireDue(ctx, f.room.ID, room.Actor{}); err == nil {
		t.Fatal("unauthorized expiry succeeded")
	}
	if _, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, approval.Action{}, time.Hour); !errors.Is(err, approval.ErrInvalidAction) {
		t.Fatalf("empty action = %v", err)
	}
	if _, _, err := f.approvals.Decide(ctx, a.ID, f.actor, "maybe"); !errors.Is(err, approval.ErrInvalidDecision) {
		t.Fatalf("invalid decision = %v", err)
	}
	if _, err := f.approvals.List(ctx, f.room.ID, "bogus"); err == nil {
		t.Fatal("invalid status filter succeeded")
	}
	other := f.actor
	other.AccountID = "account-2"
	if _, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, other, action, time.Hour); err == nil {
		t.Fatal("other account requested approval for agent")
	}
}

func TestAgentMustBeInRoom(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	r2, _, err := f.rooms.CreateRoom(ctx, "Other", "", f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Request(ctx, r2.ID, f.agent.ID, f.actor, approval.Action{Kind: "x"}, time.Hour); !errors.Is(err, approval.ErrNotInRoom) {
		t.Fatalf("agent not in room = %v", err)
	}
}

func TestPolicyAllowAutoGrantsAndDenyRefuses(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, _, err := f.policies.Save(ctx, f.room.ID, f.agent.ID, []policy.Rule{
		{Effect: policy.EffectAllow, Kind: "tool.call", Tool: "mail.send"},
		{Effect: policy.EffectDeny, Kind: "tool.call", Tool: "shell.exec"},
	}, f.actor); err != nil {
		t.Fatal(err)
	}
	allowed := approval.Action{Kind: "tool.call", Tool: "mail.send", Recipient: "a@example.com"}
	a, event, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, allowed, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != approval.StatusGranted || a.PolicyID == "" || a.DecidedBy == "" || event.Type != room.EventApprovalGranted {
		t.Fatalf("auto grant = %#v %#v", a, event)
	}
	digest, err := approval.Digest(allowed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Consume(ctx, a.ID, digest, f.actor); err != nil {
		t.Fatalf("consume auto grant = %v", err)
	}
	if _, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor,
		approval.Action{Kind: "tool.call", Tool: "shell.exec"}, time.Hour); !errors.Is(err, approval.ErrPolicyDenied) {
		t.Fatalf("deny = %v", err)
	}
}
