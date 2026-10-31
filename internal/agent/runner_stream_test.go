package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/internal/room"
)

type controlledStreamRuntime struct {
	started chan struct{}
	release chan struct{}
	emit    func(func(agent.StreamDelta))
	result  agent.RunResult
	once    sync.Once
}

func (r *controlledStreamRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return r.RunStream(ctx, req, nil)
}

func (r *controlledStreamRuntime) RunStream(ctx context.Context, req agent.RunRequest, emit func(agent.StreamDelta)) (agent.RunResult, error) {
	if r.emit != nil {
		r.emit(emit)
	}
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return r.result, nil
	case <-ctx.Done():
		return agent.RunResult{}, ctx.Err()
	}
}

func waitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not start")
	}
}

func TestRunnerExposesLiveStreamAndClearsIt(t *testing.T) {
	runtime := &controlledStreamRuntime{
		started: make(chan struct{}),
		release: make(chan struct{}),
		emit: func(emit func(agent.StreamDelta)) {
			if emit == nil {
				return
			}
			emit(agent.StreamDelta{Text: "old"})
			emit(agent.StreamDelta{Reset: true})
			emit(agent.StreamDelta{Text: "Hello"})
			emit(agent.StreamDelta{Text: " world"})
		},
		result: agent.RunResult{Text: "Hello world", Format: "markdown"},
	}
	h := newRuntimeHarnessWithRuntime(t, runtime)
	h.start(t)
	trigger := h.post(t, room.MessagePostedPayload{Text: "@Claude stream please"})
	waitStarted(t, runtime.started)
	waitUntil(t, 3*time.Second, func() bool { return len(h.runner.Streams(h.room.ID)) == 1 })

	streams := h.runner.Streams(h.room.ID)
	stream := streams[0]
	if stream.RunID == "" || stream.AgentID != h.agent.ID || stream.AgentName != "Claude" ||
		stream.RoomID != h.room.ID || stream.TriggerEventID != trigger.ID || stream.TriggerSeq != trigger.Seq {
		t.Fatalf("stream = %#v", stream)
	}
	if stream.Text != "Hello world" || stream.Truncated {
		t.Fatalf("stream text = %q, truncated=%v", stream.Text, stream.Truncated)
	}
	if stream.UpdatedAt.Before(stream.StartedAt) {
		t.Fatalf("stream timestamps = %#v", stream)
	}
	if other := h.runner.Streams("another-room"); len(other) != 0 {
		t.Fatalf("streams for another room = %#v", other)
	}

	close(runtime.release)
	waitUntil(t, 3*time.Second, func() bool { return replyEvent(h.events(t), h.agent.ID) != nil })
	waitUntil(t, 3*time.Second, func() bool { return len(h.runner.Streams(h.room.ID)) == 0 })
}

func TestRunnerCapsLiveStreamText(t *testing.T) {
	runtime := &controlledStreamRuntime{
		started: make(chan struct{}),
		release: make(chan struct{}),
		emit: func(emit func(agent.StreamDelta)) {
			if emit != nil {
				emit(agent.StreamDelta{Text: strings.Repeat("x", 64*1024+100)})
			}
		},
	}
	h := newRuntimeHarnessWithRuntime(t, runtime)
	h.start(t)
	h.post(t, room.MessagePostedPayload{Text: "@Claude stream a lot"})
	waitStarted(t, runtime.started)
	waitUntil(t, 3*time.Second, func() bool { return len(h.runner.Streams(h.room.ID)) == 1 })
	stream := h.runner.Streams(h.room.ID)[0]
	if len(stream.Text) != 64*1024 || !stream.Truncated {
		t.Fatalf("stream length = %d, truncated=%v", len(stream.Text), stream.Truncated)
	}
	close(runtime.release)
	waitUntil(t, 3*time.Second, func() bool { return len(h.runner.Streams(h.room.ID)) == 0 })
}
