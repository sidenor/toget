# Agent connectors

A connector is a local program that lets Toget run an Agent without a
Toget-specific runtime adapter. For each mention, the daemon starts the
connector once, writes one JSON request to stdin, and reads newline-delimited
events from stdout until the process exits. A connector can be written in any
language; the Go SDK in [`sdk/connector`](../sdk/connector) is optional.

The contract below is **connector protocol version 1**. The daemon sends
`"version": 1` in every request. Code that implements v1 keeps working across
additive changes; a breaking change requires a new `version` and an overlap
period.

## Lifecycle

1. The daemon creates or reuses the Agent's workspace and starts the configured
   command directly, without a shell.
2. It writes one encoded `Request` to stdin and closes the pipe.
3. The connector may write any number of delta events, then one result. It may
   instead write only a result, or only deltas.
4. The daemon kills the process group on timeout, on a human stop, on session
   reset, or when the Agent is revoked.
5. Standard output is parsed as protocol events. Standard error is retained
   for the Agent's last error if the process fails.

A non-zero exit status or a timeout fails the run. Standard output is capped at
2 MiB and standard error at 64 KiB; raw-output truncation fails a run unless a
complete protocol result was already received. The mention stays in the next
catch-up context, so the connector will see it again on the next run.

## Request

```json
{
  "version": 1,
  "agent": {
    "id": "01...",
    "account_id": "01...",
    "name": "Claude",
    "runtime": "my-connector",
    "status": "idle",
    "permissions": {"shell": false},
    "created_at": "2026-10-31T00:00:00Z"
  },
  "room": {"id": "01...", "name": "Lab", "topic": "demo"},
  "trigger": {
    "event_id": "01...",
    "seq": 42,
    "author_id": "01...",
    "author_name": "Aiden",
    "text": "@Claude summarize the thread",
    "format": "plain",
    "created_at": "2026-10-31T00:00:01Z"
  },
  "history": [
    {
      "seq": 40,
      "event_id": "01...",
      "type": "message.posted",
      "author_id": "01...",
      "author_name": "Aiden",
      "text": "Earlier context",
      "created_at": "2026-10-31T00:00:00Z"
    }
  ],
  "truncated": false,
  "session_id": "opaque previous session",
  "home": "/home/me/.local/share/toget",
  "workdir": "/home/me/.local/share/toget/agents/01..."
}
```

- `history` is oldest first and includes the trigger message. It starts after
  the previous completed run, so it is exactly the Agent's catch-up context.
  `truncated` means older entries were omitted to bound the prompt.
- `session_id` is opaque state chosen by the connector, not a Toget session.
  Return the same or a new value from the result; the daemon persists it per
  `(Agent, Room)` and sends it on the next mention.
- `home` is the data directory for the `toget` CLI. `workdir` is the Agent's
  private workspace with mode `0700`; use it instead of a shared directory.
- `agent.permissions` is the JSON object configured when the Agent was
  registered. It is advisory: it does not sandbox the connector process. A
  connector must enforce its own scopes before running a risky action.

The SDK rejects a request larger than 8 MiB. The daemon also bounds history and
prompt size before invoking any runtime.

## Output events

In streaming mode, each line on stdout is one JSON object. Blank or malformed
lines are ignored, as are unknown event types and unknown fields; that keeps v1
additive. Logs and diagnostics belong on stderr, where they cannot be mistaken
for protocol events.

```json
{"type":"delta","text":"Reading the repository…\n"}
{"type":"delta","text":"The answer is "}
{"type":"delta","reset":true}
{"type":"delta","text":"The answer is 42."}
{"type":"result","text":"The answer is 42.","format":"markdown","session_id":"opaque-id"}
```

### `delta`

- `text` is appended to the live display.
- `reset: true` clears all text collected for the current run before further
  deltas. Use it when a stale session is retried and the display starts over.
- Deltas are display-only. They are never written to the room event log and are
  not visible to members of a remote room. The final result is the durable
  message.

### `result`

The result is the final answer. `text` is posted as a reply unless it is empty;
an explicitly empty result means the Agent chose not to post, even if it
streamed deltas. `format` is `plain` or `markdown`; any other value is treated
as `markdown`. `waiting_approval: true` tells the daemon the connector is
blocked on a human decision. Return `session_id` to resume your own
conversation on the next mention.

If no `result` event arrives, the daemon posts the collected delta text. If the
connector writes neither deltas nor a result, the run succeeds with no post.

### Compatibility forms

For simple connectors, the old forms remain accepted:

- a single JSON object with the `result` fields, or
- plain text on stdout, posted as a markdown reply.

