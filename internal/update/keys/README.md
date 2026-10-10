Release signing public keys (minisign `*.pub`), built into storagebd.
`storagebd upgrade` only installs a release whose `SHA256SUMS.minisig` was made
with one of these keys. To rotate, add the new key here one release before
signing with it; remove the old one once no supported version needs it.
See docs/RELEASING.md.
