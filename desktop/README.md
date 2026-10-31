# Toget desktop shell (evaluation)

Status: **scaffold only — not built or tested on this machine.** The host has
no Rust toolchain and no WebKitGTK development packages, so the desktop shell
cannot be compiled here. This directory records the Tauri 2 evaluation and a
minimal starting point; it does not mean the desktop app is shipped.

## Decision

Use Tauri 2 for the desktop shell. It reuses the existing React web UI, uses
the operating system's WebView instead of bundling Chromium, and has native
tray and autostart support. The alternatives (Electron, a second native UI)
were rejected in [technology stack](../docs/tech-stack.md).

## Shape of the shell

The node remains the source of truth. The shell is not a second server:

1. It opens the node's local UI at `http://127.0.0.1:7718`.
2. It keeps a tray icon with Show and Quit.
3. It never injects a token or exposes a remote API without Tauri IPC, because
   the page uses the same localhost origin and the node's normal cookie.
4. A future iteration can start and stop the `toget` sidecar, add autostart,
   and show a native "node is not running" screen.

The scaffold loads the local node URL directly, so the `toget serve` process
must already be running. It does not bundle the Go binary yet. That packaging
step needs release artifacts and platform signing, both external to this host.

## Build requirements

- Rust 1.77.2 or newer, Cargo, and the Tauri CLI 2 (`cargo install tauri-cli --version "^2"`).
- Linux: `webkit2gtk-4.1`, `libappindicator3`, `librsvg2`, `patchelf` development packages.
- macOS: Xcode command-line tools; Windows: WebView2 runtime.
- Node 20+ and pnpm to build the web UI.

This host has neither `cargo`/`rustc` nor `webkit2gtk-4.1.pc`, so the scaffold
is intentionally unverified. Do not mark the roadmap item done until someone
builds it on a machine with those dependencies.

## Try it

```bash
# Start the node in another terminal.
cd ..
go run ./cmd/toget serve

# Build the web assets used by the packaged configuration.
pnpm --dir web/app build

# Build the desktop shell (requires the Rust and WebView dependencies above).
cd desktop/src-tauri
cargo tauri dev
```

`tauri.conf.json` points its window at the running node. The `frontendDist`
path exists so Tauri can validate the configuration, but the window uses the
node URL to keep cookies, CSRF origin checks, and Web Push on one origin.

## Remaining work

- Sidecar packaging of the Go binary with a platform-specific path.
- Native "starting node" and "node failed" states.
- Autostart and deep-link handling.
- Tray badge for pending approvals.
- Code signing and notarization.
- Real-machine tests on Linux, macOS, and Windows.
