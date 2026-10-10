package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"storage-bd-client/internal/dbdump"
	"storage-bd-client/internal/jobs"
	"storage-bd-client/internal/store"
)

// ExitError carries a specific process exit code (e.g. 3 for a partial
// backup, mirroring restic) out of a command.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }

func (a *app) jobRunner() (*jobs.Runner, error) {
	m, err := a.repos()
	if err != nil {
		return nil, err
	}
	tools, err := dbdump.ToolsFromSettings(context.Background(), m.Store)
	if err != nil {
		return nil, err
	}
	return &jobs.Runner{Store: m.Store, Repos: m, Tools: tools}, nil
}

func checkDirExists(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", p)
	}
	return nil
}

// progressPrinter renders backup events on stderr: a live single line on
// a terminal, a line every 15s otherwise (cron/journald).
type progressPrinter struct {
	w        io.Writer
	tty      bool
	lastLine time.Time
	width    int
}

func newProgressPrinter() *progressPrinter {
	p := &progressPrinter{w: os.Stderr, tty: term.IsTerminal(int(os.Stderr.Fd()))}
	if p.tty {
		if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil {
			p.width = w
		}
	}
	return p
}

func (p *progressPrinter) clear() {
	if p.tty {
		fmt.Fprint(p.w, "\r\033[K")
	}
}

