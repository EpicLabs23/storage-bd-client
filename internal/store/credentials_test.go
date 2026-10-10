package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tenantA = "3f1c2b6e-8a4d-4c1e-9f0a-1b2c3d4e5f60"
	tenantB = "7d9e0f1a-2b3c-4d5e-8f6a-7b8c9d0e1f2a"
	install = "9a2e4c6b-1d3f-4a5b-8c7d-9e0f1a2b3c4d"
	secret  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func newCred(name, tenant string) *Credential {
	return &Credential{
		Name: name, APIURL: "https://api.storage.bd", TenantID: tenant, InstallID: install,
		InstallSecret: secret, ResticPassword: "correct-horse-battery-staple-42",
	}
}

func TestOpenCreatesFile0600AndIsReopenable(t *testing.T) {
	s, path := openTest(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("db mode = %04o, want 0600", perm)
	}
	s.Close()
	// Re-opening must not re-run migrations.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.Close()
}

func TestFirstCredentialBecomesDefault(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if err := s.CreateCredential(ctx, newCred("one", tenantA)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCredential(ctx, newCred("two", tenantB)); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetDefaultCredential(ctx)
	if err != nil || d.Name != "one" {
		t.Fatalf("default = %v, %v; want one", d, err)
	}
}

func TestSetDefaultIsExclusive(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	s.CreateCredential(ctx, newCred("one", tenantA))
	s.CreateCredential(ctx, newCred("two", tenantB))
	if err := s.SetDefaultCredential(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	creds, _ := s.ListCredentials(ctx)
	defaults := 0
	for _, c := range creds {
		if c.IsDefault {
			defaults++
			if c.Name != "two" {
				t.Fatalf("wrong default %q", c.Name)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("%d defaults, want 1", defaults)
	}
}

func TestDeleteDefaultPromotesAnother(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	s.CreateCredential(ctx, newCred("one", tenantA))
	s.CreateCredential(ctx, newCred("two", tenantB))
	if err := s.DeleteCredential(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetDefaultCredential(ctx)
	if err != nil || d.Name != "two" {
		t.Fatalf("default after delete = %v, %v; want two", d, err)
	}
	if err := s.DeleteCredential(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing = %v, want ErrNotFound", err)
	}
}

func TestUniqueConstraints(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	s.CreateCredential(ctx, newCred("one", tenantA))
	if err := s.CreateCredential(ctx, newCred("one", tenantB)); err == nil || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("duplicate name err = %v", err)
	}
	if err := s.CreateCredential(ctx, newCred("other", tenantA)); err == nil || !strings.Contains(err.Error(), "tenant already exists") {
		t.Fatalf("duplicate tenant err = %v", err)
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	c := newCred("one", tenantA)
	s.CreateCredential(ctx, c)
	c.Name = "renamed"
	c.AccountEmail = "a@example.com"
	if err := s.UpdateCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCredentialByName(ctx, "renamed")
	if err != nil || got.AccountEmail != "a@example.com" || !got.IsDefault {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.GetCredentialByName(ctx, "one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old name still resolves: %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]func(c *Credential){
		"bad name":        func(c *Credential) { c.Name = "Has Space" },
		"bad tenant":      func(c *Credential) { c.TenantID = "not-a-uuid" },
		"short secret":    func(c *Credential) { c.InstallSecret = "abc" },
		"short password":  func(c *Credential) { c.ResticPassword = "short" },
		"newline in pass": func(c *Credential) { c.ResticPassword = "aaaaaaaaaaaaaaaa\nbbbb" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := openTest(t)
			c := newCred("ok", tenantA)
			mutate(c)
			if err := s.CreateCredential(context.Background(), c); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateAPIURL(t *testing.T) {
	ok := map[string]string{
		"https://api.storage.bd/":  "https://api.storage.bd",
		"http://localhost:3000":    "http://localhost:3000",
		"http://127.0.0.1:3000":    "http://127.0.0.1:3000",
		" https://x.example:8443 ": "https://x.example:8443",
	}
	for in, want := range ok {
		got, err := ValidateAPIURL(in, false)
		if err != nil || got != want {
			t.Errorf("ValidateAPIURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"http://10.0.0.5:3000", "ftp://x", "https://user:pw@x", "https://x/path",
		"https://x?q=1", "api.storage.bd", "",
	}
	for _, in := range bad {
		if _, err := ValidateAPIURL(in, false); err == nil {
			t.Errorf("ValidateAPIURL(%q) accepted, want error", in)
		}
	}
	if _, err := ValidateAPIURL("http://10.0.0.5:3000", true); err != nil {
		t.Errorf("allowHTTP should permit LAN http: %v", err)
	}
}

func TestGeneratedPasswordIsStrongAndValid(t *testing.T) {
	a, _ := GenerateResticPassword()
	b, _ := GenerateResticPassword()
	if a == b || len(a) < 40 {
		t.Fatalf("weak/duplicate passwords: %q %q", a, b)
	}
	if err := ValidateResticPassword(a); err != nil {
		t.Fatal(err)
	}
}
