# Website bridge and installers

> Status: implemented. The local UI and `https://toget.vu` share one Agent
> inventory; the website can manage those Agents but cannot start a shell or
> read files.

Toget Agents run on the user's own computer. The website bridge lets a person
install and start a local node, pair it with the official website, and then
manage that machine's Agents from `https://toget.vu`. The local UI remains the
authority: pairing codes are shown only there, every sensitive request needs
the local vault to be unlocked, and either side can revoke a paired browser.

This document covers the install path, the pairing flow, the security model,
the HTTP API, and release download publishing.

## Install and start

macOS and Linux:

```bash
curl -fsSL https://toget.vu/install.sh | sh
toget serve
```

Windows PowerShell:

```powershell
powershell -c "irm https://toget.vu/install.ps1 | iex"
toget serve
```

The installer downloads the archive for the current OS and architecture,
verifies its SHA-256 checksum, and atomically replaces
`$TOGET_INSTALL_DIR/toget` (default `/usr/local/bin` when writable,
`~/.local/bin` otherwise). Nothing outside the install directory and a
temporary directory is written.

`toget serve` listens on loopback only. It prints the local address:

```text
Local node: http://toget.localhost:7718
```

Open that URL, not `127.0.0.1`, when copying the address to a person. Browsers
resolve `*.localhost` to loopback on macOS, Windows, and Linux, and the name
keeps cookies and the local UI separated by site. `toget open` opens the same
URL; `toget open --print` prints it without launching a browser. The daemon
only auto-opens a browser when it was started from a terminal with a graphical
session; `TOGET_NO_BROWSER=1` disables that.

The local UI is same-origin with the API. It signs in with the local API token
through an HttpOnly, SameSite=Strict cookie, so the token is not exposed to
scripts.

## Pairing flow

1. Install Toget and run `toget serve`.
2. Open `http://toget.localhost:7718`, create an account, and keep the node
   unlocked.
3. In the local UI, open **Machine** and select **生成配对码**. The six-digit
   code is valid for five minutes.
4. On `https://toget.vu/agents`, enter the code. The browser receives a token
   and shows the machine name, OS, account, Agent count, and a folder browser.
5. Add, edit, or revoke Agents from the website. The local UI updates on its
   next refresh.

The website also probes `GET /api/v1/bridge/info` before showing the pairing
form. If the local node is older, locked, or the origin is not allowed, the
site explains which step is missing.

The website client calls `http://localhost:7718` directly. The daemon never
advertises a LAN address to the site.

## Security model

### Pairing codes

- Six digits, generated with `crypto/rand`, valid for five minutes.
- Single-use. A second redemption fails even before the first token is used.
- Generating a new code invalidates the previous one.
- Twenty failed attempts in five minutes block pairing for five minutes.
- A code is only issued through the local API, so the request must carry the
  local API token and come from a loopback origin.

### Browser tokens

- 32 random bytes, base64url-encoded; 180-day lifetime.
- Stored only as a SHA-256 hash in `bridge-tokens.json` (mode 0600). The file
  never contains a usable token.
- Bound to the website origin that redeemed the code. Using the token from a
  different origin fails even if that origin is otherwise allowlisted.
- At most 32 paired browsers. Revoking one is immediate.
- `last_used_at` is persisted at most once per minute to avoid writing on
  every request.
- The local UI lists paired browsers and can revoke any of them. The website
  can revoke only the browser that is calling (`POST /api/v1/bridge/unpair`).

### Origins and CORS

The default website allowlist is `https://toget.vu`. A self-hosted deployment
can replace it with a comma-separated `TOGET_BRIDGE_ORIGINS` environment
variable, or `bridge_origins` in the config file. Entries must be absolute
`http`/`https` origins without user information. The bridge adds CORS headers,
`Vary: Origin`, and the Private Network Access preflight response only for
allowlisted origins. Other origins receive `403 origin_denied`.

Local routes use a separate origin allowlist built from the loopback listen
address: `127.0.0.1`, `localhost`, `[::1]`, `toget.localhost`, and
`toget.local` on the configured port. A website origin cannot request a
pairing code even though it can redeem one.

### What the bridge can do

Every website operation except pairing and probing requires a valid token and
an unlocked vault:

| Operation | Endpoint | Notes |
| --- | --- | --- |
| Machine summary | `GET /api/v1/bridge/session` | Works while locked; reports only state. |
| Machine summary | `GET /api/v1/bridge/machine` | Requires unlock. |
| Folder browser | `GET /api/v1/bridge/directory` | Requires unlock. |
| List Agents | `GET /api/v1/bridge/agents` | Includes revoked Agents for history. |
| Add Agent | `POST /api/v1/bridge/agents` | Name, runtime, permissions. |
| Edit Agent | `PATCH /api/v1/bridge/agents/{id}` | Does not touch presence or sessions. |
| Revoke Agent | `POST /api/v1/bridge/agents/{id}/revoke` | Removes it from rooms. |

