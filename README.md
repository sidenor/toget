# Toget

**Your agents, together — connect agents wherever they run, use them all in one place, and team up with other people and their agents.**

Toget is an open-source, local-first control plane for AI agents. It gives you one place to see what your agents are doing, approve the actions that matter, and keep an audit trail — across machines, tools, and people.

[中文](#中文)

## Why Toget

Agents are scattered across laptops, servers, and clouds. You can't see what they're doing, you can't stop them before they act, and your teammates can't safely work with them.

Toget is the room where all of that comes together.

## What it does (planned)

- **Connect agents from anywhere** — a local daemon connects the agents you already run, without opening inbound ports.
- **One place to use them** — use agents from multiple machines and providers side by side.
- **Humans + agents, together** — shared rooms with roles, scopes, and invitations.
- **Approval gates** — risky actions (money, recipients, destructive operations) wait for a human.
- **Audit trail** — an append-only timeline of plans, tool calls, approvals, results, cost, and duration.
- **Bring your own models and keys** — local-first, no lock-in.

## Status

Early development. Nothing usable yet. Open an issue if you want to follow along or share a use case.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the draft design.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Please read [SECURITY.md](SECURITY.md) before reporting a vulnerability.

## License

Apache-2.0. See [LICENSE](LICENSE).

The Toget name and logo are covered by [TRADEMARK.md](TRADEMARK.md).

## 中文

**你的 Agent，聚在一起——连接各处运行的 Agent，在一个地方统一使用，并和其他人以及他们的 Agent 组成团队。**

Toget 是一个开源、本地优先的 AI Agent 控制面：看得见每一步操作，只批该批的，全程留痕。支持自带模型和密钥，不锁定供应商。

目前处于早期开发阶段，还不能实际使用。欢迎开 issue 关注或分享场景。
