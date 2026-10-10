package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/update"
)

// Install locations written by packaging/install.sh.
const systemBin = "/usr/local/bin/storagebd"

// git describe output (v0.1.0-3-gabc1234[-dirty]) or a dirty tree: not a release.
var devBuildRE = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$|-dirty$`)

func isDevBuild(v string) bool {
	return !update.ValidVersion(v) || devBuildRE.MatchString(v)
}

func newUpgradeCmd(_ *app) *cobra.Command {
	var check, force bool
	var version string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the latest signed release of storagebd",
		Long: "Downloads the latest storagebd release (or --version), checks its signature against the\n" +
			"release key built into this program, and installs it the same way it was installed\n" +
			"(server-wide with sudo, or --user). The service is restarted if it was running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			ctx := cmd.Context()
			keys, err := update.ReleaseKeys()
			if err != nil {
				return err
			}
			c := update.New(keys)

			target := version
			if target != "" {
				if !strings.HasPrefix(target, "v") {
					target = "v" + target
				}
				if !update.ValidVersion(target) {
					return fmt.Errorf("invalid version %q (want e.g. v0.2.0)", version)
				}
			} else if target, err = c.Latest(ctx); err != nil {
				return err
			}

			dev := isDevBuild(Version)
			if check {
				label := "Latest:   "
				if version != "" {
					label = "Requested:"
				}
				fmt.Fprintf(out, "Installed: %s\n%s %s\n", Version, label, target)
				if !dev && update.Compare(target, Version) > 0 {
					fmt.Fprintln(out, "An upgrade is available: run `storagebd upgrade` (with sudo for a server-wide install).")
				}
				return nil
			}
			switch {
			case dev && version == "":
				return fmt.Errorf("this is a development build (%s); pass --version to install a release", Version)
			case !dev && !force && update.Compare(target, Version) == 0:
				fmt.Fprintf(out, "storagebd %s is already installed.\n", Version)
				return nil
			case !dev && update.Compare(target, Version) < 0:
				if version == "" {
					fmt.Fprintf(out, "The latest release (%s) is older than this one (%s); nothing to do.\n", target, Version)
					return nil
				}
				fmt.Fprintf(out, "Note: %s is older than the installed %s.\n", target, Version)
			}

			if len(keys) == 0 {
				return errors.New("this build of storagebd has no release signing key, so it can't verify upgrades; " +
					"upgrade with the install command in docs/INSTALL.md instead")
			}
			installArgs, err := installMode()
			if err != nil {
				return err
			}
			arch := runtime.GOARCH
			if arch != "amd64" && arch != "arm64" {
				return fmt.Errorf("no storagebd release for %s", arch)
			}

			tmp, err := os.MkdirTemp("", "storagebd-upgrade-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)
			fmt.Fprintf(out, "Downloading storagebd %s for linux/%s...\n", target, arch)
			pkg, keyID, err := c.Download(ctx, target, arch, tmp)
			if errors.Is(err, update.ErrUnsigned) {
				return fmt.Errorf("%w; `storagebd upgrade` only installs signed releases (v0.1.1 and later)", err)
			} else if err != nil {
				return err
			}
			fmt.Fprintf(out, "Signature OK (release key %s), checksum OK.\n", keyID)

			if !serviceActive(installArgs) {
				installArgs = append(installArgs, "--no-start")
			}
			// Absolute sh: install.sh comes from the verified package, not PATH.
			run := exec.CommandContext(ctx, "/bin/sh", append([]string{filepath.Join(pkg, "install.sh")}, installArgs...)...)
			run.Stdout, run.Stderr = out, cmd.ErrOrStderr()
			if err := run.Run(); err != nil {
				return fmt.Errorf("install.sh: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only show whether a newer release is available")
	cmd.Flags().StringVar(&version, "version", "", "install this release instead of the latest (e.g. v0.2.0)")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if this version is already installed")
	return cmd
}

// installMode works out how this copy was installed, so the upgrade replaces
// it in place: install.sh's system or --user layout.
func installMode() ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	root := os.Geteuid() == 0
	home, _ := os.UserHomeDir()
	switch {
	case exe == systemBin && root:
		return nil, nil
	case exe == systemBin:
		return nil, errors.New("storagebd is installed server-wide; run: sudo storagebd upgrade")
	case home != "" && exe == filepath.Join(home, ".local", "bin", "storagebd") && !root:
		return []string{"--user"}, nil
	default:
		return nil, fmt.Errorf("%s wasn't installed by storagebd's installer, so upgrade can't replace it; "+
			"install the new release as in docs/INSTALL.md", exe)
	}
}

func serviceActive(installArgs []string) bool {
	args := []string{"is-active", "--quiet", "storagebd.service"}
	if len(installArgs) > 0 && installArgs[0] == "--user" {
		args = append([]string{"--user"}, args...)
	}
	sc, err := exec.LookPath("systemctl")
	if err != nil {
		return false
	}
	return exec.Command(sc, args...).Run() == nil
}
