#!/usr/bin/env bash
# Build the downloadable Toget node archives served at https://toget.vu/downloads.
#
# Usage: scripts/build-release.sh --out DIR [--version VERSION] [--skip-web]
#
# Writes one archive and one .sha256 file per supported platform, plus a
# `latest` pointer. The web UIs are rebuilt first so the embedded assets match
# the source tree. Binaries are statically linked (CGO disabled) and include
# the release version in `toget version`.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: build-release.sh --out DIR [options]

Required:
  --out DIR           Directory that receives archives, checksums, and latest

Options:
  --version VERSION   Release version (default: 0.1.0-dev.1)
  --skip-web          Reuse the existing web/app/dist and web/site/dist
  -h, --help          Show this help

Environment:
  TOGET_RELEASE_VERSION   Default version when --version is omitted
EOF
}

die() {
	printf 'build-release: %s\n' "$*" >&2
	exit 1
}

out=""
version="${TOGET_RELEASE_VERSION:-0.1.0-dev.1}"
skip_web=0

while [[ $# -gt 0 ]]; do
	case "$1" in
	--out)
		[[ $# -ge 2 ]] || die "--out requires a value"
		out=$2
		shift 2
		;;
	--version)
		[[ $# -ge 2 ]] || die "--version requires a value"
		version=$2
		shift 2
		;;
	--skip-web)
		skip_web=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
done

[[ -n "$out" ]] || die "--out is required"
[[ "$version" =~ ^[0-9A-Za-z._-]+$ ]] || die "invalid version: $version"

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo"

if ((skip_web == 0)); then
	command -v pnpm >/dev/null 2>&1 || die "pnpm is required to build the embedded web UIs (or pass --skip-web)"
	pnpm --dir web/app build
	pnpm --dir web/site build
fi
[[ -f web/app/dist/index.html ]] || die "web/app/dist is missing; run without --skip-web"
[[ -f web/site/dist/index.html ]] || die "web/site/dist is missing; run without --skip-web"

commit=$(git rev-parse --short=12 HEAD 2>/dev/null || printf 'dev')
build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ldflags="-s -w"
ldflags+=" -X github.com/sidenor/toget/internal/version.App=$version"
ldflags+=" -X github.com/sidenor/toget/internal/version.Commit=$commit"
ldflags+=" -X github.com/sidenor/toget/internal/version.BuildTime=$build_time"

mkdir -p "$out"
out=$(cd "$out" && pwd -P)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/toget-release.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

targets=(
	"linux amd64 tar.gz"
	"linux arm64 tar.gz"
	"darwin amd64 tar.gz"
	"darwin arm64 tar.gz"
	"windows amd64 zip"
	"windows arm64 zip"
)

for target in "${targets[@]}"; do
	read -r os arch format <<<"$target"
	stage="$tmp/$os-$arch"
	mkdir -p "$stage"
	binary="toget"
	[[ "$os" == "windows" ]] && binary="toget.exe"

	printf 'Building %s/%s...\n' "$os" "$arch"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
		-ldflags "$ldflags" -o "$stage/$binary" ./cmd/toget
	cp LICENSE "$stage/LICENSE"

	name="toget_${version}_${os}_${arch}.${format}"
	if [[ "$format" == "zip" ]]; then
		command -v zip >/dev/null 2>&1 || die "zip is required for Windows archives"
		(cd "$stage" && zip -q -9 "$tmp/$name" "$binary" LICENSE)
	else
		tar -czf "$tmp/$name" -C "$stage" "$binary" LICENSE
	fi
	mv "$tmp/$name" "$out/$name"
	(cd "$out" && sha256sum "$name" >"$name.sha256")
done

printf '%s\n' "$version" >"$out/latest"
printf 'Wrote release %s to %s\n' "$version" "$out"
ls -lh "$out"
