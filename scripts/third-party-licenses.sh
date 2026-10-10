#!/usr/bin/env bash
# Writes the license texts of every Go module linked into storagebd to
# the given file (default dist/THIRD_PARTY_LICENSES.txt).
set -euo pipefail
cd "$(dirname "$0")/.."
out="${1:-dist/THIRD_PARTY_LICENSES.txt}"
mkdir -p "$(dirname "$out")"
{
  echo "storagebd includes the following third-party Go modules."
  echo "Their license texts follow, as shipped in each module."
  printf '\n================================================================\n%s %s\n================================================================\n\n' "Go standard library" "$(go env GOVERSION)"
  cat "$(go env GOROOT)/LICENSE"
  go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/storagebd | sort -u |
  while read -r path version dir; do
    lic="$(find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'LICENCE*' \) | sort | head -1)"
    [ -n "$lic" ] || { echo "no license file found for $path" >&2; exit 1; }
    printf '\n================================================================\n%s %s\n================================================================\n\n' "$path" "$version"
    cat "$lic"
  done
} > "$out"
echo "wrote $out ($(grep -c '^====' "$out" | awk '{print $1/2}') modules)"