func (p *progressPrinter) handle(e jobs.Event) {
	switch e.Type {
	case "warning":
		p.clear()
		fmt.Fprintln(p.w, "Warning:", e.Message)
	case "source-start":
		p.clear()
		fmt.Fprintf(p.w, "Backing up %s ...\n", e.Source)
	case "progress":
		if !p.tty && time.Since(p.lastLine) < 15*time.Second {
			return
		}
		pr := e.Progress
		line := fmt.Sprintf("  %s %5.1f%%  %s / %s  %d/%d files", e.Source, pr.PercentDone*100,
			humanize.IBytes(uint64(pr.BytesDone)), humanize.IBytes(uint64(pr.TotalBytes)), pr.FilesDone, pr.TotalFiles)
		if pr.SecondsRemaining > 0 {
			line += "  ETA " + (time.Duration(pr.SecondsRemaining) * time.Second).String()
		}
		if p.tty {
			if p.width > 0 && len(line) >= p.width {
				line = line[:p.width-1]
			}
			fmt.Fprint(p.w, "\r\033[K"+line)
		} else {
			fmt.Fprintln(p.w, line)
		}
		p.lastLine = time.Now()
	case "source-done":
		p.clear()
		r := e.Result
		switch r.Status {
		case store.StatusOK:
			fmt.Fprintf(p.w, "  %s: done, snapshot %s, %s new data\n", r.Source, short(r.SnapshotID), humanize.IBytes(uint64(r.DataAdded)))
		case store.StatusPartial:
			fmt.Fprintf(p.w, "  %s: done with warnings, snapshot %s; %d file(s) could not be read:\n", r.Source, short(r.SnapshotID), r.SkippedTotal)
			for _, s := range r.Skipped {
				fmt.Fprintf(p.w, "      %s (%s)\n", s.Item, s.Message)
			}
			if r.SkippedTotal > len(r.Skipped) {
				fmt.Fprintf(p.w, "      ... and %d more\n", r.SkippedTotal-len(r.Skipped))
			}
		default:
			fmt.Fprintf(p.w, "  %s: FAILED: %s\n", r.Source, r.Error)
		}
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func newBackupCmd(a *app) *cobra.Command {
	var sources []string
	var trigger string
	var noCleanup bool
	cmd := &cobra.Command{
		Use:   "backup [credential]",
		Short: "Back up sources now",
		Long: `Back up every enabled source of a credential (or only --source ones).

Exit status: 0 success, 3 finished but some files could not be read,
1 failed. Suitable for cron.`,
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
			pp := newProgressPrinter()
			job, _, err := r.Backup(cmd.Context(), cred, jobs.BackupOptions{
				Trigger: trigger, Sources: sources, OnEvent: pp.handle,
			})
			if err != nil {
				return err
			}
			if !noCleanup {
				cj, d, err := r.AfterBackup(cmd.Context(), cred, job, jobs.CleanupOptions{Trigger: trigger})
				switch {
				case err != nil:
					fmt.Fprintln(os.Stderr, "Warning: removing old snapshots failed:", err)
				case cj != nil && cj.Status != store.StatusOK:
					fmt.Fprintf(os.Stderr, "Warning: cleanup job #%d %s: %s\n", cj.ID, cj.Status, cj.Error)
				case cj != nil && (d.Removed > 0 || d.Pruned):
					fmt.Fprintf(os.Stderr, "Cleanup: removed %d old snapshot(s)%s.\n", d.Removed,
						map[bool]string{true: " and freed unused space", false: ""}[d.Pruned])
				}
			}
			switch job.Status {
			case store.StatusOK:
				fmt.Fprintf(cmd.OutOrStdout(), "Backup job #%d succeeded.\n", job.ID)
				return nil
			case store.StatusPartial:
				return &ExitError{Code: 3, Err: fmt.Errorf("backup job #%d finished with warnings (some files could not be read)", job.ID)}
			case store.StatusAborted:
				return fmt.Errorf("backup job #%d was interrupted", job.ID)
			default:
				return fmt.Errorf("backup job #%d failed: %s", job.ID, job.Error)
			}
		},
	}
	cmd.Flags().StringSliceVar(&sources, "source", nil, "only these sources (comma-separated or repeated)")
	cmd.Flags().BoolVar(&noCleanup, "no-cleanup", false, "don't apply the retention policy after this backup")
	cmd.Flags().StringVar(&trigger, "trigger", store.TriggerManual, "recorded in job history (manual|schedule)")
	cmd.Flags().MarkHidden("trigger")
	return cmd
}

func newRestoreCmd(a *app) *cobra.Command {
	var source, snapshot, target string
	var includes []string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore files from a directory source's snapshot",
		Long: `Restore a directory source into --target. Files keep their original absolute
path under the target: /var/www/index.html restored with --target /tmp/r lands
at /tmp/r/var/www/index.html. Use --target / to restore in place.

Existing files at the destination are overwritten.`,
		Example: `  storagebd restore --source www --target /tmp/restore
  storagebd restore --source www --snapshot 4f2a9c1e --include /var/www/uploads --target /`,
		Args: cobra.NoArgs,
	}
	resolve := credFlag(cmd)
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "directory source to restore (required)")
	f.StringVar(&snapshot, "snapshot", "latest", "snapshot ID (or prefix), or latest")
	f.StringVar(&target, "target", "", "directory to restore into (required)")
	f.StringArrayVar(&includes, "include", nil, "only restore this path from the snapshot (repeatable)")
	f.BoolVarP(&yes, "yes", "y", false, "don't ask before writing into a non-empty target")
	cmd.MarkFlagRequired("source")
	cmd.MarkFlagRequired("target")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		r, err := a.jobRunner()
		if err != nil {
			return err
		}
		cred, err := resolve(r.Store)
		if err != nil {
			return err
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if !yes {
			if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
				ok, err := confirm(fmt.Sprintf("%s is not empty; files from the snapshot will overwrite files there. Continue?", abs))
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("aborted")
				}
			}
		}
		fmt.Fprintf(os.Stderr, "Restoring %s into %s ...\n", source, abs)
		job, snap, err := r.RestoreFiles(cmd.Context(), cred, jobs.RestoreOptions{
			Source: source, SnapshotID: snapshot, Target: abs, Includes: includes,
		})
		if err != nil {
			if job != nil {
				return fmt.Errorf("restore job #%d failed: %w", job.ID, err)
			}
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Restored snapshot %s (%s) into %s (job #%d).\n",
			snap.ShortID, snap.Time.Local().Format(time.DateTime), abs, job.ID)
		return nil
	}
	return cmd
}

