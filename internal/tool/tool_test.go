package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/tool"
	"github.com/sidenor/toget/internal/vault"
)

type fixture struct {
	tools     *tool.Service
	approvals *approval.Service
	agents    *agent.Service
	rooms     *room.Service
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
	approvals := approval.New(db, rooms)
	tools := tool.New(db, rooms, approvals)

	_, devPriv, err := identity.NewDeviceKey()
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
	actor := room.Actor{AccountID: "account-1", DeviceID: "device-1", DisplayName: "Aiden",
		SigningKey: devPriv, AccountPublicKey: acctPub, EncryptionPublicKey: encPub}
	r, _, err := rooms.CreateRoom(context.Background(), "General", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agents.Register(context.Background(), actor.AccountID, "Claude", "claude-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := agents.JoinRoom(context.Background(), ag.ID, r.ID, actor); err != nil {
		t.Fatal(err)
	}
	return &fixture{tools: tools, approvals: approvals, agents: agents, rooms: rooms,
		actor: actor, room: r, agent: ag}
}

func TestReadToolNeedsNoApproval(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	input := json.RawMessage(`{"path":"/tmp/a"}`)
	r, event, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindRead, "read_file", input, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != tool.StatusRequested || event.Type != room.EventToolRequested {
		t.Fatalf("receipt = %#v event = %#v", r, event)
	}
	done, event, err := f.tools.Complete(ctx, r.ID, f.actor, "hello", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != tool.StatusSucceeded || event.Type != room.EventToolResult {
		t.Fatalf("done = %#v event = %#v", done, event)
	}
	if _, _, err := f.tools.Complete(ctx, r.ID, f.actor, "again", true); !errors.Is(err, tool.ErrNotActive) {
		t.Fatalf("second complete = %v", err)
	}
}

func TestRiskyToolRequiresApproval(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	input := json.RawMessage(`{"command":"rm -rf /tmp/x"}`)
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindShell, "shell", input, ""); !errors.Is(err, tool.ErrApprovalRequired) {
		t.Fatalf("no approval = %v", err)
	}

	action, err := approval.ToolAction("shell", input)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, action, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Decide(ctx, a.ID, f.actor, approval.DecisionGrant); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindShell, "shell", input, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ApprovalID != a.ID {
		t.Fatalf("approval id = %q", receipt.ApprovalID)
	}
	if _, _, err := f.approvals.Consume(ctx, a.ID, a.ActionDigest, f.actor); !errors.Is(err, approval.ErrConsumed) {
		t.Fatalf("approval reused: %v", err)
	}
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindShell, "shell", input, a.ID); !errors.Is(err, approval.ErrConsumed) {
		t.Fatalf("tool reused approval: %v", err)
	}

	// A different action must not match the same approval.
	other := json.RawMessage(`{"command":"echo hi"}`)
	b, _, err := f.approvals.Request(ctx, f.room.ID, f.agent.ID, f.actor, approval.Action{Kind: "tool.call", Tool: "shell", Parameters: map[string]any{"input": map[string]any{"command": "echo hi"}}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.approvals.Decide(ctx, b.ID, f.actor, approval.DecisionGrant); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindShell, "other", other, b.ID); !errors.Is(err, approval.ErrDigestMismatch) {
		t.Fatalf("tool mismatch = %v", err)
	}
}

func TestValidationAndReceiptList(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, "bogus", "x", nil, ""); !errors.Is(err, tool.ErrInvalidKind) {
		t.Fatalf("invalid kind = %v", err)
	}
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindRead, "", nil, ""); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("invalid tool = %v", err)
	}
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindRead, "x", json.RawMessage(`nope`), ""); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, room.Actor{}, tool.KindRead, "x", nil, ""); err == nil {
		t.Fatal("unauthorized actor accepted")
	}
	r, _, err := f.tools.Request(ctx, f.room.ID, f.agent.ID, f.actor, tool.KindRead, "x", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := f.tools.List(ctx, f.room.ID, 0)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("list = %#v, err = %v", list, err)
	}
	if !tool.RequiresApproval(tool.KindExternal) || tool.RequiresApproval(tool.KindRead) {
		t.Fatal("approval policy mismatch")
	}
}
