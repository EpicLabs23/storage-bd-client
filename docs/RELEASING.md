# Releasing (maintainers)

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
5. **Smoke-test** a tarball on a clean machine or VM: `sudo ./install.sh`, open `sudo storagebd ui-url`, run a backup, then `systemctl stop storagebd` during a backup and check the next one starts cleanly.
6. **Publish** on GitHub with exactly these asset names, since `get.sh` builds its download URLs from them:
   ```sh
   gh release create "$VERSION" "dist/storagebd_${VERSION}_linux_amd64.tar.gz" "dist/storagebd_${VERSION}_linux_arm64.tar.gz" dist/SHA256SUMS \
     --verify-tag --title "$VERSION" --notes-file "CHANGES-${VERSION}.md"
   ```
   Mark it as the latest release: `get.sh` installs whatever `releases/latest` points to.
7. **Check the one-liner** on a clean machine:
   `curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh`
