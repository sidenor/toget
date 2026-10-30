# Toget

**Your agents, together — connect agents wherever they run, use them all in one place, and team up with other people and their agents.**

Toget is an open-source, local-first place where people and their Agents work together in rooms — across machines, servers, and the open network.

> **Early stage.** Toget is just getting started. The design is incomplete and will change. We welcome developers, designers, writers, and users from everywhere to shape it with us — see [Philosophy](docs/philosophy.md) and [Contributing](CONTRIBUTING.md).

[中文](#中文)

## Why Toget

Agents now live on laptops, home servers, cloud VMs, and inside different tools. The places we use them are either:

- **chat apps built for humans**, where Agents are bots with limited context and no real identity; or
- **cloud dashboards**, where the company running the service holds the keys and can read everything.

Toget is the room where people and Agents meet instead: one timeline, real identities, clear permissions, and messages the relay cannot read.

## What Toget is

- **Rooms, not dashboards.** People and Agents are members of the same conversation. Messages, files, task cards, approvals, and tool receipts live in one timeline.
- **Local-first.** Keys and Agent execution stay on machines you control. Self-hosting keeps the data there too; official hosting stores only ciphertext. The official service is optional, and self-hosting is a first-class path.
- **End-to-end encrypted.** Relays and the official service handle ciphertext. They see who, when, and how large — not what. A self-hosted server is your own endpoint, so it can decrypt your rooms.
- **Self-host or use the official service.** Same protocol, same identity model, same clients. An account lives on your machine or on the official service; either way it has one 14-digit ID that others can reach.
- **Direct when possible, relay when necessary.** Nodes connect directly across a LAN or the internet. When they cannot, any reachable node can relay encrypted traffic.
- **Agents are members.** Agents have identities, scopes, and a place in the room. They are not anonymous bots with borrowed credentials.
- **Approval at the boundary.** Autonomy inside the workspace; a human decision before money moves, messages leave, or something is destroyed.
- **Open protocol, no lock-in.** Apache-2.0, no CLA, exportable data, bring your own models and keys.

Toget is not a model provider, a public social network, or an enterprise admin console. It is a private place for people and their Agents to get work done.

## Try the local loop

Toget currently runs on one machine: create an account, create a room, and
send messages to yourself through the CLI or the web UI. There is no network
transport yet, so two machines cannot talk to each other.

Requirements: Go 1.27+, Node 24, and pnpm 9. Or install the pinned toolchain
with `mise install`.

```bash
pnpm install
pnpm --filter @toget/app build   # web/app/dist is also committed
go run ./cmd/toget serve
```

Open <http://127.0.0.1:7718> and create your account. There is no recovery
flow yet. At this stage the password is an Argon2id verifier for future
sign-in; it does not yet unlock the local vault, so protect the data directory
with OS permissions and full-disk encryption on a shared machine.

The same loop from the CLI, while `toget serve` is running:

```bash
go run ./cmd/toget account create --name Aiden
go run ./cmd/toget room create --name General
go run ./cmd/toget send "hello from this machine"
go run ./cmd/toget messages
```

Data lives in the per-user data directory (`~/.local/share/toget` on Linux,
`~/Library/Application Support/Toget` on macOS, `%LOCALAPPDATA%\Toget` on
Windows). Set `TOGET_HOME` to use another directory. `task dev` rebuilds the
web UI and starts the node; `task test` runs the Go tests and the web type
check.

The local API binds to loopback only. Private keys and event payloads are
encrypted at rest; the append-only event log is hash-chained and the database
rejects updates and deletes.

## Design docs

- [Philosophy](docs/philosophy.md) — why Toget exists, what we believe, and what we are not
- [Product](docs/product.md) — what Toget is, who it is for, and the MVP scope
- [Architecture](docs/architecture.md) — nodes, account hosts, rooms, relay tree, and trust boundaries
- [Protocol](docs/protocol.md) — node protocol, end-to-end envelope, local API, versioning
- [Security model](docs/security-model.md) — who can see what, threats, controls, and limits
- [Deployment](docs/deployment.md) — self-hosted, official-hosted, relay, and offline modes
- [Technology stack](docs/tech-stack.md) — choices, rationale, and rejected alternatives
- [Repository layout](docs/repo-layout.md) — where every kind of code belongs
- [Roadmap](docs/roadmap.md) — milestones and near-term tasks

## Status

**Early implementation — the local loop works.** The M0 scaffold is in place
and the first M1 slice runs: account and device identity, the 14-digit ID,
SQLite storage, an encrypted append-only event log, the loopback API, the
CLI, and the React web UI. The node protocol, LAN transport, end-to-end
envelope, rooms with more than one member, and Agents are not implemented.

Many parts are incomplete, and some ideas will be redesigned as we learn.
That is exactly why the project is open this early. If you care about
local-first software, privacy, open protocols, or humans and Agents working
together, you are welcome to help shape it. You do not need to be an expert in
all of it: use cases, criticism, documentation, translations, tests, design
feedback, and code are all valuable.

See [docs/philosophy.md](docs/philosophy.md) for the ideas behind the project, and [CONTRIBUTING.md](CONTRIBUTING.md) for how to get involved.

## Contributing

Toget is open to contributors from all over the world. There is no CLA: contributions are accepted under Apache-2.0, and we do not plan to change the license.

See [CONTRIBUTING.md](CONTRIBUTING.md). Please read [SECURITY.md](SECURITY.md) before reporting a vulnerability.

## License

Apache-2.0. See [LICENSE](LICENSE).

The Toget name and logo are covered by [TRADEMARK.md](TRADEMARK.md).

## 中文

**你的 Agent，聚在一起——连接各处运行的 Agent，在一个地方统一使用，并和其他人以及他们的 Agent 组成团队。**

Toget 是一个开源、本地优先的空间：人和自己的 Agent 在同一个房间里协作，跨机器、跨服务器、跨开放网络。

> **项目刚起步。** 设计还不完整，很多地方不完善，以后也会调整。正因为如此，我们选择现在就开放：欢迎全世界的开发者、设计师、写作者和用户一起共建。不需要什么都懂，带着使用场景、批评、文档、翻译、测试或代码来都很好。

### 它是什么

- **房间，而不是仪表盘。** 人和 Agent 是同一个会话里的成员。消息、文件、任务卡片、审批和工具回执都在一条时间线上。
- **本地优先。** 密钥和 Agent 的执行都在你自己控制的机器上；自建时数据也在自己手里，官方托管只存密文。官方服务是可选的，自建是一等公民。
- **端到端加密。** 中转和官方托管只接触密文，知道谁在什么时候发了多大的消息，但看不到内容；自建服务器是你自己的端点，能解密你自己的房间。
- **可以自建，也可以用官方服务。** 同一套协议、同一套身份模型、同一套客户端。账号可以放在自己的机器上，也可以放在官方；两种方式都有一个 14 位账号 ID，别人都能找到你。
- **能直连就直连，需要中转才中转。** 节点在局域网或公网上优先直连；连不上时，任何可到达的节点都可以帮忙中转密文。
- **Agent 是成员。** Agent 有自己的身份、权限和房间里的位置，不是借用别人凭据的匿名机器人。
- **在边界上由人决定。** 工作区里可以自主；涉及花钱、对外发送或破坏性操作时，等人来批。
- **开放协议，不锁定。** Apache-2.0，无 CLA，数据可导出，模型和密钥自带。

Toget 不是一个模型供应商，不是公开社交网络，也不是企业管理后台。它是人和自己的 Agent 一起做事的一个私密空间。

### 本地试用

目前可以在单机上跑通第一个闭环：创建账号、创建房间、在命令行或网页里发消息。还没有网络传输，两台机器暂时不能互通。

```bash
pnpm install
pnpm --filter @toget/app build
go run ./cmd/toget serve
```

打开 <http://127.0.0.1:7718> 创建账号即可。也可以让服务在后台运行，再用 CLI：

```bash
go run ./cmd/toget account create --name Aiden
go run ./cmd/toget room create --name General
go run ./cmd/toget send "你好"
go run ./cmd/toget messages
```

数据默认放在用户数据目录（Linux 为 `~/.local/share/toget`），可用 `TOGET_HOME` 指定。私钥和事件内容在本地加密存储，事件日志只可追加、带哈希链。目前密码只保存为 Argon2id 校验值，用于以后的登录，还不能解锁本地保险库；请依赖系统权限和全盘加密保护数据目录。

### 文档

- [理念](docs/philosophy.md)
- [产品](docs/product.md)
- [架构](docs/architecture.md)
- [协议](docs/protocol.md)
- [安全模型](docs/security-model.md)
- [部署](docs/deployment.md)
- [技术栈](docs/tech-stack.md)
- [代码结构](docs/repo-layout.md)
- [路线图](docs/roadmap.md)

**早期实现：本地闭环已经可用。** M0 脚手架已就位，M1 的第一块可以运行：账号与设备身份、14 位 ID、SQLite 存储、加密的只追加事件日志、本地 API、CLI 和 React 网页。节点协议、局域网传输、端到端信封、多人房间和 Agent 还没有实现，很多地方仍会调整。欢迎开 issue 关注、分享场景，或直接参与建设。项目理念见 [docs/philosophy.md](docs/philosophy.md)，参与方式见 [CONTRIBUTING.md](CONTRIBUTING.md)。
