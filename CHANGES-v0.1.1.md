## New: signed releases and `storagebd upgrade`

storagebd can now upgrade itself:

```sh
sudo storagebd upgrade           # server-wide install
storagebd upgrade                # --user install
storagebd upgrade --check        # only show whether a newer release is available
```

Releases are now signed. `storagebd upgrade` installs a release only if its signature matches the storagebd release key built into the version you're running. A release that wasn't signed with that key, or an older release passed off as a newer one, is refused. It installs the new version the same way the current one was installed, and restarts the service only if it was running.

Release signing key: `210D3F57189BCEA1` ([public key](https://github.com/EpicLabs23/storage-bd-client/blob/main/internal/update/keys/storagebd-release.pub)).

## Upgrading from v0.1.0

v0.1.0 has no `upgrade` command, so run the install command once more:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

Your settings, credentials and history are kept. From v0.1.1 on, use `storagebd upgrade`.

## Downloads

| File | For |
|---|---|
| `storagebd_v0.1.1_linux_amd64.tar.gz` | Linux x86_64 |
| `storagebd_v0.1.1_linux_arm64.tar.gz` | Linux arm64 |
| `SHA256SUMS` | Checksums for both tarballs |
| `SHA256SUMS.minisig` | Signature of `SHA256SUMS` |

To check a download by hand, save the public key linked above as `storagebd-release.pub`, then run:

```sh
minisign -V -p storagebd-release.pub -m SHA256SUMS      # should print: Trusted comment: storagebd v0.1.1
sha256sum -c --ignore-missing SHA256SUMS
```
