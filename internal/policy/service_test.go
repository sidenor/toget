package policy_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/policy"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type fixture struct {
	policies *policy.Service
	agents   *agent.Service
	rooms    *room.Service
	room     *room.Room
	owner    room.Actor
	agent    *agent.Agent
}

func newActor(t *testing.T, accountID, deviceID, name string) room.Actor {
	t.Helper()
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
	return room.Actor{AccountID: accountID, DeviceID: deviceID, DisplayName: name,
		SigningKey: devPriv, AccountPublicKey: acctPub, EncryptionPublicKey: encPub}
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
	owner := newActor(t, "account-owner", "device-owner", "Owner")
	r, _, err := rooms.CreateRoom(context.Background(), "General", "", owner)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agents.Register(context.Background(), owner.AccountID, "Claude", "claude-code", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := agents.JoinRoom(context.Background(), ag.ID, r.ID, owner); err != nil {
		t.Fatal(err)
	}
	return &fixture{policies: policies, agents: agents, rooms: rooms, room: r, owner: owner, agent: ag}
}

func TestSaveAndEvaluate(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	rules := []policy.Rule{
		{Effect: policy.EffectDeny, Tool: "shell.*", Note: "no shell"},
		{Effect: policy.EffectAllow, Kind: "tool.call", AmountMax: "50", Currency: "USD"},
	}
	saved, event, err := f.policies.Save(ctx, f.room.ID, f.agent.ID, rules, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Rules) != 2 || saved.Rules[0].ID == "" || event.Type != room.EventPolicyUpdated {
		t.Fatalf("saved = %#v event = %#v", saved, event)
	}
	got, err := f.policies.Get(ctx, f.room.ID, f.agent.ID, f.owner)
	if err != nil || len(got.Rules) != 2 {
		t.Fatalf("get = %#v %v", got, err)
	}

	decision, err := f.policies.Evaluate(ctx, f.room.ID, f.agent.ID, policy.Action{Kind: "tool.call", Tool: "shell.exec"}, at)
	if err != nil || decision.Effect != policy.EffectDeny {
		t.Fatalf("deny = %#v %v", decision, err)
	}
	decision, err = f.policies.Evaluate(ctx, f.room.ID, f.agent.ID, policy.Action{Kind: "tool.call", Tool: "mail.send", Amount: "50", Currency: "usd"}, at)
	if err != nil || decision.Effect != policy.EffectAllow {
		t.Fatalf("allow = %#v %v", decision, err)
	}
	decision, err = f.policies.Evaluate(ctx, f.room.ID, f.agent.ID, policy.Action{Kind: "tool.call", Tool: "mail.send", Amount: "51", Currency: "USD"}, at)
	if err != nil || decision.Effect != policy.EffectReview {
		t.Fatalf("review = %#v %v", decision, err)
	}

	// Replacing the rules keeps the policy identity but changes the rule set.
	replaced, _, err := f.policies.Save(ctx, f.room.ID, f.agent.ID, []policy.Rule{
		{Effect: policy.EffectAllow, Tool: "mail.send"},
	}, f.owner)
	if err != nil || replaced.ID != saved.ID || len(replaced.Rules) != 1 {
		t.Fatalf("replace = %#v %v", replaced, err)
	}
	if _, err := f.policies.Delete(ctx, f.room.ID, f.agent.ID, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.policies.Get(ctx, f.room.ID, f.agent.ID, f.owner); !errors.Is(err, policy.ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
}

func TestRoomPolicyIsOwnerOnlyAndAgentPolicyAllowsAgentOwner(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	guest := newActor(t, "account-guest", "device-guest", "Guest")
	invite, _, err := f.rooms.CreateInvite(ctx, f.room.ID, f.owner, room.HistoryNone, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.rooms.RedeemInvite(ctx, invite.Secret, guest); err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.policies.Save(ctx, f.room.ID, "", []policy.Rule{{Effect: policy.EffectAllow, Kind: "x"}}, guest); !errors.Is(err, policy.ErrForbidden) {
		t.Fatalf("guest room policy = %v", err)
	}
	if _, _, err := f.policies.Save(ctx, f.room.ID, "", []policy.Rule{{Effect: policy.EffectAllow, Kind: "x"}}, f.owner); err != nil {
		t.Fatalf("owner room policy = %v", err)
	}

	guestAgent, err := f.agents.Register(ctx, guest.AccountID, "Guest Agent", "claude-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.agents.JoinRoom(ctx, guestAgent.ID, f.room.ID, guest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.policies.Save(ctx, f.room.ID, guestAgent.ID, []policy.Rule{{Effect: policy.EffectAllow, Tool: "mail.send"}}, guest); err != nil {
		t.Fatalf("agent owner policy = %v", err)
	}
	if _, _, err := f.policies.Save(ctx, f.room.ID, guestAgent.ID, []policy.Rule{{Effect: policy.EffectAllow, Tool: "mail.send"}}, f.owner); err != nil {
		t.Fatalf("room owner policy = %v", err)
	}
	if _, _, err := f.policies.Save(ctx, f.room.ID, f.agent.ID, []policy.Rule{{Effect: policy.EffectAllow, Tool: "mail.send"}}, guest); !errors.Is(err, policy.ErrForbidden) {
		t.Fatalf("guest other agent policy = %v", err)
	}
}

func TestSaveRejectsAgentOutsideRoomAndInvalidRules(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	other, err := f.agents.Register(ctx, f.owner.AccountID, "Other", "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.policies.Save(ctx, f.room.ID, other.ID, nil, f.owner); !errors.Is(err, policy.ErrAgentNotInRoom) {
		t.Fatalf("outside room = %v", err)
	}
	if _, _, err := f.policies.Save(ctx, f.room.ID, f.agent.ID, []policy.Rule{{Effect: "allow"}}, f.owner); !errors.Is(err, policy.ErrInvalid) {
		t.Fatalf("blank allow = %v", err)
	}
}
