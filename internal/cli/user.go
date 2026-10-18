package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

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
			fmt.Fprintln(tw, "USERNAME\tCREATED\tPASSWORD CHANGED")
			for _, u := range users {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Username, u.CreatedAt.Local().Format("2006-01-02 15:04"), u.UpdatedAt.Local().Format("2006-01-02 15:04"))
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

	cmd.AddCommand(add, list, passwd, signout, remove)
	return cmd
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
