package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

// CleanupGroup is one source's outcome in a cleanup (or preview).
type CleanupGroup struct {
	Source  string            `json:"source"`
	Kept    int               `json:"kept"`
	Removed []restic.Snapshot `json:"removed"`
}

type CleanupDetail struct {
	DryRun      bool            `json:"dry_run"`
	Groups      []*CleanupGroup `json:"groups"`
	Removed     int             `json:"removed"`
	Pruned      bool            `json:"pruned"`
	PruneReport string          `json:"prune_report,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
}

type CleanupOptions struct {
	Trigger string
	// ForcePrune prunes even if the prune interval hasn't elapsed.
	ForcePrune bool
	// SkipPrune never prunes (e.g. right after an interactive backup when
	// the user wants it fast). The interval clock isn't touched.
	SkipPrune bool
	OnEvent   func(Event)
}

func keepOf(r *store.Retention) restic.Keep {
	return restic.Keep{Last: r.KeepLast, Daily: r.KeepDaily, Weekly: r.KeepWeekly, Monthly: r.KeepMonthly, Yearly: r.KeepYearly}
}

// ErrNoRetention means the credential has no (enabled) cleanup policy.
var ErrNoRetention = errors.New("no cleanup policy set for this repo (storagebd retention set ...)")

func toGroups(fg []restic.ForgetGroup) ([]*CleanupGroup, int) {
	var out []*CleanupGroup
	total := 0
	for _, g := range fg {
		cg := &CleanupGroup{Kept: len(g.Keep), Removed: g.Remove}
		if cg.Removed == nil {
			cg.Removed = []restic.Snapshot{}
		}
		for _, t := range g.Tags {
			if strings.HasPrefix(t, "source:") {
				cg.Source = strings.TrimPrefix(t, "source:")
			}
		}
		total += len(g.Remove)
		out = append(out, cg)
	}
	return out, total
}

// PreviewCleanup reports what the given policy would remove, without
// changing anything or taking the job lock (forget --dry-run only reads).
func (r *Runner) PreviewCleanup(ctx context.Context, cred *store.Credential, pol *store.Retention) (*CleanupDetail, error) {
	if err := pol.Validate(); err != nil {
		return nil, err
	}
	sess, err := r.Repos.Open(ctx, cred)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	fg, err := sess.Forget(ctx, []string{TagClient}, keepOf(pol), true)
	if err != nil {
		return nil, err
	}
	d := &CleanupDetail{DryRun: true}
	d.Groups, d.Removed = toGroups(fg)
	return d, nil
}

// Cleanup applies cred's retention policy as its own job: forget (only
// snapshots tagged storagebd, per source), then prune if due or forced.
func (r *Runner) Cleanup(ctx context.Context, cred *store.Credential, o CleanupOptions) (*store.Job, *CleanupDetail, error) {
	pol, err := r.Store.GetRetention(ctx, cred.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !pol.Enabled) {
		return nil, nil, ErrNoRetention
	} else if err != nil {
		return nil, nil, err
	}
	job, err := r.claim(ctx, cred, store.JobCleanup, o.Trigger)
	if err != nil {
		return nil, nil, err
	}
	if o.OnEvent != nil {
		o.OnEvent(Event{Type: "job-start", JobID: job.ID})
	}
	detail := &CleanupDetail{Groups: []*CleanupGroup{}}
	status, errMsg := store.StatusFailed, ""
	defer func() {
		if ctx.Err() != nil {
			status = store.StatusAborted
		}
		job.Status, job.Error = status, errMsg
		r.Store.FinishJob(job.ID, status, detail, errMsg)
	}()

	sess, err := r.Repos.Open(ctx, cred)
	if err != nil {
		errMsg = err.Error()
		return job, detail, nil
	}
	defer sess.Close()
	detail.Warnings = append(detail.Warnings, sess.Warnings...)

	now := time.Now()
	fg, err := sess.Forget(ctx, []string{TagClient}, keepOf(pol), false)
	if err != nil {
		errMsg = err.Error()
		return job, detail, nil
	}
	detail.Groups, detail.Removed = toGroups(fg)
	if err := r.Store.MarkRetentionRun(ctx, cred.ID, true, false, now); err != nil {
		detail.Warnings = append(detail.Warnings, "could not record cleanup time: "+err.Error())
	}

	if !o.SkipPrune && (o.ForcePrune || pol.PruneDue(now)) {
		report, err := sess.Prune(ctx)
		detail.PruneReport = report
		if err != nil {
			errMsg = "old snapshots were removed, but freeing their space failed: " + err.Error()
			return job, detail, nil
		}
		detail.Pruned = true
		if err := r.Store.MarkRetentionRun(ctx, cred.ID, false, true, now); err != nil {
			detail.Warnings = append(detail.Warnings, "could not record prune time: "+err.Error())
		}
	}
	status = store.StatusOK
	return job, detail, nil
}

// AfterBackup runs cleanup following a backup job, if the backup produced
// snapshots (succeeded or partial) and a policy is enabled. A failed
// backup skips cleanup: nothing new was saved, so nothing old should go.
// Returns (nil, nil, nil) when nothing ran.
func (r *Runner) AfterBackup(ctx context.Context, cred *store.Credential, backup *store.Job, o CleanupOptions) (*store.Job, *CleanupDetail, error) {
	if backup == nil || (backup.Status != store.StatusOK && backup.Status != store.StatusPartial) {
		return nil, nil, nil
	}
	job, d, err := r.Cleanup(ctx, cred, o)
	if errors.Is(err, ErrNoRetention) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("cleanup after backup: %w", err)
	}
	return job, d, nil
}
