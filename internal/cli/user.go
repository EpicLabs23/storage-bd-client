package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"storage-bd-client/internal/store"
	"storage-bd-client/internal/uiauth"
)

// Web UI users. Created only here, never from the page itself, so
// reaching the page doesn't let anyone claim it.
func newUserCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage who can sign in to the web UI",
	}

	var addPassFile string
	add := &cobra.Command{
		Use:   "add <username>",
		Short: "Create a web UI user (asks for the password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			if err := store.ValidateUsername(args[0]); err != nil {
				return err
			}
			hash, err := newPasswordHash(addPassFile)
			if err != nil {
				return err
			}
			if _, err := s.CreateUIUser(cmd.Context(), args[0], hash); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "User %s created. Sign in to the web UI with it (address: storagebd ui-url).\n", args[0])
			return nil
		},
	}
	add.Flags().StringVar(&addPassFile, "password-file", "", "read the password from this file's first line (- for stdin)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List web UI users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			users, err := s.ListUIUsers(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(users) == 0 {
				fmt.Fprintln(out, "No users. Create one with: storagebd user add <username>")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "USERNAME\tTWO-STEP\tCREATED\tCHANGED")
			for _, u := range users {
				twoStep := "off"
				if u.TOTPEnabled {
					twoStep = "on"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Username, twoStep,
					u.CreatedAt.Local().Format("2006-01-02 15:04"), u.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}

	var pwPassFile string
	passwd := &cobra.Command{
		Use:   "passwd <username>",
		Short: "Set a new password for a user and sign them out everywhere",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			u, err := s.GetUIUser(cmd.Context(), args[0])
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no user %q (see: storagebd user list)", args[0])
			} else if err != nil {
				return err
			}
			hash, err := newPasswordHash(pwPassFile)
			if err != nil {
				return err
			}
			if err := s.SetUIUserPassword(cmd.Context(), u.ID, hash, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Password changed for %s; signed out of every browser.\n", u.Username)
			return nil
		},
	}
	passwd.Flags().StringVar(&pwPassFile, "password-file", "", "read the password from this file's first line (- for stdin)")

	signout := &cobra.Command{
		Use:   "signout <username>",
		Short: "Sign a user out of every browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			u, err := s.GetUIUser(cmd.Context(), args[0])
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no user %q (see: storagebd user list)", args[0])
			} else if err != nil {
				return err
			}
			n, err := s.DeleteUISessionsForUser(cmd.Context(), u.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed %s out of %d browser session(s).\n", u.Username, n)
			return nil
		},
	}

	remove := &cobra.Command{
		Use:   "remove <username>",
		Short: "Delete a web UI user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			if err := s.DeleteUIUser(cmd.Context(), args[0]); errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no user %q (see: storagebd user list)", args[0])
			} else if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s.\n", args[0])
			return nil
		},
	}

	cmd.AddCommand(add, list, passwd, signout, remove, newUser2FACmd(a))
	return cmd
}

func lookupUser(cmd *cobra.Command, s *store.Store, name string) (*store.UIUser, error) {
	u, err := s.GetUIUser(cmd.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no user %q (see: storagebd user list)", name)
	}
	return u, err
}

func newUser2FACmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "2fa",
		Short: "Two-step sign-in (authenticator app codes) for a web UI user",
		Long: `Two-step sign-in: after the password, the page asks for a 6-digit code
from an authenticator app (Google Authenticator, Microsoft Authenticator,
1Password, Bitwarden, Aegis, ...). Required for every user while the page
is open to the network (ui-listen on a non-loopback address, or ui-hosts set).`,
	}
	enable := &cobra.Command{
		Use:   "enable <username>",
		Short: "Turn on two-step sign-in (shows a QR code to scan)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			u, err := lookupUser(cmd, s, args[0])
			if err != nil {
				return err
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return errors.New("run this in a terminal: it shows a QR code and asks for a code from the app")
			}
			secret, err := uiauth.NewTOTPSecret()
			if err != nil {
				return err
			}
			if err := s.StartUITOTP(cmd.Context(), u.ID, secret); err != nil {
				return err
			}
			host, _ := os.Hostname()
			account := u.Username
			if host != "" {
				account += "@" + host
			}
			uri := uiauth.TOTPURI(account, secret)
			qr, err := uiauth.QRTerminal(uri)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Scan this with your authenticator app:\n\n%s\n", qr)
			fmt.Fprintf(out, "Can't scan it? Add the key by hand: %s\n(type: time-based, 6 digits)\n\n", spaced(secret))
			for tries := 0; ; tries++ {
				code, err := promptLine("Code shown in the app: ")
				if err != nil {
					return err
				}
				step, ok := uiauth.CheckTOTP(secret, code, time.Now(), 0)
				if ok {
					codes, hashes, err := uiauth.NewBackupCodes()
					if err != nil {
						return err
					}
					if err := s.EnableUITOTP(cmd.Context(), u.ID, secret, step, hashes, ""); err != nil {
						return err
					}
					fmt.Fprintf(out, "\nTwo-step sign-in is on for %s; signed out of every browser.\n\n", u.Username)
					fmt.Fprintln(out, "Backup codes. Each works once, instead of an app code, if you lose the phone.")
					fmt.Fprintln(out, "Store them somewhere safe; they aren't shown again:")
					for _, c := range codes {
						fmt.Fprintf(out, "  %s\n", c)
					}
					return nil
				}
				if tries >= 2 {
					return errors.New("the codes didn't match; check the phone's clock and run this again")
				}
				fmt.Fprintln(os.Stderr, "That code doesn't match. Try the next one the app shows.")
			}
		},
	}
	var yes bool
	disable := &cobra.Command{
		Use:   "disable <username>",
		Short: "Turn off two-step sign-in (e.g. a lost phone and no backup codes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			u, err := lookupUser(cmd, s, args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := confirm("Turn off two-step sign-in for " + u.Username + "? While the page is open to the network, they then can't sign in")
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("cancelled")
				}
			}
			if err := s.DisableUITOTP(cmd.Context(), u.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Two-step sign-in is off for %s. Turn it back on with: storagebd user 2fa enable %s\n", u.Username, u.Username)
			return nil
		},
	}
	disable.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	cmd.AddCommand(enable, disable)
	return cmd
}

// spaced groups a base32 key in fours, the way apps show it.
func spaced(k string) string {
	var b strings.Builder
	for i, c := range k {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// newPasswordHash reads a new password (file, or prompted twice) and
// returns its hash.
func newPasswordHash(file string) (string, error) {
	p, err := readSecret(file, "New password: ")
	if err != nil {
		return "", err
	}
	if err := uiauth.ValidatePassword(p); err != nil {
		return "", err
	}
	if file == "" {
		again, err := readSecret("", "Repeat password: ")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errors.New("passwords don't match")
		}
	}
	return uiauth.HashPassword(p)
}
