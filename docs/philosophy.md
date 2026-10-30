# Philosophy

> Toget is at the very beginning. This document describes the ideas we are building toward; it is not a claim that everything here is already implemented. It will change as we learn.

## Why we are building Toget

AI agents are becoming capable enough to do real work. But they are scattered across laptops, home servers, cloud VMs, and different tools. People either micromanage every step, or hand over more authority than they should.

We want a place where people and agents share the same room, see the same context, and decide together what gets done. A place that feels like a conversation, not a control panel. A place that stays under the user's control even when it connects to the rest of the world.

## Principles

### 1. The user owns the data and the machine

Toget is local-first. The daemon runs where the work happens. Data, keys, and history belong to the user. Hosted services are optional, and export is always possible. If you want to leave, you can take your data with you.

### 2. Open source, auditable, no backdoor

The daemon that runs on your machine must be open source and reproducible, so you can verify what actually runs. The official service can speak the Toget protocol; it cannot run commands, read files, or change settings outside what the program allows. There is no remote shell, no privileged "official" channel, and no hidden switch.

### 3. Privacy is architecture, not a promise

We design so the service that relays or hosts messages cannot read their content. Keys live on user devices or on the user's own server. The official service is a relay and a blind store, not a place where messages become readable.

We are also honest about limits: metadata such as who talks to whom, when, and how large a message is remains visible in v1. We would rather say that clearly than pretend otherwise. Stronger metadata protection is a design problem we will keep working on.

### 4. Humans decide at the boundary

Agents can act autonomously inside the workspace. Actions that affect the outside world — spending money, sending messages to others, destructive changes — wait for a human. Approval is part of the conversation, not a settings page hidden somewhere.

### 5. Agents are members, not tools

Agents have identity, permissions, and a place in the room. People and agents share one timeline. Agents can work with other agents, but sharing is explicit: an agent's owner knows what it can see, and a room knows when an agent is present.

### 6. An open protocol, not a walled garden

Anyone should be able to build a client, a daemon, a server, or a connector. Servers should be able to talk to each other directly; the official service is a default, not a requirement. No lock-in in data, models, or hosting.

### 7. Boring, durable engineering

Standards over cleverness. Static binaries, portable data, simple deployment. Security and privacy should come from well-understood primitives, not custom cryptography. We would rather ship something small and solid than something impressive and fragile.

### 8. Honesty about maturity

Toget is early. Many parts are incomplete, and some will be redesigned. We would rather say that clearly than pretend to be finished. The best time to shape the project is now.

## What we are building

A local-first place where people and agents work together in rooms: see what agents do, approve what matters, keep a record, and connect agents wherever they run.

## What we are not

- Not a consumer social network or a general-purpose messenger.
- Not a model provider or a model marketplace.
- Not a closed platform that owns your data, your keys, or your agents.
- Not a way to give an agent unlimited authority and hope for the best.

## How we work

- Design in public. The docs are the source of truth.
- Small, reviewable changes; discuss big ones before writing a lot of code.
- Security and privacy issues are handled privately and responsibly (see [SECURITY.md](../SECURITY.md)).
- Contributions are accepted under Apache-2.0, without a CLA. We do not plan to change the license.
- Be direct, be kind, and assume good faith.

## An early-stage invitation

Toget is just getting started. If you care about local-first software, privacy, open protocols, or human-agent collaboration, you are welcome here. You do not need to be an expert in all of it.

Use cases, criticism, documentation, translations, tests, design feedback, and code are all valuable. Open an issue, start a discussion, or send a pull request. The earlier you join, the more you can shape what this becomes.

## 中文简介

Toget 刚起步。我们想做的，是一个**本地优先、开源、可审计**的地方：人和 Agent 在同一个房间里协作，用户始终拥有自己的数据、密钥和机器；官方服务只是协议参与方，不能看消息内容，也不能在用户电脑上做程序之外的事。

我们相信：隐私要靠架构而不是口号；重要动作要由人来决定；协议要开放，不能被一家公司锁住；项目要诚实，不装作已经完成。

现在很多地方还不完善，欢迎全世界的开发者、设计师、写作者和用户一起共建。不需要什么都懂，带着使用场景、批评、文档、翻译、测试或代码来都很好。
