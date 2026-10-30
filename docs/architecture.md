# Toget Architecture (Draft)

> Status: design draft. Nothing here is implemented yet. Names and boundaries will change as the first version is built.

## Goals

- Give one person a single place to use agents that run on different machines, in different tools, and on different model providers.
- Let people and agents work in the same room without giving every agent unlimited authority.
- Make every consequential action visible, approvable, and auditable.
- Keep data local by default. A hosted service is optional, not required.
- Stay open: the core protocol and client should be implementable by anyone.

## Non-goals

- Training or serving foundation models.
- Replacing agent runtimes such as coding agents, browser agents, or workflow engines.
- Being a general-purpose social network or consumer messenger.
- Storing a user's passwords for third-party services in plaintext.

## Design principles

1. **Local-first.** The daemon, agent credentials, and execution history live with the user. The hub coordinates; it does not need to see everything.
2. **Bring your own agents.** Toget integrates with existing runtimes instead of forcing a single one.
3. **Deny by default.** An agent has no authority until a policy grants it, and risky actions require a human decision.
4. **Append-only history.** Events are immutable; corrections are new events, not edits.
5. **Least privilege.** Every agent, connector, and room gets its own identity and scoped tokens.
6. **Open core.** The protocol and core runtime are Apache-2.0. Enterprise governance features may be separately licensed.

## Components

```
┌──────────────┐        ┌──────────────┐        ┌──────────────┐
│   Client      │        │     Hub       │        │  Local       │
│ desktop/mobile│◄──────►│ identity,     │◄──────►│  Daemon      │
│               │  WS    │ rooms, relay, │  WS    │              │
└──────────────┘        │ event store   │        └──────┬───────┘
                        └──────────────┘               │
                                                       │ spawns / connects
                                                ┌──────▼───────┐
                                                │   Agents      │
                                                │ coding, browser│
                                                │ custom, MCP   │
                                                └──────────────┘
```

### Client

The human interface: task inbox, room timeline, approval prompts, agent status, and audit history. Desktop first; mobile as a companion for approvals and status.

### Hub

Coordinates identity, rooms, invitations, presence, and message relay. It stores the event log for hosted deployments. In local-only mode the hub can run on the same machine as the daemon.

### Local daemon

Runs next to the agents. It:

- connects outbound to the hub (no inbound ports required);
- registers agents and reports heartbeats;
- starts or attaches to agent processes;
- executes approved tool calls;
- streams events back to the hub;
- holds local secrets in an encrypted vault.

### Agent runtime adapter

A thin layer that normalizes different agent runtimes into a common event and tool-call model. MCP is the first-class tool protocol; other adapters can be added without changing the core.

### Policy and approval service

Evaluates each proposed action against scoped policies. Produces `allow`, `deny`, or `require_approval`. Approval decisions are recorded as events and bound to a specific action digest so an approval cannot be replayed for a different action.

### Event store

An append-only log per room. Each event has an ID, timestamp, actor, type, payload, and references to prior events. Optional hash chaining makes tampering detectable.

## Core objects

| Object | Meaning |
| --- | --- |
| `User` | A human identity. |
| `Agent` | A registered agent runtime with an owner, capabilities, and scopes. |
| `Room` | A shared workspace containing humans, agents, tasks, and events. |
| `Task` | A unit of work with a goal, owner, status, and budget. |
| `Event` | An immutable record of something that happened. |
| `Approval` | A human decision bound to a specific proposed action. |
| `Policy` | A rule that maps an action to allow / deny / require approval. |
| `Connector` | An integration that exposes tools or data to agents. |

## Event types (initial set)

- `room.created`, `room.member_added`
- `agent.registered`, `agent.heartbeat`, `agent.offline`
- `task.created`, `task.assigned`, `task.completed`, `task.failed`
- `plan.proposed`
- `tool.requested`, `tool.allowed`, `tool.denied`, `tool.result`
- `approval.requested`, `approval.granted`, `approval.rejected`, `approval.expired`
- `message.posted`
- `policy.changed`

## Approval model

Actions are classified by scope:

| Scope | Examples | Default |
| --- | --- | --- |
| `read` | Read a file, query a database | Allow inside the workspace |
| `write` | Modify files, create records | Allow with audit |
| `external.send` | Send email, post publicly, call a third-party API | Require approval |
| `money.spend` | Purchases, transfers, paid API calls above a threshold | Require approval |
| `destructive` | Delete data, revoke access, force-push | Require approval |

Policies can narrow these defaults by agent, room, connector, amount, recipient, or time window. An approval is valid only for the exact action it describes and expires after a configurable window.

## Security model

Threats considered in the first design:

- **Prompt injection** from untrusted content. Tool results are labeled as untrusted, and actions that cross a trust boundary require approval.
- **Malicious or compromised agent.** Each agent has its own token, scopes, and process boundary.
- **Compromised daemon.** Tokens are short-lived and revocable; the hub can isolate a daemon.
- **Approval phishing.** Approval prompts show the concrete action, target, and amount, not just a summary written by the agent.
- **Audit tampering.** Events are append-only; hosted deployments can hash-chain them.
- **Secret leakage.** Secrets stay in the local vault and are injected only into the process that needs them.

End-to-end encryption for room content is desirable but not in the first milestone. The initial hosted mode should be explicit about what the server can see.

## Deployment modes

1. **Local only.** Client, hub, and daemon on one machine. No account required.
2. **Self-hosted team.** A team runs the hub; members run daemons on their own machines.
3. **Hosted cloud.** The project runs the hub; users can still keep execution local.

## Open-core boundary

**Apache-2.0 core:** protocol, client, local daemon, hub, policy engine, basic event log, MCP connector.

**Separately licensed enterprise features:** SSO/SCIM, advanced RBAC, long-term audit retention and export, compliance reports, high availability, private deployment support.

The boundary should be documented per feature, and no core security capability should depend on a proprietary component.

## Roadmap

- **M0 — scaffold.** Documentation, license, repository hygiene.
- **M1 — local MVP.** One user, local daemon, one agent adapter, task inbox, approval prompts, event timeline.
- **M2 — team rooms.** Multiple humans and agents, invitations, roles, scoped tokens.
- **M3 — connectors and governance.** More agent adapters, policy templates, audit export, self-hosted deployment.

## Open questions

- Which agent runtime should the first adapter target?
- Should rooms be end-to-end encrypted from the start, or after the local MVP?
- How much of the hub can be stateless?
- What is the smallest useful policy language?
- Which enterprise features are worth keeping closed without weakening trust?
