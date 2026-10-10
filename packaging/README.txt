storage.bd backup client (storagebd)
=====================================

Backs up folders and MySQL/MariaDB/PostgreSQL databases on this Linux
machine to storage.bd, using a bundled copy of restic (BSD-2-Clause, see
LICENSE.restic).

Install
  sudo ./install.sh          server-wide (recommended for servers)
  ./install.sh --user        only for your user

Then open the backup panel with the private link from:
  sudo storagebd ui-url      (or: storagebd ui-url for --user installs)

On a remote server, tunnel first and open the link on your computer:
  ssh -L 7878:127.0.0.1:7878 your-server

Everything also works from the terminal:
  storagebd login
  storagebd connect --storage-gb 10
  storagebd source add-dir www /var/www
  storagebd source add-db shop --engine mysql --database shop --user backup
  storagebd backup
  storagebd snapshots
  storagebd restore --source www --target /tmp/restore
  storagebd --help

Uninstall
  sudo ./install.sh --uninstall           keeps /var/lib/storagebd
  sudo ./install.sh --uninstall --purge   also deletes it (credentials and
                                          encryption passwords!)
