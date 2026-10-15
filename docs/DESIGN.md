# storage-bd-client design

`storagebd` is the open-source (Apache-2.0) storage.bd backup client for Linux machines. It wraps the official restic binary and talks to the storage.bd API for accounts, data tokens and password escrow. This document records the design and the reasoning behind it, for contributors; the user-facing docs are [INSTALL.md](INSTALL.md) and [USER_GUIDE.md](USER_GUIDE.md).

## Decisions (2026-10-10)

| Decision | Choice | Why |
|---|---|---|
| Form factor | One static Go binary: CLI + `storagebd serve` (local web UI + scheduler) | Works on headless servers (SSH tunnel) and desktops; one artifact to ship |
| restic | Wrap the official binary (pinned, checksum-verified, invoked by full path), never fork | §19: no stable restic Go API; a fork means perpetual rebasing |
| Local state | SQLite via `modernc.org/sqlite` (pure Go, no cgo) | Static cross-compiles for linux/amd64 + arm64 |
| Connect flow | Account login → create a new tenant per machine (or paste a connect credential) | Uses existing `/auth/account-login` + `/d2c/tenants`; no API changes; one repo + one restic password per machine |
| Platforms | Linux amd64/arm64 | Where D2C server customers are |
| Default API | `https://api.storage.bd`, overridable at build (`make build API_URL=...`) and per credential (`--api-url`) | "pre-configured" without locking out dev/staging |

## Security model

- **Data dir** (`/var/lib/storagebd` as root, else `~/.local/share/storagebd`, or `--home` / `$STORAGEBD_HOME`) is 0700, DB file 0600. The client refuses to start if the dir is group/world-accessible rather than silently fixing it.
- Install secrets and restic passwords are stored in plaintext in SQLite. This is the same trust model as §19's `RESTIC_PASSWORD_FILE`; encrypting them would need a key stored right next to them, so it adds nothing.
- Secrets are **never** accepted as CLI arguments, because argv is visible to other users via `ps`. They come from a no-echo prompt or `--*-file PATH|-`.
- API URLs must be https; plain http is allowed only for loopback, or with an explicit `--allow-http`.
- When restic runs (module 3), it gets the password through a short-lived 0600 `RESTIC_PASSWORD_FILE` and a minimal constructed environment. It never gets the password through env vars or argv.
- Local web UI (module 5): binds 127.0.0.1 only, requires a random per-start token, and checks `Host`/`Origin` headers. Binding to loopback alone isn't enough, because any local process or a malicious web page (via CSRF or DNS rebinding) could otherwise trigger a restore to an arbitrary path.

## Modules

Each module is built and then paused for review.

1. **Scaffold + SQLite store + credential CRUD CLI** — done. `storagebd credential list|add|show|update|use|remove`.
2. **storage.bd API client + `login`/`connect`** — done. `storagebd login|logout|account|connect|activate`, `storagebd credential test`. Details below.
3. **restic wrapper** — done. `storagebd repo init|status`, `storagebd snapshots`, `storagebd settings list|set|unset`. Details below.
4. **Sources + jobs** — done. `storagebd source list|add-dir|add-db|update|remove`, `backup`, `restore`, `restore-db`, `jobs`. Details below.
5. **`storagebd serve`** — done. Local web UI + scheduler, `storagebd ui-url`. Details below.
6. **Packaging** — done. Pinned, signature-verified restic, release tarballs, installer, systemd units. Details below.

## Account session (module 2)