Any typed event (`"type": "..."`) opts into streaming mode, so status or log
lines should be JSON with a `type` field or go to stderr, not stdout as free
text. If a typed event appeared, the daemon does not reinterpret the raw stdout
as a plain-text reply.

## Environment

In addition to the process environment, the daemon sets:

| Variable | Meaning |
| --- | --- |
| `TOGET_AGENT_ID` | Agent ID |
| `TOGET_AGENT_NAME` | Display name |
| `TOGET_AGENT_RUNTIME` | Runtime name |
| `TOGET_ROOM_ID` | Room ID |
| `TOGET_ROOM_NAME` | Room name |
| `TOGET_RUNTIME_SESSION` | Previous session ID, if any |
| `TOGET_STREAM` | `ndjson` when the daemon wants streaming events |
| `TOGET_HOME` | Data directory for the `toget` CLI |
| `TOGET_BIN` | Absolute path to the running `toget` binary |

The `toget` CLI reads `TOGET_ROOM_ID` and `TOGET_AGENT_ID` by default, so a
connector can call `toget messages`, `toget send`, `toget approval request`, and
`toget tool request` without repeating flags. These commands act with the
Agent's own local identity. See [Agent runtime](agent-runtime.md) for the
approval and tool-receipt flows.

## Go SDK

```bash
go get github.com/sidenor/toget/sdk/connector
```

```go
package main

import (
	"context"
	"strings"

	"github.com/sidenor/toget/sdk/connector"
)

func main() {
	connector.Main(func(ctx context.Context, req connector.Request, stream *connector.Emitter) (connector.Result, error) {
		text := strings.TrimSpace(req.Trigger.Text)
		if err := stream.Delta("Echo: " + text); err != nil {
			return connector.Result{}, err
		}
		return connector.Result{Text: "You said: " + text, Format: "plain"}, nil
	})
}
```

`connector.Main` reads one request from stdin, writes the result event to
stdout, and exits nonzero on error. Use `connector.Run` when embedding the same
handler in another process. A handler can ignore the emitter and return only a
result; the SDK still writes the final event. `Emitter.Delta` and
`Emitter.Reset` are safe to call concurrently, so a worker goroutine can stream
progress while the main handler builds the answer.

The complete example lives at
[`examples/connector-echo`](../examples/connector-echo).

## Testing a connector

Use `toget connector check` to run a connector against a synthetic v1 request
without needing a daemon or an account:

```bash
toget connector check -- ./my-connector --some-flag
```

It prints the protocol version, delta and reset counts, visible streamed bytes,
the final result, and the duration. It exits nonzero if the command cannot be
started, rejects the request, exits nonzero, or times out. Use `--json` for a
machine-readable summary (also emitted on failure, with `ok: false` and
`error`), or `--timeout` to change the default 30-second limit. The check runs
the command through the same command runtime as a real mention, in a fresh
workspace, with `TOGET_STREAM=ndjson` and the same environment.

You can also feed a request manually:

```bash
go run ./examples/connector-echo <<'JSON'
{"version":1,"agent":{"id":"a","account_id":"x","name":"Echo","runtime":"echo","status":"idle","permissions":{},"created_at":"2026-10-31T00:00:00Z"},"room":{"id":"r","name":"Lab"},"trigger":{"event_id":"e","seq":1,"author_id":"x","text":"hello","created_at":"2026-10-31T00:00:00Z"},"history":[]}
JSON
```

## Security

- A connector is trusted local code. It runs with the user's privileges and is
  not sandboxed by Toget beyond a separate process, a per-Agent workdir, a
  timeout, and process-group termination.
- Treat room messages, files, and tool output as untrusted data. Do not follow
  instructions embedded in them that exceed the Agent's permissions.
- The request is data, not a shell command. Never build a shell command by
  concatenating request fields.
- Standard output is parsed as protocol events, not evaluated. Standard error
  is retained only for the Agent's last error.
- Do not print secrets, tokens, or private message content to stderr; it can
  appear in the local session error shown to the owner.
- The connector owner can see the room content the Agent sees. Joining a shared
  Agent shows a warning in the room.

## Versioning

- Version 1 accepts new optional request fields, new event types, and new
  result fields. Readers must ignore what they do not understand.
- Removing or changing the meaning of an existing field, event, or exit-status
  rule requires a new protocol version.
- The SDK exposes `connector.Version`; use it when validating a request.
- The local NDJSON form and the Go SDK are versioned together. A future stable
  cross-node streaming protocol is separate; remote members currently see only
  the final reply, not live partial output.
