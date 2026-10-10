//go:build integration

// Real-engine round trips: seed → backup (real mariadb-dump/pg_dump) →
// drop → restore-db → verify. Needs running servers; scripts/it-dbs.sh
// starts throwaway containers and runs this:
//
//	scripts/it-dbs.sh
package jobs

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"storage-bd-client/internal/dbdump"
	"storage-bd-client/internal/store"
)

// runSQL pipes sql into the engine's client via storagebd's own restore
// command builder (same option-file / PGPASSFILE path).
func runSQL(t *testing.T, e *env, src *store.Source, db, sql string) string {
	t.Helper()
	cmd, err := e.r.Tools.Restore(context.Background(), src, db, e.dir+"/tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer cmd.Cleanup()
	if strings.Contains(sql, "DATABASE") {
		// Postgres refuses CREATE/DROP DATABASE inside a transaction block.
		args := cmd.Cmd.Args[:0]
		for _, a := range cmd.Cmd.Args {
			if a != "--single-transaction" {
				args = append(args, a)
			}
		}
		cmd.Cmd.Args = args
	}
	cmd.Cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.Cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", sql, err, out)
	}
	return string(out)
}

func engineSource(t *testing.T, kind, portEnv, user string) *store.Source {
	port, _ := strconv.Atoi(os.Getenv(portEnv))
	pass := os.Getenv("STORAGEBD_IT_DBPASS")
	if port == 0 || pass == "" {
		t.Skipf("%s / STORAGEBD_IT_DBPASS not set", portEnv)
	}
	return &store.Source{Name: "it", Kind: kind, DBName: "itdb", DBHost: "127.0.0.1", DBPort: port,
		DBUser: user, DBPassword: pass}
}

func roundTrip(t *testing.T, src *store.Source, countSQL string) {
	e := setup(t)
	e.r.Tools = dbdump.DefaultTools()
	ctx := context.Background()
	addSource(t, e, src)

	runSQL(t, e, src, "itdb", `DROP TABLE IF EXISTS orders;
CREATE TABLE orders (id INT PRIMARY KEY, note VARCHAR(100));
INSERT INTO orders VALUES (1, 'first'), (2, 'it''s quoted'), (3, 'ünïcode');`)

	job, detail, err := e.r.Backup(ctx, e.cred, BackupOptions{})
	if err != nil || job.Status != store.StatusOK {
		t.Fatalf("backup: %v %+v %+v", err, job, *detail.Sources[0])
	}

	runSQL(t, e, src, "itdb", `DROP TABLE orders;`)
	if _, _, err := e.r.RestoreDB(ctx, e.cred, RestoreDBOptions{Source: "it"}); err != nil {
		t.Fatalf("restore in place: %v", err)
	}
	if out := runSQL(t, e, src, "itdb", countSQL); !strings.Contains(out, "3") || !strings.Contains(runSQL(t, e, src, "itdb", "SELECT note FROM orders WHERE id = 3;"), "ünïcode") {
		t.Fatalf("after restore: %q", out)
	}

	// Restore into a second, pre-created database.
	runSQL(t, e, src, "itdb", `DROP DATABASE IF EXISTS itdb_copy;`)
	runSQL(t, e, src, "itdb", `CREATE DATABASE itdb_copy;`)
	if _, _, err := e.r.RestoreDB(ctx, e.cred, RestoreDBOptions{Source: "it", Into: "itdb_copy"}); err != nil {
		t.Fatalf("restore --into: %v", err)
	}
	if out := runSQL(t, e, src, "itdb_copy", countSQL); !strings.Contains(out, "3") {
		t.Fatalf("copy: %q", out)
	}

	// Wrong password: the dump fails cleanly and no snapshot is added.
	before := e.fake.SnapshotCount()
	bad := *src
	bad.Name, bad.DBPassword = "bad", "definitely-wrong"
	addSource(t, e, &bad)
	job, detail, _ = e.r.Backup(ctx, e.cred, BackupOptions{Sources: []string{"bad"}})
	if job.Status != store.StatusFailed || e.fake.SnapshotCount() != before {
		t.Fatalf("bad password: %+v %+v", job, detail.Sources)
	}
	t.Logf("bad-password error, as shown to the user: %s", detail.Sources[0].Error)
}

func TestIntegrationMariaDB(t *testing.T) {
	roundTrip(t, engineSource(t, store.KindMySQL, "STORAGEBD_IT_MARIADB_PORT", "root"), "SELECT COUNT(*) FROM orders;")
}

func TestIntegrationMySQL8(t *testing.T) {
	src := engineSource(t, store.KindMySQL, "STORAGEBD_IT_MYSQL_PORT", "root")
	tools := dbdump.DefaultTools()
	if out, _ := exec.Command(tools.MySQLDump, "--version").Output(); strings.Contains(string(out), "MariaDB") {
		// MariaDB's dump tool can't dump MySQL 8. Check that this fails
		// cleanly (no snapshot, actionable hint) instead of the round trip.
		e := setup(t)
		e.r.Tools = tools
		addSource(t, e, src)
		job, detail, _ := e.r.Backup(context.Background(), e.cred, BackupOptions{})
		if job.Status != store.StatusFailed || e.fake.SnapshotCount() != 0 ||
			!strings.Contains(detail.Sources[0].Error, "mysqldump-path") {
			t.Fatalf("expected clean failure with hint: %+v %+v", job, *detail.Sources[0])
		}
		t.Skip("only MariaDB's dump tool is installed; it cannot dump MySQL 8 (clean failure + hint verified)")
	}
	roundTrip(t, src, "SELECT COUNT(*) FROM orders;")
}

func TestIntegrationPostgres(t *testing.T) {
	roundTrip(t, engineSource(t, store.KindPostgres, "STORAGEBD_IT_PG_PORT", "postgres"), "SELECT COUNT(*) FROM orders;")
}
