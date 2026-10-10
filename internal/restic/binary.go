// Package restic runs the official restic binary (§19): pinned path,
// argument slices only, constructed minimal environment, password via
// RESTIC_PASSWORD_FILE, repo reached through a loopback Relay.
package restic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// EnvBinary overrides restic binary resolution.
const EnvBinary = "STORAGEBD_RESTIC"

// Minimum restic version: 0.14 introduced repo format v2 (compression)
// and stable --json output for backup.
const minMajor, minMinor = 0, 14

// Candidates for a bundled restic, checked in order after an explicit
// setting. Never $PATH (§19): a writable directory earlier in PATH would
// let another user substitute a binary that then runs with our privileges.
func bundledCandidates() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			out = append(out, filepath.Join(filepath.Dir(real), "restic"))
		}
	}
	return append(out, "/usr/lib/storagebd/restic", "/usr/local/lib/storagebd/restic")
}

// Locate resolves the restic binary: explicit (from settings or flag),
// then $STORAGEBD_RESTIC, then the bundled locations.
func Locate(explicit string) (string, error) {
	if explicit != "" {
		return explicit, VerifyBinary(explicit)
	}
	if env := os.Getenv(EnvBinary); env != "" {
		return env, VerifyBinary(env)
	}
	for _, c := range bundledCandidates() {
		if VerifyBinary(c) == nil {
			return c, nil
		}
	}
	return "", errors.New("restic binary not found. Install storagebd's bundled restic, or point at one: " +
		"storagebd settings set restic-path /usr/bin/restic")
}

// VerifyBinary checks that path is an absolute, regular, executable file
// that no other user can modify. A storagebd running as root executes
// it with root's privileges, so an attacker-writable binary (or a
// directory it lives in) would be a privilege escalation.
func VerifyBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("restic path %q must be absolute", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("restic binary %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("restic binary %s is not an executable file", real)
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
		return fmt.Errorf("%s is writable by other users; refusing to run restic from it", p)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := uint32(os.Geteuid())
		if st.Uid != 0 && st.Uid != uid {
			return fmt.Errorf("%s is owned by another user (uid %d); refusing to run restic from it", p, st.Uid)
		}
	}
	return nil
}

var versionRe = regexp.MustCompile(`restic (\d+)\.(\d+)\.(\d+)`)

// Version runs `restic version` and checks it meets the minimum.
func Version(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not run %s: %w", bin, err)
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return "", fmt.Errorf("%s does not look like restic: %q", bin, out)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minMajor || (major == minMajor && minor < minMinor) {
		return "", fmt.Errorf("restic %s.%s is too old; storagebd needs %d.%d or newer", m[1], m[2], minMajor, minMinor)
	}
	return m[1] + "." + m[2] + "." + m[3], nil
}
