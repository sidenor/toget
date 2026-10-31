package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/room"
)

type recordingRuntime struct {
	mu       sync.Mutex
	requests []agent.RunRequest
	run      func(context.Context, agent.RunRequest) (agent.RunResult, error)
}

func (r *recordingRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	run := r.run
	r.mu.Unlock()
	if run != nil {
		return run(ctx, req)
	}
	return agent.RunResult{Text: "reply " + fmt.Sprint(len(r.snapshot())), Format: "markdown"}, nil
}

func (r *recordingRuntime) snapshot() []agent.RunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.RunRequest(nil), r.requests...)
}

func (r *recordingRuntime) setRun(run func(context.Context, agent.RunRequest) (agent.RunResult, error)) {
	r.mu.Lock()
	r.run = run
	r.mu.Unlock()
}

type runtimeHarness struct {
	agents  *agent.Service
	rooms   *room.Service
	actor   room.Actor
	room    *room.Room
	agent   *agent.Agent
	runtime *recordingRuntime
	runner  *agent.Runner
}

func newRuntimeHarness(t *testing.T) *runtimeHarness {
	t.Helper()
	runtime := &recordingRuntime{}
	h := newRuntimeHarnessWithRuntime(t, runtime)
	h.runtime = runtime
	return h
}

func newRuntimeHarnessWithRuntime(t *testing.T, runtime agent.Runtime) *runtimeHarness {
	t.Helper()
	agents, rooms, _ := setup(t)
	actor := actor(t)
	record, _, err := rooms.CreateRoom(context.Background(), "Runtime room", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	created, err := agents.Register(context.Background(), actor.AccountID, "Claude", "test-runtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := agents.JoinRoom(context.Background(), created.ID, record.ID, actor); err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(agent.RunnerOptions{
		Agents: agents,
		Rooms:  rooms,
		Actor: func(context.Context) (room.Actor, error) {
			return actor, nil
		},
		Runtimes:     map[string]agent.Runtime{"test-runtime": runtime},
		PollInterval: 10 * time.Millisecond,
		RunTimeout:   2 * time.Second,
	})
	return &runtimeHarness{agents: agents, rooms: rooms, actor: actor, room: record, agent: created, runner: runner}
}

func (h *runtimeHarness) start(t *testing.T) {
	t.Helper()
	if err := h.runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.runner.Close)
}

