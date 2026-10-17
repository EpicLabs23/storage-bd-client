## Snapshot details open beside the list

On the web page's **Snapshots & restore** screen, clicking a snapshot used to open its files and restore options below the list. With many snapshots, you had to scroll down to find them. They now open on the right, next to the list, and stay in view while you scroll it. On narrow screens they still appear below the list, and the page scrolls down to them.

## Upgrading from v0.1.1 or v0.1.2

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
| `storagebd_v0.1.3_linux_amd64.tar.gz` | Linux x86_64 |
| `storagebd_v0.1.3_linux_arm64.tar.gz` | Linux arm64 |
| `SHA256SUMS` | Checksums for both tarballs |
| `SHA256SUMS.minisig` | Signature of `SHA256SUMS` |

Bundles restic 0.19.1. Release signing key: `210D3F57189BCEA1` ([public key](https://github.com/EpicLabs23/storage-bd-client/blob/main/internal/update/keys/storagebd-release.pub)). To check a download by hand, save the public key as `storagebd-release.pub`, then run:

```sh
minisign -V -p storagebd-release.pub -m SHA256SUMS      # should print: Trusted comment: storagebd v0.1.3
sha256sum -c --ignore-missing SHA256SUMS
```
