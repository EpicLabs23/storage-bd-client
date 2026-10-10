package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

// seed makes `days` daily snapshots (ending yesterday) for each named
// source, plus one snapshot NOT made by storagebd (no storagebd tag).
func seed(t *testing.T, e *env, days int, sources ...string) {
	t.Helper()
	ctx := context.Background()
	sess, err := e.r.Repos.Open(ctx, e.cred)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	for _, src := range sources {
		for d := days; d >= 1; d-- {
			os.WriteFile(f, []byte(src+time.Now().String()), 0o600)
			at := time.Now().AddDate(0, 0, -d).Truncate(time.Hour)
			if _, err := sess.Backup(ctx, restic.BackupOptions{Paths: []string{dir}, Tags: []string{TagClient, SourceTag(src)}, Time: at}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := sess.Backup(ctx, restic.BackupOptions{Paths: []string{dir}, Tags: []string{"other-tool"}}); err != nil {
		t.Fatal(err)
	}
}

func countSnaps(t *testing.T, e *env, tags ...string) int {
	sess, err := e.r.Repos.Open(context.Background(), e.cred)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	snaps, err := sess.Snapshots(context.Background(), tags...)
	if err != nil {
		t.Fatal(err)
	}
	return len(snaps)
}

func TestCleanupNeedsPolicy(t *testing.T) {
	e := setup(t)
	if _, _, err := e.r.Cleanup(context.Background(), e.cred, CleanupOptions{}); !errors.Is(err, ErrNoRetention) {
		t.Fatalf("err = %v", err)
	}
	if err := e.r.Store.SaveRetention(context.Background(), &store.Retention{CredentialID: e.cred.ID, Enabled: true, PruneIntervalDays: 7}); err == nil {
		t.Fatal("policy keeping nothing was accepted")
	}
}

func TestPreviewChangesNothingThenCleanupApplies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	seed(t, e, 5, "www", "db")
	pol := &store.Retention{CredentialID: e.cred.ID, Enabled: true, KeepDaily: 2, PruneIntervalDays: 7}

	prev, err := e.r.PreviewCleanup(ctx, e.cred, pol)
	if err != nil {
		t.Fatal(err)
	}
	if !prev.DryRun || prev.Removed != 6 || len(prev.Groups) != 2 {
		t.Fatalf("preview = %+v", prev)
	}
	for _, g := range prev.Groups {
		if g.Kept != 2 || len(g.Removed) != 3 || (g.Source != "www" && g.Source != "db") {
			t.Fatalf("group = %+v", g)
		}
	}
	if n := countSnaps(t, e); n != 11 {
		t.Fatalf("preview removed snapshots: %d left", n)
	}

	if err := e.r.Store.SaveRetention(ctx, pol); err != nil {
		t.Fatal(err)
	}
	packsBefore := countPacks(e)
	job, d, err := e.r.Cleanup(ctx, e.cred, CleanupOptions{Trigger: store.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.StatusOK || d.Removed != 6 || !d.Pruned || d.PruneReport == "" {
		t.Fatalf("job = %+v detail = %+v", job, d)
	}
	if n := countSnaps(t, e, TagClient); n != 4 {
		t.Fatalf("storagebd snapshots left = %d, want 4", n)
	}
	if n := countSnaps(t, e, "other-tool"); n != 1 {
		t.Fatal("cleanup touched a snapshot storagebd didn't make")
	}
	if countPacks(e) >= packsBefore {
		t.Fatalf("prune freed nothing: packs %d -> %d", packsBefore, countPacks(e))
	}
	got, _ := e.r.Store.GetRetention(ctx, e.cred.ID)
	if got.LastForgetAt == nil || got.LastPruneAt == nil {
		t.Fatalf("run times not recorded: %+v", got)
	}

	// Second run: prune isn't due yet, only forget.
	_, d, err = e.r.Cleanup(ctx, e.cred, CleanupOptions{})
	if err != nil || d.Pruned || d.Removed != 0 {
		t.Fatalf("second run: %+v %v", d, err)
	}
	// ...unless forced.
	if _, d, _ = e.r.Cleanup(ctx, e.cred, CleanupOptions{ForcePrune: true}); !d.Pruned {
		t.Fatal("forced prune didn't run")
	}
}

func TestKeepRulesNeverEmptyASource(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	seed(t, e, 3, "old")
	// Every snapshot is older than a "keep 1 daily" window would cover in
	// calendar terms; restic still keeps the newest one.
	e.r.Store.SaveRetention(ctx, &store.Retention{CredentialID: e.cred.ID, Enabled: true, KeepDaily: 1, PruneIntervalDays: 7})
	if _, _, err := e.r.Cleanup(ctx, e.cred, CleanupOptions{SkipPrune: true}); err != nil {
		t.Fatal(err)
	}
	if n := countSnaps(t, e, TagClient, SourceTag("old")); n != 1 {
		t.Fatalf("source left with %d snapshots, want 1", n)
	}
}

func TestAfterBackupRunsOnlyWithPolicyAndSuccess(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o600)
	addSource(t, e, &store.Source{Name: "s", Kind: store.KindDir, Path: src})

	job, _, _ := e.r.Backup(ctx, e.cred, BackupOptions{})
	if cj, _, err := e.r.AfterBackup(ctx, e.cred, job, CleanupOptions{}); cj != nil || err != nil {
		t.Fatalf("ran without a policy: %v %v", cj, err)
	}
	e.r.Store.SaveRetention(ctx, &store.Retention{CredentialID: e.cred.ID, Enabled: true, KeepLast: 1, PruneIntervalDays: 7})
	if cj, _, _ := e.r.AfterBackup(ctx, e.cred, &store.Job{Status: store.StatusFailed}, CleanupOptions{}); cj != nil {
		t.Fatal("ran after a failed backup")
	}
	job, _, _ = e.r.Backup(ctx, e.cred, BackupOptions{})
	cj, d, err := e.r.AfterBackup(ctx, e.cred, job, CleanupOptions{Trigger: store.TriggerSchedule})
	if err != nil || cj == nil || cj.Kind != store.JobCleanup || d.Removed != 1 {
		t.Fatalf("after backup: %+v %+v %v", cj, d, err)
	}
}

func countPacks(e *env) int {
	e.fake.Mu.Lock()
	defer e.fake.Mu.Unlock()
	n := 0
	for k := range e.fake.Files {
		if len(k) > 5 && k[:5] == "data/" {
			n++
		}
	}
	return n
}
