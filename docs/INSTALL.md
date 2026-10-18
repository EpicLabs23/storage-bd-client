# Installing storagebd

storagebd runs on **Linux, x86_64 (amd64) or arm64**. It needs `curl` (or `wget`), `tar` and `sha256sum`, which most distributions already have. systemd is optional; it's used to start the backup service at boot.

To back up databases, the database's own client tools must be on the machine. Database servers normally have them already:

| Database | Needed for backup | Needed for restore |
|---|---|---|
| MySQL / MariaDB | `mysqldump` or `mariadb-dump` | `mysql` or `mariadb` |
| PostgreSQL | `pg_dump` | `psql` |

Everything else, including restic, comes in the package.

> **Which machine?** Every command in this guide runs **on the server** you're backing up, in an SSH session or at its console. The one exception is the SSH tunnel in [Open the backup page](#open-the-backup-page), which runs **on your own computer**.

- [Quick install (recommended)](#quick-install-recommended)
- [Install for one user only](#install-for-one-user-only)
- [Install a specific version](#install-a-specific-version)
- [Manual install](#manual-install)
- [Build from source](#build-from-source)
- [Open the backup page](#open-the-backup-page)
- [Upgrade](#upgrade)
- [Uninstall](#uninstall)
- [What goes where](#what-goes-where)
- [Troubleshooting the install](#troubleshooting-the-install)

## Quick install (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

This:

1. finds the latest release on GitHub and downloads the package for your CPU;
2. checks it against the release's `SHA256SUMS`, and stops if anything doesn't match;
3. installs `storagebd` and its bundled restic under `/usr/local`, owned by root;
4. installs and starts the `storagebd` systemd service (the backup page and the scheduler).

A server-wide install runs as root, so it can back up any file on the machine and restore anywhere. This is what you want on a server.

> Prefer to read a script before running it? Download it, read it, then run it:
> ```sh
> curl -fsSLO https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh
> less get.sh
> sudo sh get.sh
> ```

## Install for one user only

No root needed. storagebd can then back up only what your user can read:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sh -s -- --user
```

It installs into `~/.local/bin`, `~/.local/lib/storagebd` and a systemd *user* service. Make sure `~/.local/bin` is on your `PATH`. Scheduled backups need the service to keep running after you log out:

```sh
loginctl enable-linger "$USER"
```

## Install a specific version

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo STORAGEBD_VERSION=v0.2.0 sh
```

## Manual install

1. Download `storagebd_<version>_linux_<arch>.tar.gz`, `SHA256SUMS` and `SHA256SUMS.minisig` from the [releases page](https://github.com/EpicLabs23/storage-bd-client/releases).
2. Verify and unpack. The first command checks the release signature with [minisign](https://jedisct1.github.io/minisign/) and the [release public key](../internal/update/keys/); it should print `Trusted comment: storagebd <version>`. Releases before v0.1.1 aren't signed.
   ```sh
   minisign -V -p storagebd-release.pub -m SHA256SUMS
   sha256sum -c --ignore-missing SHA256SUMS
   tar xzf storagebd_v0.2.0_linux_amd64.tar.gz
   cd storagebd_v0.2.0_linux_amd64
   ```
3. Install:
   ```sh
   sudo ./install.sh          # server-wide
   ./install.sh --user        # or just for your user
   ```

`install.sh` checks the package's own `SHA256SUMS` again before installing anything. Use `--no-start` to install without enabling the service.

## Build from source

You need Go 1.26 or newer, `make`, `curl` and `bunzip2`.

```sh
git clone https://github.com/EpicLabs23/storage-bd-client.git
cd storage-bd-client
make release VERSION=v0.0.0-local
```

This downloads the restic version pinned in `packaging/restic.lock`, refuses it unless its checksum matches, builds static binaries, and writes installable packages to `dist/`. Install one with the manual steps above.

For just the `storagebd` binary: `make build` gives `bin/storagebd`. You then need restic yourself; point storagebd at it with `storagebd settings set restic-path /usr/bin/restic` (restic 0.14 or newer).

## Open the backup page

The backup page runs on the server. By default it listens only on `127.0.0.1:7878`, so nothing on the network can reach it, and you reach it from your own computer through an SSH tunnel (below). You can also [open it on a network port](#open-the-page-on-a-network-port) with HTTPS and two-step sign-in.

You sign in to the page with a username and password. The installer asks you for the `admin` user's password. Users are managed only on the server, never from the page:

```sh
sudo storagebd user add <name>      # create a user (asks for the password)
sudo storagebd user passwd <name>   # set a new password; signs that user out everywhere
sudo storagebd user signout <name>  # sign a user out of every browser
sudo storagebd user list
sudo storagebd user remove <name>
```

Drop the `sudo` for a `--user` install. Signing in stays valid for 12 hours of inactivity (at most 7 days). Five wrong passwords in a row lock that username out for 15 minutes from that address.

**Two-step sign-in** adds a 6-digit code from an authenticator app (Google or Microsoft Authenticator, 1Password, Bitwarden, Aegis…) after the password. Turn it on in the page (*Settings → Two-step sign-in*), or on the server:

```sh
sudo storagebd user 2fa enable <name>    # shows a QR code to scan, then asks for a code
sudo storagebd user 2fa disable <name>   # e.g. a lost phone and no backup codes left
```

Turning it on gives you 8 backup codes. Each one works once instead of an app code. It's optional while the page is on `127.0.0.1`, and required while it's open to the network.

### At the server itself

If the server has a screen and a browser (a desktop or a laptop), open `http://127.0.0.1:7878` in the browser there and sign in. That's all.

### From your computer, for a remote server

**1. On your own computer**, open a terminal (Terminal on macOS or Linux, PowerShell on Windows 10/11, which has SSH built in) and start the tunnel with your SSH key:

```sh
ssh -i ~/.ssh/id_ed25519 -N -L 7878:127.0.0.1:7878 you@your-server
```

| Part | Meaning |
|---|---|
| `-i ~/.ssh/id_ed25519` | Your SSH private key, the same one you use to log in to the server. Often `~/.ssh/id_rsa` or `~/.ssh/id_ecdsa` instead. Leave `-i …` out if your key is loaded in `ssh-agent` or is the default key. |
| `-N` | Only forward the port; don't open a shell. |
| `-L 7878:127.0.0.1:7878` | Port 7878 on your computer goes to port 7878 on the server's own loopback, where the page listens. |
| `you@your-server` | Your SSH login and the server's hostname or IP, for example `root@203.0.113.10`. |

The command prints nothing and doesn't return. That means the tunnel is open. Leave this window open while you use the page.

**2. On your own computer**, open `http://127.0.0.1:7878` in your browser and sign in. Press **Ctrl-C** in the tunnel window when you're done.

#### Variations

- **SSH on another port** (say 2222): add `-p 2222`.
  ```sh
  ssh -i ~/.ssh/id_ed25519 -p 2222 -N -L 7878:127.0.0.1:7878 you@your-server
  ```
- **Port 7878 already used on your computer** ("bind: Address already in use"): pick another local port and open that one.
  ```sh
  ssh -i ~/.ssh/id_ed25519 -N -L 8787:127.0.0.1:7878 you@your-server
  # then open http://127.0.0.1:8787
  ```
- **A shortcut for next time:** add this to `~/.ssh/config` on your computer,
  ```
  Host backup-myserver
      HostName 203.0.113.10
      User you
      IdentityFile ~/.ssh/id_ed25519
      LocalForward 7878 127.0.0.1:7878
  ```
  then `ssh -N backup-myserver` opens the tunnel.

#### If it doesn't work

- **"Permission denied (publickey)"**: SSH didn't accept the key. Check that `-i` points at the right private key and that you can log in normally with `ssh -i ~/.ssh/id_ed25519 you@your-server`.
- **The browser says it can't connect**: the tunnel window was closed, or the service isn't running. Check with `systemctl status storagebd` on the server.
- **The page says "Create a user to sign in"**: no user exists yet (for example after an upgrade from v0.1.3 or older that ran without a terminal). Run `sudo storagebd user add admin` on the server, then reload.
- **Forgot the password**: `sudo storagebd user passwd <name>` on the server.
- **"too many failed sign-ins"**: wait 15 minutes, or reset the password on the server.
- **"two-step sign-in is required"**: the page is open to the network. Run `sudo storagebd user 2fa enable <name>` on the server.
- **Lost the phone**: sign in with a backup code, or turn 2FA off on the server with `sudo storagebd user 2fa disable <name>` and set it up again.
- **"refusing to open the page to the network … without HTTPS"**: set `ui-tls-cert` and `ui-tls-key` first (see [Open the page on a network port](#open-the-page-on-a-network-port)).

### Without systemd

For example in a container: start it yourself on the server with `sudo storagebd serve`. It prints the page's address when it starts, and stays in the foreground.

## Open the page on a network port

Instead of an SSH tunnel, you can make the page reachable at an address like `https://backup.example.com:7878`. The page can restore files anywhere on the server, so storagebd then insists on:

- **HTTPS.** Your certificate and key files, as for any website (for example from Let's Encrypt). A renewed certificate is picked up without a restart.
- **Two-step sign-in for every user.** Users who haven't turned it on can't sign in until they do (`sudo storagebd user 2fa enable <name>`). Browsers that signed in with just a password are signed out.
- **Server-only settings.** These settings can only be changed in the terminal, never from the page.

**Keep the port closed when you're not using it.** Open it in the firewall only while you use the page, and only to your own IP address. See [Firewall](#firewall) below.

### With storagebd's own HTTPS

```sh
sudo storagebd user 2fa enable admin
sudo storagebd settings set ui-tls-cert /etc/letsencrypt/live/backup.example.com/fullchain.pem
sudo storagebd settings set ui-tls-key  /etc/letsencrypt/live/backup.example.com/privkey.pem
sudo storagebd settings set ui-hosts backup.example.com
sudo storagebd settings set ui-listen 0.0.0.0:7878
sudo systemctl restart storagebd
```

Then open `https://backup.example.com:7878`. `ui-hosts` lists the names the page answers to (comma-separated). Addresses like `https://203.0.113.10:7878` always work, but the browser warns if the certificate doesn't cover them. The key file must not be readable by other users.

To get a certificate with certbot when nothing else serves port 80: `sudo certbot certonly --standalone -d backup.example.com` (port 80 must be open while it runs).

### Behind nginx (or another reverse proxy)

Leave storagebd on `127.0.0.1:7878` and set `ui-hosts` to the proxy's name, so storagebd knows it's behind a proxy:

```sh
sudo storagebd user 2fa enable admin
sudo storagebd settings set ui-hosts backup.example.com
sudo systemctl restart storagebd
```

```nginx
server {
    listen 443 ssl;
    server_name backup.example.com;
    ssl_certificate     /etc/letsencrypt/live/backup.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/backup.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:7878;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;   # for the sign-in rate limit and log
        proxy_set_header X-Forwarded-Proto https;  # marks the session cookie Secure
        proxy_buffering off;                       # live backup progress
        proxy_read_timeout 1h;
    }
}
```

storagebd believes `X-Real-IP` and `X-Forwarded-Proto` only from loopback, and only when `ui-hosts` is set.

### Firewall

Open the port only while you use the page, and only to your own IP address. Then close it again.

| Firewall | Open | Close |
|---|---|---|
| ufw | `sudo ufw allow from YOUR.IP to any port 7878 proto tcp` | `sudo ufw delete allow from YOUR.IP to any port 7878 proto tcp` |
| firewalld | `sudo firewall-cmd --add-rich-rule='rule family="ipv4" source address="YOUR.IP" port port="7878" protocol="tcp" accept'` | `sudo firewall-cmd --remove-rich-rule='…same rule…'`, or reload/reboot (without `--permanent` it isn't saved) |
| Cloud provider | Add an inbound rule: TCP 7878 from `YOUR.IP/32` | Remove the rule |

Use 443 instead of 7878 behind a reverse proxy. Find your IP address by searching "what is my IP" in your browser. The page shows the same reminder while it's open to the network, and `storagebd serve` prints it at startup.

### Back to the SSH tunnel only

```sh
sudo storagebd settings unset ui-listen
sudo storagebd settings unset ui-hosts
sudo systemctl restart storagebd
```

## Upgrade

From v0.1.1 on, upgrade with storagebd itself:

```sh
sudo storagebd upgrade           # server-wide install
storagebd upgrade                # --user install
```

It downloads the latest release and installs it only if the release is signed with the storagebd release key built into the version you're running. That check doesn't depend on GitHub: a release someone else uploaded, or an old release passed off as a new one, is refused. Then it installs the new version the same way the current one was installed and restarts the service if it was running. Your settings, credentials and history stay where they are (`/var/lib/storagebd`), and upgrades update the database format automatically.

- `storagebd upgrade --check` only shows whether a newer release is available.
- `storagebd upgrade --version v0.2.0` installs a specific release, including an older one.

If a backup is running, the service stops it cleanly first. Run it again afterwards, or let the schedule do it.

**Upgrading to v0.2.0 or later from v0.1.x:** the backup page now has a username and password sign-in. In a terminal, the upgrade asks for the `admin` user's password. If it couldn't ask (for example when run from a script), run `sudo storagebd user add admin` afterwards (drop `sudo` for a `--user` install). Then sign in at `http://127.0.0.1:7878`.

**From v0.1.0**, which has no `upgrade` command, run the install command once more:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

## Uninstall

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh -s -- --uninstall
```

This removes the program and the service, and **keeps** `/var/lib/storagebd`. That folder holds your backup credentials and **the encryption password of each backup space**.

To delete that folder too, run `install.sh --uninstall --purge` from an unpacked release, in a terminal. It asks you to type `PURGE`. Before you do, check that each backup space's password is saved with storage.bd ("Recovery copy saved" in *Settings*, or `storagebd credential list`) or somewhere safe. Without it, those backups can't be decrypted.

Your backups on storage.bd are never deleted by uninstalling.

## What goes where

| | Server-wide | `--user` |
|---|---|---|
| Program | `/usr/local/bin/storagebd` | `~/.local/bin/storagebd` |
| Bundled restic | `/usr/local/lib/storagebd/restic` | `~/.local/lib/storagebd/restic` |
| Service | `/etc/systemd/system/storagebd.service` | `~/.config/systemd/user/storagebd.service` |
| Settings, credentials, history | `/var/lib/storagebd` (mode 0700) | `~/.local/share/storagebd` (mode 0700) |
| Licenses | `/usr/local/share/doc/storagebd/` | `~/.local/share/doc/storagebd/` |
| Logs | `journalctl -u storagebd` | `journalctl --user -u storagebd` |

## Troubleshooting the install

**"run with sudo for a server-wide install"**: add `sudo` in front of `sh`, or install just for yourself with `sh -s -- --user`.

**"checksum mismatch; not installing"**: the download was corrupted or altered. Try again. If it keeps happening, please [report it](../SECURITY.md).

**"unsupported CPU"**: storagebd is published for x86_64 and arm64 only.

**`systemctl status storagebd` shows it failing**: look at `journalctl -u storagebd -n 50`. The most common cause is a data folder other users can access. storagebd refuses to run until it's private: `sudo chmod 700 /var/lib/storagebd`.

**"restic binary not found"** (source builds only): install restic 0.14+ and run `storagebd settings set restic-path /usr/bin/restic`.
