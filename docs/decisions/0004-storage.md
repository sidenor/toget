# 0004 — Postgres for team deployments, SQLite for local mode

**Status:** accepted
**Date:** 2026-10-30

## Context

The server stores users, rooms, memberships, agents, tasks, approvals, policies, and an append-only event log. A team deployment needs a real database server. A local-first desktop user should not need to install one.

The daemon also needs local state that survives restarts and works offline: an outbox, agent configuration, and a cache of room metadata.

## Decision

- **Server, team/hosted mode:** PostgreSQL, accessed with `pgx`, `sqlc`, and `goose` migrations.
- **Server, local mode:** SQLite via the pure-Go `modernc.org/sqlite` driver, with the same migration discipline.
- **Daemon:** SQLite for the outbox, local state, and cache.
- **No ORM.** Queries are SQL; generated types come from `sqlc`.
- The domain layer depends on a `store` interface, not on a database driver.
- A shared behavior suite runs against both Postgres and SQLite to keep parity.

## Consequences

- Postgres provides transactions, JSONB, `LISTEN/NOTIFY`, and a mature managed-service ecosystem for teams.
- SQLite gives local users zero-ops storage and keeps cross-compilation free of CGO.
- The daemon can work offline and replay events after reconnecting.
- Two SQL dialects mean two sets of queries and a parity test suite. The store interface is intentionally small.
- Schema changes require migrations and backward-compatible rollout in both implementations.

## Alternatives considered

- **Postgres only:** simpler server code, but forces every desktop/local user to run Postgres. Rejected for a local-first product.
- **SQLite only:** excellent local mode, but weak for multi-instance team deployments and concurrent writes at team scale. It would force a migration later.
- **MySQL/MariaDB:** viable, but weaker JSONB and ecosystem fit for this data model.
- **MongoDB:** the data is relational; transactions and joins are useful.
- **ORM (GORM/Ent):** faster CRUD, but hides queries and creates a schema abstraction that is hard to undo.

## Review trigger

Revisit if the two-store parity cost becomes disproportionate to the product value, or if Postgres becomes a measured bottleneck at a scale that justifies a different store. The store interface isolates either change.