The bridge intentionally has no endpoint for running a command, reading a
file, sending a message as a person, or changing the account password. Agent
execution stays on the local daemon, subject to the local runtime and policy
engine.

### Folder browser

- Restricted to `os.UserHomeDir()` after resolving symlinks.
- Only directories are returned. File names, file contents, sizes, and owners
  never leave the machine.
- Hidden directories (components beginning with `.`) are rejected, and hidden
  entries are omitted from listings.
- Entries that are symlinks escaping the home directory are skipped.
- At most 500 directories per listing; `truncated` is set when the cap is hit.
- Shortcuts are limited to conventional folders that exist (`Desktop`,
  `Documents`, `Downloads`, `Projects`, `Code`, `workspace`).

### Browser-side storage

The website stores the token in `localStorage` under
`toget.site.localBridge.v1`, together with the machine name and origin. A
script compromise on the website could read it, so the site must keep a strict
CSP and no third-party scripts. The token is origin-bound, cannot be used by
another site, and can be revoked from the local Machine page. The local UI
stores no website token.

## Local API

Local token or local UI cookie:

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/bridge/code` | Generate a pairing code. |
| `GET` | `/api/v1/bridge/browsers` | List paired browsers. |
| `DELETE` | `/api/v1/bridge/browsers/{id}` | Revoke one paired browser. |
| `GET` | `/api/v1/machine` | Machine summary for the local UI. |
| `GET` | `/api/v1/machine/directory` | Folder browser for the local UI. |
| `POST` | `/api/v1/agents/{id}/settings` | Edit name, runtime, permissions. |

Website token:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/bridge/info` | Probe support (no token). |
| `POST` | `/api/v1/bridge/pair` | Redeem a code (no token). |
| `POST` | `/api/v1/bridge/unpair` | Revoke the calling browser. |
| `GET` | `/api/v1/bridge/session` | Machine and lock state. |
| `GET` | `/api/v1/bridge/machine` | Machine summary. |
| `GET` | `/api/v1/bridge/directory` | Folder browser. |
| `GET` | `/api/v1/bridge/agents` | List Agents. |
| `POST` | `/api/v1/bridge/agents` | Add an Agent. |
| `PATCH` | `/api/v1/bridge/agents/{id}` | Edit an Agent. |
| `POST` | `/api/v1/bridge/agents/{id}/revoke` | Revoke an Agent. |

The request and response shapes are in `api/openapi.yaml`.

## Release downloads

`scripts/build-release.sh` builds the archives served at
`https://toget.vu/downloads`:

```bash
scripts/build-release.sh --out dist/downloads --version 0.1.0
```

It rebuilds both web UIs first (`--skip-web` reuses existing `dist/`
directories), then cross-compiles six statically linked targets:

```text
linux amd64, linux arm64
darwin amd64, darwin arm64
windows amd64, windows arm64
```

Each target produces:

```text
toget_<version>_<os>_<arch>.tar.gz     (zip for Windows)
toget_<version>_<os>_<arch>.*.sha256
latest                                  (version string)
```

The binary carries the release version, commit, and build time through
`-ldflags`.

`toget-official` serves the directory at `/downloads/`:

```bash
toget-official --host-download-dir /srv/toget/downloads ...
```

Only flat, visible files are served. Subdirectories, traversal, hidden files,
and symlinks that escape the directory are rejected. Archives and checksums
get one-year immutable caching; `latest` is `no-cache`. Shell and PowerShell
scripts are served with their own content types. The installers verify the
archive's SHA-256 before installing, so a truncated or altered download fails
closed.

To exercise the install path locally, build the release into an empty
directory, serve it with any static file server on a loopback origin, and run:

```bash
TOGET_BASE_URL=http://127.0.0.1:8080 TOGET_INSTALL_DIR=/tmp/toget-install \
  sh web/site/public/install.sh
```

The loopback exception in the installer exists for this test; public installs
must use HTTPS.

## Limits

- The bridge manages Agent definitions, not Agent processes. Running an Agent
  still requires the local daemon, a configured runtime, and the local policy
  checks.
- There is no remote terminal, file content viewer, or arbitrary path
  browser.
- Website access depends on the browser allowing a public HTTPS origin to call
  `http://localhost` (Private Network Access). Current Chrome, Edge, and Safari
  allow it after the preflight; some embedded browsers do not.
- There is no friends or 1:1 room UI yet.
