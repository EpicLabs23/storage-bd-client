# storage-bd-client design

`storagebd` is a branded, storage.bd-preconfigured backup client for direct (D2C) customers backing up their own Linux machines. It reverses `storage-bd-api/docs/ARCHITECTURE.md` §01's "no packaged D2C client for v1"; everything else in that doc still applies, especially §03 (data flow), §04 (token model), §05 (escrow) and §19 (restic invocation discipline).

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
4. **Sources + jobs**: directory sources (canonicalized, excludes) and DB sources (MySQL/MariaDB `mysqldump --single-transaction`, Postgres `pg_dump`, piped into `restic backup --stdin`, no pre-compression, both exit codes checked), DB connection creds stored like other secrets (defaults-file / `PGPASSFILE`, never argv). Durable per-credential job lock + job history in SQLite. `storagebd source ...`, `storagebd backup`, `storagebd snapshots`, `storagebd restore` (files to a target dir; DB via `restic dump` → `mysql`/`psql`).
5. **`storagebd serve`**: scheduler + embedded web UI: connect account, manage sources, run/watch backups, browse snapshots, restore files/DBs. Optional systemd unit.
6. **Packaging**: bundled restic per arch, install script, systemd unit, release build.

## Account session (module 2)

- `login` stores one session per API URL in `account_sessions` (migration 2): the refresh token plus a cached access token. The refresh token comes from the `account_refresh_token` cookie, with `remember=true` (30 days) unless `--no-remember` is passed.
- storage-bd-api refresh tokens are **single-use**. Every refresh runs inside a `BEGIN IMMEDIATE` SQLite transaction (`Store.UpdateSessionLocked`), so the CLI and `serve` never both spend the same token. The cached access token is reused until it is within 1 minute of its `exp`, which is decoded (not verified) only to decide when to refresh.
- If a refresh comes back 401/403, the session is deleted locally ("session expired, run login").
- The account session is only used for account actions (create, activate, balance). Backups use the per-repo install credential, so logging out never stops them.
- `connect` checks the local credential name **before** creating the remote tenant. If the local save fails after the remote create, an `OrphanError` carries the one-time install secret, and the CLI prints it to stderr so it isn't lost.
- `connect` and `activate` ask before charging (they show the price from `/public/pricing`) unless `--yes` is passed. A 402 when activating still leaves the repo created and saved.

## Running restic (module 3)

- **Loopback relay, not a token in the repo URL.** ecp-go and ehm-api build `rest:https://user:<data-token>@host/repo/` once per run. Data tokens expire after 15 min and the proxy checks expiry on every request, so any run longer than that fails partway. Instead, `storagebd` runs a relay on `127.0.0.1:<random>` for each session (`internal/restic/relay.go`). restic connects to it with a random per-session nonce as its Basic-auth password. For each request the relay adds a data token, minted again 2 minutes before it expires, and forwards to `{api}/repo/...`. 302 responses (presigned S3) go back to restic unchanged; Go drops the Authorization header on a cross-host redirect, so the nonce never reaches S3. restic's environment never contains the install secret or a data token.
- **Binary.** Resolved from the `restic-path` setting, then `$STORAGEBD_RESTIC`, then the bundled locations (next to the `storagebd` executable, `/usr/lib/storagebd/restic`, `/usr/local/lib/storagebd/restic`). Never from `$PATH`. The file and its directory must not be writable by group/others or owned by another non-root user. Minimum restic 0.14. (Bundling and checksum pinning: module 6.)
- **Environment.** Built from scratch: `RESTIC_REPOSITORY` (the relay), `RESTIC_PASSWORD_FILE` (a 0600 temp file under `<data>/tmp`, removed on close), `RESTIC_CACHE_DIR=<data>/cache/<tenant>`, `TMPDIR`, `HOME=<data>`, `PATH=/usr/bin:/bin`, `RESTIC_PROGRESS_FPS=1`. Arguments are always passed as a slice, with `--` before paths and snapshot IDs.
- **First use** (`repo.Manager.Open`): mint a token up front (so a bad credential fails clearly), then `restic init`. If restic says "config file already exists", run `restic cat config` to prove this machine's password opens the repo, and refuse with a clear message if it doesn't. Then `PUT /escrow/password`. If escrow fails, that's a warning, not an error: it's retried on every open until it succeeds, because refusing to back up would be worse (§05: the local copy is primary).
- **Exit codes.** 3 returns a summary with `Partial=true` and the per-file errors. restic 0.16 writes `--json` `error` messages to **stderr**, so both streams are parsed. Recognized failures map to `ErrNoRepo` / `ErrWrongPassword` / `ErrLocked` (codes 10/12/11 on restic 0.17+, message matching on 0.16). Ctrl-C/SIGTERM sends restic SIGINT so it can drop its lock, then kills it after 30s.
- **Tests** (`internal/repo/repo_test.go`) run the real restic against a fake storage.bd that checks tokens, redirects `data/` reads to a separate "S3" server, and records escrow. They cover init plus escrow once; backup → snapshots → restore through the redirect; partial backup; stdin backup → dump; an existing repo with the wrong password; escrow failure then retry; and a bad install secret failing before restic runs.

## Open items

- Re-linking a machine to an *existing* tenant requires a D2C "regenerate install secret" endpoint plus the repo's restic password (escrow recovery is support-gated, §05). Out of scope for v1 by decision.
- Mongo/MSSQL dumps: not in v1 (only MySQL/MariaDB + Postgres).
- storage-bd-api's `GET /d2c/tenants` and `GET /d2c/tenants/:id` return the raw Tenant row, including `restic_password_escrow` (KMS-wrapped ciphertext) and the S3 bucket/prefix. It can't be decrypted without KMS, but the owner's browser/client has no reason to receive it. The API should select only the fields it needs. Not changed here.
- **Cross-repo bug (not fixed here):** ecp-go (`handler/backup/backup.go` `getRepoURL`) and ehm-api (`storage-bd/host-backup.service.ts`, `storage-bd-restic.service.ts`) mint one 15-minute data token and use it for the whole restic run, so any backup or restore running past ~15 minutes will get 401s partway. Options: a relay like this one, or a longer-lived data token scoped to a single job.
