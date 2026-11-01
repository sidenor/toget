#!/bin/sh
# Install the Toget local node on macOS or Linux.
#
#   curl -fsSL https://toget.vu/install.sh | sh
#
# Environment:
#   TOGET_VERSION       install a specific version instead of the latest
#   TOGET_INSTALL_DIR   directory for the toget binary
#   TOGET_BASE_URL      download origin (default: https://toget.vu)
#
# The archive is verified against its SHA-256 checksum before anything is
# installed. Nothing is written outside the install directory.

set -eu

BASE_URL="${TOGET_BASE_URL:-https://toget.vu}"
VERSION="${TOGET_VERSION:-}"
INSTALL_DIR="${TOGET_INSTALL_DIR:-}"

say() {
	printf '%s\n' "$*"
}

die() {
	printf 'toget install: %s\n' "$*" >&2
	exit 1
}

case "$BASE_URL" in
https://*) ;;
http://localhost | http://localhost:* | http://localhost/* | http://127.0.0.1 | http://127.0.0.1:* | http://127.0.0.1/* | "http://[::1]" | "http://[::1]:"* | "http://[::1]"/*) ;;
*) die "TOGET_BASE_URL must use https (loopback http is allowed for testing)" ;;
esac
BASE_URL="${BASE_URL%/}"

os=$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]') || die "cannot detect the operating system"
case "$os" in
linux | darwin) ;;
mingw* | msys* | cygwin*)
	die "on Windows, run: powershell -c \"irm https://toget.vu/install.ps1 | iex\""
	;;
*) die "unsupported operating system: $os (supported: macOS, Linux)" ;;
esac

machine=$(uname -m 2>/dev/null) || die "cannot detect the processor architecture"
case "$machine" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported processor architecture: $machine (supported: x86_64, arm64)" ;;
esac

tmp=$(mktemp -d "${TMPDIR:-/tmp}/toget-install.XXXXXX") || die "cannot create a temporary directory"
cleanup() {
	rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM

download() {
	url=$1
	dest=$2
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 15 -o "$dest" "$url" ||
			die "could not download $url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$dest" "$url" || die "could not download $url"
	else
		die "curl or wget is required"
	fi
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		die "no SHA-256 tool found (need sha256sum, shasum, or openssl)"
	fi
}

if [ -z "$VERSION" ]; then
	download "$BASE_URL/downloads/latest" "$tmp/latest"
	VERSION=$(tr -d ' \t\r\n' <"$tmp/latest")
	[ -n "$VERSION" ] || die "the release server did not return a version"
fi
case "$VERSION" in
*[!0-9A-Za-z._-]*) die "invalid version: $VERSION" ;;
esac

artifact="toget_${VERSION}_${os}_${arch}.tar.gz"
archive="$tmp/$artifact"
say "Downloading Toget $VERSION for $os/$arch..."
download "$BASE_URL/downloads/$artifact" "$archive"
download "$BASE_URL/downloads/$artifact.sha256" "$tmp/checksum"
expected=$(awk 'NR == 1 {print $1}' "$tmp/checksum")
case "$expected" in
*[!0-9a-fA-F]* | "") die "the release server returned a malformed checksum" ;;
esac
[ "${#expected}" -eq 64 ] || die "the release server returned a malformed checksum"
actual=$(sha256_file "$archive")
if [ "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" != "$actual" ]; then
	die "checksum mismatch for $artifact; the download may be incomplete or altered"
fi

mkdir "$tmp/unpack"
tar -xzf "$archive" -C "$tmp/unpack" || die "could not extract $artifact"
[ -f "$tmp/unpack/toget" ] || die "the archive does not contain the toget binary"

if [ -z "$INSTALL_DIR" ]; then
	if [ "$(id -u)" -eq 0 ]; then
		INSTALL_DIR=/usr/local/bin
	elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		INSTALL_DIR=/usr/local/bin
	else
		INSTALL_DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR"
cp "$tmp/unpack/toget" "$INSTALL_DIR/.toget.new.$$" || die "could not write to $INSTALL_DIR"
chmod 0755 "$INSTALL_DIR/.toget.new.$$"
mv -f "$INSTALL_DIR/.toget.new.$$" "$INSTALL_DIR/toget" || die "could not install to $INSTALL_DIR"
if [ "$os" = darwin ] && command -v xattr >/dev/null 2>&1; then
	xattr -d com.apple.quarantine "$INSTALL_DIR/toget" 2>/dev/null || true
fi

"$INSTALL_DIR/toget" version >/dev/null 2>&1 ||
	die "the installed binary could not run; check that $INSTALL_DIR is writable and executable"

say ""
say "Toget $VERSION is installed at $INSTALL_DIR/toget"
case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	say ""
	say "$INSTALL_DIR is not on your PATH yet. Add it to your shell profile:"
	say "  export PATH=\"$INSTALL_DIR:\$PATH\""
	say "Then reopen the terminal (or run the line above in this one)."
	;;
esac
say ""
say "Start it:"
say "  toget serve"
say ""
say "The local page opens at http://toget.localhost:7718"
