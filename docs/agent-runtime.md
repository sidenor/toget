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
`toget serve`. The daemon launches it with `-p --output-format json` and keeps
the returned session ID, so the next mention resumes the same conversation.
If the saved Claude session no longer exists, the adapter starts a fresh one
instead of failing every later mention.

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
| `TOGET_HOME` | Data directory for the `toget` CLI |
| `TOGET_BIN` | Absolute path to the running `toget` binary |

The `toget` CLI reads `TOGET_ROOM_ID` and `TOGET_AGENT_ID` by default, so
commands such as `toget messages`, `toget send`, `toget approval request`, and
`toget tool request` work in the room without repeating flags.

## Command runtime protocol

A command runtime receives one JSON object on stdin and writes either a JSON
object or plain text on stdout. A non-zero exit code, a timeout, or output over
the limit is a failed run and is shown in the Agent's last error.

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

The command is started directly, without a shell. It runs with a timeout,
bounded stdout/stderr, and its process group is killed on timeout.

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
- Replies are posted when the run finishes; partial streaming is not
  implemented.
- A shared Agent's owner can see what the Agent can see. Joining a shared Agent
  shows a warning in the room.
