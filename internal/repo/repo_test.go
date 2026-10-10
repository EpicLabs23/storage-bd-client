package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

const (
	tenantID  = "3f1c2b6e-8a4d-4c1e-9f0a-1b2c3d4e5f60"
	installID = "9a2e4c6b-1d3f-4a5b-8c7d-9e0f1a2b3c4d"
)

var installSecret = strings.Repeat("ab", 32)

// fakeStorage is a minimal storage.bd: data-token issuance, escrow, and a
// restic REST-protocol repo at /repo/ that requires a valid data token,
// with data/ blob reads redirected to a separate "S3" server (§03).
type fakeStorage struct {
	mu          sync.Mutex
	files       map[string][]byte
	tokens      map[string]bool
	issued      int
	escrowed    string
	escrowFails bool
	redirects   int
	s3SawAuth   bool
	api, s3     *httptest.Server
}

func newFakeStorage(t *testing.T) *fakeStorage {
	f := &fakeStorage{files: map[string][]byte{}, tokens: map[string]bool{}}
	f.s3 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if r.Header.Get("Authorization") != "" {
			f.s3SawAuth = true
		}
		b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/")]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	}))
	f.api = httptest.NewServer(http.HandlerFunc(f.serveAPI))
	t.Cleanup(func() { f.api.Close(); f.s3.Close() })
	return f
}

func (f *fakeStorage) validToken(r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	f.mu.Lock()
	defer f.mu.Unlock()
	return ok && f.tokens[pass]
}

func (f *fakeStorage) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/auth/data-token" && r.Method == http.MethodPost:
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["install_secret"] != installSecret || b["tenant_id"] != tenantID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.issued++
		tok := fmt.Sprintf("dt-%d", f.issued)
		f.tokens[tok] = true
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 900})
	case r.URL.Path == "/escrow/password" && r.Method == http.MethodPut:
		if !f.validToken(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.escrowFails {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.escrowed = b["password"]
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/repo/"):
		if !f.validToken(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.serveRepo(w, r, strings.TrimPrefix(r.URL.Path, "/repo/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeStorage) serveRepo(w http.ResponseWriter, r *http.Request, p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case p == "" && r.Method == http.MethodPost: // create repo
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(p, "/") && r.Method == http.MethodGet: // list type
		prefix := p
		type entry struct {
			Name string `json:"name"`
			Size int    `json:"size"`
		}
		var v2 []entry
		var v1 []string
		for k, b := range f.files {
			if strings.HasPrefix(k, prefix) {
				name := strings.TrimPrefix(k, prefix)
				v2 = append(v2, entry{name, len(b)})
				v1 = append(v1, name)
			}
		}
		sort.Strings(v1)
		if strings.Contains(r.Header.Get("Accept"), "vnd.x.restic.rest.v2") {
			w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v2")
			if v2 == nil {
				v2 = []entry{}
			}
			json.NewEncoder(w).Encode(v2)
			return
		}
		if v1 == nil {
			v1 = []string{}
		}
		json.NewEncoder(w).Encode(v1)
	default:
		b, exists := f.files[p]
		switch r.Method {
		case http.MethodHead:
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		case http.MethodGet:
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if strings.HasPrefix(p, "data/") {
				f.redirects++
				http.Redirect(w, r, f.s3.URL+"/"+p, http.StatusFound)
				return
			}
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
		case http.MethodPost:
			data, _ := io.ReadAll(r.Body)
			f.files[p] = data
		case http.MethodDelete:
			delete(f.files, p)
		}
	}
}

func resticOrSkip(t *testing.T) string {
	for _, p := range []string{os.Getenv(restic.EnvBinary), "/usr/bin/restic", "/usr/local/bin/restic"} {
		if p != "" && restic.VerifyBinary(p) == nil {
			return p
		}
	}
	t.Skip("no usable restic binary; set STORAGEBD_RESTIC")
	return ""
}

func setup(t *testing.T) (*Manager, *store.Credential, *fakeStorage) {
	t.Helper()
	bin := resticOrSkip(t)
	f := newFakeStorage(t)
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
	cred := &store.Credential{Name: "test", APIURL: f.api.URL, TenantID: tenantID, InstallID: installID,
		InstallSecret: installSecret, ResticPassword: pw}
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
	if f.escrowed != cred.ResticPassword {
		t.Fatal("password not escrowed")
	}
	if _, ok := f.files["config"]; !ok {
		t.Fatal("repo not initialized")
	}
	stored, _ := m.Store.GetCredentialByName(ctx, "test")
	if stored.RepoInitializedAt == nil || stored.PasswordEscrowedAt == nil {
		t.Fatalf("setup not recorded: %+v", stored)
	}

	// Second open: no re-init, no re-escrow.
	f.escrowed = ""
	o, err = m.Open(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if f.escrowed != "" {
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
	if f.redirects == 0 {
		t.Fatal("restore never took the presigned-redirect path")
	}
	if f.s3SawAuth {
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
	f.escrowFails = true
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
	f.escrowFails = false
	o, err = m.Open(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if f.escrowed != cred.ResticPassword {
		t.Fatal("escrow not retried")
	}
}

func TestBadInstallSecretFailsBeforeRestic(t *testing.T) {
	m, cred, f := setup(t)
	cred.InstallSecret = strings.Repeat("cd", 32)
	_, err := m.Open(context.Background(), cred)
	if err == nil || !strings.Contains(err.Error(), "rejected this credential") {
		t.Fatalf("err = %v", err)
	}
	if len(f.files) != 0 {
		t.Fatal("restic touched the repo")
	}
	var re *restic.Error
	if errors.As(err, &re) {
		t.Fatal("should fail at token mint, not in restic")
	}
}
