# CLAUDE.md

**storage-bd-client** (`storagebd`) — storage.bd-branded backup client for D2C customers' Linux machines. Wraps the official restic binary; local state in SQLite. Design, decisions and module plan: [`docs/DESIGN.md`](docs/DESIGN.md). Platform architecture: `../storage-bd-api/docs/ARCHITECTURE.md` (§03/§04/§05/§19 apply directly).

## Commands

```bash
make build                                  # static bin/storagebd (CGO_ENABLED=0)
make build API_URL=http://localhost:3010    # dev build pointed at local storage-bd-api
make build-all                              # linux amd64 + arm64
make test && make vet                       # repo tests run the real restic (/usr/bin/restic or $STORAGEBD_RESTIC); skipped if absent
./bin/storagebd --home /tmp/sbtest credential list
./bin/storagebd --home /tmp/sbtest login --api-url http://localhost:3010   # local storage-bd-api (npm run start:dev)
```

## Layout

- `cmd/storagebd` — entrypoint
- `internal/cli` — cobra commands; build-time `DefaultAPIURL` / `Version`
- `internal/store` — SQLite (modernc, pure Go), append-only migrations, validation
- `internal/paths` — data dir resolution + 0700 enforcement
- `internal/api` — storage-bd-api HTTP client (account auth, D2C tenants, balance, pricing, data token)
- `internal/account` — login/session refresh (serialized via SQLite), connect/activate; shared by CLI and the future web UI
- `internal/restic` — binary resolution/verification, loopback token relay, Session (env, password file, run/exit codes), ops (init, snapshots, backup, restore, dump)
- `internal/repo` — opens a credential's repo: data-token source, first-use init + password verify + escrow

## Rules

- Secrets never via argv or plain env vars: no-echo prompt or `--*-file PATH|-`; restic gets `RESTIC_PASSWORD_FILE`.
- restic/dump tools are exec'd with argument slices and a constructed minimal env, never a shell string (§19).
- Migrations in `internal/store/store.go` are append-only.
- Same workflow as storage-bd-api: one module per review checkpoint; the user commits (one-line message, no trailer); flag anything needing root.
