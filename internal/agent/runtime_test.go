package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/agent"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testRequest() agent.RunRequest {
	return agent.RunRequest{
		Version:   1,
		Agent:     agent.Agent{ID: "agent-1", Name: "Claude", Runtime: "command"},
		Room:      agent.RunRoom{ID: "room-1", Name: "General"},
		Trigger:   agent.Trigger{EventID: "event-1", Seq: 7, AuthorID: "account-1", AuthorName: "Aiden", Text: "hello"},
		SessionID: "resume-1",
		Home:      "/tmp/home",
	}
}

func TestCommandRuntimeSendsRequestAndParsesJSON(t *testing.T) {
	script := writeScript(t, `input=$(cat)
case "$input" in
  *'"event_id":"event-1"'*) ;;
  *) echo "missing request" >&2; exit 2 ;;
esac
printf '%s|%s|%s|%s\n' "$TOGET_AGENT_ID" "$TOGET_ROOM_ID" "$TOGET_RUNTIME_SESSION" "$TOGET_HOME" >&2
echo '{"text":"  **hi**  ","format":"markdown","session_id":"next-1"}'
`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: script})
	result, err := runtime.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "**hi**" || result.Format != "markdown" || result.SessionID != "next-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCommandRuntimeEnvAndPlainText(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.txt")
	script := writeScript(t, `printf '%s|%s|%s|%s\n' "$TOGET_AGENT_NAME" "$TOGET_ROOM_NAME" "$TOGET_AGENT_RUNTIME" "$TOGET_EXTRA" > "$1"
printf 'plain **answer**\n'`)
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{
		Command: script,
		Args:    []string{envFile},
		Env:     []string{"TOGET_EXTRA=extra-value"},
	})
	result, err := runtime.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "plain **answer**" || result.Format != "markdown" {
		t.Fatalf("result = %#v", result)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(env)); got != "Claude|General|command|extra-value" {
		t.Fatalf("env = %q", got)
	}
}

func TestCommandRuntimeEmptyIsSilent(t *testing.T) {
	runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: writeScript(t, "exit 0\n")})
	result, err := runtime.Run(context.Background(), testRequest())
	if err != nil || result.Text != "" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestCommandRuntimeFailures(t *testing.T) {
	t.Run("nonexistent binary", func(t *testing.T) {
		runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: "/does/not/exist/toget-runtime"})
		_, err := runtime.Run(context.Background(), testRequest())
		if !errors.Is(err, agent.ErrRuntimeUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nonzero exit", func(t *testing.T) {
		runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: writeScript(t, "echo boom >&2\nexit 3\n")})
		_, err := runtime.Run(context.Background(), testRequest())
		if err == nil || !strings.Contains(err.Error(), "code 3") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: writeScript(t, "echo '{oops'\n")})
		_, err := runtime.Run(context.Background(), testRequest())
		if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		runtime := agent.NewCommandRuntime("test", agent.CommandOptions{Command: writeScript(t, "sleep 30 &\nwait\n")})
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := runtime.Run(ctx, testRequest())
		if !errors.Is(err, agent.ErrRuntimeTimeout) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("timeout took %v", time.Since(start))
		}
	})
}

func TestClaudeCodeRuntimeParsesResult(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" > "$ARGS_FILE"
echo '{"result":"answer","session_id":"sid-2","is_error":false}'
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{
		Binary: script,
		Env:    []string{"ARGS_FILE=" + argsFile},
		Model:  "sonnet",
	})
	result, err := runtime.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.SessionID != "sid-2" {
		t.Fatalf("result = %#v", result)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argText := string(args)
	for _, want := range []string{"-p", "--output-format", "json", "--resume", "resume-1", "--model", "sonnet"} {
		if !strings.Contains(argText, want) {
			t.Fatalf("args missing %q: %s", want, argText)
		}
	}
	if strings.Contains(argText, "--session-id") {
		t.Fatalf("resume invocation also passed --session-id: %s", argText)
	}
}

func TestClaudeCodeRuntimeStartsFreshSession(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" > "$ARGS_FILE"
echo '{"result":"fresh","session_id":"sid-new","is_error":false}'
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script, Env: []string{"ARGS_FILE=" + argsFile}})
	req := testRequest()
	req.SessionID = ""
	result, err := runtime.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "sid-new" {
		t.Fatalf("result = %#v", result)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--session-id") || strings.Contains(string(args), "--resume") {
		t.Fatalf("args = %s", args)
	}
}

func TestClaudeCodeRuntimeRetriesMissingSession(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, `printf '%s\n' "$@" >> "$ARGS_FILE"
printf -- '---\n' >> "$ARGS_FILE"
case "$*" in
  *--resume*) echo '{"result":"No conversation found with session ID: resume-1","is_error":true,"subtype":"error"}' ;;
  *) echo '{"result":"recovered","session_id":"sid-fresh","is_error":false}' ;;
esac
`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script, Env: []string{"ARGS_FILE=" + argsFile}})
	result, err := runtime.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "recovered" || result.SessionID != "sid-fresh" {
		t.Fatalf("result = %#v", result)
	}
	args, _ := os.ReadFile(argsFile)
	text := strings.TrimSpace(string(args))
	text = strings.TrimSpace(strings.TrimSuffix(text, "---"))
	calls := strings.Split(text, "---")
	if len(calls) != 2 {
		t.Fatalf("expected two invocations: %s", args)
	}
	if !strings.Contains(calls[0], "--resume") || !strings.Contains(calls[1], "--session-id") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestClaudeCodeRuntimeErrorResult(t *testing.T) {
	script := writeScript(t, `echo '{"result":"rate limited","is_error":true,"subtype":"error"}'`)
	runtime := agent.NewClaudeCodeRuntime(agent.ClaudeCodeOptions{Binary: script})
	_, err := runtime.Run(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildPromptFramesUntrustedRoomAndBoundsHistory(t *testing.T) {
	req := testRequest()
	req.Agent.Permissions = json.RawMessage(`{"shell":false}`)
	req.History = []agent.HistoryEntry{
		{Seq: 6, EventID: "before", AuthorName: "Aiden", Text: "earlier"},
		{Seq: 7, EventID: "event-1", AuthorName: "Aiden", Text: "duplicate trigger"},
	}
	prompt := agent.BuildPrompt(req)
	for _, want := range []string{"Claude", "General", "room-1", "untrusted", "approval", `{"shell":false}`, "earlier"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "duplicate trigger") {
		t.Fatalf("prompt repeated the trigger in history:\n%s", prompt)
	}
	req.History = append(req.History, agent.HistoryEntry{Seq: 8, EventID: "huge", AuthorName: "Aiden", Text: strings.Repeat("x", 2*maxPromptForTest)})
	if got := len(agent.BuildPrompt(req)); got > 256*1024 {
		t.Fatalf("prompt is not bounded: %d", got)
	}
}

const maxPromptForTest = 512 * 1024
