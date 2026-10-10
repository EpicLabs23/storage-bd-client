#!/bin/sh
# storage.bd backup client installer. Run from an unpacked release:
#
#   sudo ./install.sh              server-wide: backs up anything on the machine
#   ./install.sh --user            just this user: backs up what you can read
#   sudo ./install.sh --uninstall  remove the program (keeps your data)
#   sudo ./install.sh --uninstall --purge
#                                  also delete /var/lib/storagebd: saved
#                                  credentials, encryption passwords, history
#
# Options: --no-start (install but don't enable/start the service)
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
MODE=system
ACTION=install
PURGE=0
START=1
for arg in "$@"; do
  case "$arg" in
    --user) MODE=user ;;
    --uninstall) ACTION=uninstall ;;
    --purge) PURGE=1 ;;
    --no-start) START=0 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg (see --help)" >&2; exit 2 ;;
  esac
done

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

if [ "$MODE" = system ]; then
  [ "$(id -u)" = 0 ] || die "a server-wide install needs root: sudo $0 $*  (or install for yourself only: $0 --user)"
  PREFIX=/usr/local
  UNIT_DIR=/etc/systemd/system
  UNIT_SRC=storagebd.service
  DATA=/var/lib/storagebd
  SYSTEMCTL="systemctl"
  UI_URL_CMD="sudo storagebd ui-url"
else
  [ "$(id -u)" != 0 ] || die "--user is for a regular user; as root, run without --user"
  PREFIX="$HOME/.local"
  UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  UNIT_SRC=storagebd-user.service
  DATA="${XDG_DATA_HOME:-$HOME/.local/share}/storagebd"
  SYSTEMCTL="systemctl --user"
  UI_URL_CMD="storagebd ui-url"
fi
BIN="$PREFIX/bin/storagebd"
LIBDIR="$PREFIX/lib/storagebd"
DOCDIR="$PREFIX/share/doc/storagebd"
UNIT="$UNIT_DIR/storagebd.service"

# systemd usable? (not in most containers, or for --user without a session)
have_systemd() {
  command -v systemctl >/dev/null 2>&1 || return 1
  if [ "$MODE" = system ]; then [ -d /run/systemd/system ]; else systemctl --user show-environment >/dev/null 2>&1; fi
}

if [ "$ACTION" = uninstall ]; then
  if have_systemd; then
    $SYSTEMCTL disable --now storagebd.service >/dev/null 2>&1 || true
  fi
  rm -f "$UNIT" "$BIN" "$LIBDIR/restic"
  rmdir "$LIBDIR" 2>/dev/null || true
  rm -rf "$DOCDIR"
  have_systemd && $SYSTEMCTL daemon-reload || true
  say "storagebd removed."
  if [ "$PURGE" = 1 ]; then
    if [ -d "$DATA" ]; then
      say ""
      say "--purge deletes $DATA, including each backup space's encryption password."
      say "Backups whose password was never saved with storage.bd become unreadable."
      if [ -t 0 ]; then
        printf 'Type PURGE to delete it: '
        read -r answer
        [ "$answer" = PURGE ] || die "not purged"
      else
        die "refusing to purge without a terminal to confirm; delete $DATA yourself if you're sure"
      fi
      rm -rf "$DATA"
      say "Deleted $DATA."
    fi
  else
    [ -d "$DATA" ] && say "Your settings and credentials are still in $DATA (remove with --uninstall --purge)."
  fi
  exit 0
fi

# ---- install ----
for f in storagebd restic "$UNIT_SRC"; do
  [ -f "$HERE/$f" ] || die "$f is missing next to install.sh; run this from an unpacked storagebd release"
done
if [ -f "$HERE/SHA256SUMS" ]; then
  (cd "$HERE" && sha256sum -c --quiet SHA256SUMS) || die "release files don't match SHA256SUMS; re-download the release"
fi
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m); storagebd ships for linux amd64 and arm64" ;;
esac
grep -q "linux_$arch" "$HERE/RELEASE" 2>/dev/null || say "warning: this release may not match this machine ($arch)"

# Root-owned and not writable by anyone else: storagebd refuses to run a
# restic (or itself be trusted) from a location another user could modify.
if [ "$MODE" = system ]; then OWN="-o root -g root"; else OWN=""; fi
# shellcheck disable=SC2086
install -d -m 0755 $OWN "$PREFIX/bin" "$LIBDIR" "$DOCDIR" "$UNIT_DIR"
# shellcheck disable=SC2086
install -m 0755 $OWN "$HERE/storagebd" "$BIN.new" && mv -f "$BIN.new" "$BIN"
# shellcheck disable=SC2086
install -m 0755 $OWN "$HERE/restic" "$LIBDIR/restic.new" && mv -f "$LIBDIR/restic.new" "$LIBDIR/restic"
for doc in LICENSE.restic README.txt; do
  # shellcheck disable=SC2086
  [ -f "$HERE/$doc" ] && install -m 0644 $OWN "$HERE/$doc" "$DOCDIR/$doc"
done
# shellcheck disable=SC2086
install -m 0644 $OWN "$HERE/$UNIT_SRC" "$UNIT"
install -d -m 0700 "$DATA"
chmod 0700 "$DATA"

say "Installed $("$BIN" version | head -1)"
"$BIN" version | sed -n '2p'

if [ "$START" = 1 ] && have_systemd; then
  $SYSTEMCTL daemon-reload
  if $SYSTEMCTL is-active --quiet storagebd.service; then
    $SYSTEMCTL restart storagebd.service
  else
    $SYSTEMCTL enable --now storagebd.service
  fi
  # Give it a moment to write its login link.
  i=0
  while [ $i -lt 20 ] && [ ! -f "$DATA/serve.json" ]; do sleep 0.25; i=$((i+1)); done
  say ""
  say "storagebd is running. Open the backup panel with the link from:"
  say "  $UI_URL_CMD"
  [ "$MODE" = user ] && say "To keep backups running while you're logged out: loginctl enable-linger $(id -un)"
else
  say ""
  if [ "$START" = 1 ]; then say "systemd isn't available here, so nothing was started."; fi
  say "Start the backup panel and scheduler with:"
  if [ "$MODE" = system ]; then say "  sudo STORAGEBD_HOME=$DATA $BIN serve"; else say "  $BIN serve"; fi
fi
case ":$PATH:" in *":$PREFIX/bin:"*) ;; *) say "Note: $PREFIX/bin is not on your PATH." ;; esac
say "Get started from the terminal instead: storagebd login && storagebd connect --storage-gb 10"