func newRestoreDBCmd(a *app) *cobra.Command {
	var source, snapshot, into string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore-db",
		Short: "Restore a database source's dump into a database",
		Long: `Stream a database source's dump from a snapshot into the database server,
using the source's connection settings. Tables in the dump replace the
existing ones of the same name.

Postgres restores run in one transaction: on any error nothing is changed.
MySQL/MariaDB cannot do that (DDL commits implicitly): a failure part-way
leaves the database partially restored. Use --into to restore into a
different, already-created database first if in doubt.`,
		Example: `  storagebd restore-db --source shop
  storagebd restore-db --source shop --snapshot 4f2a9c1e --into shop_restored`,
		Args: cobra.NoArgs,
	}
	resolve := credFlag(cmd)
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "database source to restore (required)")
	f.StringVar(&snapshot, "snapshot", "latest", "snapshot ID (or prefix), or latest")
	f.StringVar(&into, "into", "", "target database (default: the source's own; must already exist)")
	f.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.MarkFlagRequired("source")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		r, err := a.jobRunner()
		if err != nil {
			return err
		}
		cred, err := resolve(r.Store)
		if err != nil {
			return err
		}
		src, err := r.Store.GetSource(cmd.Context(), cred.ID, source)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no source named %q for %q", source, cred.Name)
		} else if err != nil {
			return err
		}
		target := into
		if target == "" {
			target = src.DBName
		}
		if !yes {
			ok, err := confirm(fmt.Sprintf("Restore %s snapshot %s into %s database %q? Tables in the dump replace existing ones.",
				source, snapshot, src.Kind, target))
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("aborted")
			}
		}
		job, snap, err := r.RestoreDB(cmd.Context(), cred, jobs.RestoreDBOptions{Source: source, SnapshotID: snapshot, Into: into})
		if err != nil {
			if job != nil {
				return fmt.Errorf("restore-db job #%d failed: %w", job.ID, err)
			}
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Restored %s from snapshot %s (%s) into %q (job #%d).\n",
			source, snap.ShortID, snap.Time.Local().Format(time.DateTime), target, job.ID)
		return nil
	}
	return cmd
}

func newJobsCmd(a *app) *cobra.Command {
	var limit int
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:   "jobs [credential]",
		Short: "Show recent backup/restore jobs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			var credID int64
			names := map[int64]string{}
			creds, err := s.ListCredentials(cmd.Context())
			if err != nil {
				return err
			}
			for _, c := range creds {
				names[c.ID] = c.Name
			}
			if !all {
				cred, err := credentialArg(cmd, s, args)
				if err != nil {
					return err
				}
				credID = cred.ID
			}
			list, err := s.ListJobs(cmd.Context(), credID, limit)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				type jobView struct {
					ID         int64           `json:"id"`
					Credential string          `json:"credential"`
					Kind       string          `json:"kind"`
					Trigger    string          `json:"trigger"`
					Status     string          `json:"status"`
					StartedAt  time.Time       `json:"started_at"`
					FinishedAt *time.Time      `json:"finished_at"`
					Error      string          `json:"error,omitempty"`
					Detail     json.RawMessage `json:"detail"`
				}
				views := []jobView{}
				for _, j := range list {
					views = append(views, jobView{j.ID, names[j.CredentialID], j.Kind, j.Trigger, j.Status, j.StartedAt, j.FinishedAt, j.Error, j.Detail})
				}
				return writeJSON(out, views)
			}
			if len(list) == 0 {
				fmt.Fprintln(out, "No jobs yet.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCREDENTIAL\tKIND\tSTATUS\tSTARTED\tDURATION\tNOTE")
			for _, j := range list {
				dur := "-"
				if j.FinishedAt != nil {
					dur = j.FinishedAt.Sub(j.StartedAt).Round(time.Second).String()
				}
				note := j.Error
				if len(note) > 60 {
					note = note[:57] + "..."
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, names[j.CredentialID], j.Kind, j.Status,
					j.StartedAt.Local().Format(time.DateTime), dur, strings.ReplaceAll(note, "\n", " "))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "how many jobs")
	cmd.Flags().BoolVar(&all, "all", false, "jobs of every credential")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON (includes per-source detail)")
	return cmd
}
