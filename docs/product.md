# Product

## One sentence

Toget is a local-first control plane for AI agents: connect agents wherever they run, use them all in one place, approve what matters, and keep an audit trail.

## The problem

People increasingly run several agents at once — on a laptop, a home server, a cloud VM, and inside different tools. Those agents are hard to see, hard to stop, and hard to trust with anything important.

The result is either:

- too little autonomy, because every action needs a human; or
- too much autonomy, because there is no safe way to delegate.

Toget is the missing control plane between the two.

## Users

**Primary (MVP):** developers and technical founders who already run multiple agents and want one place to use them.

**Secondary:** small teams that want to share agents, tasks, and approvals without sharing passwords or full machine access.

**Later:** companies that need SSO, retention policies, audit export, and private deployment.

## Jobs to be done

1. "I have agents on different machines. I want one inbox for all of them."
2. "I want to see what an agent did, and approve the risky step before it happens."
3. "I want to hand work to a teammate and their agent without giving them my whole machine."
4. "I want a record I can audit when something goes wrong."
5. "I want to use my own models and keys, and keep my data local."

## Product surfaces

| Surface | Responsibility | First release |
| --- | --- | --- |
| **Server (Hub)** | Identity, rooms, membership, relay, policy, event log, audit export | MVP |
| **Daemon** | Runs next to agents; connects outbound; executes approved actions; streams events; local secrets | MVP |
| **Web app** | Task inbox, rooms, approvals, agent status, timeline, settings | MVP |
| **CLI** | Install, pair, run daemon, register agents, inspect logs | MVP |
| **Desktop app** | Tray, auto-start, deep links, bundled daemon | Later |
| **Mobile app** | Approvals and status on the go | Later |

The web app is the primary client. Desktop and mobile are shells around the same web UI, not separate products.

## Core loop

1. Install the daemon on a machine and pair it with an account.
2. Register an agent — a local command, a remote endpoint, or an MCP server.
3. Create a task in a room.
4. The agent proposes a plan and actions.
5. Safe actions are allowed automatically; risky actions wait for a human.
6. A human approves or denies in the web app.
7. The daemon executes and streams events back.
8. The room timeline shows the plan, each action, the approval, the result, cost, and duration.

## What makes Toget different

- **Local-first:** the daemon and secrets stay with the user; the server coordinates.
- **Bring your own agents:** integrate existing runtimes instead of replacing them.
- **Approval is the product:** not another chat window — a place where delegated work is actually safe.
- **Auditable by default:** every consequential action is recorded, not reconstructed afterward.
- **Open core:** the protocol and core runtime are Apache-2.0; no lock-in.

## MVP scope

The first usable version should prove one loop end to end:

- one user, one daemon, one machine;
- sign in with GitHub or email;
- create a room and a task;
- register one CLI-based agent;
- see its plan and tool calls in a timeline;
- require approval for `external.send`, `money.spend`, and `destructive` actions;
- approve or deny from the web app;
- keep an append-only event log;
- self-host with Docker Compose and Postgres.

## Explicit non-goals for the MVP

- Mobile app, desktop app, and app-store distribution.
- Billing, subscriptions, and a marketplace.
- Multi-tenant SaaS and enterprise SSO.
- Public agent sharing or a social network.
- Human-to-human chat as a standalone product.
- Model hosting, fine-tuning, or inference resale.
- A general-purpose workflow builder.

## Success metrics

- **Time to first approved task:** under 10 minutes from install.
- **Risky-action coverage:** percentage of `external.send`, `money.spend`, and `destructive` actions that passed through an approval.
- **Weekly active agents and rooms.**
- **Approval latency:** median time from request to decision.
- **Audit completeness:** percentage of tool calls with a matching result event.
- **Retention:** users who return in week 2 and week 4.

## Principles

1. **Local-first.** The server is optional for execution; it never needs agent credentials.
2. **Deny by default.** Authority is granted explicitly and scoped narrowly.
3. **No invisible actions.** If it can affect the world outside the workspace, it leaves a trace.
4. **Human control at the boundary.** Automate inside the workspace; ask at the edge.
5. **Boring infrastructure.** Standards, static binaries, and portable data over clever platforms.
6. **Open protocol.** Anyone should be able to build a client, daemon, or connector.
