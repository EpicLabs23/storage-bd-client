## New: sign in to the backup page with a username and password

The backup page no longer opens through a private link that changed every time the service restarted. You now sign in like on any website, with a username and password you create on the server:

```sh
sudo storagebd user add admin        # asks for the password (12 characters or more)
```

The installer asks for the `admin` password, so a fresh install is ready to sign in. Users can only be created on the server, never from the page, so reaching the page isn't enough to use it. Manage them with:

```sh
sudo storagebd user list
sudo storagebd user passwd <name>    # new password; signs that user out everywhere
sudo storagebd user signout <name>   # sign a user out of every browser
sudo storagebd user remove <name>
```

You can also change your own password in the page, under **Settings**. Five wrong passwords in a row lock that username out for 15 minutes. A sign-in lasts 12 hours of inactivity, and 7 days at most.

`storagebd ui-url` still works. It now prints the page's address (`http://127.0.0.1:7878/` by default) instead of a one-time link.

## New: two-step sign-in

After the password, the page can also ask for a 6-digit code from an authenticator app on your phone (Google or Microsoft Authenticator, 1Password, Bitwarden, Aegis…). Turn it on in the page under **Settings → Two-step sign-in**, and scan the QR code shown. Or do it on the server, where the QR code appears in the terminal:

```sh
sudo storagebd user 2fa enable <name>
```

You also get 8 backup codes. Each one works once instead of an app code, so keep them somewhere safe. If you lose your phone and your backup codes, turn it off on the server with `sudo storagebd user 2fa disable <name>` and set it up again.

Two-step sign-in is optional while the page is only on this machine. It's required once the page is open to the network (below).

## New: open the backup page on a network port

Until now, the only way to use the page from another computer was an SSH tunnel. That still works and is still the default. You can now also make the page reachable at an address like `https://backup.example.com:7878`.

Four new settings control this. They can only be changed in the terminal, never from the page, and they take effect after a restart (`sudo systemctl restart storagebd`):

| Setting | What it does | Example |
|---|---|---|
| `ui-listen` | The address and port the page listens on. Default `127.0.0.1:7878` (this machine only). `0.0.0.0:7878` opens it on every network address. | `0.0.0.0:7878` |
| `ui-tls-cert` | Your HTTPS certificate file (PEM, full chain), the same kind any website uses, e.g. from Let's Encrypt. A renewed certificate is picked up without a restart. | `/etc/letsencrypt/live/backup.example.com/fullchain.pem` |
| `ui-tls-key` | The private key for that certificate. It must not be readable by other users on the machine. | `/etc/letsencrypt/live/backup.example.com/privkey.pem` |
| `ui-hosts` | The host names the page answers to, comma-separated. IP addresses always work. When the page sits behind a reverse proxy, set this to the proxy's name. | `backup.example.com` |

The page counts as **open to the network** when `ui-listen` isn't a `127.0.0.1` address, or when `ui-hosts` is set. Then:

- **HTTPS is required.** storagebd won't start on a network address without `ui-tls-cert` and `ui-tls-key`, so your password never crosses the network unencrypted.
- **Two-step sign-in is required for every user.** Users who haven't turned it on can't sign in until they do. Browsers that signed in with just a password are signed out.
- **Two-step sign-in can't be turned off from the page.** Use `sudo storagebd user 2fa disable <name>` on the server instead.

### With storagebd's own HTTPS

```sh
sudo storagebd user 2fa enable admin
sudo storagebd settings set ui-tls-cert /etc/letsencrypt/live/backup.example.com/fullchain.pem
sudo storagebd settings set ui-tls-key  /etc/letsencrypt/live/backup.example.com/privkey.pem
sudo storagebd settings set ui-hosts backup.example.com
sudo storagebd settings set ui-listen 0.0.0.0:7878
sudo systemctl restart storagebd
```

Then open `https://backup.example.com:7878`.

### Behind nginx or another reverse proxy

Leave `ui-listen` at its default, set `ui-hosts` to the proxy's name, and let the proxy handle HTTPS:

```sh
sudo storagebd user 2fa enable admin
sudo storagebd settings set ui-hosts backup.example.com
sudo systemctl restart storagebd
```

The proxy must pass the `Host`, `X-Real-IP` and `X-Forwarded-Proto` headers and turn off response buffering (for live backup progress). A complete nginx example is in the [install guide](https://github.com/EpicLabs23/storage-bd-client/blob/main/docs/INSTALL.md#behind-nginx-or-another-reverse-proxy).

### Keep the port closed when you're not using it

The page can restore files anywhere on the server. Open its port in the firewall only while you use it, and only to your own IP address, then close it again. For example, with ufw:

```sh
sudo ufw allow from YOUR.IP to any port 7878 proto tcp           # open
sudo ufw delete allow from YOUR.IP to any port 7878 proto tcp    # close
```

The [install guide](https://github.com/EpicLabs23/storage-bd-client/blob/main/docs/INSTALL.md#firewall) has the same commands for firewalld and cloud firewalls. `storagebd serve` prints them at startup, and the page shows a reminder while it's open to the network.

### Back to this machine only

```sh
sudo storagebd settings unset ui-listen
sudo storagebd settings unset ui-hosts
sudo systemctl restart storagebd
```

`sudo storagebd settings list` shows the current values.

## Upgrading from v0.1.1 or later

```sh
sudo storagebd upgrade           # server-wide install
storagebd upgrade                # --user install
```

**Then create a user to sign in with.** `storagebd upgrade` doesn't ask for one, so until you do, the page says "Create a user to sign in":

```sh
sudo storagebd user add admin    # drop sudo for a --user install
```

Your settings, credentials, schedules and history are kept. Old private links stop working; open `http://127.0.0.1:7878` and sign in instead (through your SSH tunnel, as before, on a remote server).

From v0.1.0, run the install command once more instead. It asks for the `admin` password itself:

```sh
curl -fsSL https://raw.githubusercontent.com/EpicLabs23/storage-bd-client/main/get.sh | sudo sh
```

## Downloads

| File | For |
|---|---|
| `storagebd_v0.2.0_linux_amd64.tar.gz` | Linux x86_64 |
| `storagebd_v0.2.0_linux_arm64.tar.gz` | Linux arm64 |
| `SHA256SUMS` | Checksums for both tarballs |
| `SHA256SUMS.minisig` | Signature of `SHA256SUMS` |

Bundles restic 0.19.1. Release signing key: `210D3F57189BCEA1` ([public key](https://github.com/EpicLabs23/storage-bd-client/blob/main/internal/update/keys/storagebd-release.pub)). To check a download by hand, save the public key as `storagebd-release.pub`, then run:

```sh
minisign -V -p storagebd-release.pub -m SHA256SUMS      # should print: Trusted comment: storagebd v0.2.0
sha256sum -c --ignore-missing SHA256SUMS
```
