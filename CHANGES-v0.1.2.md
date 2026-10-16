## Clearer names, matching the storage.bd dashboard

storagebd now uses the same words as the storage.bd dashboard:

| Before | Now |
|---|---|
| tenant / repo | backup space |
| Tenant ID | Space ID |
| Install ID | Space access key ID |
| Install secret | Space secret key |
| restic password | encryption password |

The space access key belongs to one backup space and is only for backups. It is not the same as a reseller's API client ID / API secret.

New flag names:

- `credential add`: `--space-id`, `--key-id`, `--secret-file`
- `credential update`: `--secret`, `--secret-file`

The old flags (`--tenant-id`, `--install-id`, `--install-secret`, `--install-secret-file`) still work, so existing scripts don't need changing. They are no longer listed in `--help`. `--json` output is unchanged.

## New: use a backup space from the dashboard in the web page

If you created a backup space in the storage.bd dashboard, you can now add it from storagebd's web page: choose **Use it on this machine** under the sign-in form (or **Settings → Use a space from the dashboard**) and paste its Space ID, Space access key ID and Space secret key. No sign-in is needed. storagebd checks the values with storage.bd before saving, so a wrong secret key saves nothing. Before, this was only possible with `storagebd credential add`.

## Clearer setup for remote servers

The install guide now says which machine each command runs on, and shows a complete SSH tunnel command for opening the web page of a remote server from your own computer. `storagebd serve --help` shows the same command.

## Upgrading from v0.1.1

```sh
sudo storagebd upgrade           # server-wide install
storagebd upgrade                # --user install
```

Your settings, credentials and history are kept. From v0.1.0, run the install command once more instead:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

## Downloads

| File | For |
|---|---|
| `storagebd_v0.1.2_linux_amd64.tar.gz` | Linux x86_64 |
| `storagebd_v0.1.2_linux_arm64.tar.gz` | Linux arm64 |
| `SHA256SUMS` | Checksums for both tarballs |
| `SHA256SUMS.minisig` | Signature of `SHA256SUMS` |

Bundles restic 0.19.1. Release signing key: `210D3F57189BCEA1` ([public key](https://github.com/EpicLabs23/storage-bd-client/blob/main/internal/update/keys/storagebd-release.pub)). To check a download by hand, save the public key as `storagebd-release.pub`, then run:

```sh
minisign -V -p storagebd-release.pub -m SHA256SUMS      # should print: Trusted comment: storagebd v0.1.2
sha256sum -c --ignore-missing SHA256SUMS
```
