package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDataDirCreates0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode = %04o, want 0700", perm)
	}
}

func TestEnsureDataDirRejectsGroupOrWorldAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	os.Mkdir(dir, 0o755)
	os.Chmod(dir, 0o755) // defeat umask
	if err := EnsureDataDir(dir); err == nil {
		t.Fatal("expected error for 0755 data dir")
	}
}

func TestDataDirPrecedence(t *testing.T) {
	t.Setenv(EnvHome, "/tmp/from-env")
	if got, _ := DataDir("/tmp/from-flag"); got != "/tmp/from-flag" {
		t.Fatalf("flag should win, got %s", got)
	}
	if got, _ := DataDir(""); got != "/tmp/from-env" {
		t.Fatalf("env should apply, got %s", got)
	}
}
