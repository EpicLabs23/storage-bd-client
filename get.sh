#!/bin/sh
# storagebd one-line installer. Downloads the latest release from GitHub,
# verifies it against the release's SHA256SUMS, and runs its install.sh.
#
#   Server-wide (recommended):
#     curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
#   Just for your user:
#     curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sh -s -- --user
#   A specific version:
#     curl -fsSL .../get.sh | sudo STORAGEBD_VERSION=v0.1.0 sh
#
# Any arguments are passed to install.sh (--user, --no-start, --uninstall).
# Prefer to read it first? Download it, look, then run: sudo sh get.sh
set -eu

REPO="EpicLabs23/storage-bd-client"
BASE="${STORAGEBD_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download}"
VERSION="${STORAGEBD_VERSION:-}"

say() { printf '%s\n' "$*"; }
die() { printf 'storagebd install: %s\n' "$*" >&2; exit 1; }

case " $* " in
  *" --user "*) ;;
  *) [ "$(id -u)" = 0 ] || die "run with sudo for a server-wide install (curl ... | sudo sh), or add '-s -- --user' to install just for yourself" ;;
esac

[ "$(uname -s)" = Linux ] || die "storagebd runs on Linux only"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU $(uname -m) (supported: x86_64, arm64)" ;;
esac
for tool in tar sha256sum; do command -v "$tool" >/dev/null 2>&1 || die "'$tool' is required"; done
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
  latest() { curl -fsSLI --proto '=https' -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q --https-only -O "$2" "$1"; }
  latest() { wget -q --https-only --max-redirect=5 -S -O /dev/null "https://github.com/$REPO/releases/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -1; }
else
  die "curl or wget is required (e.g. apt install curl)"
fi
# Tests point BASE at a local server over plain http.
case "$BASE" in
  https://*) ;;
  http://127.0.0.1*|http://localhost*) fetch() { curl -fsSL -o "$2" "$1"; } ;;
  *) die "STORAGEBD_DOWNLOAD_BASE must be https" ;;
esac

if [ -z "$VERSION" ]; then
  url="$(latest)" || die "could not reach GitHub to find the latest release"
  VERSION="${url##*/}"
fi
case "$VERSION" in
  v[0-9]*) ;;
  [0-9]*) VERSION="v$VERSION" ;;
  *) die "could not determine the release version (got '$VERSION'); set STORAGEBD_VERSION=v0.1.0" ;;
esac
printf '%s' "$VERSION" | grep -Eq '^v[0-9A-Za-z.+-]+$' || die "invalid version '$VERSION'"

NAME="storagebd_${VERSION}_linux_${ARCH}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

say "Downloading storagebd $VERSION for linux/$ARCH..."
fetch "$BASE/$VERSION/$NAME.tar.gz" "$TMP/$NAME.tar.gz" || die "download failed: $BASE/$VERSION/$NAME.tar.gz"
fetch "$BASE/$VERSION/SHA256SUMS" "$TMP/SHA256SUMS" || die "download failed: SHA256SUMS"
expected="$(awk -v f="$NAME.tar.gz" '$2 == f || $2 == "*"f {print $1}' "$TMP/SHA256SUMS")"
[ -n "$expected" ] || die "$NAME.tar.gz is not listed in the release's SHA256SUMS"
(cd "$TMP" && printf '%s  %s\n' "$expected" "$NAME.tar.gz" | sha256sum -c --quiet -) || die "checksum mismatch; not installing"
say "Checksum OK."

tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
sh "$TMP/$NAME/install.sh" "$@"
