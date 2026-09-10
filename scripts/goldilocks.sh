#!/bin/sh
# Download once per version/platform; keep hook stdin and stdout untouched.
set -eu
PLUGIN_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export PLUGIN_ROOT
fail() { printf 'goldilocks launcher: %s\n' "$*" >&2; exit 1; }
version=$(sed -nE 's/.*"version"[[:space:]]*:[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)".*/\1/p' "$PLUGIN_ROOT/.codex-plugin/plugin.json")
[ -n "$version" ] || fail 'missing or invalid plugin version'
platform=$(uname -s)-$(uname -m)
case "$platform" in
    Darwin-arm64|Darwin-x86_64|Linux-aarch64|Linux-x86_64) ;;
    *) fail "unsupported platform: $platform" ;;
esac
asset=goldilocks-$platform
expected=$(awk -v name="$asset" '$2 == name {print $1}' "$PLUGIN_ROOT/scripts/SHA256SUMS")
[ "${#expected}" = 64 ] || fail "missing or duplicate checksum for $asset"
case "$expected" in *[!0-9a-f]*) fail "invalid checksum for $asset" ;; esac
cache=${PLUGIN_DATA:-${XDG_CACHE_HOME:-$HOME/.cache}/goldilocks}/bin/$version/$platform
binary=$cache/goldilocks
if command -v sha256sum >/dev/null 2>&1; then
    digest() { sha256sum "$1" | awk '{print $1}'; }
else
    digest() { shasum -a 256 "$1" | awk '{print $1}'; }
fi
valid() { [ -x "$binary" ] && [ "$(digest "$binary")" = "$expected" ]; }
if ! valid; then
    (
        umask 077
        mkdir -p "$cache"
        lock=$cache/download.lock
        waited=0
        # ponytail: portable mkdir lock; after SIGKILL, remove a stale lock only
        # after confirming no downloader is active. Normal exits clean it up.
        until mkdir "$lock" 2>/dev/null; do
            valid && exit 0
            [ "$waited" -lt 75 ] || fail "download lock timed out: $lock (check for a stale downloader)"
            sleep 1
            waited=$((waited + 1))
        done
        trap 'rm -rf "$lock"' EXIT
        trap 'exit 130' INT
        trap 'exit 143' HUP TERM
        valid && exit 0
        printf 'goldilocks: downloading %s for %s\n' "$version" "$platform" >&2
        curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' \
            --connect-timeout 10 --max-time 60 --output "$lock/binary" \
            "https://github.com/baranwang/goldilocks/releases/download/v$version/$asset" \
            || fail "download failed for v$version/$asset; retry when the release and network are available"
        [ "$(digest "$lock/binary")" = "$expected" ] || fail "SHA-256 mismatch for $asset"
        chmod 700 "$lock/binary"
        mv -f "$lock/binary" "$binary"
    )
fi
exec "$binary" "$@"
