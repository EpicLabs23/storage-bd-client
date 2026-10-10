#!/usr/bin/env bash
# Downloads the pinned restic (packaging/restic.lock) for the given linux
# architectures, verifies each download against the pinned SHA256, and
# unpacks it to dist/restic/linux_<arch>/restic. Downloads are cached in
# dist/cache, re-verified on every run.
#
#   scripts/fetch-restic.sh amd64 arm64
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=/dev/null
. packaging/restic.lock
[ $# -gt 0 ] || set -- amd64 arm64

mkdir -p dist/cache
fetch() { # url dest sha256
  if [ ! -f "$2" ] || ! echo "$3  $2" | sha256sum -c --status; then
    curl -fsSL --proto '=https' "$1" -o "$2.part"
    mv "$2.part" "$2"
  fi
  echo "$3  $2" | sha256sum -c --status || { echo "checksum mismatch for $2; refusing to use it" >&2; rm -f "$2"; exit 1; }
}

lic="dist/cache/restic_${RESTIC_VERSION}_LICENSE"
fetch "https://raw.githubusercontent.com/restic/restic/v${RESTIC_VERSION}/LICENSE" "$lic" "$RESTIC_LICENSE_SHA256"

for arch in "$@"; do
  var="RESTIC_SHA256_linux_${arch}"
  sum="${!var:?no pinned checksum for linux_$arch}"
  bz="dist/cache/restic_${RESTIC_VERSION}_linux_${arch}.bz2"
  fetch "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${arch}.bz2" "$bz" "$sum"
  out="dist/restic/linux_${arch}"
  mkdir -p "$out"
  # storagebd refuses a restic in a directory other users can write to,
  # so don't leave these to the umask.
  chmod 0755 dist/restic "$out"
  bunzip2 -c "$bz" > "$out/restic.part"
  chmod 0755 "$out/restic.part"
  mv "$out/restic.part" "$out/restic"
  cp "$lic" "$out/LICENSE.restic"
  echo "restic $RESTIC_VERSION linux/$arch -> $out/restic (verified)"
done
