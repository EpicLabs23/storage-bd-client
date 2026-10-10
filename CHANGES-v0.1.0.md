First public release of storagebd, the backup client for [storage.bd](https://storage.bd).

It backs up folders and MySQL/MariaDB/PostgreSQL databases on a Linux machine to storage.bd and restores them. You can use it from a private web page on the machine or from the terminal. It's free and open source under Apache-2.0.

## What's included

- **Encrypted on your machine.** Data is encrypted before it leaves, with a key generated on your machine. storage.bd keeps a separately protected recovery copy of that key.
- **Folders and databases.** Databases are dumped with their own tools (`mysqldump`/`mariadb-dump`, `pg_dump`) and streamed straight into the backup. A failed or cut-off dump never becomes a snapshot.
- **Incremental and deduplicated.** Only changed data is uploaded, and duplicate data is stored once.
- **Schedules and cleanup.** Run backups on a schedule and keep a set number of daily, weekly and monthly snapshots. Cleanup shows what it would remove before removing anything.
- **Browse and restore.** Pick files and folders from any snapshot and restore them to a folder or back in place. Databases restore in place or into a new database.
- **Private web page.** It listens only on the machine itself, behind a one-time login link (`storagebd ui-url`).
- **Runs as a service.** Installs a systemd service, system-wide or just for your user.

Built on [restic](https://restic.net) 0.19.1, which is bundled and checked against restic's signed checksums. Nothing else needs to be installed.

## Install

On Linux x86_64 or arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
sudo storagebd ui-url
```

Other install methods, including manual install from the tarballs below, are in [docs/INSTALL.md](https://github.com/EpicLabs23/storage-bd-client/blob/main/docs/INSTALL.md). Day-to-day use is covered in the [user guide](https://github.com/EpicLabs23/storage-bd-client/blob/main/docs/USER_GUIDE.md).

## Downloads

| File | For |
|---|---|
| `storagebd_v0.1.0_linux_amd64.tar.gz` | Linux x86_64 |
| `storagebd_v0.1.0_linux_arm64.tar.gz` | Linux arm64 |
| `SHA256SUMS` | Checksums for both tarballs. Check with `sha256sum -c SHA256SUMS --ignore-missing` |
