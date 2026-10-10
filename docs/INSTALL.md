# Installing storagebd

storagebd runs on **Linux, x86_64 (amd64) or arm64**. It needs `curl` (or `wget`), `tar` and `sha256sum`, which most distributions already have. systemd is optional; it's used to start the backup service at boot.

To back up databases, the database's own client tools must be on the machine. Database servers normally have them already:

| Database | Needed for backup | Needed for restore |
|---|---|---|
| MySQL / MariaDB | `mysqldump` or `mariadb-dump` | `mysql` or `mariadb` |
| PostgreSQL | `pg_dump` | `psql` |

Everything else, including restic, comes in the package.

- [Quick install (recommended)](#quick-install-recommended)
- [Install for one user only](#install-for-one-user-only)
- [Install a specific version](#install-a-specific-version)
- [Manual install](#manual-install)
- [Build from source](#build-from-source)
- [Open the backup page](#open-the-backup-page)
- [Upgrade](#upgrade)
- [Uninstall](#uninstall)
- [What goes where](#what-goes-where)
- [Troubleshooting the install](#troubleshooting-the-install)

## Quick install (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

This:

1. finds the latest release on GitHub and downloads the package for your CPU;
2. checks it against the release's `SHA256SUMS`, and stops if anything doesn't match;
3. installs `storagebd` and its bundled restic under `/usr/local`, owned by root;
4. installs and starts the `storagebd` systemd service (the backup page and the scheduler).

A server-wide install runs as root, so it can back up any file on the machine and restore anywhere. This is what you want on a server.

> Prefer to read a script before running it? Download it, read it, then run it:
> ```sh
> curl -fsSLO https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh
> less get.sh
> sudo sh get.sh
> ```

## Install for one user only

No root needed. storagebd can then back up only what your user can read:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sh -s -- --user
```

It installs into `~/.local/bin`, `~/.local/lib/storagebd` and a systemd *user* service. Make sure `~/.local/bin` is on your `PATH`. Scheduled backups need the service to keep running after you log out:

```sh
loginctl enable-linger "$USER"
```

## Install a specific version

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo STORAGEBD_VERSION=v0.1.0 sh
```

## Manual install

1. Download `storagebd_<version>_linux_<arch>.tar.gz` and `SHA256SUMS` from the [releases page](https://github.com/EpicLabs23/storage-bd-client/releases).
2. Verify and unpack:
   ```sh
   sha256sum -c --ignore-missing SHA256SUMS
   tar xzf storagebd_v0.1.0_linux_amd64.tar.gz
   cd storagebd_v0.1.0_linux_amd64
   ```
3. Install:
   ```sh
   sudo ./install.sh          # server-wide
   ./install.sh --user        # or just for your user
   ```

`install.sh` checks the package's own `SHA256SUMS` again before installing anything. Use `--no-start` to install without enabling the service.

## Build from source

You need Go 1.26 or newer, `make`, `curl` and `bunzip2`.

```sh
git clone https://github.com/EpicLabs23/storage-bd-client.git
cd storage-bd-client
make release VERSION=v0.0.0-local
```

This downloads the restic version pinned in `packaging/restic.lock`, refuses it unless its checksum matches, builds static binaries, and writes installable packages to `dist/`. Install one with the manual steps above.

For just the `storagebd` binary: `make build` gives `bin/storagebd`. You then need restic yourself; point storagebd at it with `storagebd settings set restic-path /usr/bin/restic` (restic 0.14 or newer).

## Open the backup page

The service prints a private login link, which changes every time it starts. Get the current one with:

```sh
sudo storagebd ui-url        # server-wide install
storagebd ui-url             # --user install
```

The page only listens on `127.0.0.1:7878`, so nothing on the network can reach it. On a remote server, forward the port over SSH and open the link on your own computer:

```sh
ssh -L 7878:127.0.0.1:7878 you@your-server
```

No systemd (for example in a container)? Start it yourself with `sudo storagebd serve`. It prints the link when it starts.

## Upgrade

Run the install command again. It installs the new version and restarts the service. Your settings, credentials and history stay where they are (`/var/lib/storagebd`), and upgrades update the database format automatically.

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

If a backup is running, the service stops it cleanly first. Run it again afterwards, or let the schedule do it.

## Uninstall

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh -s -- --uninstall
```

This removes the program and the service, and **keeps** `/var/lib/storagebd`. That folder holds your backup credentials and **the encryption password of each backup space**.

To delete that folder too, run `install.sh --uninstall --purge` from an unpacked release, in a terminal. It asks you to type `PURGE`. Before you do, check that each backup space's password is saved with storage.bd ("Recovery copy saved" in *Settings*, or `storagebd credential list`) or somewhere safe. Without it, those backups can't be decrypted.

Your backups on storage.bd are never deleted by uninstalling.

## What goes where

| | Server-wide | `--user` |
|---|---|---|
| Program | `/usr/local/bin/storagebd` | `~/.local/bin/storagebd` |
| Bundled restic | `/usr/local/lib/storagebd/restic` | `~/.local/lib/storagebd/restic` |
| Service | `/etc/systemd/system/storagebd.service` | `~/.config/systemd/user/storagebd.service` |
| Settings, credentials, history | `/var/lib/storagebd` (mode 0700) | `~/.local/share/storagebd` (mode 0700) |
| Licenses | `/usr/local/share/doc/storagebd/` | `~/.local/share/doc/storagebd/` |
| Logs | `journalctl -u storagebd` | `journalctl --user -u storagebd` |

## Troubleshooting the install

**"run with sudo for a server-wide install"**: add `sudo` in front of `sh`, or install just for yourself with `sh -s -- --user`.

**"checksum mismatch; not installing"**: the download was corrupted or altered. Try again. If it keeps happening, please [report it](../SECURITY.md).

**"unsupported CPU"**: storagebd is published for x86_64 and arm64 only.

**`systemctl status storagebd` shows it failing**: look at `journalctl -u storagebd -n 50`. The most common cause is a data folder other users can access. storagebd refuses to run until it's private: `sudo chmod 700 /var/lib/storagebd`.

**"restic binary not found"** (source builds only): install restic 0.14+ and run `storagebd settings set restic-path /usr/bin/restic`.
