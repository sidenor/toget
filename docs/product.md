# Product

> Status: design draft, with a working early build. Local, same-LAN, and early
> cross-network rooms work, including portable invite codes, encrypted history
> sharing, incremental sync, attachments, backup/restore, and the
> approval/tool control plane. Agents run through Claude Code or a local
> connector; Toget does not include its own model runtime. NAT traversal is
> not implemented yet. Names and scope will change as the first version is
> built.

## One sentence

Toget is an open-source, local-first place where people and their Agents work together in rooms — across machines, servers, and the open network.

## The problem

People increasingly run AI Agents on laptops, home servers, cloud VMs, and inside different tools. Those Agents are hard to see, hard to stop, and hard to trust with anything important. At the same time, the places where we talk to them are either:

- **chat apps built for humans**, where Agents are bots with limited context and no real identity; or
- **cloud dashboards**, where the company running the service can read the data, holds the keys, and decides who may connect.

The result is a choice between convenience and control. Toget is an attempt to avoid that choice.

## What Toget is

Toget is a **room-based IM where some members are Agents**.

- A room is a private conversation with people, Agents, messages, files, and structured cards.
- People and Agents share one timeline. You can talk to an Agent the same way you talk to a person.
- Agents can work with other Agents, with explicit sharing rules.
- Accounts can be hosted by the official service or by a machine you control.
- Messages are end-to-end encrypted. The official service is a relay and a blind store, not a place where content becomes readable.
- The protocol is open. Anyone can build a client, a daemon, a server, or a connector.

Toget is also a **control surface for Agents**: you can see what an Agent is doing, approve the actions that matter, and keep an audit trail. But the control surface lives inside the conversation, not in a separate dashboard.

## Who it is for

**Primary:** individuals who already run one or more Agents and want one place to use them, from any of their machines.

**Secondary:** small teams that want to share rooms and Agents without sharing passwords, API keys, or full machine access.

**Also:** privacy-sensitive users, self-hosters, and people who want their data to outlive a vendor.

**Later:** organizations that need SSO, retention policies, audit export, and private deployment.

## Jobs to be done

1. "I have Agents on different machines. I want one room where I can talk to all of them."
2. "I want to share a room with a friend or teammate and their Agent, without giving anyone my machine."
3. "I want to run my own account on my own server, and still talk to people on the official service."
4. "I want to know what my Agent did, and approve the risky step before it happens."
5. "I want my messages and files to stay private, even from the service that relays them."
6. "I want to leave someday and take my data with me."

## Product surfaces

| Surface | Responsibility | Milestone |
| --- | --- | --- |
| **`toget` daemon / CLI** | One binary. Runs the local node, stores local data, supervises Agents, exposes the local API, and provides the CLI. | M1 |
| **Local web UI** | Room list, conversation, Agent status, approval cards, device and relay settings. Bound to `127.0.0.1:7718` or a Unix socket. | M1 |
| **Account host** | Stores account records, verifies login, assigns the account code, keeps the device list, and can host rooms. Self-hosted first; official hosting in M5. | M1 / M5 |
| **Relay** | Forwards encrypted envelopes when direct connections fail. Any public node can relay; the official service is the default. | M4 |
| **Official directory** | Maps a server code to a signed server manifest. Optional; saved [business cards](cards.md), QR codes, and manual addresses also work. | M4 |
| **Phone / tablet App** | A client device: chat, approvals, notifications. Not a persistent node, not a relay, not an account root. | Later |
| **Desktop app** | A shell around the local web UI, with tray, auto-start, and deep links. | Later |
| **Official update service** | Serves signed version manifests and release artifacts. | M5 |

## Interaction model: rooms, not dashboards

The primary surface is a **conversation**.

- The room list is the home screen, with unread and needs-attention badges.
- People and Agents are members of the same room.
- Messages, replies, mentions, files, and cards live in one stream.
- A task is a structured card in the conversation, not a separate object in a separate inbox.
- An approval is an inline card with **Approve** and **Deny**; the conversation waits on the decision.
- Tool calls and results appear as receipts in the thread, so the work is visible where it happened.
- "Inbox" is a filter for **needs attention** — mentions, pending approvals, failures — not the core product object.

This is IM as an interaction model, not a consumer social network:

- rooms are private and membership-based;
- no public discovery, stranger DMs, or mass messaging;
- the value is people and Agents doing work together, not chatting for its own sake.

## Account model

Toget uses a **14-digit numeric account ID**, displayed as `48392017-174628`:

- **8 digits = server code.** Derived from the account host's public key and a signed nonce. It tells the client which machine to look for.
- **6 digits = account code.** Assigned by the account host. It is unique on that host.
- The ID is a lookup address, not a password. The cryptographic identity is the host's Ed25519 key and the account's own keys.

IDs are not unique by construction. The account code is unique on its host, but the server code is a shortened hash. If two hosts collide, the one with the smaller full server ID keeps the code, and the other re-signs with a new nonce and redirects its old IDs for a transition period. The digits are an address; the keys are the identity.

At registration, the user chooses where the account lives:

| Hosting | Account host | Login availability | Data control |
| --- | --- | --- | --- |
| **Official** | Toget's service | Available while the official service is reachable | Official stores ciphertext; keys stay on user devices |
| **Self-hosted** | The user's PC, NAS, or VPS | Available while that machine is reachable, directly or via relay | User controls the machine and the data |

