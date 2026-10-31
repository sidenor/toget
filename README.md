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
- **Direct when possible, relay when necessary.** Nodes connect directly across a LAN or the internet. When they cannot, a self-hosted or community relay forwards encrypted traffic.
- **Agents are members.** Agents have identities, scopes, and a place in the room. They are not anonymous bots with borrowed credentials.
- **Approval at the boundary.** Autonomy inside the workspace; a human decision before money moves, messages leave, or something is destroyed.
- **Open protocol, no lock-in.** Apache-2.0, no CLA, exportable data, bring your own models and keys.

Toget is not a model provider, a public social network, or an enterprise admin console. It is a private place for people and their Agents to get work done.

## Try it on one machine or a LAN

Toget runs as a local node with an encrypted room log. On one machine, create
an account, create a room, and send messages through the CLI or the web UI.

Two machines on the same LAN can share a room today. Both run `toget serve`;
the inviter creates an invite code and the other person redeems it. Toget
connects over pinned HTTPS on port `7717`, so the code carries the node
address and the certificate fingerprint. History is shared only if the
inviter chooses it: none, the current epoch, or all epochs.

For machines that cannot reach each other directly, run the optional relay
and directory:

```bash
go run ./cmd/toget-official
# Relay:     https://<host>:7719#fp=...
# Directory: https://<host>:7720#fp=...
```

Point `toget serve` at them with `--node-relay '<relay-url>'` and
`--node-directory '<directory-url>'`, or use `--node-relay auto` to follow the
directory's default relay. The relay forwards encrypted requests and sees
metadata, not room content. Multi-hop relays and a short offline queue are
implemented. There is still no NAT traversal or hole punching: across
networks, the relay is the path. See [Relay and directory](docs/relay.md).

Requirements: Go 1.27+, Node 24, and pnpm 9. Or install the pinned toolchain
with `mise install`.

```bash
pnpm install
pnpm --filter @toget/app build   # web/app/dist is also committed
go run ./cmd/toget serve
```

Open <http://127.0.0.1:7718> and create your account. There is no password
recovery service yet, but you can link a second device through pairing (below),
and an encrypted backup can move an account between machines. At this stage
the password is an Argon2id verifier for local sign-in; it does not yet unlock
the local vault, so protect the data directory with OS permissions and
full-disk encryption on a shared machine.

The same loop from the CLI, while `toget serve` is running:

```bash
go run ./cmd/toget account create --name Aiden
go run ./cmd/toget room create --name General
go run ./cmd/toget send "hello from this machine"
go run ./cmd/toget messages
```

If `claude` is installed and authenticated, give the room an Agent:

```bash
go run ./cmd/toget agent add --name Claude --runtime claude-code
go run ./cmd/toget room add-agent <room-id> <agent-id>
go run ./cmd/toget send "@Claude summarize what we talked about"
```

The daemon wakes the Agent on the mention, catches it up after its last run,
resumes its Claude Code session, posts the reply, and shows status cards in the
timeline. See [Agent runtime](docs/agent-runtime.md) for command runtimes,
timeouts, sessions, and approvals.

To invite a second machine on the same LAN, keep `toget serve` running on both.
On the inviter's machine:

```bash
toget room invite <room-id> --history none --qr
# prints the portable code (toget-invite:v1:…) and a terminal QR code
# add --png invite.png to write a PNG QR code (mode 0600)
```

On the other machine:

```bash
toget join 'toget-invite:v1:eyJ...'
```

The web UI has the same flow: **Room details → Invite someone** shows the code
and its QR, **Join with code** in the room sidebar redeems either, and
**Scan QR image** there reads a screenshot or photo locally in the browser. If
the node advertises the wrong interface, set `--node-advertise <host:port>` or
`node_advertise` in `config.json`. `--no-node` keeps a node local-only.

To use the same account on a **second device**, pair the devices instead of
copying the data directory. Keep `toget serve` running on the existing device:

```bash
toget pair create --qr
# Pairing code (valid for 10 minutes): toget-pair:v1:eyJ…
# plus a terminal QR code; --png pair.png writes a PNG (mode 0600)
```

On the fresh device, run `toget pair join 'toget-pair:v1:eyJ...' --name Laptop`.
It sends its own new keys and prints a fingerprint. Back on the existing
device, check that the fingerprint matches, then approve:

```bash
toget pair list
toget pair approve <pairing-id>
```

The new device receives the account record, its own device certificate, and
the account private key sealed to its encryption key. It also receives room
membership and epoch keys, then syncs message history from each room's home.
Paired devices keep their account device lists in sync in the background, so a
device added through one sibling appears on the others; `toget account sync`
asks for a round immediately, and a signed revocation reaches every reachable
device. The pairing code is a short-lived bearer secret, and the account key
is never transferred in plaintext. The web UI has the same flow under **Devices → Add
another device**: it shows the pairing QR, and the onboarding screen can scan a
screenshot or photo locally. The QR carries the same secret and expires with
the code.

To hand someone your node without an invite or a directory, show a **business
card**:

