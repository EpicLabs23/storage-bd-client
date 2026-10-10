package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"storage-bd-client/internal/dbdump"
	"storage-bd-client/internal/repo"
	"storage-bd-client/internal/store"
	"storage-bd-client/internal/testutil"
)

type env struct {
	r    *Runner
	cred *store.Credential
	fake *testutil.FakeStorage
	dir  string
}

func setup(t *testing.T) *env {
	t.Helper()
	bin := testutil.ResticOrSkip(t)
	fake := testutil.NewFakeStorage(t)
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	s.SetSetting(ctx, store.SettingResticPath, bin)
	pw, _ := store.GenerateResticPassword()
	cred := &store.Credential{Name: "test", APIURL: fake.API.URL, TenantID: testutil.TenantID,
		InstallID: testutil.InstallID, InstallSecret: testutil.InstallSecret, ResticPassword: pw}
	if err := s.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	return &env{
		r:    &Runner{Store: s, Repos: &repo.Manager{Store: s, DataDir: dir}, Host: "test-host"},
		cred: cred, fake: fake, dir: dir,
	}
}

// fakeTool writes an executable shell script standing in for a dump or
// client binary. It records its argv (one per line) and, for clients,
// stdin, next to itself.
func fakeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	// storagebd refuses binaries in group/world-writable dirs; t.TempDir
	// follows the umask, which may be 0002.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + p + ".args\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func addSource(t *testing.T, e *env, src *store.Source) {
	t.Helper()
	src.CredentialID = e.cred.ID
	src.Enabled = true
	if err := e.r.Store.CreateSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
}

