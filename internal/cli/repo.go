package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/repo"
	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

func (a *app) repos() (*repo.Manager, error) {
	s, err := a.openStore()
	if err != nil {
		return nil, err
	}
	return &repo.Manager{Store: s, DataDir: a.dataDir, UserAgent: "storagebd/" + Version}, nil
}

func printWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "Warning:", w)
	}
}

func newRepoCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Prepare and inspect the backup repo behind a credential",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "init [credential]",
			Short: "Initialize the repo and escrow its password (done automatically on first backup)",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				m, err := a.repos()
				if err != nil {
					return err
				}
				cred, err := credentialArg(cmd, m.Store, args)
				if err != nil {
					return err
				}
				o, err := m.Open(cmd.Context(), cred)
				if err != nil {
					return err
				}
				defer o.Close()
				printWarnings(o.Warnings)
				fmt.Fprintf(cmd.OutOrStdout(), "Repo for %q is ready.\n", cred.Name)
				return nil
			},
		},
		&cobra.Command{
			Use:   "status [credential]",
			Short: "Check restic, the credential, and the repo",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				m, err := a.repos()
				if err != nil {
					return err
				}
				cred, err := credentialArg(cmd, m.Store, args)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				defer tw.Flush()

				bin, version, err := m.ResticBinary(cmd.Context())
				if err != nil {
					fmt.Fprintf(tw, "restic:\tERROR %v\n", err)
					return errors.New("restic is not usable")
				}
				fmt.Fprintf(tw, "restic:\t%s (%s)\n", version, bin)
				fmt.Fprintf(tw, "Credential:\t%s (tenant %s)\n", cred.Name, cred.TenantID)

				o, err := m.Open(cmd.Context(), cred)
				if err != nil {
					fmt.Fprintf(tw, "Repo:\tERROR %v\n", err)
					return errors.New("repo is not usable")
				}
				defer o.Close()
				printWarnings(o.Warnings)
				snaps, err := o.Snapshots(cmd.Context())
				if err != nil {
					fmt.Fprintf(tw, "Repo:\tERROR %v\n", err)
					return errors.New("repo is not usable")
				}
				fmt.Fprintf(tw, "Repo:\tOK, %d snapshot(s)\n", len(snaps))
				fmt.Fprintf(tw, "Password escrowed:\t%s\n", fmtTime(cred.PasswordEscrowedAt))
				if len(snaps) > 0 {
					fmt.Fprintf(tw, "Latest snapshot:\t%s\n", snaps[len(snaps)-1].Time.Local().Format(time.DateTime))
				}
				return nil
			},
		},
	)
	return cmd
}

func newSnapshotsCmd(a *app) *cobra.Command {
	var asJSON bool
	var tags []string
	cmd := &cobra.Command{
		Use:   "snapshots [credential]",
		Short: "List backup snapshots",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.repos()
			if err != nil {
				return err
			}
			cred, err := credentialArg(cmd, m.Store, args)
			if err != nil {
				return err
			}
			o, err := m.Open(cmd.Context(), cred)
			if err != nil {
				return err
			}
			defer o.Close()
			printWarnings(o.Warnings)
			snaps, err := o.Snapshots(cmd.Context(), tags...)
			if err != nil {
				return err
			}
			sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
			out := cmd.OutOrStdout()
			if asJSON {
				if snaps == nil {
					snaps = []restic.Snapshot{}
				}
				return writeJSON(out, snaps)
			}
			if len(snaps) == 0 {
				fmt.Fprintln(out, "No snapshots yet.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTIME\tHOST\tTAGS\tPATHS")
			for _, s := range snaps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ShortID, s.Time.Local().Format(time.DateTime),
					s.Hostname, strings.Join(s.Tags, ","), strings.Join(s.Paths, " "))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "only snapshots with all of these tags")
	return cmd
}

func newSettingsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "View or change client settings",
	}
	unknown := func(key string, err error) error {
		if errors.Is(err, store.ErrUnknownSetting) {
			keys := make([]string, 0)
			for k := range store.KnownSettings() {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(keys, ", "))
		}
		return err
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List settings and their values",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				s, err := a.openStore()
				if err != nil {
					return err
				}
				keys := make([]string, 0)
				for k := range store.KnownSettings() {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "KEY\tVALUE\tDESCRIPTION")
				for _, k := range keys {
					v, err := s.GetSetting(cmd.Context(), k)
					if errors.Is(err, store.ErrNotFound) {
						v = "(default)"
					} else if err != nil {
						return err
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\n", k, v, store.KnownSettings()[k])
				}
				return tw.Flush()
			},
		},
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Change a setting",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := a.openStore()
				if err != nil {
					return err
				}
				key, value := args[0], args[1]
				if key == store.SettingResticPath {
					if err := restic.VerifyBinary(value); err != nil {
						return err
					}
					if _, err := restic.Version(cmd.Context(), value); err != nil {
						return err
					}
				}
				if err := s.SetSetting(cmd.Context(), key, value); err != nil {
					return unknown(key, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", key, value)
				return nil
			},
		},
		&cobra.Command{
			Use:   "unset <key>",
			Short: "Revert a setting to its default",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := a.openStore()
				if err != nil {
					return err
				}
				if err := s.UnsetSetting(cmd.Context(), args[0]); err != nil {
					return unknown(args[0], err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s reverted to default\n", args[0])
				return nil
			},
		},
	)
	return cmd
}
