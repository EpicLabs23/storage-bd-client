#!/usr/bin/env bash
# Runs the real-engine database integration tests (internal/jobs/
# integration_test.go) against throwaway MariaDB, MySQL 8 and Postgres
# containers bound to 127.0.0.1 on random ports. Containers are removed on
# exit. The DB password is generated per run and exists only in these
# disposable containers.
set -euo pipefail
cd "$(dirname "$0")/.."

PASS="it-$(head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
NAMES=(sbd-it-mariadb sbd-it-mysql sbd-it-pg)
cleanup() { docker rm -f "${NAMES[@]}" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

docker run -d --name sbd-it-mariadb -p 127.0.0.1::3306 \
  -e MARIADB_ROOT_PASSWORD="$PASS" -e MARIADB_DATABASE=itdb mariadb:11.4 >/dev/null
docker run -d --name sbd-it-mysql -p 127.0.0.1::3306 \
  -e MYSQL_ROOT_PASSWORD="$PASS" -e MYSQL_DATABASE=itdb mysql:8.0 >/dev/null
docker run -d --name sbd-it-pg -p 127.0.0.1::5432 \
  -e POSTGRES_PASSWORD="$PASS" -e POSTGRES_DB=itdb postgres:18 >/dev/null

port() { docker port "$1" "$2" | head -1 | sed 's/.*://'; }
export STORAGEBD_IT_DBPASS="$PASS"
export STORAGEBD_IT_MARIADB_PORT="$(port sbd-it-mariadb 3306/tcp)"
export STORAGEBD_IT_MYSQL_PORT="$(port sbd-it-mysql 3306/tcp)"
export STORAGEBD_IT_PG_PORT="$(port sbd-it-pg 5432/tcp)"

echo "Waiting for databases..."
for i in $(seq 1 90); do
  ok=0
  docker exec sbd-it-mariadb healthcheck.sh --connect >/dev/null 2>&1 && ok=$((ok+1))
  docker exec -e MYSQL_PWD="$PASS" sbd-it-mysql mysql -uroot -e 'SELECT 1' itdb >/dev/null 2>&1 && ok=$((ok+1))
  docker exec sbd-it-pg pg_isready -U postgres -d itdb >/dev/null 2>&1 && ok=$((ok+1))
  [ "$ok" = 3 ] && break
  sleep 2
done
[ "$ok" = 3 ] || { echo "databases did not become ready"; exit 1; }
sleep 3 # postgres image restarts once after init

go test -tags integration -count=1 -v -run Integration ./internal/jobs/
