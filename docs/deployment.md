# Deployment

## Modes

| Mode | Server | Database | Daemon | Use case |
| --- | --- | --- | --- | --- |
| **Local dev** | `toget server` on localhost | Postgres in Docker, or SQLite for local mode | `toget daemon` on the same machine | Development and demos |
| **Self-hosted** | Docker container or binary | PostgreSQL | User machines | A team or a privacy-conscious user |
| **Hosted** | Managed containers | Managed PostgreSQL | User machines | The commercial service |
| **Enterprise** | Helm chart, HA | Managed or external PostgreSQL | Managed fleet | SSO, retention, support |

## Self-hosted reference deployment

```yaml
# deploy/compose/docker-compose.yml (sketch)
services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_DB: toget
      POSTGRES_USER: toget
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U toget"]
      interval: 5s
      timeout: 3s
      retries: 10

  server:
    image: ghcr.io/sidenor/toget:latest
    environment:
      TOGET_DATABASE_URL: postgres://toget:${POSTGRES_PASSWORD}@postgres:5432/toget?sslmode=disable
      TOGET_PUBLIC_URL: https://toget.example.com
      TOGET_LISTEN_ADDR: :8080
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "8080:8080"

volumes:
  postgres-data:
```

Put a TLS-terminating reverse proxy (Caddy, Nginx, or a cloud load balancer) in front of the server. Do not expose Postgres to the public internet.

## Configuration

Configuration comes from environment variables first, then an optional YAML file. All variables use the `TOGET_` prefix.

| Variable | Purpose |
| --- | --- |
| `TOGET_DATABASE_URL` | PostgreSQL connection string |
| `TOGET_PUBLIC_URL` | Public base URL for links and OAuth callbacks |
| `TOGET_LISTEN_ADDR` | HTTP listen address, default `:8080` |
| `TOGET_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |
| `TOGET_SESSION_SECRET` | Session signing secret |
| `TOGET_GITHUB_CLIENT_ID` / `_SECRET` | GitHub OAuth |
| `TOGET_SMTP_URL` | Email magic links and notifications |
| `TOGET_S3_*` | Optional object storage for attachments and exports |

Secrets should be provided by the platform's secret manager or a root-readable env file, never committed.

## Database migrations

- Migrations are embedded in the server binary.
- `toget migrate` applies them explicitly; `toget server --migrate` applies them at startup for small deployments.
- For HA deployments, run migrations as a separate job before rolling out the new version.
- Migrations must be backward-compatible for one release: add columns first, backfill, then remove old code.

## Daemon installation

### Linux

- Install the binary to `/usr/local/bin/toget`.
- Install `deploy/systemd/toget.service`.
- State: `/var/lib/toget` for system service, or `~/.local/state/toget` for user service.
- Config: `/etc/toget/config.yaml` or `~/.config/toget/config.yaml`.

### macOS

- Install the binary to `/usr/local/bin/toget` or bundle it in the app.
- Install `deploy/launchd/com.toget.daemon.plist`.
- State: `~/Library/Application Support/Toget`.
- Secrets: Keychain.
- Sign and notarize the binary and the app bundle for distribution.

### Windows

- Install the binary to `%ProgramFiles%\Toget`.
- Register a Windows service (via `golang.org/x/sys/windows/svc` or the installer).
- State: `%ProgramData%\Toget`.
- Secrets: Credential Manager.
- Sign the installer and binaries before public distribution.

## Upgrades

- The server exposes `/healthz` and `/readyz`.
- The daemon checks for signed releases and can self-update, but auto-update is opt-in.
- Protocol version is negotiated; a newer daemon can talk to an older server within the supported window.
- Rollbacks require a database migration policy; destructive migrations are not allowed in a patch release.

## Backups

- **Server:** nightly PostgreSQL logical backups, plus point-in-time recovery for hosted deployments.
- **Daemon:** local state is reconstructable from the server event log and the agent configuration file. Back up the config and the local vault.
- **Audit exports:** store hash-chain manifests with the export so integrity can be verified later.

## Observability

- Structured JSON logs to stdout.
- Prometheus metrics at `/metrics` (protected or internal-only).
- OpenTelemetry traces exported via OTLP.
- Health endpoints for load balancers.
- No proprietary APM dependency in the core.