- `login` stores one session per API URL in `account_sessions` (migration 2): the refresh token plus a cached access token. The refresh token comes from the `account_refresh_token` cookie, with `remember=true` (30 days) unless `--no-remember` is passed.
- storage-bd-api refresh tokens are **single-use**. Every refresh runs inside a `BEGIN IMMEDIATE` SQLite transaction (`Store.UpdateSessionLocked`), so the CLI and `serve` never both spend the same token. The cached access token is reused until it is within 1 minute of its `exp`, which is decoded (not verified) only to decide when to refresh.
- If a refresh comes back 401/403, the session is deleted locally ("session expired, run login").
- The account session is only used for account actions (create, activate, balance). Backups use the per-repo install credential, so logging out never stops them.
- `connect` checks the local credential name **before** creating the remote tenant. If the local save fails after the remote create, an `OrphanError` carries the one-time install secret, and the CLI prints it to stderr so it isn't lost.
- `connect` and `activate` ask before charging (they show the price from `/public/pricing`) unless `--yes` is passed. A 402 when activating still leaves the repo created and saved.

## Running restic (module 3)

- **Loopback relay, not a token in the repo URL.** The obvious approach, building `rest:https://user:<data-token>@host/repo/` once per run, breaks long runs: data tokens expire after 15 min and the proxy checks expiry on every request, so any run longer than that fails partway. Instead, `storagebd` runs a relay on `127.0.0.1:<random>` for each session (`internal/restic/relay.go`). restic connects to it with a random per-session nonce as its Basic-auth password. For each request the relay adds a data token, minted again 2 minutes before it expires, and forwards to `{api}/repo/...`. 302 responses (presigned S3) go back to restic unchanged; Go drops the Authorization header on a cross-host redirect, so the nonce never reaches S3. restic's environment never contains the install secret or a data token.
- **Binary.** Resolved from the `restic-path` setting, then `$STORAGEBD_RESTIC`, then the bundled locations (next to the `storagebd` executable, `/usr/lib/storagebd/restic`, `/usr/local/lib/storagebd/restic`). Never from `$PATH`. The file and its directory must not be writable by group/others or owned by another non-root user. Minimum restic 0.14. (Bundling and checksum pinning: module 6.)
- **Environment.** Built from scratch: `RESTIC_REPOSITORY` (the relay), `RESTIC_PASSWORD_FILE` (a 0600 temp file under `<data>/tmp`, removed on close), `RESTIC_CACHE_DIR=<data>/cache/<tenant>`, `TMPDIR`, `HOME=<data>`, `PATH=/usr/bin:/bin`, `RESTIC_PROGRESS_FPS=1`. Arguments are always passed as a slice, with `--` before paths and snapshot IDs.
- **First use** (`repo.Manager.Open`): mint a token up front (so a bad credential fails clearly), then `restic init`. If restic says "config file already exists", run `restic cat config` to prove this machine's password opens the repo, and refuse with a clear message if it doesn't. Then `PUT /escrow/password`. If escrow fails, that's a warning, not an error: it's retried on every open until it succeeds, because refusing to back up would be worse (§05: the local copy is primary).
- **Exit codes.** 3 returns a summary with `Partial=true` and the per-file errors. restic 0.16 writes `--json` `error` messages to **stderr**, so both streams are parsed. Recognized failures map to `ErrNoRepo` / `ErrWrongPassword` / `ErrLocked` (codes 10/12/11 on restic 0.17+, message matching on 0.16). Ctrl-C/SIGTERM sends restic SIGINT so it can drop its lock, then kills it after 30s.
- **Tests** (`internal/repo/repo_test.go`) run the real restic against a fake storage.bd that checks tokens, redirects `data/` reads to a separate "S3" server, and records escrow. They cover init plus escrow once; backup → snapshots → restore through the redirect; partial backup; stdin backup → dump; an existing repo with the wrong password; escrow failure then retry; and a bad install secret failing before restic runs.

## Sources, jobs, backup/restore (module 4)

