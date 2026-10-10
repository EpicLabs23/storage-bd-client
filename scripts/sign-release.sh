#!/usr/bin/env bash
# Signs dist/SHA256SUMS with the release key, writing dist/SHA256SUMS.minisig
# with the trusted comment "storagebd <version>". `storagebd upgrade` checks
# both the signature (against internal/update/keys/*.pub, built into the
# binary) and that comment.
#
#   scripts/sign-release.sh v0.1.1
#
# Secret key: $STORAGEBD_SIGNING_KEY (default ~/.minisign/storagebd-release.key).
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "sign-release: $*" >&2; exit 1; }
VERSION="${1:?usage: $0 <version>}"
KEY="${STORAGEBD_SIGNING_KEY:-$HOME/.minisign/storagebd-release.key}"

command -v minisign >/dev/null 2>&1 || die "minisign is required (apt install minisign)"
[ -f "$KEY" ] || die "no secret key at $KEY (set STORAGEBD_SIGNING_KEY)"
[ -f dist/SHA256SUMS ] || die "no dist/SHA256SUMS; run: make release VERSION=$VERSION"
grep -q " storagebd_${VERSION}_linux_amd64.tar.gz\$" dist/SHA256SUMS ||
  die "dist/SHA256SUMS isn't for $VERSION; run: make release VERSION=$VERSION"
shopt -s nullglob
pubs=(internal/update/keys/*.pub)
[ ${#pubs[@]} -gt 0 ] || die "no public key in internal/update/keys/, so binaries from this commit can't verify upgrades"

rm -f dist/SHA256SUMS.minisig
minisign -S -s "$KEY" -m dist/SHA256SUMS -x dist/SHA256SUMS.minisig -t "storagebd $VERSION"

# The signature only helps if a key built into this commit's binaries checks it.
for pub in "${pubs[@]}"; do
  if out="$(minisign -V -p "$pub" -m dist/SHA256SUMS -x dist/SHA256SUMS.minisig 2>&1)"; then
    grep -qx "Trusted comment: storagebd $VERSION" <<<"$out" || die "unexpected trusted comment: $out"
    echo "signed dist/SHA256SUMS -> dist/SHA256SUMS.minisig (verified with $pub)"
    exit 0
  fi
done
rm -f dist/SHA256SUMS.minisig
die "$KEY doesn't match any key in internal/update/keys/"