func TestBackupDirAndDBThenRestore(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tools := t.TempDir()
	e.r.Tools = dbdump.Tools{
		MySQLDump: fakeTool(t, tools, "mysqldump", `printf 'CREATE TABLE t (id int);\n-- Dump completed on 2026-10-10\n'`),
		MySQL:     fakeTool(t, tools, "mysql", `cat > `+filepath.Join(tools, "mysql.stdin")),
	}

	www := t.TempDir()
	os.WriteFile(filepath.Join(www, "index.html"), []byte("<h1>hi</h1>"), 0o600)
	os.WriteFile(filepath.Join(www, "debug.log"), []byte("noise"), 0o600)
	addSource(t, e, &store.Source{Name: "www", Kind: store.KindDir, Path: www, Excludes: []string{"*.log"}})
	addSource(t, e, &store.Source{Name: "shop", Kind: store.KindMySQL, DBName: "shop", DBUser: "u", DBPassword: `se"cret`})

	var events []string
	job, detail, err := e.r.Backup(ctx, e.cred, BackupOptions{OnEvent: func(ev Event) { events = append(events, ev.Type+":"+ev.Source) }})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.StatusOK || len(detail.Sources) != 2 {
		t.Fatalf("job = %+v detail = %+v", job, detail.Sources[0])
	}
	if e.fake.SnapshotCount() != 2 {
		t.Fatalf("snapshots = %d, want 2 (one per source)", e.fake.SnapshotCount())
	}
	if !strings.Contains(strings.Join(events, ","), "source-done:shop") {
		t.Fatalf("events = %v", events)
	}

	// The DB password went through an option file, not argv, and the
	// option file is gone afterwards.
	args, _ := os.ReadFile(filepath.Join(tools, "mysqldump.args"))
	if strings.Contains(string(args), "secret") || !strings.HasPrefix(string(args), "--defaults-extra-file=") {
		t.Fatalf("mysqldump argv = %q", args)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "tmp", "my-*")); len(left) != 0 {
		t.Fatalf("option files left: %v", left)
	}

	// Files restore, exclude honored.
	target := t.TempDir()
	if _, _, err := e.r.RestoreFiles(ctx, e.cred, RestoreOptions{Source: "www", Target: target}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(target, www, "index.html")); err != nil || string(b) != "<h1>hi</h1>" {
		t.Fatalf("index.html = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(target, www, "debug.log")); !os.IsNotExist(err) {
		t.Fatal("excluded file was backed up")
	}

	// DB restore streams the dump into the client, targeting --into.
	if _, _, err := e.r.RestoreDB(ctx, e.cred, RestoreDBOptions{Source: "shop", Into: "shop_copy"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(tools, "mysql.stdin"))
	if !strings.Contains(string(got), "CREATE TABLE t") {
		t.Fatalf("client received %q", got)
	}
	cargs, _ := os.ReadFile(filepath.Join(tools, "mysql.args"))
	if !strings.HasSuffix(strings.TrimSpace(string(cargs)), "shop_copy") {
		t.Fatalf("client argv = %q", cargs)
	}

	// Restoring a DB source as files (and vice versa) is refused.
	if _, _, err := e.r.RestoreFiles(ctx, e.cred, RestoreOptions{Source: "shop", Target: target}); err == nil {
		t.Fatal("restore of DB source as files accepted")
	}
}

func TestFailedDumpNeverBecomesASnapshot(t *testing.T) {
	cases := map[string]struct{ body, wantErr string }{
		// Writes plenty, then dies: a naive pipeline would snapshot this.
		"exit non-zero": {`head -c 3000000 /dev/zero; echo "connection lost" >&2; exit 2`, "connection lost"},
		// Exits 0 but output is cut short (no completion trailer).
		"missing trailer": {`printf 'CREATE TABLE t (id int);\n'`, "incomplete"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			tools := t.TempDir()
			e.r.Tools = dbdump.Tools{MySQLDump: fakeTool(t, tools, "mysqldump", tc.body)}
			addSource(t, e, &store.Source{Name: "shop", Kind: store.KindMySQL, DBName: "shop", DBUser: "u"})

			job, detail, err := e.r.Backup(ctx, e.cred, BackupOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != store.StatusFailed || !strings.Contains(detail.Sources[0].Error, tc.wantErr) {
				t.Fatalf("job = %+v, source = %+v", job, detail.Sources[0])
			}
			if n := e.fake.SnapshotCount(); n != 0 {
				t.Fatalf("%d snapshot(s) committed from a failed dump", n)
			}
			e.fake.Mu.Lock()
			for k := range e.fake.Files {
				if strings.HasPrefix(k, "locks/") {
					t.Errorf("stale restic lock left behind: %s", k)
				}
			}
			e.fake.Mu.Unlock()
		})
	}
}

func TestOneFailingSourceDoesNotStopOthers(t *testing.T) {
	e := setup(t)
	ok := t.TempDir()
	os.WriteFile(filepath.Join(ok, "f"), []byte("x"), 0o600)
	addSource(t, e, &store.Source{Name: "gone", Kind: store.KindDir, Path: filepath.Join(ok, "missing")})
	addSource(t, e, &store.Source{Name: "ok", Kind: store.KindDir, Path: ok})
	job, detail, err := e.r.Backup(context.Background(), e.cred, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.StatusFailed || len(detail.Sources) != 2 || detail.Sources[1].Status != store.StatusOK {
		t.Fatalf("job = %+v detail = %+v %+v", job, detail.Sources[0], detail.Sources[1])
	}
	if e.fake.SnapshotCount() != 1 {
		t.Fatal("the healthy source was not backed up")
	}
}

func TestBackupRefusedWhileJobRunning(t *testing.T) {
	e := setup(t)
	addSource(t, e, &store.Source{Name: "ok", Kind: store.KindDir, Path: t.TempDir()})
	// This process holds the lock (alive), as a concurrent run would.
	pid := os.Getpid()
	if _, err := e.r.Store.ClaimJob(context.Background(), e.cred.ID, store.JobBackup, store.TriggerManual,
		pid, processStart(pid), processAlive); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.r.Backup(context.Background(), e.cred, BackupOptions{})
	var running *store.ErrJobRunning
	if !errors.As(err, &running) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessLiveness(t *testing.T) {
	pid := os.Getpid()
	start := processStart(pid)
	if start == "" || !processAlive(pid, start) {
		t.Fatalf("own process not alive (start %q)", start)
	}
	if processAlive(pid, "1") {
		t.Fatal("PID reuse not detected (start time mismatch)")
	}
	if processAlive(1<<22+12345, "") {
		t.Fatal("nonexistent PID reported alive")
	}
}

func TestPartialStatusPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	e := setup(t)
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "ok"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(d, "locked"), []byte("x"), 0o000)
	addSource(t, e, &store.Source{Name: "d", Kind: store.KindDir, Path: d})
	job, detail, err := e.r.Backup(context.Background(), e.cred, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.StatusPartial || detail.Sources[0].SkippedTotal != 1 {
		t.Fatalf("job = %+v detail = %+v", job, detail.Sources[0])
	}
	jobs, _ := e.r.Store.ListJobs(context.Background(), e.cred.ID, 1)
	if jobs[0].Status != store.StatusPartial || !strings.Contains(string(jobs[0].Detail), "locked") {
		t.Fatalf("recorded job = %+v", jobs[0])
	}
}