func (h *runtimeHarness) post(t *testing.T, body room.MessagePostedPayload) *room.Event {
	t.Helper()
	event, err := h.rooms.PostMessage(context.Background(), h.room.ID, h.actor, body)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func waitUntil(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func (h *runtimeHarness) events(t *testing.T) []room.Event {
	t.Helper()
	events, err := h.rooms.ListEvents(context.Background(), h.room.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func replyEvent(events []room.Event, agentID string) *room.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != room.EventMessagePosted {
			continue
		}
		var body room.MessagePostedPayload
		if err := jsonUnmarshal(events[i].Payload, &body); err == nil && body.AgentID == agentID {
			return &events[i]
		}
	}
	return nil
}

func jsonUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

func TestRunnerMentionOnlyAndCatchUp(t *testing.T) {
	h := newRuntimeHarness(t)
	h.start(t)

	h.post(t, room.MessagePostedPayload{Text: "hello, no mention"})
	time.Sleep(120 * time.Millisecond)
	if got := len(h.runtime.snapshot()); got != 0 {
		t.Fatalf("runtime ran without a mention: %d", got)
	}

	trigger := h.post(t, room.MessagePostedPayload{Text: "@Claude please summarize"})
	waitUntil(t, 3*time.Second, func() bool { return len(h.runtime.snapshot()) == 1 })
	req := h.runtime.snapshot()[0]
	if req.Trigger.EventID != trigger.ID || req.Room.ID != h.room.ID || req.Agent.ID != h.agent.ID {
		t.Fatalf("run request = %#v", req)
	}
	if !historyHas(req.History, "hello, no mention") || !historyHas(req.History, "please summarize") {
		t.Fatalf("first history = %#v", req.History)
	}

	waitUntil(t, 3*time.Second, func() bool { return replyEvent(h.events(t), h.agent.ID) != nil })
	waitUntil(t, 3*time.Second, func() bool { return countType(h.events(t), room.EventAgentStatus) >= 2 })
	events := h.events(t)
	reply := replyEvent(events, h.agent.ID)
	var body room.MessagePostedPayload
	if err := jsonUnmarshal(reply.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.ReplyTo != trigger.ID || body.Text != "reply 1" {
		t.Fatalf("reply = %#v", body)
	}
	if countType(events, room.EventAgentStatus) < 2 {
		t.Fatalf("expected running and idle status cards, events = %#v", events)
	}
	waitUntil(t, 2*time.Second, func() bool {
		got, err := h.agents.Get(context.Background(), h.agent.ID)
		return err == nil && got.Status == agent.StatusIdle
	})

	// An Agent-authored message never triggers its own Agent.
	h.post(t, room.MessagePostedPayload{Text: "@Claude loop", AgentID: h.agent.ID})
	time.Sleep(120 * time.Millisecond)
	if got := len(h.runtime.snapshot()); got != 1 {
		t.Fatalf("agent message triggered a run: %d", got)
	}

	second := h.post(t, room.MessagePostedPayload{Text: "@Claude second task"})
	waitUntil(t, 3*time.Second, func() bool { return len(h.runtime.snapshot()) == 2 })
	req = h.runtime.snapshot()[1]
	if historyHas(req.History, "hello, no mention") {
		t.Fatalf("second history replayed old messages: %#v", req.History)
	}
	if !historyHas(req.History, "loop") || !historyHas(req.History, "second task") {
		t.Fatalf("second history missed catch-up: %#v", req.History)
	}
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.LastRunSeq == second.Seq && session.RunCount == 2
	})
}

func TestRunnerCatchesUpWhileBusy(t *testing.T) {
	h := newRuntimeHarness(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	h.runtime.run = func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		if strings.Contains(req.Trigger.Text, "first") {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return agent.RunResult{}, ctx.Err()
			}
		}
		return agent.RunResult{Text: "done: " + req.Trigger.Text}, nil
	}
	h.start(t)

	h.post(t, room.MessagePostedPayload{Text: "@Claude first"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first run did not start")
	}
	h.post(t, room.MessagePostedPayload{Text: "something happened while busy"})
	h.post(t, room.MessagePostedPayload{Text: "@Claude second"})
	close(release)
	waitUntil(t, 3*time.Second, func() bool { return len(h.runtime.snapshot()) == 2 })
	second := h.runtime.snapshot()[1]
	if !historyHas(second.History, "something happened while busy") || !historyHas(second.History, "@Claude second") {
		t.Fatalf("catch-up history = %#v", second.History)
	}
}

func TestRunnerTimeoutDoesNotLoseNextMention(t *testing.T) {
	h := newRuntimeHarness(t)
	h.runner = agent.NewRunner(agent.RunnerOptions{
		Agents: h.agents, Rooms: h.rooms,
		Actor:        func(context.Context) (room.Actor, error) { return h.actor, nil },
		Runtimes:     map[string]agent.Runtime{"test-runtime": h.runtime},
		PollInterval: 10 * time.Millisecond,
		RunTimeout:   80 * time.Millisecond,
	})
	calls := 0
	var mu sync.Mutex
	h.runtime.run = func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			<-ctx.Done()
			return agent.RunResult{}, ctx.Err()
		}
		return agent.RunResult{Text: "recovered"}, nil
	}
	h.start(t)

	first := h.post(t, room.MessagePostedPayload{Text: "@Claude slow"})
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.Status == agent.StatusIdle && strings.Contains(session.LastError, "timed out")
	})
	session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.LastRunSeq >= first.Seq {
		t.Fatalf("failed run advanced LastRunSeq: %#v", session)
	}

	h.post(t, room.MessagePostedPayload{Text: "@Claude retry"})
	waitUntil(t, 3*time.Second, func() bool { return len(h.runtime.snapshot()) == 2 })
	second := h.runtime.snapshot()[1]
	if !historyHas(second.History, "@Claude slow") {
		t.Fatalf("retry did not include the failed mention: %#v", second.History)
	}
	waitUntil(t, 3*time.Second, func() bool { return replyEvent(h.events(t), h.agent.ID) != nil })
}