- **Sources** (migration 4) belong to a credential: `dir` (path + restic excludes), `mysql` (also MariaDB) and `postgres` (database, host/port or socket, user, password). DB names, users and hosts are restricted to characters that can't start a flag or break an option file. They reach the tools only as `--opt=value` arguments, so `--all-databases` as a "database name" is rejected.
- **One snapshot per source per run**, tagged `storagebd` + `source:<name>`. This differs from §03's "one snapshot per job": restic 0.16 can't combine `--stdin` and paths in one snapshot, and per-source snapshots make "restore source X as of T" a simple lookup. A failing source doesn't stop the others. Job status: `failed` if any source failed, else `partial` if any skipped files, else `succeeded`.
- **DB dumps** (`internal/dbdump`): `mariadb-dump`/`mysqldump --single-transaction --quick --routines --triggers --hex-blob --no-tablespaces`, `pg_dump --format=plain --clean --if-exists --no-owner --no-acl`, piped straight into `restic backup --stdin --stdin-filename <kind>-<db>.sql`, never compressed first. Passwords are passed via a 0600 `--defaults-extra-file` / `PGPASSFILE` in `<data>/tmp`, removed afterwards. Tools are found in fixed locations (never `$PATH`), can be overridden with the `mysqldump-path` / `mysql-path` / `pg_dump-path` / `psql-path` settings, and go through the same ownership/permission check as restic (`internal/execsafe`).
- **A failed dump never becomes a snapshot.** restic only commits a `--stdin` snapshot when it sees EOF. `dbdump.Reader` keeps the last 8 KB of output; at EOF it waits for the dump to exit and checks for the completion trailer (`-- Dump completed` / `-- PostgreSQL database dump complete`; pg_dump 18 writes `\unrestrict` after its trailer, hence the 8 KB window). If either check fails, the read returns an error instead of EOF. `restic.Session.Run` then sends SIGINT to restic *while its stdin is still open*, so no snapshot is written. Tested with a dump that streams 3 MB and then exits 2, and with one that exits 0 but has no trailer.
- **Restores.** `restore` (dir sources) runs `restic restore --target`; files land under target + original path, and the CLI asks before writing into a non-empty target. `restore-db` streams `restic dump` into `mariadb`/`psql` against the source's server, either in place or `--into` an existing database. Postgres uses `--single-transaction -v ON_ERROR_STOP=1`, so it's all-or-nothing. MySQL can't be (DDL commits implicitly), which the help text says. If `restic dump` fails partway, the client is killed before it sees EOF.
- **Job lock** (§19): `jobs_one_running` is a unique partial index, so there is one running job per credential across all processes. `ClaimJob` takes it inside a `BEGIN IMMEDIATE` transaction. A `running` row whose PID is gone, or whose PID was reused (`/proc/<pid>/stat` start time differs), is marked `interrupted` and taken over. Finishing the job records per-source results (snapshot, bytes, up to 50 skipped files) in `detail` JSON, even after Ctrl-C.
- **Exit codes:** `backup` returns 0 (ok), 3 (partial, same as restic) or 1 (failed), so it fits cron.
- **Real engines:** `scripts/it-dbs.sh` starts throwaway MariaDB 11.4 / MySQL 8.0 / Postgres 18 containers on random loopback ports and runs the `integration`-tagged round-trip tests (seed → backup → drop → restore in place → restore `--into` a copy → bad password fails with no snapshot). MariaDB and Postgres pass. MySQL 8 can't be dumped by MariaDB's dump tool (it emits `/*!100100 ...*/` comments MySQL rejects). With only MariaDB client tools installed, the test confirms the failure is clean, with a hint to set `mysqldump-path` to Oracle's mysqldump.

## Web UI and scheduler (module 5)

