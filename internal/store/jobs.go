package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	JobBackup       = "backup"
	JobRestore      = "restore"
	JobRestoreDB    = "restore-db"
	StatusRunning   = "running"
	StatusOK        = "succeeded"
	StatusPartial   = "partial" // finished, some files skipped (restic exit 3)
	StatusFailed    = "failed"
	StatusAborted   = "interrupted" // cancelled, or its process died
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
)

type Job struct {
	ID           int64
	CredentialID int64
	Kind         string
	Trigger      string
	Status       string
	PID          int
	PIDStart     string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Detail       json.RawMessage
	Error        string
}

// ErrJobRunning is returned by ClaimJob when another live job already
// holds the credential's lock.
type ErrJobRunning struct{ Job *Job }

func (e *ErrJobRunning) Error() string {
	return "another " + e.Job.Kind + " job is already running for this repo (started " +
		e.Job.StartedAt.Local().Format(time.DateTime) + ")"
}

const jobCols = `id, credential_id, kind, trigger, status, pid, pid_start, started_at, finished_at, detail, error`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var started string
	var finished sql.NullString
	var detail string
	err := row.Scan(&j.ID, &j.CredentialID, &j.Kind, &j.Trigger, &j.Status, &j.PID, &j.PIDStart, &started,
		&finished, &detail, &j.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.StartedAt, _ = time.Parse(time.RFC3339, started)
	j.FinishedAt = parseNullTime(finished)
	j.Detail = json.RawMessage(detail)
	return &j, nil
}

// ClaimJob takes the credential's job lock by inserting a `running` row.
// If a running row exists but alive(pid, pidStart) says its process is
// gone (crash, reboot, kill -9), that row is marked interrupted and the
// lock is taken over. All of it in one BEGIN IMMEDIATE transaction, so
// two processes can't both win.
func (s *Store) ClaimJob(ctx context.Context, credentialID int64, kind, trigger string, pid int, pidStart string,
	alive func(pid int, pidStart string) bool) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := scanJob(tx.QueryRowContext(ctx,
		`SELECT `+jobCols+` FROM jobs WHERE credential_id = ? AND status = ?`, credentialID, StatusRunning))
	switch {
	case err == nil:
		if alive(cur.PID, cur.PIDStart) {
			return nil, &ErrJobRunning{Job: cur}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, finished_at = ?, error = ? WHERE id = ?`,
			StatusAborted, now(), "process exited without finishing the job", cur.ID); err != nil {
			return nil, err
		}
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}

	ts := now()
	res, err := tx.ExecContext(ctx, `INSERT INTO jobs (credential_id, kind, trigger, status, pid, pid_start, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, credentialID, kind, trigger, StatusRunning, pid, pidStart, ts)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	started, _ := time.Parse(time.RFC3339, ts)
	return &Job{ID: id, CredentialID: credentialID, Kind: kind, Trigger: trigger, Status: StatusRunning,
		PID: pid, PIDStart: pidStart, StartedAt: started, Detail: json.RawMessage("{}")}, nil
}

// FinishJob records the outcome and releases the lock. It uses a fresh
// context: it must run even when the job's own context was cancelled.
func (s *Store) FinishJob(id int64, status string, detail any, errMsg string) error {
	d, err := json.Marshal(detail)
	if err != nil {
		d = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, finished_at = ?, detail = ?, error = ? WHERE id = ?`,
		status, now(), string(d), errMsg, id)
	return err
}

// ListJobs returns the newest jobs first; credentialID 0 means all.
func (s *Store) ListJobs(ctx context.Context, credentialID int64, limit int) ([]*Job, error) {
	q := `SELECT ` + jobCols + ` FROM jobs`
	args := []any{}
	if credentialID != 0 {
		q += ` WHERE credential_id = ?`
		args = append(args, credentialID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
