# 0001 — One monorepo

**Status:** accepted
**Date:** 2026-10-30

## Context

Toget consists of a server, a cross-platform daemon, a web app, shared protocol types, deployment assets, and documentation. The protocol changes frequently during early development, and a change often touches the server, daemon, web client, and docs at the same time.

## Decision

Use one repository for the core server, daemon, web app, shared packages, deployment assets, and public documentation.

## Consequences

- Protocol changes can be made atomically across all components.
- One CI pipeline can catch contract drift and cross-platform build failures.
- One release train keeps server, daemon, and web versions aligned.
- Contributors can run the whole system with one checkout.
- The repository must enforce boundaries through package structure and the API contract, because it does not have separate-repository walls.
- Private notes and customer data must stay outside the repository.

## Alternatives considered

- **Separate server, daemon, and web repositories:** more independent releases, but contract changes become multi-repository dances. Rejected for a small team.
- **A monorepo only for the core, with separate connector repositories:** possible later for third-party connectors; the core stays together.

## Review trigger

Revisit if the repository becomes too large for CI, or if independent release cadences become a real bottleneck. The package boundaries are designed so a split is possible without rewriting domain logic.