- `storagebd serve [--listen 127.0.0.1:7878]`: an HTTP server in `internal/server` with the UI embedded (`web/`, vanilla JS + CSS, no build step, storage.bd brand tokens and logos copied from storage-bd-ui) and a JSON API that reuses the same `account`/`repo`/`jobs` code as the CLI.
- **Access control** (the UI can restore to any path, often as root):
  - Loopback addresses only; `--listen` refuses anything else. Remote access is through an SSH tunnel.
  - A random 256-bit token is generated on each start. It's printed as `/auth?t=…` and written to `<data>/serve.json` (0600) for `storagebd ui-url`. Visiting that link exchanges it for an HttpOnly, SameSite=Strict cookie and redirects, so the token leaves the address bar.
  - `Host` must be a loopback name (blocks DNS rebinding), with any port so SSH tunnels work.
  - Non-GET API calls need an `X-Storagebd: 1` header and a same-origin `Origin` (blocks CSRF). JSON bodies are capped at 1 MB and unknown fields are rejected.
  - Strict CSP (`script-src 'self'`, no inline styles or scripts, `frame-ancestors 'none'`) plus nosniff, no-referrer and COOP/CORP headers.
  - All dynamic text (file names from snapshots, errors) goes through `textContent`, never `innerHTML`.