```bash
toget card show --qr                 # card text plus a terminal QR code
toget card show --png card.png       # PNG QR code for a chat window or print
toget card add '<toget-card:v1:…>'   # verify and save someone else's card
toget node lookup 12345678           # saved card first, then mDNS, then directory
```

A card carries the signed identity and current endpoint, not room access.
Saving one pins the full server ID, so a different server with the same
eight-digit code is rejected. See [Business cards](docs/cards.md).

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
- [Agent runtime](docs/agent-runtime.md) — mentions, sessions, Claude Code, and command runtimes
- [Relay and directory](docs/relay.md) — cross-network routing, trust boundaries, quotas, and limits
- [LAN discovery](docs/discovery.md) — mDNS advertisement, verified lookup, privacy, and limits
- [Business cards](docs/cards.md) — portable signed cards, QR codes, saved-card pinning, and limits
- [Deployment](docs/deployment.md) — self-hosted, official-hosted, relay, and offline modes
- [Technology stack](docs/tech-stack.md) — choices, rationale, and rejected alternatives
- [Repository layout](docs/repo-layout.md) — where every kind of code belongs
- [Roadmap](docs/roadmap.md) — milestones and near-term tasks

## Status

**Early implementation — local and same-LAN rooms work.** The current build
has account and device identity, end-to-end encrypted room epochs, an
encrypted hash-chained event log, attachments, backup and restore, the
loopback API, the CLI, and a bilingual React web UI. Two nodes on the same
LAN can join a room with a portable invite code, exchange messages, and sync
history. A second device can pair with an existing one to recover the same
account without copying private keys, then sync its rooms from their homes.
Remote rooms support attachments, self-leave, and local archival. Agents can be
registered, joined to rooms, run locally when mentioned, and gated through
local approval and tool-receipt cards. An optional relay and signed-manifest
directory work across networks, with direct-first fallback, automatic relay
discovery, multi-hop routes, loop detection, quotas, and a bounded offline
queue. Same-LAN nodes also discover each other over multicast DNS and can
resolve a server code without a directory; every discovered endpoint is still
verified through its pinned TLS certificate and signed manifests. Portable
business cards can be copied as text or shown as a QR code, and a saved card
pins the full server ID for its short code. Room invites and device-pairing
codes can also be shown as QR codes and scanned from a still image.

Not implemented yet: NAT traversal or hole punching, account-key rotation,
multiple accounts on one node, and a bundled model. The daemon runs
Agents locally on a mention, through Claude Code or any command that speaks the
runtime protocol. Many parts will change as we learn.
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
- **能直连就直连，需要中转才中转。** 节点在局域网或公网上优先直连；连不上时，可以由自建或社区中继转发密文。
- **Agent 是成员。** Agent 有自己的身份、权限和房间里的位置，不是借用别人凭据的匿名机器人。
- **在边界上由人决定。** 工作区里可以自主；涉及花钱、对外发送或破坏性操作时，等人来批。
- **开放协议，不锁定。** Apache-2.0，无 CLA，数据可导出，模型和密钥自带。

Toget 不是一个模型供应商，不是公开社交网络，也不是企业管理后台。它是人和自己的 Agent 一起做事的一个私密空间。

### 本地或局域网试用

Toget 以本地节点的方式运行。单机上可以创建账号、创建房间，在命令行或网页里发消息。

同一局域网内的两台机器现在可以共享房间：两边都运行 `toget serve`，邀请方生成邀请码，另一台用邀请码加入。连接走固定证书指纹的 HTTPS（端口 `7717`），邀请码里带着节点地址和证书指纹。历史是否共享由邀请方决定：不共享、只共享当前一代、或共享全部。

两台机器无法直接连通时，可以运行可选的中继和目录：

```bash
go run ./cmd/toget-official
# Relay:     https://<host>:7719#fp=...
# Directory: https://<host>:7720#fp=...
```

给 `toget serve` 加上 `--node-relay '<中继地址>'` 和
`--node-directory '<目录地址>'`，或用 `--node-relay auto` 跟随目录的默认中继。
中继只转发加密请求、看得到元数据，看不到房间内容。已实现多跳中继和短暂的离线队列；
NAT 穿透和打洞还没有，跨网络时中继就是通路。详见 [中继与目录](docs/relay.md)。

```bash
pnpm install
pnpm --filter @toget/app build
go run ./cmd/toget serve
```

打开 <http://127.0.0.1:7718> 创建账号。目前还没有密码找回服务，但可以用下面的「设备配对」把同一账号连到第二台设备，也可以用备份把账号迁移到新机器。现阶段密码只作为本地登录的 Argon2id 校验值，还不能解锁本地保险库，请依赖系统权限和全盘加密保护数据目录。

也可以让服务在后台运行，再用 CLI：

```bash
go run ./cmd/toget account create --name Aiden
go run ./cmd/toget room create --name General
go run ./cmd/toget send "你好"
go run ./cmd/toget messages
```

如果本机装好并登录了 `claude`，可以让 Agent 加入房间：

```bash
go run ./cmd/toget agent add --name Claude --runtime claude-code
go run ./cmd/toget room add-agent <房间ID> <AgentID>
go run ./cmd/toget send "@Claude 帮我总结一下刚才聊的"
```

