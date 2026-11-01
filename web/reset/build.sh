#!/usr/bin/env bash
# Build the static reset page served at /reset/ by the official account host.
set -euo pipefail

RESET_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${RESET_DIR}/../.." && pwd)"
DIST="${RESET_DIR}/dist"

GO_BIN="${GO:-go}"
GOROOT="$("${GO_BIN}" env GOROOT)"

rm -rf "${DIST}"
mkdir -p "${DIST}"
cp "${RESET_DIR}/index.html" "${RESET_DIR}/main.js" \
  "${RESET_DIR}/style.css" "${RESET_DIR}/favicon.svg" "${DIST}/"
cp "${GOROOT}/lib/wasm/wasm_exec.js" "${DIST}/wasm_exec.js"

cd "${ROOT}"
GOOS=js GOARCH=wasm "${GO_BIN}" build -trimpath -ldflags="-s -w" \
  -o "${DIST}/toget-reset.wasm" ./web/reset/wasm

printf 'built %s (%s)\n' "${DIST}/toget-reset.wasm" \
  "$(du -h "${DIST}/toget-reset.wasm" | cut -f1)"
