// Package testutil holds test doubles shared across packages: a fake
// storage.bd (data tokens, escrow, restic REST repo with S3-style
// redirects). Not used by production code.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"storage-bd-client/internal/restic"
)

const (
	TenantID  = "3f1c2b6e-8a4d-4c1e-9f0a-1b2c3d4e5f60"
	InstallID = "9a2e4c6b-1d3f-4a5b-8c7d-9e0f1a2b3c4d"
)

var InstallSecret = strings.Repeat("ab", 32)

// FakeStorage is a minimal storage.bd: data-token issuance, escrow, and a
// restic REST-protocol repo at /repo/ that requires a valid data token,
// with data/ blob reads redirected to a separate "S3" server (§03).
type FakeStorage struct {
	Mu          sync.Mutex
	Files       map[string][]byte
	Tokens      map[string]bool
	Issued      int
	Escrowed    string
	EscrowFails bool
	Redirects   int
	S3SawAuth   bool
	API, S3     *httptest.Server

	// account side (fakeaccount.go)
	Balance float64
	tenants map[string]map[string]any
	refresh map[string]bool
}

func NewFakeStorage(t testing.TB) *FakeStorage {
	f := StartFakeStorage()
	t.Cleanup(f.Close)
	return f
}

// StartFakeStorage starts the fake without a test (dev runner).
func StartFakeStorage() *FakeStorage {
	f := &FakeStorage{Files: map[string][]byte{}, Tokens: map[string]bool{}, tenants: map[string]map[string]any{}, refresh: map[string]bool{}, Balance: 500}
	f.S3 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.Mu.Lock()
		if r.Header.Get("Authorization") != "" {
			f.S3SawAuth = true
		}
		b, ok := f.Files[strings.TrimPrefix(r.URL.Path, "/")]
		f.Mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	}))
	f.API = httptest.NewServer(http.HandlerFunc(f.serveAPI))
	return f
}

func (f *FakeStorage) Close() { f.API.Close(); f.S3.Close() }

func (f *FakeStorage) validToken(r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return ok && f.Tokens[pass]
}

func (f *FakeStorage) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/auth/data-token" && r.Method == http.MethodPost:
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.Mu.Lock()
		_, created := f.tenants[b["tenant_id"]]
		f.Mu.Unlock()
		if b["install_secret"] != InstallSecret || (b["tenant_id"] != TenantID && !created) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.Mu.Lock()
		f.Issued++
		tok := fmt.Sprintf("dt-%d", f.Issued)
		f.Tokens[tok] = true
		f.Mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 900})
	case r.URL.Path == "/escrow/password" && r.Method == http.MethodPut:
		if !f.validToken(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.EscrowFails {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.Mu.Lock()
		f.Escrowed = b["password"]
		f.Mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/repo/"):
		if !f.validToken(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.serveRepo(w, r, strings.TrimPrefix(r.URL.Path, "/repo/"))
	default:
		f.serveAccount(w, r)
	}
}

func (f *FakeStorage) serveRepo(w http.ResponseWriter, r *http.Request, p string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
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
		for k, b := range f.Files {
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
		b, exists := f.Files[p]
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
				f.Redirects++
				http.Redirect(w, r, f.S3.URL+"/"+p, http.StatusFound)
				return
			}
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
		case http.MethodPost:
			data, _ := io.ReadAll(r.Body)
			f.Files[p] = data
		case http.MethodDelete:
			delete(f.Files, p)
		}
	}
}

// SnapshotCount is how many snapshot files the fake repo holds.
func (f *FakeStorage) SnapshotCount() int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	n := 0
	for k := range f.Files {
		if strings.HasPrefix(k, "snapshots/") {
			n++
		}
	}
	return n
}

// ResticOrSkip returns a usable restic binary or skips the test.
// $STORAGEBD_RESTIC, if set, must be usable: silently testing a different
// restic than the one asked for would hide version-specific breakage.
func ResticOrSkip(t testing.TB) string {
	if p := os.Getenv(restic.EnvBinary); p != "" {
		if err := restic.VerifyBinary(p); err != nil {
			t.Fatalf("%s is set but unusable: %v", restic.EnvBinary, err)
		}
		logVersion(t, p)
		return p
	}
	for _, p := range []string{"/usr/bin/restic", "/usr/local/bin/restic"} {
		if restic.VerifyBinary(p) == nil {
			logVersion(t, p)
			return p
		}
	}
	t.Skip("no usable restic binary; set STORAGEBD_RESTIC")
	return ""
}

var loggedVersion sync.Once

func logVersion(t testing.TB, p string) {
	loggedVersion.Do(func() {
		v, err := restic.Version(context.Background(), p)
		if err != nil {
			t.Fatalf("restic at %s: %v", p, err)
		}
		fmt.Fprintf(os.Stderr, "testing with restic %s (%s)\n", v, p)
	})
}
