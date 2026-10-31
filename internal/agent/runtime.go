package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrRuntimeUnavailable means the configured runtime executable cannot be
	// started on this endpoint.
	ErrRuntimeUnavailable = errors.New("agent runtime is unavailable")
	// ErrRuntimeTimeout means one runtime invocation exceeded its limit.
	ErrRuntimeTimeout = errors.New("agent runtime timed out")
)

// Runtime executes one mention for an Agent. Implementations must treat the
// request as data: room messages are untrusted input, not instructions to the
// host process.
type Runtime interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// StreamDelta is one incremental piece of visible Agent output. Reset asks the
// owner to discard the text accumulated so far; adapters use it when a stale
// session is retried and the stream starts over. Deltas are display-only and
// are never written to the room event log; the final RunResult remains the
// authoritative reply.
type StreamDelta struct {
	Text  string `json:"text"`
	Reset bool   `json:"reset,omitempty"`
}

// StreamingRuntime is implemented by runtimes that can expose partial output
// while a run is active. The runner falls back to Runtime.Run when it is not
// implemented. emit is safe to call from the runtime's goroutine.
type StreamingRuntime interface {
	RunStream(ctx context.Context, req RunRequest, emit func(StreamDelta)) (RunResult, error)
}

// RunRoom is the room context handed to a runtime.
type RunRoom struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Topic string `json:"topic,omitempty"`
}

