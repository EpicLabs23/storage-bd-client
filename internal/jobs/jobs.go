// Package jobs runs backups and restores for a credential under its job
// lock, records each run in the jobs table, and reports progress events.
// Shared by the CLI and (module 5) the scheduler / web UI.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"storage-bd-client/internal/dbdump"
	"storage-bd-client/internal/repo"
	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

type Runner struct {
	Store *store.Store
	Repos *repo.Manager
	Tools dbdump.Tools
	// Host is recorded as the snapshot hostname (defaults to os.Hostname).
	Host string
}

// Snapshot tags: every snapshot this client makes carries TagClient and
// its source's tag, so restores can find "latest snapshot of source X".
const TagClient = "storagebd"

func SourceTag(name string) string { return "source:" + name }

// Event is a progress notification. Exactly one of the pointer fields is
// set, according to Type.
type Event struct {
	Type     string // "warning", "source-start", "progress", "source-done"
	Source   string
	Message  string
	Progress *restic.Progress
	Result   *SourceResult
}

// SourceResult is one source's outcome within a backup job, stored in
// the job's detail JSON.
type SourceResult struct {
	Source       string             `json:"source"`
	Kind         string             `json:"kind"`
	Status       string             `json:"status"`
	SnapshotID   string             `json:"snapshot_id,omitempty"`
	FilesNew     int64              `json:"files_new"`
	FilesChanged int64              `json:"files_changed"`
	DataAdded    int64              `json:"data_added"`
	BytesTotal   int64              `json:"bytes_processed"`
	Duration     float64            `json:"duration_seconds"`
	Skipped      []restic.FileError `json:"skipped,omitempty"` // capped, see maxSkipped
	SkippedTotal int                `json:"skipped_total,omitempty"`
	Error        string             `json:"error,omitempty"`
}

type BackupDetail struct {
	Warnings []string        `json:"warnings,omitempty"`
	Sources  []*SourceResult `json:"sources"`
}

const maxSkipped = 50

type BackupOptions struct {
	Trigger string
	// Sources limits the run to these names (enabled or not). Empty means
	// every enabled source.
	Sources []string
	OnEvent func(Event)
}

func (o *BackupOptions) emit(e Event) {
	if o.OnEvent != nil {
		o.OnEvent(e)
	}
}

// claim takes cred's job lock for this process.
func (r *Runner) claim(ctx context.Context, cred *store.Credential, kind, trigger string) (*store.Job, error) {
	if trigger == "" {
		trigger = store.TriggerManual
	}
	pid := os.Getpid()
	return r.Store.ClaimJob(ctx, cred.ID, kind, trigger, pid, processStart(pid), processAlive)
}

func (r *Runner) host() string {
	if r.Host != "" {
		return r.Host
	}
	h, _ := os.Hostname()
	return h
}

// Backup runs every selected source into cred's repo, one snapshot per
// source. A failing source doesn't stop the others. The returned job
// carries the final status; err is only for failures that prevented the
// job from running at all (lock held, repo unreachable, ...).
func (r *Runner) Backup(ctx context.Context, cred *store.Credential, o BackupOptions) (*store.Job, *BackupDetail, error) {
	sources, err := r.selectSources(ctx, cred, o.Sources)
	if err != nil {
		return nil, nil, err
	}
	job, err := r.claim(ctx, cred, store.JobBackup, o.Trigger)
	if err != nil {
		return nil, nil, err
	}

	detail := &BackupDetail{Sources: []*SourceResult{}}
	status, errMsg := store.StatusFailed, ""
	defer func() {
		if ctx.Err() != nil {
			status = store.StatusAborted
		}
		job.Status, job.Error = status, errMsg
		if ferr := r.Store.FinishJob(job.ID, status, detail, errMsg); ferr != nil {
			o.emit(Event{Type: "warning", Message: "could not record job result: " + ferr.Error()})
		}
	}()

	opened, err := r.Repos.Open(ctx, cred)
	if err != nil {
		errMsg = err.Error()
		return job, detail, nil
	}
	defer opened.Close()
	for _, w := range opened.Warnings {
		detail.Warnings = append(detail.Warnings, w)
		o.emit(Event{Type: "warning", Message: w})
	}

	tmpDir := filepath.Join(r.Repos.DataDir, "tmp")
	failed, partial := 0, 0
	for _, src := range sources {
		if ctx.Err() != nil {
			break
		}
		o.emit(Event{Type: "source-start", Source: src.Name})
		res := r.backupSource(ctx, opened, src, tmpDir, func(p restic.Progress) {
			o.emit(Event{Type: "progress", Source: src.Name, Progress: &p})
		})
		detail.Sources = append(detail.Sources, res)
		switch res.Status {
		case store.StatusFailed:
			failed++
		case store.StatusPartial:
			partial++
		}
		o.emit(Event{Type: "source-done", Source: src.Name, Result: res})
	}

	switch {
	case failed > 0:
		status = store.StatusFailed
		errMsg = fmt.Sprintf("%d of %d source(s) failed", failed, len(sources))
	case partial > 0:
		status = store.StatusPartial
	default:
		status = store.StatusOK
	}
	return job, detail, nil
}

