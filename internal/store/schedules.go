package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	ScheduleDaily    = "daily"
	ScheduleInterval = "interval"
)

// Schedule runs a credential's enabled sources automatically while
// `storagebd serve` is running.
type Schedule struct {
	CredentialID  int64
	Enabled       bool
	Mode          string // daily | interval
	DailyMinute   int    // minutes after local midnight, for daily
	IntervalHours int    // for interval
	NextRunAt     *time.Time
	LastRunAt     *time.Time
}

func (s *Schedule) Validate() error {
	switch s.Mode {
	case ScheduleDaily:
		if s.DailyMinute < 0 || s.DailyMinute >= 24*60 {
			return errors.New("daily time must be between 00:00 and 23:59")
		}
	case ScheduleInterval:
		if s.IntervalHours < 1 || s.IntervalHours > 24*7 {
			return errors.New("interval must be between 1 and 168 hours")
		}
	default:
		return fmt.Errorf("unknown schedule mode %q", s.Mode)
	}
	return nil
}

// Next returns the first run time strictly after from.
func (s *Schedule) Next(from time.Time) time.Time {
	if s.Mode == ScheduleInterval {
		return from.Add(time.Duration(s.IntervalHours) * time.Hour)
	}
	local := from.Local()
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	at := day.Add(time.Duration(s.DailyMinute) * time.Minute)
	if !at.After(from) {
		at = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, time.Local).
			Add(time.Duration(s.DailyMinute) * time.Minute)
	}
	return at
}

const scheduleCols = `credential_id, enabled, mode, daily_minute, interval_hours, next_run_at, last_run_at`

func scanSchedule(row interface{ Scan(...any) error }) (*Schedule, error) {
	var s Schedule
	var enabled int
	var next, last sql.NullString
	err := row.Scan(&s.CredentialID, &enabled, &s.Mode, &s.DailyMinute, &s.IntervalHours, &next, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled == 1
	s.NextRunAt = parseNullTime(next)
	s.LastRunAt = parseNullTime(last)
	return &s, nil
}

func (st *Store) GetSchedule(ctx context.Context, credentialID int64) (*Schedule, error) {
	return scanSchedule(st.db.QueryRowContext(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE credential_id = ?`, credentialID))
}

// SaveSchedule creates or replaces a credential's schedule and computes
// its next run from now.
func (st *Store) SaveSchedule(ctx context.Context, s *Schedule) error {
	if err := s.Validate(); err != nil {
		return err
	}
	next := s.Next(time.Now())
	s.NextRunAt = &next
	_, err := st.db.ExecContext(ctx, `INSERT INTO schedules
		(credential_id, enabled, mode, daily_minute, interval_hours, next_run_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(credential_id) DO UPDATE SET enabled = excluded.enabled, mode = excluded.mode,
			daily_minute = excluded.daily_minute, interval_hours = excluded.interval_hours,
			next_run_at = excluded.next_run_at, updated_at = excluded.updated_at`,
		s.CredentialID, boolInt(s.Enabled), s.Mode, s.DailyMinute, s.IntervalHours, fmtNullTime(&next), now())
	return err
}

func (st *Store) DeleteSchedule(ctx context.Context, credentialID int64) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM schedules WHERE credential_id = ?`, credentialID)
	return err
}

// DueSchedules returns enabled schedules whose next run is at or before t.
func (st *Store) DueSchedules(ctx context.Context, t time.Time) ([]*Schedule, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+scheduleCols+` FROM schedules
		WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?`, t.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkScheduleRun records that a scheduled run started at `at` and sets
// the next run. Missed runs collapse into this one: next is computed
// from `at`, not from the old next_run_at.
func (st *Store) MarkScheduleRun(ctx context.Context, s *Schedule, at time.Time) error {
	next := s.Next(at)
	s.LastRunAt, s.NextRunAt = &at, &next
	_, err := st.db.ExecContext(ctx, `UPDATE schedules SET last_run_at = ?, next_run_at = ?, updated_at = ?
		WHERE credential_id = ?`, fmtNullTime(&at), fmtNullTime(&next), now(), s.CredentialID)
	return err
}

func (st *Store) GetCredentialByID(ctx context.Context, id int64) (*Credential, error) {
	return scanCredential(st.db.QueryRowContext(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE id = ?`, id))
}