// Trigger is the message that mentioned the Agent.
type Trigger struct {
	EventID    string    `json:"event_id"`
	Seq        int64     `json:"seq"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name,omitempty"`
	Text       string    `json:"text"`
	Format     string    `json:"format,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// HistoryEntry is one room event in the Agent's catch-up context.
type HistoryEntry struct {
	Seq        int64     `json:"seq"`
	EventID    string    `json:"event_id"`
	Type       string    `json:"type"`
	AuthorID   string    `json:"author_id,omitempty"`
	AuthorName string    `json:"author_name,omitempty"`
	AgentID    string    `json:"agent_id,omitempty"`
	AgentName  string    `json:"agent_name,omitempty"`
	Text       string    `json:"text,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// RunRequest is the complete input for one invocation. The command runtime
// receives it as JSON on stdin.
type RunRequest struct {
	Version int     `json:"version"`
	Agent   Agent   `json:"agent"`
	Room    RunRoom `json:"room"`
	Trigger Trigger `json:"trigger"`
	// History is oldest first and includes the trigger message. It begins
	// after the previous completed run, so it is exactly the Agent's catch-up.
	History []HistoryEntry `json:"history"`
	// Truncated means older events were omitted to bound the prompt.
	Truncated bool   `json:"truncated,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Home      string `json:"home,omitempty"`
	// Workdir is the per-Agent workspace. Runtimes should use it as their
	// working directory so two Agents never share local files by accident.
	Workdir string `json:"workdir,omitempty"`
}

// RunResult is one runtime reply. Empty Text means the Agent chose not to post
// anything.
type RunResult struct {
	Text            string `json:"text"`
	Format          string `json:"format,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WaitingApproval bool   `json:"waiting_approval,omitempty"`
}

// EchoRuntime is a deterministic demo runtime. It is useful for trying the
// room loop without a model; it is not an AI.
type EchoRuntime struct{}

// Run implements Runtime.
func (EchoRuntime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	text := strings.TrimSpace(req.Trigger.Text)
	text = truncate(text, 500)
	if text == "" {
		text = "(no text)"
	}
	return RunResult{
		Text:   fmt.Sprintf("**%s** here — I heard: %s", req.Agent.Name, text),
		Format: "markdown",
	}, nil
}

// CommandOptions configures an external command runtime.
type CommandOptions struct {
	Command string
	Args    []string
	Workdir string
	Env     []string
}

// CommandRuntime runs a local command once per mention. The command receives a
// JSON RunRequest on stdin and returns a JSON RunResult, or plain text to post
// as the reply.
type CommandRuntime struct {
	name string
	opts CommandOptions
}

// NewCommandRuntime creates a command runtime.
func NewCommandRuntime(name string, opts CommandOptions) *CommandRuntime {
	return &CommandRuntime{name: name, opts: opts}
}

// Run implements Runtime.
func (r *CommandRuntime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if strings.TrimSpace(r.opts.Command) == "" {
		return RunResult{}, ErrRuntimeUnavailable
	}
	input, err := json.Marshal(req)
	if err != nil {
		return RunResult{}, fmt.Errorf("encode runtime request: %w", err)
	}
	input = append(input, '\n')
	env := runtimeEnv(req, r.opts.Env)
	dir := r.opts.Workdir
	if req.Workdir != "" {
		dir = req.Workdir
	}
	proc := runProcess(ctx, r.opts.Command, r.opts.Args, input, dir, env, defaultRuntimeStdout, defaultRuntimeStderr)
	if proc.err != nil {
		return RunResult{}, proc.err
	}
	return parseCommandOutput(proc.stdout)
}

// commandStreamEvent is one line of the optional NDJSON streaming protocol:
//
//	{"type":"delta","text":"partial"}
//	{"type":"result","text":"final","format":"markdown","session_id":"..."}
//
// A runtime that ignores TOGET_STREAM keeps returning the complete response on
// stdout, which RunStream parses with the normal fallback.
type commandStreamEvent struct {
	Type            string `json:"type"`
	Text            string `json:"text"`
	Reset           bool   `json:"reset,omitempty"`
	Format          string `json:"format,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WaitingApproval bool   `json:"waiting_approval,omitempty"`
}

// RunStream implements StreamingRuntime. It sets TOGET_STREAM=ndjson and reads
// newline-delimited events. Older connectors that emit only a final JSON or
// plain-text response still work and simply produce no partial output.
func (r *CommandRuntime) RunStream(ctx context.Context, req RunRequest, emit func(StreamDelta)) (RunResult, error) {
	if strings.TrimSpace(r.opts.Command) == "" {
		return RunResult{}, ErrRuntimeUnavailable
	}
	input, err := json.Marshal(req)
	if err != nil {
		return RunResult{}, fmt.Errorf("encode runtime request: %w", err)
	}
	input = append(input, '\n')
	env := runtimeEnv(req, append(append([]string(nil), r.opts.Env...), "TOGET_STREAM=ndjson"))
	dir := r.opts.Workdir
	if req.Workdir != "" {
		dir = req.Workdir
	}
	var streamed strings.Builder
	sawEvent := false
	var final RunResult
	proc := runProcessStreaming(ctx, r.opts.Command, r.opts.Args, input, dir, env,
		defaultRuntimeStdout, defaultRuntimeStderr, func(line []byte) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 || line[0] != '{' {
				return
			}
			var event commandStreamEvent
			if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
				return
			}
			// Any typed event opts into the streaming protocol; unknown types
			// are ignored rather than treated as a legacy reply.
			sawEvent = true
			switch event.Type {
			case "delta":
				if event.Reset {
					streamed.Reset()
					if emit != nil {
						emit(StreamDelta{Reset: true})
					}
					return
				}
				if event.Text == "" {
					return
				}
				streamed.WriteString(event.Text)
				if emit != nil {
					emit(StreamDelta{Text: event.Text})
				}
			case "result":
				final = RunResult{
					Text: event.Text, Format: event.Format,
					SessionID: event.SessionID, WaitingApproval: event.WaitingApproval,
				}
			}
		})
	if proc.err != nil {
		return RunResult{}, proc.err
	}
	if !sawEvent {
		if proc.truncated {
			return RunResult{}, fmt.Errorf("runtime output exceeded %d bytes", defaultRuntimeStdout)
		}
		return parseCommandOutput(proc.stdout)
	}
	if strings.TrimSpace(final.Text) == "" {
		final.Text = streamed.String()
	}
	return normalizeResult(final), nil
}

func parseCommandOutput(stdout []byte) (RunResult, error) {
	out := strings.TrimSpace(string(stdout))
	if out == "" {
		return RunResult{}, nil
	}
	var result RunResult
	if strings.HasPrefix(out, "{") {
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			return RunResult{}, fmt.Errorf("runtime returned invalid JSON: %w", err)
		}
	} else {
		result.Text = out
	}
	return normalizeResult(result), nil
}

func normalizeResult(result RunResult) RunResult {
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		result.Format = ""
		return result
	}
	switch result.Format {
	case "plain", "markdown":
	default:
		result.Format = "markdown"
	}
	if len(result.Text) > 64*1024 {
		result.Text = truncate(result.Text, 64*1024) + "\n\n…(truncated)"
	}
	return result
}

func runtimeEnv(req RunRequest, extra []string) []string {
	values := []string{
		"TOGET_AGENT_ID=" + req.Agent.ID,
		"TOGET_AGENT_NAME=" + req.Agent.Name,
		"TOGET_AGENT_RUNTIME=" + req.Agent.Runtime,
		"TOGET_ROOM_ID=" + req.Room.ID,
		"TOGET_ROOM_NAME=" + req.Room.Name,
		"TOGET_RUNTIME_SESSION=" + req.SessionID,
	}
	if req.Home != "" {
		values = append(values, "TOGET_HOME="+req.Home)
	}
	values = append(values, extra...)
	return values
}
