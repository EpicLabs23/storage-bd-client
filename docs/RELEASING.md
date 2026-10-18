# Releasing (maintainers)

## One-time: the release signing key

`storagebd upgrade` only installs a release whose `SHA256SUMS` is signed with a key built into the installed storagebd (`internal/update/keys/*.pub`). Create the key once, with [minisign](https://jedisct1.github.io/minisign/) (`sudo apt install minisign`, or `go install aead.dev/minisign/cmd/minisign@latest`):

```sh
minisign -G -p internal/update/keys/storagebd-release.pub -s ~/.minisign/storagebd-release.key
```

- Choose a strong password. Commit the `.pub` file; it must be in the commit you release.
- Keep the secret key and its password offline and backed up. If it's lost, existing installs can't verify upgrades signed with a new key; their users have to reinstall with `get.sh` once.
- If the key leaks, anyone with it can sign upgrades that existing installs will accept. Release a version with a new key as soon as you can.
- **Rotating:** add the new `.pub` next to the old one and release. Sign with the new key from the release after that, and remove the old `.pub` once no supported version needs it.

## Each release

Set the version once, in the shell you'll run the release from. Every command below reads it:
```sh
export VERSION=v0.1.0
```

1. **Pick the restic version.** To move to a newer restic:
   ```sh
   scripts/update-restic-pin.sh 0.19.1      # verifies restic's GPG-signed SHA256SUMS, writes packaging/restic.lock
   make restic && STORAGEBD_RESTIC=$PWD/dist/restic/linux_amd64/restic make test
   STORAGEBD_RESTIC=$PWD/dist/restic/linux_amd64/restic scripts/it-dbs.sh
   ```
   The tests print `testing with restic X`; check it's the version you expect. Review and commit the lock-file diff.
2. **Test** on a clean tree: `make vet && make test`.
3. **Tag** the commit: `git tag -s "$VERSION" -m "$VERSION" && git push origin "$VERSION"`.
4. **Build** from the tagged, clean tree:
   ```sh
   make release VERSION="$VERSION"
   ```
   This writes `dist/storagebd_${VERSION}_linux_{amd64,arm64}.tar.gz` and `dist/SHA256SUMS`. Builds are reproducible: the same commit gives the same files.
5. **Sign** (asks for the key's password):
   ```sh
   make sign VERSION="$VERSION"
   ```
   This writes `dist/SHA256SUMS.minisig` with the trusted comment `storagebd $VERSION`, then checks it against the keys in `internal/update/keys/`. It refuses if the key isn't one of them, or if `dist/SHA256SUMS` is for another version. The secret key is read from `~/.minisign/storagebd-release.key`; set `STORAGEBD_SIGNING_KEY` to use another path.
6. **Smoke-test** a tarball on a clean machine or VM: `sudo ./install.sh` (it asks for the admin password), sign in at `http://127.0.0.1:7878`, run a backup, then `systemctl stop storagebd` during a backup and check the next one starts cleanly.
7. **Publish** on GitHub with exactly these asset names, since `get.sh` and `storagebd upgrade` build their download URLs from them:
   ```sh
   gh release create "$VERSION" "dist/storagebd_${VERSION}_linux_amd64.tar.gz" "dist/storagebd_${VERSION}_linux_arm64.tar.gz" \
     dist/SHA256SUMS dist/SHA256SUMS.minisig \
     --verify-tag --latest --title "$VERSION" --notes-file "CHANGES-${VERSION}.md"
   ```
   It must be the latest release: `get.sh` and `storagebd upgrade` install whatever `releases/latest` points to.
8. **Check the one-liner** on a clean machine:
   `curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh`
9. **Check `storagebd upgrade`** on a machine running the previous release: `sudo storagebd upgrade --check`, then `sudo storagebd upgrade`. It should print `Signature OK` and restart the service.
