# Agent runtime

Toget runs Agents on the endpoint that owns them. A local daemon watches each
room for mentions, executes one mention at a time, and posts the answer back to
the same thread. The first adapter is Claude Code; any other tool can use the
generic command protocol.

## What a run does

1. The daemon scans rooms the Agent belongs to, after its last scan cursor.
2. It claims a `message.posted` event only when the message is not from an
   Agent, the text is non-empty, and either `mentions` contains the Agent ID or
   the text contains `@AgentName` at a word boundary.
3. It builds the trigger and the room events since the Agent's last completed
   run. Messages that arrived while the Agent was busy or offline are included.
4. It marks the session `running`, posts a status card, and starts the runtime
   in the Agent's workspace.
5. When the runtime returns, the daemon posts the reply (if non-empty) as an
   Agent-attributed message, then posts `idle`, `waiting_approval`, or
   `offline`.
6. Only a successful run advances the completed-run cursor. A failed mention
   stays in the catch-up context for the next run. A daemon restart marks an
   interrupted run as interrupted instead of silently repeating it.

Agent-authored messages never start another run. Runs are serialized per Agent;
different Agents may run in parallel. A session is persistent per
`(Agent, Room)` and stores the cursors, the resumed runtime conversation, the
run count, and the last error.

## Set up an Agent

Start the daemon, create an account if needed, then:

```bash
# Register an Agent that uses the Claude Code adapter.
toget agent add --name Claude --runtime claude-code

# Create a room if you do not have one, then join the Agent to it.
toget room create --name "Lab"
toget agent list
toget room add-agent <room-id> <agent-id>

# Mention it in the room.
toget send --room <room-id> "@Claude check the repository and summarize"
```

The web UI has the same flow under **Agents**: add, join a room, and watch the
status cards in the timeline. Agent replies appear with an **Agent** badge and
the Agent's name.

`claude` must be installed and authenticated for the account running
`toget serve`. The daemon launches it with `-p --output-format stream-json
--verbose --include-partial-messages` when the installed CLI supports those
flags, and keeps the returned session ID so the next mention resumes the same
conversation. Text deltas are shown live; the final result is what gets posted.
If the CLI does not support streaming flags, the adapter falls back to a normal
`--output-format json` run. If the saved Claude session no longer exists, the
adapter starts a fresh one instead of failing every later mention.

To install the optional Claude Code skill that documents the CLI for
interactive use:

```bash
mkdir -p ~/.claude/skills/toget
cp skills/toget/SKILL.md ~/.claude/skills/toget/SKILL.md
```

## Runtime configuration

The daemon reads `config.json` in the data directory (`TOGET_HOME` or the
platform default). A full example:

```json
{
  "listen": "127.0.0.1:7718",
  "node_listen": "127.0.0.1:7717",
  "agent_runtimes": {
    "claude-code": {
      "command": "claude",
      "args": [],
      "model": "sonnet"
    },
    "my-tool": {
      "command": "/opt/mytool/bin/agent",
      "args": ["--json"],
      "workdir": "/srv/mytool"
    }
  },
  "agent_timeout_seconds": 900,
  "agent_history_limit": 200
}
```

- `agent_runtimes` maps the Agent's `runtime` name to a local command. If the
  key is omitted entirely, the daemon uses the built-in `claude-code` entry
  (`command: "claude"`). If you provide the map, include an entry for
  `claude-code` yourself when you still want it.
- `echo` is always available as a deterministic demo runtime. It is not a
  model.
- `agent_timeout_seconds` bounds one invocation; the default is 900.
- `agent_history_limit` bounds the number of catch-up entries; the default is
  200. The prompt is also bounded in bytes.
- Each Agent runs in `<data home>/agents/<agent-id>` with mode `0700`. A
  configured `workdir` is only a fallback for callers that do not supply the
  per-Agent workspace.

The daemon also gives the runtime:

| Variable | Meaning |
| --- | --- |
| `TOGET_AGENT_ID` | The Agent being run |
| `TOGET_AGENT_NAME` | Display name |
| `TOGET_AGENT_RUNTIME` | Runtime name |
| `TOGET_ROOM_ID` | Current room |
| `TOGET_ROOM_NAME` | Room name |
| `TOGET_RUNTIME_SESSION` | Previous runtime session ID, if any |
| `TOGET_STREAM` | Set to `ndjson` when the daemon wants live output from a command runtime |
| `TOGET_HOME` | Data directory for the `toget` CLI |
| `TOGET_BIN` | Absolute path to the running `toget` binary |

