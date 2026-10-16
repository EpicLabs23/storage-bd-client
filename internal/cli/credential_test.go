package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run executes the CLI against a throwaway --home and returns stdout and
// the error.
func run(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--home", home}, args...))
	err := cmd.Execute()
	return out.String(), err
}

// The pre-rename flags (--tenant-id, --install-id, --install-secret-file)
// must keep working for existing scripts, stay out of --help, and not be
// combinable with their new spellings.
func TestCredentialFlagNames(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		space1 = "11111111-1111-4111-8111-111111111111"
		space2 = "33333333-3333-4333-8333-333333333333"
		key    = "22222222-2222-4222-8222-222222222222"
	)

	if _, err := run(t, home, "credential", "add", "new", "--space-id", space1, "--key-id", key, "--secret-file", secretFile); err != nil {
		t.Fatalf("new flags: %v", err)
	}
	if _, err := run(t, home, "credential", "add", "old", "--tenant-id", space2, "--install-id", key, "--install-secret-file", secretFile); err != nil {
		t.Fatalf("old flags: %v", err)
	}
	if _, err := run(t, home, "credential", "update", "old", "--install-secret-file", secretFile); err != nil {
		t.Fatalf("old update flag: %v", err)
	}

	out, err := run(t, home, "credential", "show", "old")
	if err != nil || !strings.Contains(out, "Space ID:") || !strings.Contains(out, space2) {
		t.Fatalf("show: err=%v out=%s", err, out)
	}

	if _, err := run(t, home, "credential", "add", "both", "--space-id", space1, "--tenant-id", space1, "--key-id", key, "--secret-file", secretFile); err == nil {
		t.Fatal("--space-id together with --tenant-id was accepted")
	}
	if _, err := run(t, home, "credential", "add", "none", "--secret-file", secretFile); err == nil || !strings.Contains(err.Error(), "--space-id and --key-id are required") {
		t.Fatalf("missing ids: err=%v", err)
	}

	help, _ := run(t, home, "credential", "add", "--help")
	for _, old := range []string{"--tenant-id", "--install-id", "--install-secret-file"} {
		if strings.Contains(help, old) {
			t.Errorf("--help still lists %s", old)
		}
	}
}
