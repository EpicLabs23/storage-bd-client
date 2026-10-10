# storagebd: the storage.bd backup client

Back up folders and MySQL/MariaDB/PostgreSQL databases on your Linux machine to [storage.bd](https://storage.bd), and restore them when you need to. You can do everything from a private web page on the machine or from the terminal.

storagebd is free and open source (Apache-2.0). It builds on [restic](https://restic.net), which handles encryption, deduplication and incremental backups. A verified copy of restic is bundled, so there's nothing else to install.

- **Encrypted on your machine.** Data is encrypted before it leaves, with a key generated on your machine. storage.bd keeps a separately protected recovery copy of that key, so a lost server doesn't mean lost backups.
- **Folders and databases.** Databases are dumped with their own tools (`mysqldump`/`mariadb-dump`, `pg_dump`) and streamed straight into the backup. A failed or cut-off dump never becomes a snapshot.
- **Fast and small.** Only changed data is uploaded. Duplicate data is stored once.
- **Schedules and cleanup.** Back up daily or every few hours, and keep e.g. 7 daily, 4 weekly and 12 monthly backups. Cleanup shows you what it would remove before it removes anything.
- **Browse and restore.** Pick files and folders from any snapshot in the web page, then restore them to a folder or back in place. Databases restore in place or into a new database.
- **Private by design.** The web page only listens on the machine itself, behind a one-time login link.

## Install

On the machine you want to back up (Linux x86_64 or arm64):

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

The command downloads the latest release, checks it against the release's published checksums, installs it, and starts the backup service. Then open the backup page:

```sh
sudo storagebd ui-url
```

It prints a private link. Open it in a browser on that machine. On a remote server, open a tunnel first and use the link on your computer:

```sh
ssh -L 7878:127.0.0.1:7878 you@your-server
```

Other ways to install (just for your user, a specific version, manually, or from source) are in **[docs/INSTALL.md](docs/INSTALL.md)**.

## Get started

In the web page: **sign in** to your storage.bd account → **create a backup space** for this machine → **add a folder or database** → **Back up now**. Then set a schedule and a cleanup policy on the *Schedule & cleanup* page.

Or from the terminal:

```sh
sudo storagebd login                                    # your storage.bd account
sudo storagebd connect --storage-gb 20                  # a backup space for this machine
sudo storagebd source add-dir www /var/www
sudo storagebd source add-db shop --engine mysql --database shop --user backup
sudo storagebd backup
sudo storagebd retention set --keep-daily 7 --keep-weekly 4 --keep-monthly 12
```

**[docs/USER_GUIDE.md](docs/USER_GUIDE.md)** covers everything: databases, restoring, schedules, cleanup, the encryption password, moving to a new machine, and troubleshooting.

## Documentation

| | |
|---|---|
| [Installation](docs/INSTALL.md) | One-line, per-user, manual and source installs; upgrading; uninstalling |
| [User guide](docs/USER_GUIDE.md) | Day-to-day use, in the web page and the terminal |
| [Design](docs/DESIGN.md) | How it works inside, for contributors |
| [Releasing](docs/RELEASING.md) | For maintainers |

## Contributing and security

Bug reports and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). To report a security issue privately, follow [SECURITY.md](SECURITY.md). Please don't open a public issue.

## License

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). Release packages also contain restic (BSD 2-Clause) and the Go modules listed in `THIRD_PARTY_LICENSES.txt`. "storage.bd" is a trademark and isn't covered by the license, so forks must use a different name.