The `toget` CLI reads `TOGET_ROOM_ID` and `TOGET_AGENT_ID` by default, so
commands such as `toget messages`, `toget send`, `toget approval request`, and
`toget tool request` work in the room without repeating flags.

## Command runtime protocol

A command runtime speaks **connector protocol version 1**. The normative
contract, Go SDK, compatibility policy, and security notes are in
[Connectors](connectors.md); this section is the short form. A command runtime
receives one JSON object on stdin and writes either a JSON object or plain text
on stdout. A non-zero exit code, a timeout, or output over the limit is a
failed run and is shown in the Agent's last error.

Request:

```json
{
  "version": 1,
  "agent": {
    "id": "01...",
    "account_id": "01...",
    "name": "Claude",
    "runtime": "my-tool",
    "status": "idle",
    "permissions": {"shell": false},
    "created_at": "2026-10-31T00:00:00Z"
  },
  "room": {"id": "01...", "name": "Lab", "topic": "..."},
  "trigger": {
    "event_id": "01...",
    "seq": 42,
    "author_id": "01...",
    "author_name": "Aiden",
    "text": "@Claude summarize",
    "format": "plain",
    "created_at": "2026-10-31T00:00:00Z"
  },
  "history": [
    {"seq": 40, "event_id": "01...", "type": "message.posted", "author_name": "Aiden", "text": "earlier"}
  ],
  "truncated": false,
  "session_id": "optional previous session",
  "home": "/home/me/.local/share/toget",
  "workdir": "/home/me/.local/share/toget/agents/01..."
}
```

Response:

```json
{
  "text": "The answer to post",
  "format": "markdown",
  "session_id": "opaque id to resume next time",
  "waiting_approval": false
}
```

- Empty `text` means the Agent chooses not to post.
- `format` is `plain` or `markdown`; anything else becomes `markdown`.
- Return the same or a new `session_id`; the daemon persists it.
- Set `waiting_approval` when the runtime is blocked on a human decision. The
  daemon also polls pending approvals every two seconds during a run.
- `version` is 1. Unknown request fields are ignored within v1, so a newer
  daemon can add context without breaking an older connector.

The command is started directly, without a shell. It runs with a timeout,
bounded stdout/stderr, and its process group is killed on timeout.

### Streaming output

The daemon sets `TOGET_STREAM=ndjson` when it wants live output. A command can
then write newline-delimited JSON events on stdout:

```json
{"type":"delta","text":"Reading the repo…\n"}
{"type":"delta","text":"The answer is "}
{"type":"delta","text":"42.","reset":false}
{"type":"result","text":"The answer is 42.","format":"markdown","session_id":"opaque-id"}
```

- `delta` events append `text` to the live display. `reset: true` clears the
  text collected so far, which lets an adapter discard a stale attempt before
  retrying with a fresh session.
- The optional `result` event is the final answer, using the same fields as the
  non-streaming response. If it is present, it is authoritative even when it
  differs from the collected deltas, and an explicitly empty `text` suppresses
  the reply. If it is absent, the daemon uses the collected delta text.
- Lines that are not recognized protocol events are ignored for streaming. If
  no protocol event appears at all, the whole stdout is parsed as a normal
  JSON or plain-text response, so existing runtimes keep working.
- If the process fails after emitting deltas, the run still fails; the partial
  text disappears with the run and no reply is posted.
- An adapter that does not understand `TOGET_STREAM` can ignore it and write
  the old single JSON object or plain text.

Use the Go SDK in [`sdk/connector`](../sdk/connector) to implement the protocol
without hand-encoding events, and `toget connector check` to run a connector
against a synthetic v1 request without a daemon or an account:

```bash
go build -o /tmp/my-connector ./examples/connector-echo
toget connector check -- /tmp/my-connector
```

## Live output

While a run is active, the daemon keeps transient display state in memory on
the Agent owner's node: the run and trigger IDs, the Agent and room, the text
collected so far (bounded to 64 KiB), a truncation flag, and timestamps. This
state is not an event and is never written to the room log.

