#!/bin/sh
# Install acurl, a free, open-source tool by tragent.io (https://acurl.dev).
#
#   curl -fsSL https://acurl.dev/install.sh | sh
#   curl -fsSL https://acurl.dev/install.sh | sh -s -- --version v1.0.0 --dir ~/.local/bin
#
# Downloads the release archive for this OS and CPU from GitHub, verifies its
# SHA-256 checksum and installs the binary.
set -eu

REPO="tragent-io/acurl"
VERSION="latest"
DIR=""

usage() {
  cat <<'USAGE'
Install acurl, a free, open-source tool by tragent.io (https://acurl.dev).

  curl -fsSL https://acurl.dev/install.sh | sh
  curl -fsSL https://acurl.dev/install.sh | sh -s -- --version v1.1.0 --dir ~/.local/bin

Options:
  --version <tag>   release to install (default: the latest)
  --dir <path>      where to put the binary (default: /usr/local/bin if writable, else ~/.local/bin)
  -h, --help        show this help
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --dir) DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install.sh: unknown option $1" >&2; exit 2 ;;
  esac
done

say() { printf '%s\n' "$*" >&2; }
fail() { say "install.sh: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }

need curl
need tar
need uname

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS $(uname -s); download a binary from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m)" ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$VERSION" ] || fail "could not find the latest release"
fi
num=${VERSION#v}
archive="acurl_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading acurl $VERSION for $os/$arch..."
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "download failed: $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "could not download checksums.txt"

expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" acurl

if [ -z "$DIR" ]; then
  if [ -w /usr/local/bin ]; then DIR=/usr/local/bin; else DIR="$HOME/.local/bin"; fi
fi
mkdir -p "$DIR"
install -m 0755 "$tmp/acurl" "$DIR/acurl" 2>/dev/null || { cp "$tmp/acurl" "$DIR/acurl" && chmod 0755 "$DIR/acurl"; }

say "Installed acurl $VERSION to $DIR/acurl"
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) say "Add $DIR to your PATH:  export PATH=\"$DIR:\$PATH\"" ;;
esac
say "Get started:  acurl https://example.com   ·   docs: https://acurl.dev/docs/"
