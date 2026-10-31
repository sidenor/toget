#!/usr/bin/env bash
# Publish a verified, append-only Toget release tree to a CDN or object store.
#
# The tree is verified with `toget-official release verify-tree` before any
# upload. Immutable prefixes are uploaded first, then manifests, channels, and
# finally the mutable release index, so a client never sees a pointer to a
# file that has not been uploaded yet. Nothing is deleted.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: publish-release.sh --tree DIR --target TARGET --root-key KEY [options]

Required:
  --tree DIR          Local release tree (channels/, manifests/, daemon/, sbom/)
  --target TARGET     rclone remote (for example r2:bucket/toget) or AWS S3 URL
  --root-key KEY      Root public key or key file used to verify signatures

Options:
  --backend NAME      rclone, aws, or auto (default: auto)
  --official PATH     toget-official binary (default: PATH or TOGET_OFFICIAL)
  --dry-run           Verify locally and print commands without uploading
  -h, --help          Show this help

Environment:
  TOGET_OFFICIAL          Default toget-official binary
  TOGET_RELEASE_ROOT_KEY  Default root public key or key file

The tree must not contain symlinks, unreferenced files, or private keys. The
script never deletes remote objects. Use the rclone backend for immutable
overwrite protection; the AWS backend warns because `aws s3 sync` can replace
an existing object.
EOF
}

die() {
	printf 'publish-release: %s\n' "$*" >&2
	exit 1
}

tree=""
target=""
root_key="${TOGET_RELEASE_ROOT_KEY:-}"
backend="auto"
official="${TOGET_OFFICIAL:-}"
dry_run=0

while [[ $# -gt 0 ]]; do
	case "$1" in
	--tree)
		[[ $# -ge 2 ]] || die "--tree requires a value"
		tree=$2
		shift 2
		;;
	--target)
		[[ $# -ge 2 ]] || die "--target requires a value"
		target=$2
		shift 2
		;;
	--root-key)
		[[ $# -ge 2 ]] || die "--root-key requires a value"
		root_key=$2
		shift 2
		;;
	--backend)
		[[ $# -ge 2 ]] || die "--backend requires a value"
		backend=$2
		shift 2
		;;
	--official)
		[[ $# -ge 2 ]] || die "--official requires a value"
		official=$2
		shift 2
		;;
	--dry-run)
		dry_run=1
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

[[ -n "$tree" ]] || die "--tree is required"
[[ -n "$target" ]] || die "--target is required"
[[ -n "$root_key" ]] || die "--root-key is required (or set TOGET_RELEASE_ROOT_KEY)"

[[ -d "$tree" ]] || die "release tree does not exist: $tree"
tree=$(cd "$tree" && pwd -P)
target="${target%/}"
[[ -n "$target" ]] || die "--target is empty"

if [[ -z "$official" ]]; then
	official=$(command -v toget-official 2>/dev/null || true)
fi
[[ -n "$official" && -x "$official" ]] || die "toget-official not found; pass --official or set TOGET_OFFICIAL"

case "$backend" in
auto)
	if command -v rclone >/dev/null 2>&1; then
		backend=rclone
	elif command -v aws >/dev/null 2>&1; then
		backend=aws
	elif ((dry_run)); then
		backend=rclone
	else
		die "neither rclone nor aws is installed; install one or pass --backend"
	fi
	;;
rclone | aws) ;;
*) die "--backend must be rclone, aws, or auto" ;;
esac

if ((dry_run == 0)); then
	command -v "$backend" >/dev/null 2>&1 || die "$backend is not installed"
fi

run() {
	if ((dry_run)); then
		printf '+'
		printf ' %q' "$@"
		printf '\n'
	else
		"$@"
	fi
}

printf 'Verifying release tree %s\n' "$tree"
"$official" release verify-tree --dir "$tree" --root-key "$root_key"

immutable_cache="public, max-age=31536000, immutable"
mutable_cache="public, max-age=60, must-revalidate"

publish_rclone() {
	if [[ -d "$tree/daemon" ]]; then
		run rclone copy "$tree/daemon" "$target/daemon" \
			--checksum --immutable --stats-one-line \
			--header-upload "Cache-Control: $immutable_cache"
	fi
	if [[ -d "$tree/sbom" ]]; then
		run rclone copy "$tree/sbom" "$target/sbom" \
			--checksum --immutable --stats-one-line \
			--header-upload "Cache-Control: $immutable_cache" \
			--header-upload "Content-Type: application/json"
	fi
	run rclone copy "$tree/manifests" "$target/manifests" \
		--checksum --immutable --stats-one-line \
		--exclude "index.json" --exclude "index.json.sig" \
		--header-upload "Cache-Control: $immutable_cache" \
		--header-upload "Content-Type: application/json"
	if [[ -d "$tree/channels" ]]; then
		run rclone copy "$tree/channels" "$target/channels" \
			--checksum --stats-one-line \
			--header-upload "Cache-Control: $mutable_cache" \
			--header-upload "Content-Type: application/json"
	fi
	run rclone copy "$tree/manifests" "$target/manifests" \
		--checksum --stats-one-line \
		--include "index.json" --include "index.json.sig" \
		--header-upload "Cache-Control: $mutable_cache" \
		--header-upload "Content-Type: application/json"
}

publish_aws() {
	printf 'warning: the AWS backend cannot enforce immutable objects; rclone --immutable is stronger\n' >&2
	run aws s3 sync "$tree/daemon" "$target/daemon" \
		--only-show-errors --cache-control "$immutable_cache"
	if [[ -d "$tree/sbom" ]]; then
		run aws s3 sync "$tree/sbom" "$target/sbom" \
			--only-show-errors --cache-control "$immutable_cache" \
			--content-type application/json
	fi
	run aws s3 sync "$tree/manifests" "$target/manifests" \
		--only-show-errors --cache-control "$immutable_cache" \
		--content-type application/json \
		--exclude "index.json" --exclude "index.json.sig"
	if [[ -d "$tree/channels" ]]; then
		run aws s3 sync "$tree/channels" "$target/channels" \
			--only-show-errors --cache-control "$mutable_cache" \
			--content-type application/json
	fi
	run aws s3 cp "$tree/manifests/index.json" "$target/manifests/index.json" \
		--only-show-errors --cache-control "$mutable_cache" \
		--content-type application/json
	if [[ -f "$tree/manifests/index.json.sig" ]]; then
		run aws s3 cp "$tree/manifests/index.json.sig" "$target/manifests/index.json.sig" \
			--only-show-errors --cache-control "$mutable_cache" \
			--content-type application/json
	fi
}

if [[ "$backend" == "rclone" ]]; then
	publish_rclone
else
	publish_aws
fi

if ((dry_run)); then
	printf 'Dry run: no files were uploaded.\n'
else
	printf 'Published %s to %s using %s. No remote files were deleted.\n' "$tree" "$target" "$backend"
fi
