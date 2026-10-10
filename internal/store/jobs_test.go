package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func credForJobs(t *testing.T, s *Store) *Credential {
	c := newCred("one", tenantA)
	if err := s.CreateCredential(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaimJobExclusiveAndReleasedByFinish(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	c := credForJobs(t, s)
	alive := func(int, string) bool { return true }

	j, err := s.ClaimJob(ctx, c.ID, JobBackup, TriggerManual, 100, "1", alive)
	if err != nil {
		t.Fatal(err)
	}
	var running *ErrJobRunning
	if _, err := s.ClaimJob(ctx, c.ID, JobRestore, TriggerManual, 101, "2", alive); !errors.As(err, &running) || running.Job.ID != j.ID {
		t.Fatalf("second claim err = %v", err)
	}
	if err := s.FinishJob(j.ID, StatusOK, map[string]int{"x": 1}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimJob(ctx, c.ID, JobRestore, TriggerManual, 101, "2", alive); err != nil {
		t.Fatalf("claim after finish: %v", err)
	}
}

func TestClaimJobTakesOverDeadHolder(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	c := credForJobs(t, s)
	old, _ := s.ClaimJob(ctx, c.ID, JobBackup, TriggerManual, 100, "1", nil)
	dead := func(int, string) bool { return false }
	if _, err := s.ClaimJob(ctx, c.ID, JobBackup, TriggerManual, 200, "9", dead); err != nil {
		t.Fatalf("takeover failed: %v", err)
	}
	jobs, _ := s.ListJobs(ctx, c.ID, 10)
	for _, j := range jobs {
		if j.ID == old.ID && j.Status != StatusAborted {
			t.Fatalf("stale job status = %s, want %s", j.Status, StatusAborted)
		}
	}
}

func TestClaimJobConcurrentOnlyOneWins(t *testing.T) {
	s, _ := openTest(t)
	c := credForJobs(t, s)
	alive := func(int, string) bool { return true }
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.ClaimJob(context.Background(), c.ID, JobBackup, TriggerManual, 1000+i, "x", alive); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims won, want 1", wins)
	}
}

func TestDeletingCredentialCascades(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	c := credForJobs(t, s)
	s.CreateSource(ctx, &Source{CredentialID: c.ID, Name: "www", Kind: KindDir, Path: "/var/www", Enabled: true})
	s.ClaimJob(ctx, c.ID, JobBackup, TriggerManual, 1, "1", nil)
	if err := s.DeleteCredential(ctx, c.Name); err != nil {
		t.Fatal(err)
	}
	if srcs, _ := s.ListSources(ctx, c.ID); len(srcs) != 0 {
		t.Fatal("sources survived credential delete")
	}
	if jobs, _ := s.ListJobs(ctx, c.ID, 10); len(jobs) != 0 {
		t.Fatal("jobs survived credential delete")
	}
}

func TestSourceValidation(t *testing.T) {
	ok := []Source{
		{Name: "www", Kind: KindDir, Path: "/var/www/../www/"},
		{Name: "shop", Kind: KindMySQL, DBName: "shop_db", DBUser: "backup", DBPassword: `p"a\ss:w0rd`},
		{Name: "app", Kind: KindPostgres, DBName: "app", DBHost: "127.0.0.1", DBPort: 5432},
		{Name: "app2", Kind: KindPostgres, DBName: "app", DBSocket: "/var/run/postgresql/.s.PGSQL.5432"},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
	bad := []Source{
		{Name: "rel", Kind: KindDir, Path: "var/www"},
		{Name: "flag", Kind: KindMySQL, DBName: "--all-databases", DBUser: "x"},
		{Name: "space", Kind: KindMySQL, DBName: "a b", DBUser: "x"},
		{Name: "nouser", Kind: KindMySQL, DBName: "a"},
		{Name: "host", Kind: KindPostgres, DBName: "a", DBHost: "-oProxyCommand=x"},
		{Name: "nl", Kind: KindMySQL, DBName: "a", DBUser: "x", DBPassword: "a\nb"},
		{Name: "kind", Kind: "mongo", DBName: "a"},
		{Name: "port", Kind: KindPostgres, DBName: "a", DBPort: 70000},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s accepted, want error", s.Name)
		}
	}
}