func TestRunnerResumesSessionAndReportsInterruptedRun(t *testing.T) {
	h := newRuntimeHarness(t)
	firstRuntime := &recordingRuntime{run: func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		return agent.RunResult{Text: "first", SessionID: "runtime-session-1"}, nil
	}}
	h.runner = agent.NewRunner(agent.RunnerOptions{
		Agents: h.agents, Rooms: h.rooms,
		Actor:        func(context.Context) (room.Actor, error) { return h.actor, nil },
		Runtimes:     map[string]agent.Runtime{"test-runtime": firstRuntime},
		PollInterval: 10 * time.Millisecond,
		RunTimeout:   time.Second,
	})
	h.start(t)
	h.post(t, room.MessagePostedPayload{Text: "@Claude first"})
	waitUntil(t, 3*time.Second, func() bool { return len(firstRuntime.snapshot()) == 1 })
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.LastRunSeq > 0
	})
	h.runner.Close()

	session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.RuntimeSession != "runtime-session-1" {
		t.Fatalf("runtime session = %q", session.RuntimeSession)
	}
	session.Status = agent.StatusRunning
	session.ActiveEventID = "interrupted-event"
	session.ActiveSeq = session.LastSeenSeq
	if err := h.agents.SaveSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	secondRuntime := &recordingRuntime{run: func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		if req.SessionID != "runtime-session-1" {
			return agent.RunResult{}, fmt.Errorf("session not resumed: %q", req.SessionID)
		}
		return agent.RunResult{Text: "second", SessionID: "runtime-session-1"}, nil
	}}
	h.runner = agent.NewRunner(agent.RunnerOptions{
		Agents: h.agents, Rooms: h.rooms,
		Actor:        func(context.Context) (room.Actor, error) { return h.actor, nil },
		Runtimes:     map[string]agent.Runtime{"test-runtime": secondRuntime},
		PollInterval: 10 * time.Millisecond,
		RunTimeout:   time.Second,
	})
	h.start(t)
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.Status == agent.StatusIdle && strings.Contains(session.LastError, "interrupted")
	})
	if len(secondRuntime.snapshot()) != 0 {
		t.Fatal("an interrupted mention was replayed automatically")
	}
	h.post(t, room.MessagePostedPayload{Text: "@Claude second"})
	waitUntil(t, 3*time.Second, func() bool { return len(secondRuntime.snapshot()) == 1 })
}

func TestRunnerWithoutRuntimeGoesOffline(t *testing.T) {
	h := newRuntimeHarness(t)
	h.agent, _ = h.agents.Register(context.Background(), h.actor.AccountID, "Ghost", "missing-runtime", nil)
	if _, _, err := h.agents.JoinRoom(context.Background(), h.agent.ID, h.room.ID, h.actor); err != nil {
		t.Fatal(err)
	}
	h.runner = agent.NewRunner(agent.RunnerOptions{
		Agents: h.agents, Rooms: h.rooms,
		Actor:        func(context.Context) (room.Actor, error) { return h.actor, nil },
		Runtimes:     map[string]agent.Runtime{},
		PollInterval: 10 * time.Millisecond,
	})
	h.start(t)
	h.post(t, room.MessagePostedPayload{Text: "@Ghost anyone there"})
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		if err != nil || session.Status != agent.StatusOffline || !strings.Contains(session.LastError, "missing-runtime") {
			return false
		}
		got, err := h.agents.Get(context.Background(), h.agent.ID)
		return err == nil && got.Status == agent.StatusOffline
	})
	got, err := h.agents.Get(context.Background(), h.agent.ID)
	if err != nil || got.Status != agent.StatusOffline {
		t.Fatalf("agent status = %#v, %v", got, err)
	}
	if replyEvent(h.events(t), h.agent.ID) != nil {
		t.Fatal("an offline Agent posted a reply")
	}
}

func TestRunnerWaitingApprovalReconciles(t *testing.T) {
	h := newRuntimeHarness(t)
	var mu sync.Mutex
	pending := false
	setPending := func(value bool) {
		mu.Lock()
		pending = value
		mu.Unlock()
	}
	h.runner = agent.NewRunner(agent.RunnerOptions{
		Agents: h.agents, Rooms: h.rooms,
		Actor:        func(context.Context) (room.Actor, error) { return h.actor, nil },
		Runtimes:     map[string]agent.Runtime{"test-runtime": h.runtime},
		PollInterval: 10 * time.Millisecond,
		RunTimeout:   time.Second,
		PendingApprovals: func(context.Context, string, string) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return pending, nil
		},
	})
	h.start(t)
	setPending(true)
	h.post(t, room.MessagePostedPayload{Text: "@Claude do something risky"})
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.Status == agent.StatusWaiting
	})
	setPending(false)
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.Status == agent.StatusIdle
	})
}

