// Package paths resolves where the client keeps its local state (the
// SQLite database, restic's cache, password files handed to restic).
//
// Everything lives under one directory, created 0700: the database holds
// install secrets and restic passwords, so the directory itself is the
// access boundary, the same trust model as §19's 0600 RESTIC_PASSWORD_FILE.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvHome overrides the resolved data directory (tests, multiple
// profiles on one machine).
const EnvHome = "STORAGEBD_HOME"

// DataDir returns the client's data directory without creating it.
// Root uses /var/lib/storagebd (a server-wide backup client); any other
// user gets $XDG_DATA_HOME/storagebd, falling back to ~/.local/share.
func DataDir(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	if env := os.Getenv(EnvHome); env != "" {
		return filepath.Abs(env)
	}
	if os.Geteuid() == 0 {
		return "/var/lib/storagebd", nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "storagebd"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "storagebd"), nil
}

// EnsureDataDir creates dir 0700 if missing and refuses to use an
// existing one that other users can read or write. Tightening the mode
// silently would hide that secrets may already have been exposed.
func EnsureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not create data directory %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("data directory %s has mode %04o; it holds secrets and must not be accessible to other users (run: chmod 700 %s)",
			dir, info.Mode().Perm(), dir)
	}
	return nil
}

// DBPath is the SQLite database inside the data directory.
func DBPath(dir string) string {
	return filepath.Join(dir, "storagebd.db")
}
