# Contributing

Thanks for helping! Bug reports, docs fixes and pull requests are all welcome.

## Reporting bugs

Open an issue with what you did, what you expected and what happened, plus `storagebd version` and your Linux distribution. For backup or restore problems, the relevant part of `storagebd jobs --json` or `journalctl -u storagebd` helps a lot. **Remove secrets before posting:** never paste output of `credential show --reveal`.

Security issues: see [SECURITY.md](SECURITY.md) instead.

## Development

You need Go 1.26+, and restic 0.14+ for the tests that run the real restic. Docker is optional; it's used for the database integration tests.

```sh
make build           # bin/storagebd
make test            # unit tests + tests against the real restic (skipped if none is installed)
make vet
make restic          # fetch the pinned, checksum-verified restic into dist/restic/
STORAGEBD_RESTIC=$PWD/dist/restic/linux_amd64/restic make test   # test against the bundled version
scripts/it-dbs.sh    # MariaDB/MySQL/PostgreSQL round trips in throwaway containers
```

To work on the web page without a storage.bd account, run the fake storage.bd used by the tests (account login password: `demo-password`):

```sh
go run ./internal/testutil/cmd/fakestorage                   # prints its URL
make build API_URL=<that URL>
./bin/storagebd --home /tmp/sbdev serve --listen 127.0.0.1:7879
```

How it's put together, and why, is in [docs/DESIGN.md](docs/DESIGN.md). Please read the relevant section before larger changes.

## Pull requests

- Keep changes focused, with tests for new behaviour (`go test ./...` must pass).
- Run `gofmt`. Match the style of the surrounding code, including its comments that explain *why*.
- Database migrations in `internal/store/store.go` are append-only: add a new one, never edit an old one.
- The web page uses no inline scripts or styles (its CSP forbids them) and renders text only via `textContent`.
- `packaging/restic.lock` changes only through `scripts/update-restic-pin.sh`.

By contributing you agree that your contribution is licensed under the Apache License 2.0.
