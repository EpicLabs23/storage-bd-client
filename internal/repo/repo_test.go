package repo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
	"storage-bd-client/internal/testutil"
)

func setup(t *testing.T) (*Manager, *store.Credential, *testutil.FakeStorage) {
	t.Helper()
	bin := testutil.ResticOrSkip(t)
	f := testutil.NewFakeStorage(t)
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.SettingResticPath, bin); err != nil {
		t.Fatal(err)
	}
	pw, _ := store.GenerateResticPassword()
	cred := &store.Credential{Name: "test", APIURL: f.API.URL, TenantID: testutil.TenantID, InstallID: testutil.InstallID,
		InstallSecret: testutil.InstallSecret, ResticPassword: pw}
	if err := s.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	return &Manager{Store: s, DataDir: dir}, cred, f
}

func TestOpenInitializesAndEscrowsOnce(t *testing.T) {
	m, cred, f := setup(t)
	ctx := context.Background()
	o, err := m.Open(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if len(o.Warnings) != 0 {
		t.Fatalf("warnings: %v", o.Warnings)
	}
	if f.Escrowed != cred.ResticPassword {
		t.Fatal("password not escrowed")
	}
	if _, ok := f.Files["config"]; !ok {
		t.Fatal("repo not initialized")
	}
	stored, _ := m.Store.GetCredentialByName(ctx, "test")
	if stored.RepoInitializedAt == nil || stored.PasswordEscrowedAt == nil {
		t.Fatalf("setup not recorded: %+v", stored)
	}

	// Second open: no re-init, no re-escrow.
	f.Escrowed = ""
	o, err = m.Open(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if f.Escrowed != "" {
		t.Fatal("escrowed again")
	}
	// Password file is gone after Close.
	left, _ := filepath.Glob(filepath.Join(m.DataDir, "tmp", "pw-*"))
	if len(left) != 0 {
		t.Fatalf("password files left behind: %v", left)
	}
}

func TestBackupSnapshotsRestoreViaRedirect(t *testing.T) {
	m, cred, f := setup(t)
	ctx := context.Background()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello storage.bd"), 0o600)
	os.MkdirAll(filepath.Join(src, "sub"), 0o700)
	os.WriteFile(filepath.Join(src, "sub", "b.bin"), bytes.Repeat([]byte{7}, 3<<20), 0o600)

	o, err := m.Open(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	sum, err := o.Backup(ctx, restic.BackupOptions{Paths: []string{src}, Tags: []string{"storagebd"}, Host: "test-host"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Partial || sum.SnapshotID == "" || sum.TotalFilesProcessed != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	snaps, err := o.Snapshots(ctx, "storagebd")
	if err != nil || len(snaps) != 1 || snaps[0].Hostname != "test-host" {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}

	// Fresh cache so restore must actually fetch data blobs.
	os.RemoveAll(filepath.Join(m.DataDir, "cache"))
	target := t.TempDir()
	if err := o.Restore(ctx, restic.RestoreOptions{SnapshotID: sum.SnapshotID, Target: target}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, src, "a.txt"))
	if err != nil || string(got) != "hello storage.bd" {
		t.Fatalf("restored a.txt = %q, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(target, src, "sub", "b.bin")); err != nil || info.Size() != 3<<20 {
		t.Fatalf("restored b.bin: %v", err)
	}
	if f.Redirects == 0 {
		t.Fatal("restore never took the presigned-redirect path")
	}
	if f.S3SawAuth {
		t.Fatal("relay nonce leaked to the redirect target")
	}
}

func TestPartialBackupIsNotAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	m, cred, _ := setup(t)
	ctx := context.Background()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "ok.txt"), []byte("fine"), 0o600)
	os.WriteFile(filepath.Join(src, "secret.txt"), []byte("nope"), 0o000)

	o, err := m.Open(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	sum, err := o.Backup(ctx, restic.BackupOptions{Paths: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Partial || len(sum.Errors) == 0 || !strings.HasSuffix(sum.Errors[0].Item, "secret.txt") {
		t.Fatalf("summary = %+v errors = %+v", sum, sum.Errors)
	}
}

func TestStdinBackupAndDump(t *testing.T) {
	m, cred, _ := setup(t)
	ctx := context.Background()
	o, err := m.Open(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	dump := "CREATE TABLE t (id int);\nINSERT INTO t VALUES (1);\n"
	sum, err := o.Backup(ctx, restic.BackupOptions{Stdin: strings.NewReader(dump), StdinAs: "shop.sql", Tags: []string{"db:shop"}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := o.Dump(ctx, sum.SnapshotID, "/shop.sql", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != dump {
		t.Fatalf("dump = %q", out.String())
	}
}

func TestExistingRepoWithWrongPassword(t *testing.T) {
	m, cred, _ := setup(t)
	ctx := context.Background()
	o, err := m.Open(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()

	// Same tenant, but this "machine" never initialized it and holds a
	// different password.
	cred.RepoInitializedAt = nil
	cred.ResticPassword, _ = store.GenerateResticPassword()
	_, err = m.Open(ctx, cred)
	if err == nil || !strings.Contains(err.Error(), "does not open it") {
		t.Fatalf("err = %v", err)
	}
}

func TestEscrowFailureWarnsAndRetries(t *testing.T) {
	m, cred, f := setup(t)
	ctx := context.Background()
	f.EscrowFails = true
	o, err := m.Open(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "escrow") {
		t.Fatalf("warnings = %v", o.Warnings)
	}
	stored, _ := m.Store.GetCredentialByName(ctx, "test")
	if stored.PasswordEscrowedAt != nil || stored.RepoInitializedAt == nil {
		t.Fatalf("state = %+v", stored)
	}
	f.EscrowFails = false
	o, err = m.Open(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if f.Escrowed != cred.ResticPassword {
		t.Fatal("escrow not retried")
	}
}

func TestBadInstallSecretFailsBeforeRestic(t *testing.T) {
	m, cred, f := setup(t)
	cred.InstallSecret = strings.Repeat("cd", 32)
	_, err := m.Open(context.Background(), cred)
	if err == nil || !strings.Contains(err.Error(), "rejected this space access key") {
		t.Fatalf("err = %v", err)
	}
	if len(f.Files) != 0 {
		t.Fatal("restic touched the repo")
	}
	var re *restic.Error
	if errors.As(err, &re) {
		t.Fatal("should fail at token mint, not in restic")
	}
}
