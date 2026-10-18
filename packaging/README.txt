storage.bd backup client (storagebd)
=====================================

Backs up folders and MySQL/MariaDB/PostgreSQL databases on this Linux
machine to storage.bd, using a bundled copy of restic.

Free and open source: Apache License 2.0 (LICENSE, NOTICE). restic is
BSD-2-Clause (LICENSE.restic); other components: THIRD_PARTY_LICENSES.txt.
Source, docs and issues: https://github.com/EpicLabs23/storage-bd-client

Install
  sudo ./install.sh          server-wide (recommended for servers)
  ./install.sh --user        only for your user

The installer asks for the password of the user you sign in with
(admin). Add more users, or reset a password, with:
  sudo storagebd user add <name>
  sudo storagebd user passwd <name>

Then open http://127.0.0.1:7878 and sign in. On a remote server, tunnel
first and open that address on your computer:
  ssh -N -L 7878:127.0.0.1:7878 your-server

Everything also works from the terminal:
  storagebd login
  storagebd connect --storage-gb 10
  storagebd source add-dir www /var/www
  storagebd source add-db shop --engine mysql --database shop --user backup
  storagebd backup
  storagebd snapshots
  storagebd restore --source www --target /tmp/restore
  storagebd --help

Full guide: https://github.com/EpicLabs23/storage-bd-client/blob/main/docs/USER_GUIDE.md

Uninstall
  sudo ./install.sh --uninstall           keeps /var/lib/storagebd
  sudo ./install.sh --uninstall --purge   also deletes it (credentials and
                                          encryption passwords!)
