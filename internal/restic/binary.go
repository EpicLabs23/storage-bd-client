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
	"time"

	"storage-bd-client/internal/execsafe"
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
			dir := filepath.Dir(real)
			// <prefix>/bin/storagebd → <prefix>/lib/storagebd/restic is
			// where install.sh puts it (/usr/local or ~/.local); next to
			// the binary covers an unpacked release tarball.
			out = append(out, filepath.Join(dir, "..", "lib", "storagebd", "restic"), filepath.Join(dir, "restic"))
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

// VerifyBinary is execsafe.VerifyBinary, kept here for callers that
// only deal with restic.
func VerifyBinary(path string) error { return execsafe.VerifyBinary(path) }

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