`GET /api/v1/rooms/{id}/agent-streams` returns the active streams in a room to
a caller who is a member of that room:

```json
{
  "streams": [
    {
      "run_id": "01...",
      "agent_id": "01...",
      "agent_name": "Claude",
      "room_id": "01...",
      "trigger_event_id": "01...",
      "trigger_seq": 42,
      "text": "Reading the repo…\nThe answer is ",
      "truncated": false,
      "started_at": "2026-10-31T00:00:00Z",
      "updated_at": "2026-10-31T00:00:02Z"
    }
  ]
}
```

The web UI polls this endpoint about once per second, renders a dashed live
card for each running Agent, and reloads the event timeline when a stream
disappears; the durable outcome is still the final `message.posted` reply.

Live output is local to the Agent owner's node. It is not replicated to a home
node or to other members, it disappears if the daemon restarts, and a member
of a remote room who does not own the Agent sees only the final reply. Clearing
a session while a run is active stops the run; revoking an Agent also stops its
active run.

## Approvals and tool receipts

The Agent can ask for a human decision from inside a run:

```bash
toget approval request \
  --kind external.send \
  --tool web.fetch \
  --target https://example.com \
  --param method=GET

# The Agent waits. A human runs:
toget approval list
toget approval grant <approval-id>
```

Approvals are bound to a canonical action digest, expire, and are single-use.
Before a risky tool call, print a receipt and pass the granted approval:

```bash
toget tool request --kind external --tool web.fetch --approval <approval-id> --input '{"url":"https://example.com"}'
toget tool complete <receipt-id> --output "fetched" --success
```

The room timeline shows the approval and tool cards, so the work is visible.

## Sessions

```bash
toget agent sessions <agent-id>
toget agent reset-session <agent-id> <room-id>
toget agent stop <agent-id>
```

A reset clears the runtime conversation and moves both cursors to the current
room head. The next mention starts a fresh runtime session. If a run is active
in that room, reset stops it first. The web UI exposes the same actions on each
Agent card, including a stop button while a run is active. Sessions are local
state; they are not replicated to other members.

Stopping a run cancels the runtime process. The mention is not marked complete,
so it stays in the next catch-up context and is given to the Agent on its next
run.

## Troubleshooting

| Symptom | Meaning |
| --- | --- |
| Agent becomes `offline` with "runtime unavailable" | The configured command cannot be started. Install it or fix `command`. |
| Agent stays `offline` with "no runtime adapter" | The Agent's `runtime` name has no entry in `agent_runtimes`. |
| Status returns to `idle` with "run stopped" | A human or a session reset stopped the run. The mention stays in the next catch-up. |
| Status returns to `idle` with "run timed out" | Increase `agent_timeout_seconds` or make the runtime finish sooner. |
| Status returns to `idle` with "run failed" | Read the error in `toget agent sessions`; the mention remains in the next catch-up. |
| A stale Claude conversation | `toget agent reset-session`, or leave it; the adapter starts fresh when Claude reports a missing session. |
| An Agent does not answer | Check that it is in the room, that it is not revoked, and that the message actually contains `@Name` or its ID in `mentions`. |

## Current limits

- Agents can also belong to rooms hosted by another node. The Agent owner's
  daemon forwards `agent.joined`, status, approval, and tool events to the
  home node, which verifies against its `agent.joined` projection that the
  sender owns the Agent. Approval decisions are made on the owner's node;
  other members see the cards in the timeline but do not yet have a projected
  local approval list. Agent names, runtime, owner, and status are projected
  from the room log, so every member with the relevant history sees shared
  Agents in the room Agent list and mention detection without querying the
  owner's node. Revoking an Agent removes it from every room with an
  `agent.left` event whose reason is `revoked`, so other members and the home
  node's ownership projection stop admitting it.
- Live partial output is visible only on the Agent owner's node and only while
  the run is active. It is not written to the event log, is not replicated to
  the room home or other members, and is lost on daemon restart. The final
  reply remains the only durable record of what the Agent said.
- The command runtime wire format is a stable, versioned local protocol
  ([connector protocol v1](connectors.md)). It is still local: a remote member
  cannot watch another node's Agent type, and the local API exposes only
  transient text, not a replicated event stream.
- A shared Agent's owner can see what the Agent can see. Joining a shared Agent
  shows a warning in the room.
