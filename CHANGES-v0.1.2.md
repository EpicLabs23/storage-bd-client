## Clearer names, matching the storage.bd dashboard

storagebd now uses the same words as the storage.bd dashboard:

| Before | Now |
|---|---|
| tenant / repo | backup space |
| Tenant ID | Space ID |
| Install ID | Access key ID |
| Install secret | Secret key |
| restic password | encryption password |

New flag names:

- `credential add`: `--space-id`, `--key-id`, `--secret-file`
- `credential update`: `--secret`, `--secret-file`

The old flags (`--tenant-id`, `--install-id`, `--install-secret`, `--install-secret-file`) still work, so existing scripts don't need changing. They are no longer listed in `--help`. `--json` output is unchanged.
