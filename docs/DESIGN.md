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
3. **restic wrapper**: pinned binary resolution + checksum, repo URL from data token (`rest:https://user:<token>@host/repo/`, as in ecp-go), `init` + escrow on first use (`PUT /escrow/password`), `--json` progress parsing, exit-code 0/1/3 semantics.
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

## Open items

- Re-linking a machine to an *existing* tenant requires a D2C "regenerate install secret" endpoint plus the repo's restic password (escrow recovery is support-gated, §05). Out of scope for v1 by decision.
- Mongo/MSSQL dumps: not in v1 (only MySQL/MariaDB + Postgres).
- storage-bd-api's `GET /d2c/tenants` and `GET /d2c/tenants/:id` return the raw Tenant row, including `restic_password_escrow` (KMS-wrapped ciphertext) and the S3 bucket/prefix. It can't be decrypted without KMS, but the owner's browser/client has no reason to receive it. The API should select only the fields it needs. Not changed here.