func TestRunnerStopLeavesMentionInCatchUp(t *testing.T) {
	h := newRuntimeHarness(t)
	started := make(chan struct{}, 1)
	h.runtime.run = func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	}
	h.start(t)

	first := h.post(t, room.MessagePostedPayload{Text: "@Claude long task"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not start")
	}
	if !h.runner.Stop(h.agent.ID) {
		t.Fatal("Stop reported no active run")
	}
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.Status == agent.StatusIdle && session.ActiveEventID == "" &&
			session.LastError == "" && session.LastRunSeq < first.Seq
	})
	waitUntil(t, 3*time.Second, func() bool {
		return lastStatusDetail(h.events(t), h.agent.ID) == "run stopped"
	})
	if replyEvent(h.events(t), h.agent.ID) != nil {
		t.Fatal("a stopped Agent posted a reply")
	}

	h.runtime.setRun(nil)
	second := h.post(t, room.MessagePostedPayload{Text: "@Claude follow up"})
	waitUntil(t, 3*time.Second, func() bool { return len(h.runtime.snapshot()) == 2 })
	if !historyHas(h.runtime.snapshot()[1].History, "long task") {
		t.Fatalf("stopped mention was not in catch-up: %#v", h.runtime.snapshot()[1].History)
	}
	if !historyHas(h.runtime.snapshot()[1].History, "follow up") {
		t.Fatalf("new mention missing from catch-up: %#v", h.runtime.snapshot()[1].History)
	}
	waitUntil(t, 3*time.Second, func() bool {
		session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
		return err == nil && session.LastRunSeq == second.Seq
	})
}

func TestRunnerResetDuringRunKeepsResetState(t *testing.T) {
	h := newRuntimeHarness(t)
	started := make(chan struct{}, 1)
	h.runtime.run = func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return agent.RunResult{Text: "late reply"}, ctx.Err()
	}
	h.start(t)

	h.post(t, room.MessagePostedPayload{Text: "@Claude first task"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not start")
	}
	head, err := h.rooms.LastSeq(context.Background(), h.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !h.runner.Stop(h.agent.ID) {
		t.Fatal("Stop reported no active run")
	}
	reset, err := h.agents.ResetSession(context.Background(), h.agent.ID, h.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.LastSeenSeq != head || reset.LastRunSeq != head {
		t.Fatalf("reset cursors = %d/%d, head = %d", reset.LastSeenSeq, reset.LastRunSeq, head)
	}

	waitUntil(t, 3*time.Second, func() bool {
		return lastStatusDetail(h.events(t), h.agent.ID) == "run stopped"
	})
	session, err := h.agents.Session(context.Background(), h.agent.ID, h.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The reset moved LastRunSeq to the room head at reset time. The scanner may
	// advance LastSeenSeq over the status card that the stopped run posted.
	if session.RunCount != 0 || session.LastRunSeq != head || session.LastSeenSeq < head ||
		session.ActiveEventID != "" || session.RuntimeSession != "" || session.Status != agent.StatusIdle {
		t.Fatalf("finished run clobbered the reset: %#v", session)
	}
	if replyEvent(h.events(t), h.agent.ID) != nil {
		t.Fatal("a stopped run posted a reply")
	}

	h.runtime.setRun(func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
		if req.SessionID != "" {
			return agent.RunResult{}, fmt.Errorf("session not reset: %q", req.SessionID)
		}
		if historyHas(req.History, "first task") {
			return agent.RunResult{}, fmt.Errorf("history before reset replayed")
		}
		return agent.RunResult{Text: "after reset"}, nil
	})
	h.post(t, room.MessagePostedPayload{Text: "@Claude after reset"})
	waitUntil(t, 3*time.Second, func() bool { return len(h.runtime.snapshot()) == 2 })
	waitUntil(t, 3*time.Second, func() bool {
		events := h.events(t)
		return replyEvent(events, h.agent.ID) != nil
	})
}

func lastStatusDetail(events []room.Event, agentID string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != room.EventAgentStatus {
			continue
		}
		var payload room.AgentStatusPayload
		if err := jsonUnmarshal(events[i].Payload, &payload); err != nil || payload.AgentID != agentID {
			continue
		}
		return payload.Detail
	}
	return ""
}

func historyHas(history []agent.HistoryEntry, text string) bool {
	for _, entry := range history {
		if strings.Contains(entry.Text, text) {
			return true
		}
	}
	return false
}

func countType(events []room.Event, eventType string) int {
	n := 0
	for _, event := range events {
		if event.Type == eventType {
			n++
		}
	}
	return n
}
