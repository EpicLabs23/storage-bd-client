package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/jobs"
	"storage-bd-client/internal/store"
)

func describeRetention(r *store.Retention) string {
	var parts []string
	add := func(n int, unit string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, unit))
		}
	}
	add(r.KeepLast, "most recent")
	add(r.KeepDaily, "daily")
	add(r.KeepWeekly, "weekly")
	add(r.KeepMonthly, "monthly")
	add(r.KeepYearly, "yearly")
	return "keep " + strings.Join(parts, ", ") + fmt.Sprintf("; free space at most every %d day(s)", r.PruneIntervalDays)
}

func newRetentionCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Set how many old snapshots to keep",
		Long: `A retention policy removes old snapshots after each successful backup,
per source, keeping the ones its rules select. restic's rules always keep at
least the newest snapshot of every source. Only snapshots storagebd made are
ever touched.

Removing snapshots is cheap. Freeing their space ("prune") may read some old
data back, so it runs at most every --prune-every days.`,
	}

	show := &cobra.Command{Use: "show", Short: "Show the policy", Args: cobra.NoArgs}
	showCred := credFlag(show)
	show.RunE = func(cmd *cobra.Command, _ []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := showCred(s)
		if err != nil {
			return err
		}
		r, err := s.GetRetention(cmd.Context(), cred.ID)
		out := cmd.OutOrStdout()
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(out, "No cleanup policy for %q: old snapshots are kept forever.\n", cred.Name)
			fmt.Fprintln(out, "Suggested: storagebd retention set --keep-daily 7 --keep-weekly 4 --keep-monthly 12")
			return nil
		} else if err != nil {
			return err
		}
		state := "on"
		if !r.Enabled {
			state = "off"
		}
		fmt.Fprintf(out, "Cleanup for %q is %s: %s\n", cred.Name, state, describeRetention(r))
		fmt.Fprintf(out, "Last cleanup: %s · last space freed: %s\n", fmtTime(r.LastForgetAt), fmtTime(r.LastPruneAt))
		return nil
	}

	var keepLast, keepDaily, keepWeekly, keepMonthly, keepYearly, pruneEvery int
	set := &cobra.Command{
		Use:     "set",
		Short:   "Set (and turn on) the policy",
		Example: "  storagebd retention set --keep-daily 7 --keep-weekly 4 --keep-monthly 12",
		Args:    cobra.NoArgs,
	}
	setCred := credFlag(set)
	f := set.Flags()
	f.IntVar(&keepLast, "keep-last", 0, "keep the N most recent snapshots")
	f.IntVar(&keepDaily, "keep-daily", 0, "keep the newest snapshot of each of the last N days that have one")
	f.IntVar(&keepWeekly, "keep-weekly", 0, "... of the last N weeks")
	f.IntVar(&keepMonthly, "keep-monthly", 0, "... of the last N months")
	f.IntVar(&keepYearly, "keep-yearly", 0, "... of the last N years")
	f.IntVar(&pruneEvery, "prune-every", 7, "free unused space at most every N days")
	set.RunE = func(cmd *cobra.Command, _ []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := setCred(s)
		if err != nil {
			return err
		}
		r := &store.Retention{CredentialID: cred.ID, Enabled: true, KeepLast: keepLast, KeepDaily: keepDaily,
			KeepWeekly: keepWeekly, KeepMonthly: keepMonthly, KeepYearly: keepYearly, PruneIntervalDays: pruneEvery}
		if err := s.SaveRetention(cmd.Context(), r); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Cleanup for %q: %s\nPreview its effect with: storagebd cleanup --dry-run\n", cred.Name, describeRetention(r))
		return nil
	}

	off := &cobra.Command{Use: "off", Short: "Stop removing old snapshots", Args: cobra.NoArgs}
	offCred := credFlag(off)
	off.RunE = func(cmd *cobra.Command, _ []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := offCred(s)
		if err != nil {
			return err
		}
		if err := s.DeleteRetention(cmd.Context(), cred.ID); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Cleanup for %q is off: old snapshots are kept forever.\n", cred.Name)
		return nil
	}

	cmd.AddCommand(show, set, off)
	return cmd
}

func printCleanup(w io.Writer, d *jobs.CleanupDetail) {
	verb := "Removed"
	if d.DryRun {
		verb = "Would remove"
	}
	if d.Removed == 0 {
		fmt.Fprintln(w, "Nothing to remove: every snapshot is kept by the policy.")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, g := range d.Groups {
		src := g.Source
		if src == "" {
			src = "(untagged)"
		}
		fmt.Fprintf(tw, "%s\tkeep %d\t%s %d\n", src, g.Kept, strings.ToLower(verb), len(g.Removed))
		for _, sn := range g.Removed {
			fmt.Fprintf(tw, "\t  %s\t%s\n", sn.ShortID, sn.Time.Local().Format(time.DateTime))
		}
	}
	tw.Flush()
	if d.Removed > 0 {
		fmt.Fprintf(w, "%s %d snapshot(s).\n", verb, d.Removed)
	}
	if d.Pruned {
		fmt.Fprintln(w, "Freed unused space:")
		for _, l := range strings.Split(d.PruneReport, "\n") {
			fmt.Fprintln(w, "  "+l)
		}
	}
	for _, warn := range d.Warnings {
		fmt.Fprintln(os.Stderr, "Warning:", warn)
	}
}

func newCleanupCmd(a *app) *cobra.Command {
	var dryRun, prune bool
	cmd := &cobra.Command{
		Use:   "cleanup [credential]",
		Short: "Remove old snapshots now, by the retention policy",
		Long: `Apply the credential's retention policy now. This normally runs by itself
after every successful backup.

--dry-run lists what would be removed and changes nothing. --prune also frees
the space right away instead of waiting for the prune interval.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.jobRunner()
			if err != nil {
				return err
			}
			cred, err := credentialArg(cmd, r.Store, args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				pol, err := r.Store.GetRetention(cmd.Context(), cred.ID)
				if errors.Is(err, store.ErrNotFound) {
					return jobs.ErrNoRetention
				} else if err != nil {
					return err
				}
				d, err := r.PreviewCleanup(cmd.Context(), cred, pol)
				if err != nil {
					return err
				}
				printCleanup(out, d)
				return nil
			}
			job, d, err := r.Cleanup(cmd.Context(), cred, jobs.CleanupOptions{ForcePrune: prune})
			if err != nil {
				return err
			}
			printCleanup(out, d)
			if job.Status != store.StatusOK {
				return fmt.Errorf("cleanup job #%d %s: %s", job.ID, job.Status, job.Error)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only show what would be removed")
	cmd.Flags().BoolVar(&prune, "prune", false, "free the space now, regardless of the prune interval")
	cmd.MarkFlagsMutuallyExclusive("dry-run", "prune")
	return cmd
}