- **Pages:** onboarding (sign in → create this machine's backup space with a live price quote) · Overview (status, live progress, account balance/plan, activate) · What to back up (folder/DB add/edit/enable/remove, back up one or all) · Snapshots & restore (snapshot list, `restic ls` folder browser with multi-select, restore to a folder or in place; DB restore in place or `--into`, with engine-specific warnings) · Schedule · Activity (per-source details, skipped files) · Settings (account, repos on this machine with test/default/remove-with-typed-confirm, tool paths).
- **Jobs from the UI** run in the background, one per repo. Progress goes out over SSE (`/api/events`); Cancel sends restic SIGINT. A job started elsewhere (CLI, cron) still holds the store's job lock, so the UI's start fails cleanly with a 409.
- **Scheduler** (migration 5 `schedules`): daily at HH:MM (local time) or every N hours. It checks every 30s, advances `next_run_at` *before* starting the run (so a failing or busy repo doesn't retry every tick), and runs a missed slot once at the next start. A busy repo skips the slot, with a warning.
- **Data-token reuse:** `repo.Manager` now caches token sources per credential. storage-bd-api rate-limits `/auth/data-token` to 20/min, and browsing snapshot folders opens the repo once per click.
- **Tests:** `internal/server/server_test.go` covers the auth/hardening rules (bad token, rebinding Host, missing CSRF header, foreign Origin, unknown JSON fields, 0600 state file, loopback-only listen), a full login → connect → add source → async backup with SSE → snapshots → ls → restore flow (including the overwrite confirmation), and the scheduler catching up a missed run. The fake storage.bd now also covers the account endpoints, and `go run ./internal/testutil/cmd/fakestorage` runs it standalone for manual UI work. The UI was also driven end to end in headless Chrome (Playwright) at 1280px and 390px, light and dark, with no console errors and no horizontal overflow.

## Packaging (module 6)

- **Pinned restic** (`packaging/restic.lock`, committed): written only by `scripts/update-restic-pin.sh <version>`. That script imports `https://restic.net/gpg-key-alex.asc` into a throwaway GNUPGHOME, requires the fingerprint published in restic's install docs (`CF8F18F2844575973F79D4E191A6868BD3F7A907`) among the file's primary keys (the file holds two keys), verifies `SHA256SUMS.asc` was made by that key or one of its subkeys, then records the linux amd64/arm64 checksums plus restic's LICENSE checksum. `scripts/fetch-restic.sh` (`make restic`) downloads into `dist/cache` and refuses anything that doesn't match the lock. Pinned now: **restic 0.19.1** (2026-07-05).
- **0.19.1 compatibility:** the full suite (including the real-restic repo/jobs/server tests) and the real MariaDB/Postgres round trips pass on both 0.19.1 and the system's 0.16.4 (`STORAGEBD_RESTIC=dist/restic/linux_amd64/restic make test`). Verified for real only during the retention work; see "restic 0.17+ and aborted stdin backups" below.
- **Release:** `make release VERSION=v0.1.0 [API_URL=https://...]` → `scripts/build-release.sh` builds `dist/storagebd_<v>_linux_{amd64,arm64}.tar.gz` (storagebd, restic, LICENSE.restic, install.sh, both units, README.txt, RELEASE, per-tarball SHA256SUMS) and `dist/SHA256SUMS`. It refuses a non-https API URL. Builds are reproducible (CGO off, `-trimpath`, owner 0:0, normalized modes, mtimes = commit time): two builds of the same tree produce identical tarball hashes.
- **Install** (`install.sh`): checks SHA256SUMS first. `sudo ./install.sh` puts storagebd in `/usr/local/bin`, restic in `/usr/local/lib/storagebd/restic` (both root:root 0755, which storagebd's binary check requires) and `/etc/systemd/system/storagebd.service`; data goes in `/var/lib/storagebd` (0700); then enable + start, or print the manual command when there's no systemd. `./install.sh --user` uses `~/.local` and a user unit. `--uninstall` keeps the data. `--purge` deletes it only after `PURGE` is typed on a TTY (it holds the restic passwords).
- **restic lookup** now also checks `<exe>/../lib/storagebd/restic`, covering both install prefixes, before the binary's own directory. `storagebd version` prints the restic it found.
- **systemd unit** runs as root (backups must read everything, restores write anywhere). Hardening is limited to what doesn't interfere with that: NoNewPrivileges, Protect{KernelTunables,KernelModules,KernelLogs,ControlGroups,Clock,Hostname}, RestrictRealtime/Namespaces, LockPersonality, native syscalls, AF_UNIX/INET/INET6 only, UMask 0077, StateDirectory 0700, TimeoutStopSec=90 (time for restic to drop its lock). PrivateTmp and RestrictSUIDSGID are deliberately left out: a restore to /tmp would vanish into a private namespace, and restores must keep setuid bits.
- **Tested** in a throwaway ubuntu:24.04 container: root install (ownership/modes, bundled restic found, serve + ui-url, serve.json removed on shutdown), tampered release refused, `--user` install as a regular user, `--user` as root refused, purge without a TTY refused, uninstall. **Not tested:** the unit actually starting under systemd. That needs a real systemd host and root; it's a manual check.

## Old-snapshot cleanup (retention)

- **Policy per credential** (migration 6 `retention`): keep last / daily / weekly / monthly / yearly, and how often to free space (`prune_interval_days`, default 7). There's no policy until one is set, so nothing is deleted on its own. The UI suggests 7 daily / 4 weekly / 12 monthly, and the overview shows a banner while backups exist with no policy. A policy that keeps nothing is rejected.
- **What it touches:** `restic forget --tag storagebd --group-by tags <keep…>`, so only snapshots storagebd made, with the rules applied per source (each source's tag set is its own group). restic's keep rules always keep the newest snapshot of every group, so a source can never be emptied (tested).
- **When:** `jobs.Runner.AfterBackup` runs a separate `cleanup` job (its own job record) after every backup that produced snapshots (succeeded or partial), from the CLI (`backup`, unless `--no-cleanup`), the UI and the scheduler. A failed backup skips cleanup. `forget` only removes snapshot records. `prune`, which actually frees space but may read partly-used packs back (egress, metered by storage.bd), runs only when the interval has passed or when forced (`cleanup --prune`, the "also free space now" checkbox).
- **Preview:** `cleanup --dry-run` / the UI's Preview button runs `forget --dry-run` with the current or unsaved rules and lists exactly which snapshots would go. It doesn't take the job lock.
- **CLI:** `storagebd retention show|set|off`, `storagebd cleanup [--dry-run|--prune]`. **UI:** "Schedule & cleanup" page, "Keep old backups" card; cleanup jobs appear in Activity with per-source counts and the prune report.

### restic 0.17+ and aborted stdin backups (found while testing this)

restic ≥ 0.17 does **not** exit on SIGINT while blocked reading a stdin pipe that's still open (0.16 did). The "a failed dump never becomes a snapshot" rule depends on keeping stdin open while restic is stopped, so on 0.19.1 a failed DB dump hung the backup forever. `Session.Run` now sends SIGINT, waits `abortGrace` (10s), then SIGKILLs. A killed restic can't commit a snapshot. After a kill it runs `restic unlock` (which only removes stale locks) to clear the lock the killed process left; the test asserts zero snapshots and zero leftover locks.

An earlier claim that the suite passed on 0.19.1 (module 6) was wrong. `dist/restic/linux_amd64/` was group-writable (umask), storagebd correctly refused it, and the test helper silently fell back to `/usr/bin/restic` 0.16.4. Fixed both: `fetch-restic.sh` sets 0755 on its directories, and `testutil.ResticOrSkip` now fails if `$STORAGEBD_RESTIC` is set but unusable and logs the restic version under test. The suite and the MariaDB/Postgres round trips have now really been run on 0.19.1 and 0.16.4.

## Signed upgrades (`storagebd upgrade`)

- **Why:** `get.sh` is fetched from GitHub on every run, so it can only prove a download matches what GitHub serves. Whoever controls the GitHub repo controls both. `storagebd upgrade` instead checks releases against a key built into the binary that's already installed. After the first install, a malicious upgrade needs the offline signing key, not just access to the repo. The first install still trusts GitHub (and HTTPS).
- **Format:** minisign, prehashed Ed25519 (`ED`: Ed25519 over BLAKE2b-512). Verified in Go (`internal/update`, stdlib ed25519 + `golang.org/x/crypto/blake2b`), so no host tool is needed. The legacy unhashed `Ed` format is refused. Interop is pinned by a test vector made with an independent minisign implementation (`internal/update/testdata/reference.*`).
- **What's signed:** `SHA256SUMS`, as `SHA256SUMS.minisig`, with the trusted comment `storagebd <version>`. The comment is covered by the signature, and upgrade requires it to equal the tag it asked for. That stops an older, genuinely signed `SHA256SUMS` being served under a newer tag. The tarball must then match the signed checksum.
- **Keys:** `internal/update/keys/*.pub`, embedded with `go:embed`. More than one key may be listed, for rotation. `scripts/sign-release.sh` (`make sign`) refuses to sign unless the key it signs with verifies against one of the committed public keys, so a release can't be signed with a key its own binaries don't trust.
- **Flow:** find the target (`releases/latest` redirect, or `--version`) → refuse a downgrade unless `--version` asks for it, and refuse development builds without `--version` → download SHA256SUMS + signature (size-capped) → verify → download the tarball (capped at 512 MiB), hashing as it streams → unpack only regular files directly inside `storagebd_<v>_linux_<arch>/` (no links, subdirectories or `..`) into a 0700 temp dir → run its `install.sh` with `/bin/sh`.
- **Install mode** comes from where the running binary is: `/usr/local/bin/storagebd` as root means the system layout, `~/.local/bin/storagebd` as a regular user means `--user`. Anything else is refused, rather than guessing what to replace. If the service isn't active, `--no-start` is passed, so an upgrade never starts a service the user had left stopped.
- **v0.1.0** predates signing and has no `upgrade` command. Users move to v0.1.1 with `get.sh` once.
- **Not covered:** an end-to-end upgrade run through `install.sh` on a real host. That's a release-checklist step (`docs/RELEASING.md`).

## Open items

- The web UI can add a credential pasted from the dashboard (`POST /api/credentials`, same as `credential add`; it mints a data token first so a wrong value is refused before saving). It can't *pick* an existing tenant from the account's list: re-linking a machine to an *existing* tenant without its install secret requires a D2C "regenerate install secret" endpoint plus the repo's restic password (escrow recovery is support-gated, §05). Out of scope for v1 by decision.
- Mongo/MSSQL dumps: not in v1 (only MySQL/MariaDB + Postgres).
- MySQL 8 round trip is unverified with Oracle's `mysqldump` (the test machine only had MariaDB's client tools).
- Manual check pending: `sudo ./install.sh` on a real systemd host (unit starts, `sudo storagebd ui-url` works, `systemctl stop` lets restic release its lock).
