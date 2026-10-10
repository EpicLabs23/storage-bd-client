package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Retention is a credential's old-snapshot cleanup policy, applied per
// source with restic's keep rules (which always keep at least the newest
// snapshot of every source).
type Retention struct {
	CredentialID      int64
	Enabled           bool
	KeepLast          int
	KeepDaily         int
	KeepWeekly        int
	KeepMonthly       int
	KeepYearly        int
	PruneIntervalDays int
	LastForgetAt      *time.Time
	LastPruneAt       *time.Time
}

// DefaultRetention is the suggested preset: a week of dailies, a month of
// weeklies, a year of monthlies.
func DefaultRetention(credentialID int64) *Retention {
	return &Retention{CredentialID: credentialID, Enabled: true, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12, PruneIntervalDays: 7}
}

const maxKeep = 10000

func (r *Retention) Validate() error {
	vals := []int{r.KeepLast, r.KeepDaily, r.KeepWeekly, r.KeepMonthly, r.KeepYearly}
	total := 0
	for _, v := range vals {
		if v < 0 || v > maxKeep {
			return fmt.Errorf("keep values must be between 0 and %d", maxKeep)
		}
		total += v
	}
	if total == 0 {
		// restic's forget with no keep rule is an error anyway, and a
		// policy that keeps nothing is never what anyone means.
		return errors.New("a cleanup policy must keep something (set at least one keep value)")
	}
	if r.PruneIntervalDays < 1 || r.PruneIntervalDays > 365 {
		return errors.New("prune interval must be between 1 and 365 days")
	}
	return nil
}

// PruneDue reports whether a prune should run now.
func (r *Retention) PruneDue(now time.Time) bool {
	return r.LastPruneAt == nil || now.Sub(*r.LastPruneAt) >= time.Duration(r.PruneIntervalDays)*24*time.Hour
}

const retentionCols = `credential_id, enabled, keep_last, keep_daily, keep_weekly, keep_monthly, keep_yearly,
	prune_interval_days, last_forget_at, last_prune_at`

func (st *Store) GetRetention(ctx context.Context, credentialID int64) (*Retention, error) {
	var r Retention
	var enabled int
	var lf, lp sql.NullString
	err := st.db.QueryRowContext(ctx, `SELECT `+retentionCols+` FROM retention WHERE credential_id = ?`, credentialID).
		Scan(&r.CredentialID, &enabled, &r.KeepLast, &r.KeepDaily, &r.KeepWeekly, &r.KeepMonthly, &r.KeepYearly,
			&r.PruneIntervalDays, &lf, &lp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Enabled = enabled == 1
	r.LastForgetAt, r.LastPruneAt = parseNullTime(lf), parseNullTime(lp)
	return &r, nil
}

// SaveRetention creates or replaces the policy, keeping the run history.
func (st *Store) SaveRetention(ctx context.Context, r *Retention) error {
	if err := r.Validate(); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx, `INSERT INTO retention
		(credential_id, enabled, keep_last, keep_daily, keep_weekly, keep_monthly, keep_yearly, prune_interval_days, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(credential_id) DO UPDATE SET enabled = excluded.enabled, keep_last = excluded.keep_last,
			keep_daily = excluded.keep_daily, keep_weekly = excluded.keep_weekly, keep_monthly = excluded.keep_monthly,
			keep_yearly = excluded.keep_yearly, prune_interval_days = excluded.prune_interval_days,
			updated_at = excluded.updated_at`,
		r.CredentialID, boolInt(r.Enabled), r.KeepLast, r.KeepDaily, r.KeepWeekly, r.KeepMonthly, r.KeepYearly,
		r.PruneIntervalDays, now())
	return err
}

func (st *Store) DeleteRetention(ctx context.Context, credentialID int64) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM retention WHERE credential_id = ?`, credentialID)
	return err
}

func (st *Store) MarkRetentionRun(ctx context.Context, credentialID int64, forgot, pruned bool, at time.Time) error {
	ts := at.UTC().Format(time.RFC3339)
	if forgot {
		if _, err := st.db.ExecContext(ctx, `UPDATE retention SET last_forget_at = ? WHERE credential_id = ?`, ts, credentialID); err != nil {
			return err
		}
	}
	if pruned {
		if _, err := st.db.ExecContext(ctx, `UPDATE retention SET last_prune_at = ? WHERE credential_id = ?`, ts, credentialID); err != nil {
			return err
		}
	}
	return nil
}
