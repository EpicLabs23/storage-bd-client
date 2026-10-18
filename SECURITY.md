# Security policy

storagebd handles backup credentials and encryption passwords and often runs as root, so we take reports seriously.

## Reporting a vulnerability

Please **don't** open a public issue. Report privately through GitHub: on this repository's **Security** tab, choose **Report a vulnerability**.

Include what you found, how to reproduce it, and the version (`storagebd version`). We'll acknowledge it, keep you updated while we fix it, and credit you in the release notes if you'd like.

## Supported versions

Security fixes go into the latest release. Upgrade by re-running the install command (see [docs/INSTALL.md](docs/INSTALL.md#upgrade)).

## Scope

In scope: the `storagebd` program, its web page, the installer scripts (`get.sh`, `packaging/install.sh`) and the release packages. Issues in restic itself should go to the [restic project](https://github.com/restic/restic/security). Issues in the storage.bd service go to storage.bd support.

## How storagebd protects you

- The backup web page listens on loopback by default. Sign-in uses users created on the server only (argon2id password hashes, optional TOTP two-step sign-in with single-use backup codes, rate-limited attempts, server-side sessions in HttpOnly SameSite=Strict cookies). Opening it to the network requires HTTPS and two-step sign-in for every user, and only the terminal can turn that on. It rejects foreign `Host`/`Origin` headers (DNS rebinding and CSRF) and sends a strict Content-Security-Policy.
- Secrets are never passed on command lines or in environment variables: restic gets its password through a 0600 file, database tools through option or pass files.
- Local state lives in a 0700 directory, and storagebd refuses to start if that's not the case.
- storagebd refuses to run a restic or database tool that other users could have replaced.
- Release packages carry SHA256 checksums, which the installer verifies. The bundled restic is pinned to checksums from restic's GPG-signed release files.

Details are in [docs/DESIGN.md](docs/DESIGN.md).
