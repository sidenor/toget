# Agent policies

Toget treats an Agent as a member, not a trusted extension of its owner. An
Agent can ask to do something; a policy decides whether the request is
pre-authorized, refused, or still needs a human approval. Policy language v1
is implemented in `internal/policy` and is enforced at both the approval and
tool-call boundaries.

## Model

A policy is a JSON rule set stored per room, optionally narrowed to one Agent:

- The **room default** applies to every Agent in the room.
- A **per-Agent policy** adds rules that apply only to that Agent.
- Any member may read policies; the room owner may edit any policy, and an
  Agent's owner may edit that Agent's policy. An Agent cannot grant itself
  authority.
- Saving replaces the entire rule set atomically. Missing rule IDs are
  generated; supplied IDs must be unique.

Evaluation merges the room default and the Agent's own rules:

1. If any matching rule says `deny`, the action is refused. Deny always wins.
2. Otherwise the first matching `allow` rule pre-authorizes the action.
3. If nothing matches, a risky action still requires human approval.

An `allow` rule must contain at least one selector. This prevents an
accidental unconditional "allow everything" rule; an empty deny rule is
allowed because it refuses everything.

The decision is recorded with the approval or tool receipt (`policy_id`) and
in the room log. A policy grant is still an exact, single-use approval: the
action digest is unchanged, and execution must present the same action to
consume it. Policies never bypass the digest check.

## Matching

All selector fields are optional. Empty means "any". Patterns support `*` for
any sequence and `?` for one character, matched against the whole value. There
are no escapes, path separators, or character classes.

| Field | Matches | Case |
| --- | --- | --- |
| `kind` | Action kind, e.g. `tool.call`, `external.send`, `money.spend` | Sensitive |
| `tool` | Tool name, e.g. `mail.send`, `shell` | Sensitive |
| `target` | Concrete target: domain, path, room, resource | Sensitive |
| `recipient` | Recipient address or handle | Insensitive |
| `amount_min`, `amount_max` | Decimal amount range, inclusive | Exact decimal |
| `currency` | Currency code | Insensitive |
| `weekdays` | `0` = Sunday through `6` = Saturday | Local to timezone |
| `start`, `end` | `HH:MM` time window, start inclusive, end exclusive | Local to timezone |
| `timezone` | IANA zone name or `Local` (default) | Exact |

Amounts are non-negative decimal strings with at most 18 fractional digits,
no leading zeros, and at most 64 characters. They are compared with exact
rational arithmetic, not floating point. A range rule only matches an action
that carries an amount.

A time window is set as a pair. If `end` is earlier than `start`, the window
crosses midnight; the after-midnight portion belongs to the previous weekday.
For example, Saturday 22:00–02:00 matches Saturday at 23:00 and Sunday at
01:00, but not Sunday at 23:00.

For tool calls, the policy matcher also reads conventional top-level input
fields `target`, `recipient`, `amount`, and `currency`. These fields are a
convenience for rules and do not change the approval digest, which still
covers the complete canonical input.

## Examples

Allow an Agent to send mail only to one domain, and never to anyone else:

```json
{
  "rules": [
    {"effect": "allow", "kind": "external.send", "tool": "mail.send",
     "recipient": "*@example.com", "note": "work mail"},
    {"effect": "deny", "kind": "external.send", "tool": "mail.send"}
  ]
}
```

Allow small purchases during weekday office hours, but require approval for
anything larger:

```json
{
  "rules": [
    {"effect": "allow", "kind": "money.spend", "currency": "USD",
     "amount_max": "25", "weekdays": [1, 2, 3, 4, 5],
     "start": "09:00", "end": "18:00", "timezone": "Asia/Shanghai"}
  ]
}
```

Deny destructive shell work outright, and allow a specific read-only helper:

```json
{
  "rules": [
    {"effect": "deny", "kind": "tool.call", "tool": "shell",
     "target": "/etc/*", "note": "system files are off limits"},
    {"effect": "allow", "kind": "tool.call", "tool": "build.status"}
  ]
}
```

Because deny wins, the second rule cannot override the first for a matching
path. An action that matches no rule still needs a human approval.

## API and UI

The local API exposes policies under `/api/v1/rooms/{id}/policies`:

- `GET` lists the room default and per-Agent policies.
- `PUT` replaces one rule set (`agent_id` empty means the room default).
- `DELETE` removes one rule set.
- `POST /api/v1/rooms/{id}/policies/evaluate` dry-runs an action without
  creating an approval or event.

The web UI has a **Policies** view with room and Agent selectors, a rule
editor, and an action evaluator. Policy edits appear in the timeline as
`policy.updated` and `policy.deleted` events.

## Limits

- At most 64 rules per policy; each document is capped at 64 KiB.
- Rule IDs are at most 64 characters, patterns 256, notes 200, timezones 64.
- Policies are local to a room. There are no organization-wide policies,
  per-user rules, rate limits, or rule templates yet.
- Policy documents are stored in the node database and protected by the same
  local vault and file permissions as the rest of the room state.
- A policy cannot make an action happen; it can only allow, refuse, or defer
  to human approval. Tool execution remains the Agent runtime's job.

See the [security model](security-model.md) for the trust boundaries and
[agent-runtime.md](agent-runtime.md) for how approvals reach the timeline.
