// Package execsafe vets external binaries before storagebd runs them.
package execsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// VerifyBinary checks that path is an absolute, regular, executable file
// that no other user can modify. A storagebd running as root executes
// it with root's privileges, so an attacker-writable binary (or a
// directory it lives in) would be a privilege escalation.
func VerifyBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("binary path %q must be absolute", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("binary %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", real)
	}
	if err := checkOwnerAndMode(real, info); err != nil {
		return err
	}
	if err := checkOwnerAndMode(filepath.Dir(real), nil); err != nil {
		return err
	}
	return nil
}

func checkOwnerAndMode(p string, info os.FileInfo) error {
	if info == nil {
		var err error
		if info, err = os.Stat(p); err != nil {
			return err
		}
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("%s is writable by other users; refusing to run it", p)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := uint32(os.Geteuid())
		if st.Uid != 0 && st.Uid != uid {
			return fmt.Errorf("%s is owned by another user (uid %d); refusing to run it", p, st.Uid)
		}
	}
	return nil
}

// FirstUsable returns the first candidate that passes VerifyBinary.
func FirstUsable(candidates ...string) (string, error) {
	for _, c := range candidates {
		if c != "" && VerifyBinary(c) == nil {
			return c, nil
		}
	}
	return "", errors.New("not found")
}