func (r *Runner) selectSources(ctx context.Context, cred *store.Credential, names []string) ([]*store.Source, error) {
	all, err := r.Store.ListSources(ctx, cred.ID)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		var out []*store.Source
		for _, s := range all {
			if s.Enabled {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no enabled sources for %q; add one with: storagebd source add-dir / add-db", cred.Name)
		}
		return out, nil
	}
	byName := map[string]*store.Source{}
	for _, s := range all {
		byName[s.Name] = s
	}
	var out []*store.Source
	for _, n := range names {
		s, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("no source named %q for %q", n, cred.Name)
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *Runner) backupSource(ctx context.Context, sess *repo.Opened, src *store.Source, tmpDir string,
	onStatus func(restic.Progress)) *SourceResult {
	res := &SourceResult{Source: src.Name, Kind: src.Kind, Status: store.StatusFailed}
	opts := restic.BackupOptions{
		Tags:     []string{TagClient, SourceTag(src.Name)},
		Host:     r.host(),
		OnStatus: onStatus,
	}

	var reader *dbdump.Reader
	switch src.Kind {
	case store.KindDir:
		// Resolve symlinks at run time, not just when the source was added:
		// the path may have been swapped for a symlink since.
		real, err := filepath.EvalSymlinks(src.Path)
		if err != nil {
			res.Error = fmt.Sprintf("path %s: %v", src.Path, err)
			return res
		}
		opts.Paths = []string{real}
		opts.Excludes = src.Excludes
	default:
		cmd, err := r.Tools.Dump(ctx, src, tmpDir)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		defer cmd.Cleanup()
		if reader, err = dbdump.StartReader(cmd); err != nil {
			res.Error = err.Error()
			return res
		}
		defer reader.Close()
		opts.Stdin = reader
		opts.StdinAs = dbdump.FileName(src)
	}

	sum, err := sess.Backup(ctx, opts)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.SnapshotID = sum.SnapshotID
	res.FilesNew, res.FilesChanged = sum.FilesNew, sum.FilesChanged
	res.DataAdded, res.BytesTotal, res.Duration = sum.DataAdded, sum.TotalBytesProcessed, sum.TotalDuration
	res.Status = store.StatusOK
	if sum.Partial {
		res.Status = store.StatusPartial
		res.SkippedTotal = len(sum.Errors)
		res.Skipped = sum.Errors
		if len(res.Skipped) > maxSkipped {
			res.Skipped = res.Skipped[:maxSkipped]
		}
	}
	return res
}

// ResolveSnapshot turns "latest" (or "") into the newest snapshot of
// src, or checks that an explicit ID belongs to src. Prefix IDs are
// accepted as restic accepts them.
func ResolveSnapshot(ctx context.Context, sess *repo.Opened, src *store.Source, id string) (*restic.Snapshot, error) {
	snaps, err := sess.Snapshots(ctx, TagClient, SourceTag(src.Name))
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, fmt.Errorf("no snapshots of source %q yet", src.Name)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	if id == "" || id == "latest" {
		return &snaps[0], nil
	}
	for i := range snaps {
		if strings.HasPrefix(snaps[i].ID, id) {
			return &snaps[i], nil
		}
	}
	return nil, fmt.Errorf("snapshot %q is not a snapshot of source %q", id, src.Name)
}

type RestoreOptions struct {
	Source     string
	SnapshotID string // "" or "latest" = newest
	Target     string // absolute directory
	Includes   []string
	Trigger    string
}

// RestoreFiles restores a directory source's snapshot into Target.
// Files land at Target + their original absolute path, as restic does.
func (r *Runner) RestoreFiles(ctx context.Context, cred *store.Credential, o RestoreOptions) (*store.Job, *restic.Snapshot, error) {
	src, err := r.Store.GetSource(ctx, cred.ID, o.Source)
	if err != nil {
		return nil, nil, sourceNotFound(o.Source, err)
	}
	if src.Kind != store.KindDir {
		return nil, nil, fmt.Errorf("%q is a %s database source; use restore-db", src.Name, src.Kind)
	}
	if !filepath.IsAbs(o.Target) {
		return nil, nil, errors.New("restore target must be an absolute path")
	}
	return r.withJob(ctx, cred, store.JobRestore, o.Trigger, src, o.SnapshotID,
		func(sess *repo.Opened, snap *restic.Snapshot) error {
			return sess.Restore(ctx, restic.RestoreOptions{SnapshotID: snap.ID, Target: filepath.Clean(o.Target), Includes: o.Includes})
		})
}

type RestoreDBOptions struct {
	Source     string
	SnapshotID string
	Into       string // database name; "" = the source's own
	Trigger    string
}

// RestoreDB streams a database source's dump out of restic straight into
// the database client.
func (r *Runner) RestoreDB(ctx context.Context, cred *store.Credential, o RestoreDBOptions) (*store.Job, *restic.Snapshot, error) {
	src, err := r.Store.GetSource(ctx, cred.ID, o.Source)
	if err != nil {
		return nil, nil, sourceNotFound(o.Source, err)
	}
	if !src.IsDB() {
		return nil, nil, fmt.Errorf("%q is a directory source; use restore", src.Name)
	}
	into := o.Into
	if into == "" {
		into = src.DBName
	}
	tmpDir := filepath.Join(r.Repos.DataDir, "tmp")
	return r.withJob(ctx, cred, store.JobRestoreDB, o.Trigger, src, o.SnapshotID,
		func(sess *repo.Opened, snap *restic.Snapshot) error {
			cmd, err := r.Tools.Restore(ctx, src, into, tmpDir)
			if err != nil {
				return err
			}
			defer cmd.Cleanup()
			stdin, err := cmd.Cmd.StdinPipe()
			if err != nil {
				return err
			}
			var clientErr strings.Builder
			cmd.Cmd.Stderr = &clientErr
			if err := cmd.Cmd.Start(); err != nil {
				return fmt.Errorf("could not start %s: %w", cmd.Cmd.Path, err)
			}
			dumpErr := sess.Dump(ctx, snap.ID, "/"+dbdump.FileName(src), stdin)
			if dumpErr != nil {
				// Don't let the client see EOF on a partial stream: kill it.
				// psql's --single-transaction then rolls everything back.
				cmd.Cmd.Process.Kill()
				cmd.Cmd.Wait()
				return dumpErr
			}
			stdin.Close()
			if err := cmd.Cmd.Wait(); err != nil {
				return fmt.Errorf("%s failed: %s", cmd.Cmd.Path, strings.TrimSpace(firstNonEmpty(clientErr.String(), err.Error())))
			}
			return nil
		})
}

func (r *Runner) withJob(ctx context.Context, cred *store.Credential, kind, trigger string, src *store.Source,
	snapID string, run func(*repo.Opened, *restic.Snapshot) error) (*store.Job, *restic.Snapshot, error) {
	job, err := r.claim(ctx, cred, kind, trigger)
	if err != nil {
		return nil, nil, err
	}
	detail := map[string]any{"source": src.Name}
	status, errMsg := store.StatusFailed, ""
	var snap *restic.Snapshot
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
		return job, nil, err
	}
	defer sess.Close()
	if snap, err = ResolveSnapshot(ctx, sess, src, snapID); err != nil {
		errMsg = err.Error()
		return job, nil, err
	}
	detail["snapshot_id"] = snap.ID
	if err := run(sess, snap); err != nil {
		errMsg = err.Error()
		return job, snap, err
	}
	status = store.StatusOK
	return job, snap, nil
}

func sourceNotFound(name string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no source named %q", name)
	}
	return err
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// processStart returns the process's start time in clock ticks since
// boot (/proc/<pid>/stat field 22). PID + start time identifies a process
// even across PID reuse.
func processStart(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	// comm (field 2) is parenthesized and may contain spaces; fields after
	// the last ')' are fixed.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}

// processAlive reports whether the job-holding process still exists and
// is the same process (not a reused PID).
func processAlive(pid int, start string) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	return start == "" || processStart(pid) == start
}
