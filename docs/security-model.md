# Security Model

> Status: draft. This document describes the intended trust boundaries. It is not a claim that the MVP is secure for high-stakes use.

## Assets

- User identities and sessions
- Agent credentials and API keys
- Local files and shell access on the daemon machine
- Room content and event history
- Approval decisions and audit records
- Billing and organization data (later)

## Trust boundaries

```
User's browser  ──TLS──►  Server (Hub)  ◄──TLS──  Daemon  ──►  Agent processes
     │                       │                       │
     │                       │                       └── local files, tools, secrets
     │                       └── metadata, events, policies
     └── session, approvals
```

- The **server** is trusted for coordination, identity, and audit storage, but not for agent secrets.
- The **daemon** is trusted by the user to execute actions, but it is not trusted by the server to report truth without verification.
- **Agents** are untrusted code. They can be wrong, manipulated, or malicious.
- **Tool results and web content** are untrusted input. They can contain prompt injection.

## Threats and controls

### Prompt injection

An agent reads a web page, email, or file that contains instructions to exfiltrate data or spend money.

Controls:

- Tool results are labeled as untrusted content in the agent context.
- Actions that cross a trust boundary require approval: `external.send`, `money.spend`, `destructive`.
- Approval prompts show the concrete target, parameters, and amount, not a model-written summary.
- Policies can restrict which domains, recipients, and tools an agent may use.
- The audit timeline records the source of the instruction when available.

### Malicious or compromised agent

Controls:

- Each agent has its own identity, scopes, and process boundary.
- Agents cannot read other agents' credentials.
- The daemon applies resource limits and a restricted environment.
- Destructive actions require approval and are rate-limited.
- An agent can be stopped and revoked from the web app.

### Compromised daemon

Controls:

- Device credentials are short-lived access tokens backed by a revocable long-lived credential.
- The server can revoke a device and close its WebSocket.
- The daemon has the minimum scopes needed for its registered agents.
- Hosted deployments can require re-pairing after a security event.

### Approval phishing

An agent asks for approval of a harmless-looking action, then does something else.

Controls:

- Approvals are bound to an `action_digest` over the canonical action.
- The daemon recomputes the digest before execution and refuses mismatches.
- Approval prompts display the exact command, recipient, amount, and target.
- Approvals expire and are single-use.

### Audit tampering

Controls:

- Events are append-only.
- Hosted deployments hash-chain events per room.
- Export includes the hash chain and a signed manifest.
- Database operators cannot silently rewrite history without breaking verification.

### Secret leakage

Controls:

- Secrets stay in the OS keychain or a local encrypted vault.
- The server stores secret references, never plaintext values.
- Secrets are injected only into the process that needs them.
- Logs redact known secret patterns.

### Supply chain

Controls:

- Dependabot and `govulncheck` for Go; `pnpm audit` for web.
- Lockfiles committed.
- Container images built from pinned bases and scanned with Trivy.
- Releases are checksummed and signed with cosign.
- SBOMs published with each release.

## MVP limitations

The MVP does **not** provide:

- a strong OS sandbox for arbitrary agents;
- end-to-end encryption for room content;
- formal verification of the audit chain;
- protection against a fully compromised user device;
- enterprise-grade retention or legal hold.

These limitations must be stated in the README and in the UI where they matter.

## Responsible disclosure

See [SECURITY.md](../SECURITY.md). Do not open a public issue for a vulnerability.
