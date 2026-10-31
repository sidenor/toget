package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/sdk/connector"
)

func writeConnectorScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connector.sh")
	script := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConnectorCheckStopsFlagParsingAtCommand(t *testing.T) {
	cmd := newConnectorCheckCommand()
	var got []string
	cmd.RunE = func(_ *cobra.Command, args []string) error {
		got = args
		return nil
	}
	cmd.SetArgs([]string{"--timeout", "2s", "--", "/bin/my-connector", "--json", "--timeout", "1s"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/my-connector", "--json", "--timeout", "1s"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	timeout, err := cmd.Flags().GetDuration("timeout")
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 2*time.Second {
		t.Fatalf("timeout = %s", timeout)
	}
}

func TestConnectorCheckSendsSyntheticV1Request(t *testing.T) {
	requestFile := filepath.Join(t.TempDir(), "request.json")
	envFile := filepath.Join(t.TempDir(), "env.txt")
	t.Setenv("REQUEST_FILE", requestFile)
	t.Setenv("ENV_FILE", envFile)
	script := writeConnectorScript(t, `cat > "$REQUEST_FILE"
printf '%s\n%s\n%s\n%s\n%s\n' \
  "${TOGET_STREAM:-}" "${TOGET_BIN:-}" "${TOGET_AGENT_ID:-}" \
  "${TOGET_ROOM_ID:-}" "${TOGET_HOME:-}" > "$ENV_FILE"
printf '%s\n' '{"type":"delta","text":"Echo: "}'
printf '%s\n' '{"type":"delta","text":"hello"}'
printf '%s\n' '{"type":"result","text":"Echo: hello","format":"plain","session_id":"next"}'
`)
	summary, err := runConnectorCheck(context.Background(), script, nil, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Version != connector.Version || summary.Deltas != 2 ||
		summary.DeltaBytes != len("Echo: hello") || summary.Text != "Echo: hello" ||
		summary.Format != "plain" || summary.SessionID != "next" {
		t.Fatalf("summary = %#v", summary)
	}
	data, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := connector.DecodeRequest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != connector.Version || decoded.Agent.ID == "" ||
		decoded.Room.ID == "" || decoded.Trigger.EventID == "" ||
		decoded.Workdir == "" || decoded.Home == "" {
		t.Fatalf("request = %#v", decoded)
	}
	if string(decoded.Agent.Permissions) != `{}` || len(decoded.History) != 1 {
		t.Fatalf("request = %#v", decoded)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(env)), "\n")
	if len(lines) != 5 || lines[0] != "ndjson" || lines[1] == "" ||
		lines[2] != "connector-check-agent" || lines[3] != "connector-check-room" || lines[4] == "" {
		t.Fatalf("environment = %q", lines)
	}
}

func TestConnectorCheckCountsResetsAndVisibleBytes(t *testing.T) {
	script := writeConnectorScript(t, `printf '%s\n' '{"type":"delta","text":"stale"}'
printf '%s\n' '{"type":"delta","reset":true}'
printf '%s\n' '{"type":"delta","text":"fresh"}'
`)
	summary, err := runConnectorCheck(context.Background(), script, nil, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Deltas != 2 || summary.Resets != 1 || summary.DeltaBytes != len("fresh") ||
		summary.Text != "fresh" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestConnectorCheckReportsFailureAfterPartialOutput(t *testing.T) {
	script := writeConnectorScript(t, `printf '%s\n' '{"type":"delta","text":"partial"}'
echo boom >&2
exit 3
`)
	summary, err := runConnectorCheck(context.Background(), script, nil, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "code 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if summary.Deltas != 1 || summary.DeltaBytes != len("partial") {
		t.Fatalf("summary = %#v", summary)
	}
	message := summarizeConnectorCheckError(summary, err, 10*time.Second)
	if !strings.Contains(message.Error(), "after 1 delta(s), 7 visible byte(s)") {
		t.Fatalf("message = %v", message)
	}
}

func TestConnectorCheckTimeout(t *testing.T) {
	script := writeConnectorScript(t, `sleep 5`)
	started := time.Now()
	_, err := runConnectorCheck(context.Background(), script, nil, 100*time.Millisecond)
	if !errors.Is(err, agent.ErrRuntimeTimeout) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	message := summarizeConnectorCheckError(connectorCheckSummary{}, err, 100*time.Millisecond)
	if !strings.Contains(message.Error(), "timed out after 100ms") {
		t.Fatalf("message = %v", message)
	}
}

func TestConnectorCheckMissingCommand(t *testing.T) {
	_, err := runConnectorCheck(context.Background(), filepath.Join(t.TempDir(), "missing"), nil, time.Second)
	if !errors.Is(err, agent.ErrRuntimeUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if message := summarizeConnectorCheckError(connectorCheckSummary{}, err, time.Second); !strings.Contains(message.Error(), "could not be started") {
		t.Fatalf("message = %v", message)
	}
}
