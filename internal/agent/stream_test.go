package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/sdk/connector"
)

func collectStream(t *testing.T, runtime agent.StreamingRuntime, req agent.RunRequest) (agent.RunResult, []agent.StreamDelta) {
	t.Helper()
	var deltas []agent.StreamDelta
	result, err := runtime.RunStream(context.Background(), req, func(delta agent.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, deltas
}

func streamText(deltas []agent.StreamDelta) string {
	var b strings.Builder
	for _, delta := range deltas {
		if !delta.Reset {
			b.WriteString(delta.Text)
		}
	}
	return b.String()
}

func TestCommandRuntimeStreamsNDJSON(t *testing.T) {
	script := writeScript(t, `if [ "$TOGET_STREAM" != "ndjson" ]; then
  echo "stream mode was not advertised" >&2
  exit 9
fi
printf '%s\n' '{"type":"delta","text":"Hel"}'
printf '%s\n' '{"type":"delta","text":"lo"}'
printf '%s\n' '{"type":"result","text":" Hello ","format":"plain","session_id":"next-2"}'
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "Hello" || result.Format != "plain" || result.SessionID != "next-2" {
		t.Fatalf("result = %#v", result)
	}
	if got := streamText(deltas); got != "Hello" {
		t.Fatalf("deltas = %#v", deltas)
	}
	if len(deltas) != 2 || deltas[0].Reset || deltas[1].Reset {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCommandRuntimeSendsStableConnectorRequest(t *testing.T) {
	requestFile := filepath.Join(t.TempDir(), "request.json")
	script := writeScript(t, `cat > "$REQUEST_FILE"
printf '%s\n' '{"type":"result","text":"ok"}'
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{
		Command: script, Env: []string{"REQUEST_FILE=" + requestFile},
	})
	req := testRequest()
	req.Agent.AccountID = "account-1"
	req.Agent.Status = "idle"
	req.Agent.Permissions = json.RawMessage(`{"shell":true}`)
	req.Agent.CreatedAt = time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	req.History = []agent.HistoryEntry{{
		Seq: 7, EventID: "event-1", Type: "message.posted",
		AuthorID: "account-1", AuthorName: "Aiden", Text: "hello",
		CreatedAt: time.Date(2026, 10, 31, 1, 2, 4, 0, time.UTC),
	}}
	req.Truncated = true
	if _, err := runtime.RunStream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := connector.DecodeRequest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != connector.Version || decoded.Agent.ID != req.Agent.ID ||
		decoded.Agent.AccountID != "account-1" || decoded.Agent.Name != req.Agent.Name ||
		decoded.Agent.Runtime != req.Agent.Runtime || decoded.Agent.Status != "idle" {
		t.Fatalf("agent = %#v", decoded.Agent)
	}
	if string(decoded.Agent.Permissions) != `{"shell":true}` {
		t.Fatalf("permissions = %s", decoded.Agent.Permissions)
	}
	if decoded.Room.ID != req.Room.ID || decoded.Trigger.EventID != req.Trigger.EventID ||
		decoded.Trigger.Text != req.Trigger.Text || decoded.SessionID != req.SessionID ||
		decoded.Home != req.Home || decoded.Workdir != req.Workdir || !decoded.Truncated {
		t.Fatalf("request = %#v", decoded)
	}
	if len(decoded.History) != 1 || decoded.History[0].EventID != "event-1" ||
		decoded.History[0].Text != "hello" || decoded.History[0].AuthorName != "Aiden" {
		t.Fatalf("history = %#v", decoded.History)
	}
}

func TestCommandRuntimeExplicitEmptyResultSuppressesReply(t *testing.T) {
	script := writeScript(t, `printf '%s\n' '{"type":"delta","text":"preview"}'
printf '%s\n' '{"type":"result","text":""}'
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "" {
		t.Fatalf("result = %#v", result)
	}
	if got := streamText(deltas); got != "preview" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCommandRuntimeStreamResetDiscardsEarlierText(t *testing.T) {
	script := writeScript(t, `printf '%s\n' '{"type":"delta","text":"stale"}'
printf '%s\n' '{"type":"delta","reset":true}'
printf '%s\n' '{"type":"delta","text":"fresh"}'
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "fresh" {
		t.Fatalf("result = %#v", result)
	}
	if len(deltas) != 3 || deltas[0].Text != "stale" || !deltas[1].Reset || deltas[2].Text != "fresh" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCommandRuntimeStreamSurvivesRawOutputTruncation(t *testing.T) {
	// The retained raw stdout is only a fallback. Once a connector has sent a
	// final result, a long stream must not fail because the raw copy hit the
	// 2 MiB cap.
	script := writeScript(t, `i=0
while [ $i -lt 2200 ]; do
  printf '{"type":"delta","text":"%01000d"}\n' 0
  i=$((i+1))
done
printf '%s\n' '{"type":"result","text":"done"}'
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, _ := collectStream(t, runtime, testRequest())
	if result.Text != "done" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCommandRuntimeIgnoresUnknownProtocolEvents(t *testing.T) {
	// A connector may log structured status lines. Without a recognized
	// delta/result event there is no reply, and the protocol JSON must not be
	// posted as plain text.
	script := writeScript(t, `printf '%s\n' '{"type":"status","text":"working"}'`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "" || len(deltas) != 0 {
		t.Fatalf("result = %#v, deltas = %#v", result, deltas)
	}
}

func TestCommandRuntimeStreamCanOmitResult(t *testing.T) {
	script := writeScript(t, `printf '%s\n' '{"type":"delta","text":"partial answer"}'`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "partial answer" || result.Format != "markdown" {
		t.Fatalf("result = %#v", result)
	}
	if got := streamText(deltas); got != "partial answer" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestCommandRuntimeStreamFallsBackToLegacyOutput(t *testing.T) {
	t.Run("plain text", func(t *testing.T) {
		script := writeScript(t, `printf 'first\nsecond\n'`)
		runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
		result, deltas := collectStream(t, runtime, testRequest())
		if result.Text != "first\nsecond" || len(deltas) != 0 {
			t.Fatalf("result = %#v, deltas = %#v", result, deltas)
		}
	})
	t.Run("json result", func(t *testing.T) {
		script := writeScript(t, `echo '{"text":"legacy","format":"plain","session_id":"old"}'`)
		runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
		result, deltas := collectStream(t, runtime, testRequest())
		if result.Text != "legacy" || result.Format != "plain" || result.SessionID != "old" || len(deltas) != 0 {
			t.Fatalf("result = %#v, deltas = %#v", result, deltas)
		}
	})
}

func TestCommandRuntimeStreamFailureKeepsPartialDeltasForCaller(t *testing.T) {
	script := writeScript(t, `printf '%s\n' '{"type":"delta","text":"before failure"}'
echo boom >&2
exit 3
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	var deltas []agent.StreamDelta
	_, err := runtime.RunStream(context.Background(), testRequest(), func(delta agent.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err == nil || !strings.Contains(err.Error(), "code 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if got := streamText(deltas); got != "before failure" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestClaudeCodeRuntimeStreamsDeltas(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" > "$ARGS_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sid-stream"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"Hello","session_id":"sid-stream"}'
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{
		Binary: script, Env: []string{"ARGS_FILE=" + argsFile}, Model: "sonnet",
	})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "Hello" || result.SessionID != "sid-stream" {
		t.Fatalf("result = %#v", result)
	}
	if got := streamText(deltas); got != "Hello" {
		t.Fatalf("deltas = %#v", deltas)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argText := string(args)
	for _, want := range []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--resume", "resume-1", "--model", "sonnet",
	} {
		if !strings.Contains(argText, want) {
			t.Fatalf("args missing %q: %s", want, argText)
		}
	}
}

func TestClaudeCodeRuntimeStreamUsesDeltasWhenResultTextMissing(t *testing.T) {
	script := writeScript(t, `printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"streamed answer"}}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"","session_id":"sid-stream"}'
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "streamed answer" || result.SessionID != "sid-stream" {
		t.Fatalf("result = %#v", result)
	}
	if got := streamText(deltas); got != "streamed answer" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestClaudeCodeRuntimeStreamFreshFallsBackWhenUnsupported(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" >> "$ARGS_FILE"
printf -- '---\n' >> "$ARGS_FILE"
case "$*" in
  *stream-json*) echo "error: unknown option '--include-partial-messages'" >&2; exit 2 ;;
  *) echo '{"result":"fresh fallback","session_id":"sid-fallback","is_error":false}' ;;
esac
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script, Env: []string{"ARGS_FILE=" + argsFile}})
	req := testRequest()
	req.SessionID = ""
	result, deltas := collectStream(t, runtime, req)
	if result.Text != "fresh fallback" || result.SessionID != "sid-fallback" || len(deltas) != 0 {
		t.Fatalf("result = %#v, deltas = %#v", result, deltas)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(args)), "---"))
	calls := strings.Split(raw, "---")
	if len(calls) != 2 {
		t.Fatalf("calls = %q", calls)
	}
	if !strings.Contains(calls[0], "stream-json") || strings.Contains(calls[1], "stream-json") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestClaudeCodeRuntimeStreamFallsBackWhenUnsupported(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" >> "$ARGS_FILE"
printf -- '---\n' >> "$ARGS_FILE"
case "$*" in
  *stream-json*) echo "error: unknown option '--include-partial-messages'" >&2; exit 2 ;;
  *) echo '{"result":"fallback","session_id":"sid-fallback","is_error":false}' ;;
esac
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script, Env: []string{"ARGS_FILE=" + argsFile}})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "fallback" || result.SessionID != "sid-fallback" || len(deltas) != 0 {
		t.Fatalf("result = %#v, deltas = %#v", result, deltas)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(args)), "---"))
	calls := strings.Split(raw, "---")
	if len(calls) != 2 {
		t.Fatalf("calls = %q", calls)
	}
	if !strings.Contains(calls[0], "stream-json") || strings.Contains(calls[1], "stream-json") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestClaudeCodeRuntimeStreamResetsOnMissingSession(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" >> "$ARGS_FILE"
printf -- '---\n' >> "$ARGS_FILE"
case "$*" in
  *--resume*)
    printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"stale"}}}'
    printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"No conversation found with session ID: resume-1"}'
    ;;
  *)
    printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"fresh"}}}'
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"fresh answer","session_id":"sid-fresh"}'
    ;;
esac
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script, Env: []string{"ARGS_FILE=" + argsFile}})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "fresh answer" || result.SessionID != "sid-fresh" {
		t.Fatalf("result = %#v", result)
	}
	if len(deltas) != 3 || deltas[0].Text != "stale" || !deltas[1].Reset || deltas[2].Text != "fresh" {
		t.Fatalf("deltas = %#v", deltas)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(args)), "---"))
	if calls := strings.Split(raw, "---"); len(calls) != 2 {
		t.Fatalf("calls = %q", calls)
	}
}

func TestClaudeCodeRuntimeStreamErrorResult(t *testing.T) {
	script := writeScript(t, `printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"rate limited"}'`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script})
	_, err := runtime.RunStream(context.Background(), testRequest(), nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeCodeRuntimeStreamPlainTextFallback(t *testing.T) {
	script := writeScript(t, `printf 'plain answer\n'`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script})
	result, deltas := collectStream(t, runtime, testRequest())
	if result.Text != "plain answer" || len(deltas) != 0 {
		t.Fatalf("result = %#v, deltas = %#v", result, deltas)
	}
}
