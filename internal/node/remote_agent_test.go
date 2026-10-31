package node_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/approval"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/tool"
)

func joinRemoteRoom(t *testing.T, host, guest *testNode) *room.Room {
	t.Helper()
	ctx := context.Background()
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := host.rooms.CreateRoom(ctx, "Remote agent room", "", hostActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.node.EnsureRoomHome(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	invite, _, err := host.rooms.CreateInvite(ctx, record.ID, hostActor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, record.ID, invite.Secret, record.Name)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := guest.node.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	return joined
}

func TestRemoteRoomAgentEvents(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	joined := joinRemoteRoom(t, host, guest)
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	agents := agent.New(guest.db, guest.rooms)
	approvals := approval.New(guest.db, guest.rooms)
	tools := tool.New(guest.db, guest.rooms, approvals)
	created, err := agents.Register(ctx, guest.snapshot.Account.ID, "Remote Claude", "echo", json.RawMessage(`{"shell":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, event, err := agents.JoinRoom(ctx, created.ID, joined.ID, guestActor); err != nil {
		t.Fatalf("join remote room: %v", err)
	} else if event == nil || event.Type != room.EventAgentJoined {
		t.Fatalf("join event = %#v", event)
	}
	binding, err := host.db.GetRoomAgent(ctx, joined.ID, created.ID)
	if err != nil || !binding.Active || binding.AccountID != guest.snapshot.Account.ID {
		t.Fatalf("host projection = %#v, %v", binding, err)
	}
	inRoom, err := guest.db.IsAgentInRoom(ctx, created.ID, joined.ID)
	if err != nil || !inRoom {
		t.Fatalf("guest local membership = %v, %v", inRoom, err)
	}

	if _, event, err := agents.SetStatus(ctx, created.ID, agent.StatusRunning, "working", joined.ID, guestActor); err != nil {
		t.Fatalf("remote status: %v", err)
	} else if event == nil || event.Type != room.EventAgentStatus {
		t.Fatalf("status event = %#v", event)
	}

	input := json.RawMessage(`{"url":"https://example.com"}`)
	action, err := approval.ToolAction("web.fetch", input)
	if err != nil {
		t.Fatal(err)
	}
	requested, _, err := approvals.Request(ctx, joined.ID, created.ID, guestActor, action, time.Minute)
	if err != nil {
		t.Fatalf("remote approval request: %v", err)
	}
	if decided, _, err := approvals.Decide(ctx, requested.ID, guestActor, approval.DecisionGrant); err != nil || decided.Status != approval.StatusGranted {
		t.Fatalf("remote approval decision = %#v, %v", decided, err)
	}
	receipt, _, err := tools.Request(ctx, joined.ID, created.ID, guestActor, tool.KindExternal, "web.fetch", input, requested.ID)
	if err != nil {
		t.Fatalf("remote tool request: %v", err)
	}
	if _, _, err := tools.Complete(ctx, receipt.ID, guestActor, "fetched", true); err != nil {
		t.Fatalf("remote tool completion: %v", err)
	}
	if _, _, err := agents.SetStatus(ctx, created.ID, agent.StatusIdle, "", joined.ID, guestActor); err != nil {
		t.Fatalf("remote idle status: %v", err)
	}

	// The home node accepted the owner's events and every event also landed in
	// the guest's own room log.
	hostEvents, err := host.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{
		room.EventAgentJoined, room.EventAgentStatus, room.EventApprovalRequested,
		room.EventApprovalGranted, room.EventApprovalConsumed, room.EventToolRequested,
		room.EventToolResult, room.EventAgentStatus,
	}
	var types []string
	for _, event := range hostEvents {
		if event.Type != room.EventRoomCreated && event.Type != room.EventMemberInvited && event.Type != room.EventMemberJoined {
			types = append(types, event.Type)
		}
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("host agent events = %v, want %v", types, wantTypes)
	}
	for i := range wantTypes {
		if types[i] != wantTypes[i] {
			t.Fatalf("host agent events = %v, want %v", types, wantTypes)
		}
	}
	guestEvents, err := guest.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var guestTypes []string
	for _, event := range guestEvents {
		if event.Type != room.EventMemberJoined {
			guestTypes = append(guestTypes, event.Type)
		}
	}
	if len(guestTypes) != len(wantTypes) {
		t.Fatalf("guest agent events = %v, want %v", guestTypes, wantTypes)
	}
	last := guestEvents[len(guestEvents)-1]
	if last.Type != room.EventAgentStatus || last.AuthorID != guest.snapshot.Account.ID {
		t.Fatalf("last guest event = %#v", last)
	}

	// Leaving closes the home projection and removes the local membership.
	if _, err := agents.LeaveRoom(ctx, created.ID, joined.ID, guestActor); err != nil {
		t.Fatalf("remote leave: %v", err)
	}
	binding, err = host.db.GetRoomAgent(ctx, joined.ID, created.ID)
	if err != nil || binding.Active {
		t.Fatalf("host projection after leave = %#v, %v", binding, err)
	}
	inRoom, err = guest.db.IsAgentInRoom(ctx, created.ID, joined.ID)
	if err != nil || inRoom {
		t.Fatalf("guest membership after leave = %v, %v", inRoom, err)
	}
}

func TestRemoteRoomAgentPresence(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	joined := joinRemoteRoom(t, host, guest)
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	agents := agent.New(guest.db, guest.rooms)
	created, err := agents.Register(ctx, guest.snapshot.Account.ID, "Shared Claude", "claude-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := agents.JoinRoom(ctx, created.ID, joined.ID, guestActor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := agents.SetStatus(ctx, created.ID, agent.StatusRunning, "working", joined.ID, guestActor); err != nil {
		t.Fatal(err)
	}

	hostPresence, err := host.rooms.ListAgentPresence(ctx, joined.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := findPresence(hostPresence, created.ID)
	if found == nil {
		t.Fatalf("host presence = %#v, want %s", hostPresence, created.ID)
	}
	if found.Name != "Shared Claude" || found.Runtime != "claude-code" || found.Status != "running" ||
		found.AccountID != guest.snapshot.Account.ID || found.JoinedAt.IsZero() {
		t.Fatalf("host presence entry = %#v", found)
	}

	// A later member who joins with full history decrypts the Agent events and
	// sees the same shared Agent without contacting the owner's node.
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := host.rooms.CreateInvite(ctx, joined.ID, hostActor, room.HistoryAll, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, joined.ID, invite.Secret, joined.Name)
	if err != nil {
		t.Fatal(err)
	}
	mallory := newNode(t, "Mallory")
	if _, err := mallory.node.Join(ctx, code); err != nil {
		t.Fatal(err)
	}
	malloryPresence, err := mallory.rooms.ListAgentPresence(ctx, joined.ID)
	if err != nil {
		t.Fatal(err)
	}
	found = findPresence(malloryPresence, created.ID)
	if found == nil || found.Name != "Shared Claude" || found.Status != "running" {
		t.Fatalf("mallory presence = %#v", malloryPresence)
	}

	// Revocation removes the Agent from every room, so the home projection and
	// the shared presence view close without the owner issuing a manual leave.
	if err := agents.Revoke(ctx, created.ID, guestActor); err != nil {
		t.Fatal(err)
	}
	binding, err := host.db.GetRoomAgent(ctx, joined.ID, created.ID)
	if err != nil || binding.Active {
		t.Fatalf("host projection after revoke = %#v, %v", binding, err)
	}
	hostEvents, err := host.rooms.ListEvents(ctx, joined.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := hostEvents[len(hostEvents)-1]
	if last.Type != room.EventAgentLeft {
		t.Fatalf("last host event after revoke = %#v", last)
	}
	var left room.AgentLeftPayload
	if err := json.Unmarshal(last.Payload, &left); err != nil || left.AgentID != created.ID || left.Reason != "revoked" {
		t.Fatalf("revoke payload = %#v, %v", left, err)
	}
	if inRoom, err := guest.db.IsAgentInRoom(ctx, created.ID, joined.ID); err != nil || inRoom {
		t.Fatalf("guest membership after revoke = %v, %v", inRoom, err)
	}
	if presence, err := host.rooms.ListAgentPresence(ctx, joined.ID); err != nil {
		t.Fatal(err)
	} else if findPresence(presence, created.ID) != nil {
		t.Fatalf("host presence after revoke = %#v", presence)
	}
}

func findPresence(list []room.AgentPresence, agentID string) *room.AgentPresence {
	for i := range list {
		if list[i].AgentID == agentID {
			return &list[i]
		}
	}
	return nil
}

func TestRemoteRoomAgentAuthorization(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Host")
	guest := newNode(t, "Guest")
	mallory := newNode(t, "Mallory")
	joined := joinRemoteRoom(t, host, guest)
	guestActor, err := guest.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Mallory joins the same room.
	hostActor, err := host.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := host.rooms.CreateInvite(ctx, joined.ID, hostActor, room.HistoryNone, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := host.node.InviteCode(ctx, joined.ID, invite.Secret, joined.Name)
	if err != nil {
		t.Fatal(err)
	}
	malloryRoom, err := mallory.node.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	malloryActor, err := mallory.node.LocalActor(ctx)
	if err != nil {
		t.Fatal(err)
	}

	agents := agent.New(guest.db, guest.rooms)
	created, err := agents.Register(ctx, guest.snapshot.Account.ID, "Guarded", "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Status before the join event exists must fail closed on the home node.
	err = postRemoteAgentStatus(t, mallory, malloryRoom.ID, malloryActor, created.ID, "running")
	var remoteErr *node.RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.Status != 403 {
		t.Fatalf("status before join = %v, want 403", err)
	}

	if _, _, err := agents.JoinRoom(ctx, created.ID, joined.ID, guestActor); err != nil {
		t.Fatal(err)
	}
	// Another member cannot speak for an Agent owned by the guest.
	err = postRemoteAgentStatus(t, mallory, malloryRoom.ID, malloryActor, created.ID, "running")
	if !errors.As(err, &remoteErr) || remoteErr.Status != 403 {
		t.Fatalf("foreign status = %v, want 403", err)
	}
	// Nor can a message borrow the guest Agent's attribution.
	_, err = mallory.node.PostRemoteMessage(ctx, malloryRoom.ID, malloryActor, room.MessagePostedPayload{
		Text: "pretending", AgentID: created.ID,
	})
	if !errors.As(err, &remoteErr) || remoteErr.Status != 403 {
		t.Fatalf("foreign attribution = %v, want 403", err)
	}

	// The owner can post the same status.
	if _, err := guest.node.PostRemoteEvent(ctx, joined.ID, guestActor, room.EventAgentStatus, room.AgentStatusPayload{
		AgentID: created.ID, Status: "running",
	}); err != nil {
		t.Fatalf("owner status: %v", err)
	}
}

func postRemoteAgentStatus(t *testing.T, n *testNode, roomID string, actor room.Actor, agentID, status string) error {
	t.Helper()
	_, err := n.node.PostRemoteEvent(context.Background(), roomID, actor, room.EventAgentStatus, room.AgentStatusPayload{
		AgentID: agentID, Status: status,
	})
	return err
}