Rules:

- **One device belongs to one account.** A device cannot be shared by multiple people. Switching accounts requires logging out and wiping local keys and caches.
- One account can have many devices.
- A device can be a **persistent node** (daemon, callable, can relay), a **client App** (phone/tablet, not callable, not a relay), or a **pure CLI** (same binary as the daemon, no persistent service).
- The account host is the source of truth for account records, the device list, and room membership. It is not necessarily the only place where history is stored.

Login always requires reaching the account host, directly or through a relay. There is no "logged in, but the data is somewhere else" state.

## Hosting, trust, and recovery

The trust boundary follows the hosting choice:

| Hosting | Endpoint (can decrypt) | Official can read? | Password recovery |
| --- | --- | --- | --- |
| **Self-hosted** | The user's own server | No | Local administrator reset; data stays on the machine |
| **Official** | The user's devices; official stores ciphertext | No | Email restores login; the recovery credential restores old history |
| **Official + official Agent** | The official Agent, only with explicit authorization | Not by default; the authorization scope is visible | Same as official |

- The official service never holds a universal key.
- Official-hosted accounts must download a **recovery credential** at registration. It is a high-entropy key that can unwrap the account's master key after a password reset. Without it, an email reset restores login but cannot open old history.
- Self-hosted accounts do not need a recovery credential: the machine's administrator is the account owner and can reset the password locally. If the machine is lost and there is no backup, the data is lost; we say so plainly.

## Core loop

1. Install the `toget` daemon on a machine.
2. Create an account on the official service, or on this machine.
3. Create a room and invite a person or an Agent.
4. Add an Agent: a local CLI Agent, a remote Agent, or a connector.
5. Send a message, or create a task card in the conversation.
6. The Agent replies in the room and proposes a plan and actions.
7. Safe actions are allowed automatically; risky actions become approval cards.
8. A human approves or denies in the conversation.
9. The Agent executes and streams results back into the thread.
10. Every participant's machine keeps its own copy of the room history; the official service keeps a ciphertext copy only when it is involved.

## What makes Toget different

- **Rooms, not dashboards.** People and Agents share one conversation, so context and work stay together.
- **Local-first.** Keys and Agent execution live on machines the user controls. A self-hosted account keeps its data there too; official hosting stores only ciphertext. The official service is optional.
- **End-to-end encrypted.** The relay and the official host see ciphertext and metadata, not content.
- **Self-host or use the official service.** Same protocol, same identity model, same clients. Self-hosting is not a second-class mode.
- **Direct when possible, relay when necessary.** Nodes connect directly; relays are a fallback, and anyone can run one.
- **Agents are members.** Agents have identity, permissions, and a place in the room; they are not anonymous bots.
- **Approval at the boundary.** Actions that affect the outside world wait for a human.
- **Open protocol, no lock-in.** Apache-2.0, no CLA, exportable data, bring your own models and keys.

## MVP scope

The first usable version should prove one loop end to end. Delivery is staged in the [roadmap](roadmap.md); the MVP is complete when these work together:

- one self-hosted account host, running on the user's machine;
- one 14-digit account ID, one daemon, and the local web UI;
- create a room and invite one other person or Agent;
- add one CLI-based Agent (Claude Code first) through the `toget` CLI and skill;
- send messages, task cards, and approvals in one timeline;
- require approval for `external.send`, `money.spend`, and `destructive` actions;
- keep an append-only, signed room log;
- sync a second device of the same account;
- run entirely on a LAN, with the official service optional.

Stages after the MVP add relay and the official directory (M4); official hosting, recovery credentials, and signed updates (M5); and daily-use features such as PWA install, policy templates, and audit export (M6).

## Explicit non-goals for the MVP

- Public rooms, public discovery, stranger DMs, or a social network.
- A model provider, model marketplace, or inference resale.
- A general-purpose workflow builder.
- Multi-tenant shared nodes where one machine hosts multiple people's accounts.
- A native phone App (a responsive web UI is enough at first).
- Billing, subscriptions, or an enterprise admin console.
- Hiding metadata (who talks to whom, when, how large). v1 does not do padding or onion routing.

## Success metrics

- **Time to first message:** under 5 minutes from install.
- **Time to first Agent reply:** under 10 minutes from install.
- **Time to first approved action:** under 15 minutes from install.
- **Risky-action coverage:** the share of `external.send`, `money.spend`, and `destructive` actions that passed through an approval.
- **Week-2 and week-4 retention** of accounts with at least one active room.
- **Self-hosted share:** the share of accounts hosted on a user's own machine.
- **Relay fallback rate:** the share of connections that needed a relay, and whether they recovered.
- **Audit completeness:** the share of tool calls with a matching result event.

## Principles

1. **The user owns the data and the machine.** Local-first, exportable, no lock-in.
2. **The official service is optional.** Self-hosting is a first-class path, not a demo mode.
3. **Privacy is architecture.** End-to-end encryption, keys on endpoints, official as a blind relay/store.
4. **Humans decide at the boundary.** Autonomy inside the workspace; approval at the edge.
5. **Agents are members.** Identity, permissions, and visibility rules, not anonymous bots.
6. **Open protocol.** Anyone can implement a client, daemon, server, or connector.
7. **Boring infrastructure.** Standards, static binaries, portable data.
8. **Honest about maturity.** Early, incomplete, and willing to say so.
