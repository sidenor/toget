# 0004 — SQLite first; Postgres only at scale

**Status:** accepted
**Date:** 2026-10-30
**Updated:** 2026-10-31 — replaced "Postgres for teams, SQLite for local" with "SQLite first".

## Context

A node stores account records, device certificates, room events, an outbox, sync cursors, and Agent configuration. It must work offline, survive restarts, and install without a database server. The official service starts with the same needs at a smaller scale; only a large deployment would need a shared database.

Earlier drafts assumed every team deployment would run Postgres. That forced a database server into a product whose first promise is "install one binary".

## Decision

- **Node, daemon, self-hosted account host, and the official service at first:** SQLite via the pure-Go `modernc.org/sqlite` driver.
- **Large official deployment, if measurements justify it:** PostgreSQL behind the same store interface, for account and room metadata; ciphertext blobs can move to object storage.
- **No ORM.** Queries are SQL and reviewed. Migrations are plain SQL, embedded in the binary, applied explicitly or at startup.
- The domain layer depends on a `store` interface, not on a database driver.
- A shared behavior suite runs against every store implementation.

## Consequences

- One binary and one data directory is a complete install.
- `CGO_ENABLED=0` cross-compilation works on every supported platform.
- The node works offline and replays its outbox after reconnecting.
- SQLite serializes writes; a single node is the right scope. Room history is per participant, so a node does not need to serve thousands of writers.
- If the official service moves to Postgres, it carries a second SQL dialect and a parity suite. The store interface is intentionally small to keep that cost bounded.
- Schema changes require migrations and a backup before an incompatible change.

## Alternatives considered

- **Postgres by default:** simpler at scale, but forces every personal node to run a database server. Rejected for a local-first product.
- **SQLite only forever:** fine for nodes, but risky for a large official deployment with many concurrent writers.
- **MySQL/MariaDB:** viable, but a weaker fit than Postgres for the scale path.
- **MongoDB:** the data is relational; transactions and joins are useful.
- **ORM (GORM/Ent):** faster CRUD, but hides queries and creates a schema abstraction that is hard to undo.
- **Embedded KV stores (BoltDB, Badger):** fast, but poor fit for relational queries, cursors, and partial indexes.

## Review trigger

Revisit if SQLite write contention becomes a measured problem for the official service, or if a self-hosted account host needs multi-writer concurrency. The store interface isolates either change.
