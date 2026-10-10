#!/usr/bin/env bash
# Pins a restic release for bundling: verifies the release's SHA256SUMS
# against restic's GPG signature (key fingerprint pinned below, as
# published in restic's installation docs), then writes the linux
# amd64/arm64 checksums and the LICENSE checksum to packaging/restic.lock.
#
#   scripts/update-restic-pin.sh 0.19.1
#
# Review the resulting lock-file diff like any other code change: it's what
# every release build trusts.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: $0 <restic version, e.g. 0.19.1>}"
VERSION="${VERSION#v}"
# https://restic.readthedocs.io/en/stable/020_installation.html
FINGERPRINT="CF8F18F2844575973F79D4E191A6868BD3F7A907"
KEY_URL="https://restic.net/gpg-key-alex.asc"
BASE="https://github.com/restic/restic/releases/download/v${VERSION}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export GNUPGHOME="$work/gnupg"
mkdir -m 700 "$GNUPGHOME"

curl -fsSL --proto '=https' "$KEY_URL" -o "$work/key.asc"
gpg --batch --quiet --import "$work/key.asc"
# The key file may carry several keys; the pinned one must be among the
# primary keys (a "pub" record's following "fpr" line).
if ! gpg --batch --with-colons --fingerprint | awk -F: '/^pub:/ {p=1; next} p && /^fpr:/ {print $10; p=0}' | grep -qx "$FINGERPRINT"; then
  echo "restic signing key $FINGERPRINT not found in $KEY_URL" >&2
  exit 1
fi

curl -fsSL --proto '=https' "$BASE/SHA256SUMS" -o "$work/SHA256SUMS"
curl -fsSL --proto '=https' "$BASE/SHA256SUMS.asc" -o "$work/SHA256SUMS.asc"
# VALIDSIG <signing-key-fpr> ... <primary-key-fpr>: accept the pinned key
# or one of its subkeys, nothing else from the imported file.
gpg --batch --status-fd 1 --verify "$work/SHA256SUMS.asc" "$work/SHA256SUMS" 2>/dev/null \
  | awk -v f="$FINGERPRINT" '$1 == "[GNUPG:]" && $2 == "VALIDSIG" && ($3 == f || $NF == f) {ok=1} END {exit !ok}' \
  || { echo "SHA256SUMS is not validly signed by $FINGERPRINT" >&2; exit 1; }
echo "SHA256SUMS signature OK (key $FINGERPRINT)"

sum_for() {
  awk -v f="restic_${VERSION}_linux_$1.bz2" '$2 == f || $2 == "*"f {print $1}' "$work/SHA256SUMS"
}
amd64="$(sum_for amd64)"; arm64="$(sum_for arm64)"
[ -n "$amd64" ] && [ -n "$arm64" ] || { echo "linux binaries not listed in SHA256SUMS" >&2; exit 1; }

# The LICENSE isn't covered by the signed SHA256SUMS; pin what the tag has now.
curl -fsSL --proto '=https' "https://raw.githubusercontent.com/restic/restic/v${VERSION}/LICENSE" -o "$work/LICENSE"
license="$(sha256sum "$work/LICENSE" | cut -d' ' -f1)"

cat > packaging/restic.lock <<LOCK
# restic release bundled with storagebd. Written by scripts/update-restic-pin.sh
# after verifying SHA256SUMS against restic's release signing key
# ($FINGERPRINT). Do not edit by hand.
RESTIC_VERSION=$VERSION
RESTIC_SHA256_linux_amd64=$amd64
RESTIC_SHA256_linux_arm64=$arm64
RESTIC_LICENSE_SHA256=$license
LOCK
echo "Pinned restic $VERSION:"
cat packaging/restic.lock