守护进程会在被 @ 时唤醒 Agent，补上它上次运行之后的消息，续接 Claude Code 会话，把回答发回房间，并在时间线里显示状态卡片。命令运行时、超时、会话重置和审批流程见 [Agent 运行时](docs/agent-runtime.md)。

局域网邀请：邀请方保持 `toget serve` 运行，然后执行：

```bash
toget room invite <房间ID> --history none --qr
# 显示邀请码（toget-invite:v1:...）和终端二维码
# 加 --png invite.png 可以写出 PNG 二维码（权限 0600）
```

另一台机器执行：

```bash
toget join 'toget-invite:v1:eyJ...'
```

网页端同样支持：**房间详情 → 邀请新成员** 会显示邀请码和二维码；侧边栏的 **用邀请码加入** 可以兑换，也可以点 **扫描二维码图片** 在浏览器本地识别截图或照片。如果节点广播了错误的网卡地址，启动时加 `--node-advertise <host:port>`，或在配置里设置 `node_advertise`；`--no-node` 可以只保留本地节点。

**第二台设备**不用拷贝数据目录，走配对即可。旧设备保持 `toget serve` 运行：

```bash
toget pair create --qr
# 输出：toget-pair:v1:eyJ...（10 分钟内有效）和终端二维码
# 加 --png pair.png 可以写出 PNG 二维码（权限 0600）
```

新设备执行 `toget pair join 'toget-pair:v1:eyJ...' --name Laptop`，会生成自己的新密钥并显示指纹。回到旧设备核对指纹后批准：

```bash
toget pair list
toget pair approve <pairing-id>
```

新设备会收到账号记录、自己的设备证书，以及用它的加密公钥封装的账号私钥；同时收到房间成员和密钥代次，再从各房间的 home 节点同步历史。配对后的设备会在后台同步账号的设备列表：通过其中一台设备加入的新设备会自动出现在其他设备上；`toget account sync` 可以立即同步一轮，带签名的撤销也会传播到每个能连上的设备。配对码是短期 bearer 密钥，账号私钥不会以明文传输。网页端在 **设备 → 添加另一台设备** 里也有同样的流程：会显示配对二维码，新设备的引导页可以在本地识别截图或照片；二维码和配对码是同一个秘密，同时过期。

要跳过邀请码和目录，把自己的节点递给人，可以出示**名片**：

```bash
toget card show --qr                 # 显示名片文本和终端二维码
toget card show --png card.png       # 生成 PNG 二维码，可发聊天或打印
toget card add '<toget-card:v1:…>'   # 验证并保存别人的名片
toget node lookup 12345678           # 先用已存名片，再走 mDNS，最后查目录
```

名片只带签名的身份和当前地址，不代表房间权限。保存名片会固定完整服务器 ID，同一个八位代码下的其他服务器会被拒绝。详见 [名片与二维码](docs/cards.md)。

数据默认放在用户数据目录（Linux 为 `~/.local/share/toget`），可用 `TOGET_HOME` 指定。私钥和事件内容在本地加密存储，事件日志只可追加、带哈希链。

### 文档

- [理念](docs/philosophy.md)
- [产品](docs/product.md)
- [架构](docs/architecture.md)
- [协议](docs/protocol.md)
- [安全模型](docs/security-model.md)
- [Agent 运行时](docs/agent-runtime.md)
- [中继与目录](docs/relay.md)
- [局域网发现](docs/discovery.md)
- [名片与二维码](docs/cards.md)
- [部署](docs/deployment.md)
- [技术栈](docs/tech-stack.md)
- [代码结构](docs/repo-layout.md)
- [路线图](docs/roadmap.md)

**早期实现：本地、局域网和跨网络中继房间已经可用。** 当前版本有账号与设备身份、端到端加密的房间密钥代次、加密的哈希链事件日志、附件、备份恢复、本地 API、CLI 和双语 React 网页。同一局域网内的两个节点可以用邀请码加入房间、互发消息并同步历史；第二台设备可以通过配对恢复同一账号并同步房间，无需拷贝私钥。远程房间支持附件、自主退出和本地归档。Agent 可以注册、加入房间，并通过本地审批和工具回执卡片受控。可选的中继和签名目录已经能跨网络工作，支持直连优先、自动发现中继、多跳路由、环路检测、配额和有限的离线队列。同一局域网里的节点还能通过 mDNS 相互发现，不依赖目录也能用服务器代码找到对方；发现的地址仍会通过固定 TLS 证书和签名清单验证。名片可以复制成文本或显示为二维码，保存后会把完整服务器 ID 固定到对应的八位代码上。房间邀请码和设备配对码也可以显示成二维码，并从图片中识别。

还没有实现：NAT 穿透与打洞、账号密钥轮换、同一节点登录多个账号，以及自带模型。守护进程会在被 @ 时在本地运行 Agent，可以用 Claude Code，也可以用任何实现了运行时协议的命令。很多地方仍会调整。欢迎开 issue 关注、分享场景，或直接参与建设。项目理念见 [docs/philosophy.md](docs/philosophy.md)，参与方式见 [CONTRIBUTING.md](CONTRIBUTING.md)。
