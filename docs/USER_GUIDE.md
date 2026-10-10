# storagebd user guide

This guide assumes storagebd is installed ([INSTALL.md](INSTALL.md)). Everything here can be done in the **web page** or in the **terminal**; both use the same settings, so you can mix them freely.

The terminal examples use `sudo`, as for a server-wide install. On a `--user` install, drop the `sudo`.

- [How it fits together](#how-it-fits-together)
- [1. Connect this machine](#1-connect-this-machine)
- [2. Choose what to back up](#2-choose-what-to-back-up)
  - [Databases: setting up a backup user](#databases-setting-up-a-backup-user)
- [3. Back up](#3-back-up)
- [4. Schedule backups](#4-schedule-backups)
- [5. Keep old backups under control](#5-keep-old-backups-under-control)
- [6. Restore files](#6-restore-files)
- [7. Restore a database](#7-restore-a-database)
- [Activity and history](#activity-and-history)
- [Your encryption password](#your-encryption-password)
- [Moving to a new machine, or losing one](#moving-to-a-new-machine-or-losing-one)
- [More than one backup space](#more-than-one-backup-space)
- [Managing credentials from the terminal](#managing-credentials-from-the-terminal)
- [Settings](#settings)
- [Security notes](#security-notes)
- [Troubleshooting](#troubleshooting)
- [Command reference](#command-reference)

## How it fits together

| Term | Meaning |
|---|---|
| **storage.bd account** | Your login at storage.bd. It's used to create and pay for backup spaces. Backups themselves don't need you to stay signed in. |
| **Backup space** | One encrypted backup repository on storage.bd, normally one per machine. It's sized and billed in GB per month. On the machine it's stored as a **credential** with a short name, for example `web1`. |
| **Source** | Something to back up: a folder, or a MySQL/MariaDB/PostgreSQL database. |
| **Snapshot** | One saved copy of one source at one point in time. Every backup run makes one snapshot per source. |
| **Schedule** | When backups run automatically. |
| **Cleanup policy** | How many old snapshots to keep. |

Under the hood, storagebd runs [restic](https://restic.net). Only data that changed since the last backup is uploaded, and identical data is stored once.

## 1. Connect this machine

**Web page:** open the link from `sudo storagebd ui-url`, sign in with your storage.bd email and password, then fill in **Create a backup space for this machine**:

- **Name on this machine**: a short name used in commands (default: the hostname).
- **Label**: how the space appears in your storage.bd dashboard.
- **Storage size (GB)**: what you pay for each month. The page shows the monthly price as you type.
- **Activate now**: charges the first month from your storage.bd balance. If your balance is too low, the space is still created, and you can activate it after topping up (the *Overview* page shows an **Activate** button).

**Terminal:**

```sh
sudo storagebd login                        # asks for your email and password
sudo storagebd connect --storage-gb 20      # asks you to confirm the charge
sudo storagebd account                      # balance and backup spaces
```

`connect` takes `--name`, `--label` and `--no-activate`; activate later with `sudo storagebd activate`.

No storage.bd account yet? Sign up at [storage.bd](https://storage.bd) and confirm your email first.

## 2. Choose what to back up

**Web page:** *What to back up* → **Add folder** or **Add database**.

**Folders:**

```sh
sudo storagebd source add-dir www /var/www
sudo storagebd source add-dir home /home --exclude '*.log' --exclude '/home/*/.cache'
```

Excludes use restic's patterns: `*.log` matches anywhere, and a pattern starting with `/` is anchored to the full path.

**Databases:**

```sh
# MySQL or MariaDB, over TCP (prompts for the password)
sudo storagebd source add-db shop --engine mysql --database shop --user storagebd --host 127.0.0.1

# PostgreSQL
sudo storagebd source add-db app --engine postgres --database app --user storagebd --host 127.0.0.1
```

Other options: `--port`, `--socket /path/to/socket`, `--password-file FILE` (or `-` for stdin, for scripts) and `--no-password`. The password is stored only on this machine. It's handed to the dump tool through a private temporary file, never on the command line.

Manage sources:

```sh
sudo storagebd source list
sudo storagebd source update www --exclude '*.tmp'       # only the flags you pass change
sudo storagebd source update shop --password             # prompts for a new password
sudo storagebd source update home --disable              # skip it in normal runs (still runnable by name)
sudo storagebd source remove www                         # its existing snapshots are kept
```

### Databases: setting up a backup user

A dedicated, read-only database user is the safest choice for backups. These are the minimum privileges, tested with MariaDB 11.4 and PostgreSQL 18.

**MySQL / MariaDB:**

```sql
CREATE USER 'storagebd'@'localhost' IDENTIFIED BY 'a-long-random-password';
GRANT SELECT, SHOW VIEW, TRIGGER, LOCK TABLES ON shop.* TO 'storagebd'@'localhost';
-- Stored procedures and functions (see the warning below):
GRANT SHOW CREATE ROUTINE ON shop.* TO 'storagebd'@'localhost';   -- MariaDB 11.3+
-- MySQL 8.0.20+ instead:  GRANT SHOW_ROUTINE ON *.* TO 'storagebd'@'localhost';
-- Older MariaDB instead:  GRANT SELECT ON mysql.proc TO 'storagebd'@'localhost';
```

> **Warning:** without the routine privilege, the dump still succeeds but **silently leaves out stored procedures and functions**. If your database has any, grant it.

`localhost` users connect over the local socket. If you created the user for `127.0.0.1` or `%`, add the source with `--host 127.0.0.1`.

**PostgreSQL** (14 or newer):

```sql
CREATE ROLE storagebd LOGIN PASSWORD 'a-long-random-password';
GRANT pg_read_all_data TO storagebd;
```

On a server-wide install, storagebd runs as root, so Postgres "peer" login over the local socket would try a database role named `root`. Use `--host 127.0.0.1` with a password instead (the default `pg_hba.conf` allows that).

**Restoring needs more than reading.** A backup-only user can't recreate tables. To restore, temporarily point the source at a user that owns the database, then switch back (see [Restore a database](#7-restore-a-database)).

**Tool versions:** use the dump tool that matches your server. MariaDB's `mariadb-dump` can't dump a MySQL 8 server: the backup fails with a hint. Install MySQL's own `mysqldump` and point storagebd at it with `sudo storagebd settings set mysqldump-path /path/to/mysqldump`. Similarly, `pg_dump` must be the same version as the server or newer; on Debian/Ubuntu, `/usr/bin/pg_dump` usually picks the right one, and `pg_dump-path` can point at a specific version (for example `/usr/lib/postgresql/16/bin/pg_dump`).

## 3. Back up

**Web page:** *Overview* → **Back up now**. To back up a single source, use its ▷ button on *What to back up*. Progress is shown live, and **Cancel** stops cleanly.

**Terminal:**

```sh
sudo storagebd backup                     # all enabled sources
sudo storagebd backup --source shop       # just one (or several, comma-separated)
```

Exit status, handy for your own scripts and monitoring:

| Exit | Meaning |
|---|---|
| `0` | Every source backed up |
| `3` | Done, but some files couldn't be read (they're listed; for example, permission denied or deleted mid-backup) |
| `1` | At least one source failed (the others still ran) |

Things worth knowing:

- Each source is backed up separately. One failing source doesn't stop the others.
- A database dump that fails or is cut short is **never** saved as a snapshot, so you can't later restore a half dump by mistake.
- Only one job (backup, restore or cleanup) runs at a time per backup space. A second one is refused with a clear message instead of colliding.
- The first backup uploads everything; after that, only changes.

## 4. Schedule backups

**Web page:** *Schedule & cleanup* → turn on **Back up automatically** → **Once a day** at a time (local time on the machine) or **Every few hours** → **Save schedule**.

Schedules are run by the storagebd service (`storagebd serve`), which the installer starts at boot. If the machine was off at the scheduled time, the missed backup runs once when the service starts again.

> Prefer cron? `sudo storagebd backup` works fine there too. Its exit status tells your monitoring what happened.

## 5. Keep old backups under control

Without a cleanup policy, every snapshot is kept forever and keeps counting against your storage. The overview reminds you until you set one.

**Web page:** *Schedule & cleanup* → **Keep old backups**. Choose how many of each to keep **for each source**:

- **Most recent**: the last N snapshots.
- **Daily / Weekly / Monthly / Yearly**: the newest snapshot of each of the last N days/weeks/months/years that have one.

The suggested setting keeps a week of daily, a month of weekly and a year of monthly snapshots. Click **Preview** to see exactly which snapshots the rules would remove, before anything is deleted. Then **Turn on cleanup**.

**Terminal:**

```sh
sudo storagebd retention set --keep-daily 7 --keep-weekly 4 --keep-monthly 12
sudo storagebd cleanup --dry-run       # what would be removed, changes nothing
sudo storagebd retention show
sudo storagebd retention off
```

How it behaves:

- Cleanup runs automatically after every successful backup. Skip it once with `backup --no-cleanup`, or run it now with `sudo storagebd cleanup` (or **Clean up now** in the page).
- The newest snapshot of every source is **always** kept, so cleanup can't empty a source. Snapshots made by other tools are never touched.
- Removing snapshots is quick. Actually **freeing the space** ("prune") may read some old data back, so it runs at most every 7 days by default (`--prune-every N`). Force it with `cleanup --prune` or the "also free space now" checkbox.

## 6. Restore files

**Web page:** *Snapshots & restore* → pick a snapshot → browse into folders → tick the files or folders you want (or tick nothing to restore the whole snapshot) → choose where:

- **Restore into folder** (default `/tmp/storagebd-restore`): files keep their full original path inside it, so `/var/www/index.html` lands at `/tmp/storagebd-restore/var/www/index.html`. Check the files there, then copy back what you need.
- **Restore to the original location**: overwrites the current files in place. You're asked to confirm.

If the target folder isn't empty, you're asked before anything is overwritten.

**Terminal:**

```sh
sudo storagebd snapshots                                    # list (newest first)
sudo storagebd snapshots --tag source:www                   # one source only
sudo storagebd restore --source www --target /tmp/restore   # latest snapshot
sudo storagebd restore --source www --snapshot 4f2a9c1e --include /var/www/uploads --target /tmp/restore
sudo storagebd restore --source www --target /              # in place (asks first)
```

## 7. Restore a database

The dump is streamed from storage.bd straight into the database server, using the source's connection settings.

**Web page:** *Snapshots & restore* → pick a snapshot of the database source → **Restore into database** (default: the original database) → **Restore database**.

**Terminal:**

```sh
sudo storagebd restore-db --source shop                          # latest, into the original database
sudo storagebd restore-db --source shop --snapshot 4f2a9c1e --into shop_check
```

- `--into` must name a database that already exists. Restoring into a new, empty database first (`CREATE DATABASE shop_check;`) is a safe way to check a backup.
- Tables in the dump **replace** existing tables with the same name. Other tables are left alone.
- **PostgreSQL** restores run in a single transaction: if anything fails, nothing changes.
- **MySQL/MariaDB** can't do that (table changes commit as they run). A restore that fails part-way leaves the database partly restored. That's another reason to restore into a new database first.
- The source's user needs rights to create and drop tables. If it's a backup-only user, switch temporarily:
  ```sh
  sudo storagebd source update shop --user root --password     # or the database owner
  sudo storagebd restore-db --source shop
  sudo storagebd source update shop --user storagebd --password
  ```

## Activity and history

**Web page:** *Activity* lists backups, restores and cleanups. Click a row for details per source: snapshot ID, data scanned and added, files that couldn't be read, and errors.

**Terminal:**

```sh
sudo storagebd jobs                 # this backup space
sudo storagebd jobs --all --json    # everything, machine-readable
journalctl -u storagebd -f          # the service's log
```

## Your encryption password

Each backup space is encrypted with a long random password, generated on this machine when you connect. Nobody else holds it, including storage.bd staff. On the first backup storagebd hands a copy to storage.bd for **recovery**: storage.bd stores it encrypted with its key-management service, and releases it only through a support-assisted recovery process with checks and an audit trail.

- The *Settings* page shows **"Recovery copy saved with storage.bd"** for each space. So does `sudo storagebd credential list` (`ESCROWED yes`). If the recovery copy couldn't be saved, you get a warning, and storagebd retries on every backup.
- For your own records, you can view the password with `sudo storagebd credential show NAME --reveal` and keep it in a password manager. Treat it like a master key: anyone with it and access to the backup space can read your backups.

## Moving to a new machine, or losing one

Restoring onto a different machine needs that backup space's credential.

- **The old machine still works:** on it, run `sudo storagebd credential show NAME --reveal`. On the new machine:
  ```sh
  sudo storagebd credential add NAME --tenant-id <tenant ID> --install-id <install ID> --existing-repo
  # prompts for the install secret, then the repo's encryption password
  ```
  Then add the same sources (or new ones) and restore as usual.
- **The old machine is gone:** contact storage.bd support. They can recover the backup space's credentials and the escrowed encryption password, after verifying it's you.

For an ongoing new machine, the usual approach is a **new backup space** (`storagebd connect`), keeping the old one until you no longer need its snapshots.

## More than one backup space

You can connect several spaces, for example one for files and one for databases with a different retention. One of them is the **default**, which commands use when you don't name one.

```sh
sudo storagebd credential list                   # * marks the default
sudo storagebd credential use db-space           # change the default
sudo storagebd source add-dir www /var/www --cred files-space
sudo storagebd backup db-space
```

In the web page, a selector at the top of the side bar switches between them. *Settings* → **Create another backup space** adds one.

## Managing credentials from the terminal

Credentials are what link this machine to a backup space. They're stored only on this machine (`/var/lib/storagebd`, readable by root only).

```sh
sudo storagebd credential list                       # never shows secrets
sudo storagebd credential show [NAME] [--reveal] [--json]
sudo storagebd credential add NAME --tenant-id ID --install-id ID [--existing-repo]
sudo storagebd credential update NAME --install-secret        # after it was regenerated
sudo storagebd credential update NAME --rename NEWNAME
sudo storagebd credential use NAME                   # make it the default
sudo storagebd credential test [NAME]                # check storage.bd accepts it
sudo storagebd credential remove NAME                # asks first; backups on storage.bd stay
```

Secrets are never accepted as command-line arguments, because other users could see them in the process list. They're typed at a hidden prompt, or read with `--install-secret-file FILE` / `--restic-password-file FILE` (`-` reads stdin).

`credential update NAME --restic-password` only corrects the copy stored on this machine. It doesn't change the password of the backup space itself.

## Settings

```sh
sudo storagebd settings list
sudo storagebd settings set mysqldump-path /opt/mysql/bin/mysqldump
sudo storagebd settings unset mysqldump-path
```

| Setting | Use |
|---|---|
| `restic-path` | Use a different restic than the bundled one (0.14 or newer) |
| `mysqldump-path`, `mysql-path` | MySQL/MariaDB tools (default: `mariadb-dump`/`mariadb`, then `mysqldump`/`mysql`) |
| `pg_dump-path`, `psql-path` | PostgreSQL tools |

storagebd refuses to run a program that other users could have replaced (one that's group- or world-writable, or owned by another non-root user).

## Security notes

- The web page listens on `127.0.0.1` only, and opens only through the private link from `storagebd ui-url`. The link changes every time the service restarts. Anyone who can run `sudo storagebd ui-url` can use the page, and the page can restore files anywhere on the machine, so treat the link like a root password.
- Settings, credentials and database passwords are stored in `/var/lib/storagebd` (root only, mode 0700). storagebd refuses to start if that folder is readable by others.
- Data is encrypted on this machine before upload. storage.bd stores encrypted data only.
- Database and storage.bd passwords never appear on command lines or in the environment of the programs storagebd runs.

## Troubleshooting

**"not logged in to storage.bd"**: only account actions (connect, activate, the balance) need a login: `sudo storagebd login`. Backups don't.

**"storage.bd rejected this credential"**: the install secret was regenerated, or the space was cancelled. Check your storage.bd dashboard. After regenerating the secret: `sudo storagebd credential update NAME --install-secret`.

**"balance too low to activate"**: top up in your storage.bd dashboard, then `sudo storagebd activate`.

**"repo … already exists and this machine's restic password does not open it"**: this backup space was set up from another machine. Store that machine's password: `sudo storagebd credential update NAME --restic-password`.

**"another backup job is already running for this repo"**: wait for it (`sudo storagebd jobs`). If the process that held it crashed, the next run detects that and takes over automatically.

**Backup finished with exit 3 / "some files could not be read"**: the files are listed under *Activity* (or in the command output). Usually permissions (run server-wide as root) or files deleted mid-backup. Everything else was saved.

**Database backup fails with "Access denied"**: check the user, password and host with the database's own client (`mariadb -u storagebd -p -h 127.0.0.1 shop`). Remember that `'user'@'localhost'` and `'user'@'127.0.0.1'` are different MySQL accounts.

**"…MariaDB's dump tool talking to a MySQL server"**: see *Tool versions* in [Databases: setting up a backup user](#databases-setting-up-a-backup-user).

**The web page says "Open this page from your terminal"**: the link changed (the service restarted). Run `sudo storagebd ui-url` again.

**Scheduled backups don't run**: check that the service is running (`systemctl status storagebd`) and that a schedule is saved (*Schedule & cleanup*). For `--user` installs, run `loginctl enable-linger $USER`.

## Command reference

Run `storagebd <command> --help` for every option. Commands act on the default backup space unless you name one (or pass `--cred`).

| Command | What it does |
|---|---|
| `login` / `logout` | Sign in to / out of your storage.bd account |
| `account` | Balance and backup spaces |
| `connect` | Create a backup space for this machine |
| `activate [NAME]` | Start (pay for) a space created without activating |
| `credential list\|show\|add\|update\|use\|test\|remove` | Manage stored credentials |
| `source list\|add-dir\|add-db\|update\|remove` | Manage what's backed up |
| `backup [NAME]` | Back up now |
| `snapshots [NAME]` | List snapshots |
| `restore` | Restore files from a folder source |
| `restore-db` | Restore a database source |
| `retention show\|set\|off` | Cleanup policy |
| `cleanup [NAME]` | Apply the cleanup policy now (`--dry-run` to preview) |
| `jobs [NAME]` | Recent backups, restores and cleanups |
| `repo init\|status [NAME]` | Prepare / check the repository behind a space |
| `settings list\|set\|unset` | Program paths |
| `serve` | Run the web page and scheduler (the service does this) |
| `ui-url` | Print the web page's private link |
| `upgrade` | Install the latest signed release (`--check` to only look) |
| `version` | storagebd and restic versions |
| `completion bash\|zsh\|fish` | Shell completion script |

The data folder can be changed with `--home DIR` or `STORAGEBD_HOME`, for example to keep two separate setups on one machine.
