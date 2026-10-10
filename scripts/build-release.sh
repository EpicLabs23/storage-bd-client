#!/usr/bin/env bash
# Builds release tarballs for linux amd64/arm64:
#   dist/storagebd_<version>_linux_<arch>.tar.gz  (storagebd, pinned restic,
#     LICENSE.restic, install.sh, systemd units, README.txt, RELEASE,
#     SHA256SUMS of its own contents)
#   dist/SHA256SUMS                               (of the tarballs)
#
#   scripts/build-release.sh v0.1.0 [api-url]
#
# Reproducible: CGO off, -trimpath, file owner 0:0, mtimes = commit time.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: $0 <version> [api-url]}"
API_URL="${2:-https://api.storage.bd}"
case "$API_URL" in https://*) ;; *) echo "release API URL must be https: $API_URL" >&2; exit 1 ;; esac
# shellcheck source=/dev/null
. packaging/restic.lock
scripts/fetch-restic.sh amd64 arm64

if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  echo "warning: working tree is dirty; the release won't match a commit" >&2
fi
EPOCH="$(git log -1 --format=%ct 2>/dev/null || date +%s)"
PKG=storage-bd-client/internal/cli
LDFLAGS="-s -w -X $PKG.Version=$VERSION -X $PKG.DefaultAPIURL=$API_URL"

rm -rf dist/stage
mkdir -p dist/stage
for arch in amd64 arm64; do
  name="storagebd_${VERSION}_linux_${arch}"
  dir="dist/stage/$name"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$dir/storagebd" ./cmd/storagebd
  install -m 0755 "dist/restic/linux_${arch}/restic" "$dir/restic"
  install -m 0644 "dist/restic/linux_${arch}/LICENSE.restic" "$dir/LICENSE.restic"
  install -m 0755 packaging/install.sh "$dir/install.sh"
  install -m 0644 packaging/storagebd.service packaging/storagebd-user.service packaging/README.txt "$dir/"
  printf 'storagebd %s linux_%s\nrestic %s\napi %s\n' "$VERSION" "$arch" "$RESTIC_VERSION" "$API_URL" > "$dir/RELEASE"
  (cd "$dir" && sha256sum storagebd restic LICENSE.restic install.sh storagebd.service storagebd-user.service README.txt RELEASE > SHA256SUMS)
  find "$dir" -exec touch -h -d "@$EPOCH" {} +
  tar --sort=name --owner=0 --group=0 --numeric-owner --mode='u+rwX,go+rX,go-w' --mtime="@$EPOCH" -C dist/stage -czf "dist/$name.tar.gz" "$name"
  echo "built dist/$name.tar.gz"
done
(cd dist && sha256sum storagebd_"${VERSION}"_linux_*.tar.gz > SHA256SUMS)
cat dist/SHA256SUMS
