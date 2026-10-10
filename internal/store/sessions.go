package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AccountSession is a persisted storage.bd account login for one API URL.
type AccountSession struct {
	APIURL           string
	Email            string
	RefreshToken     string
	RefreshExpiresAt time.Time
	AccessToken      string
	AccessExpiresAt  time.Time
}

const sessionCols = `api_url, email, refresh_token, refresh_expires_at, access_token, access_expires_at`

func scanSession(row interface{ Scan(...any) error }) (*AccountSession, error) {
	var s AccountSession
	var refreshExp string
	var accessExp sql.NullString
	err := row.Scan(&s.APIURL, &s.Email, &s.RefreshToken, &refreshExp, &s.AccessToken, &accessExp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.RefreshExpiresAt, _ = time.Parse(time.RFC3339, refreshExp)
	if accessExp.Valid {
		s.AccessExpiresAt, _ = time.Parse(time.RFC3339, accessExp.String)
	}
	return &s, nil
}

func (s *Store) GetSession(ctx context.Context, apiURL string) (*AccountSession, error) {
	return scanSession(s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM account_sessions WHERE api_url = ?`, apiURL))
}

func (s *Store) ListSessions(ctx context.Context) ([]*AccountSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM account_sessions ORDER BY api_url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccountSession
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) SaveSession(ctx context.Context, sess *AccountSession) error {
	return saveSession(ctx, s.db, sess)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func saveSession(ctx context.Context, db execer, sess *AccountSession) error {
	var accessExp any
	if !sess.AccessExpiresAt.IsZero() {
		accessExp = sess.AccessExpiresAt.UTC().Format(time.RFC3339)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO account_sessions
		(api_url, email, refresh_token, refresh_expires_at, access_token, access_expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(api_url) DO UPDATE SET email = excluded.email, refresh_token = excluded.refresh_token,
			refresh_expires_at = excluded.refresh_expires_at, access_token = excluded.access_token,
			access_expires_at = excluded.access_expires_at, updated_at = excluded.updated_at`,
		sess.APIURL, sess.Email, sess.RefreshToken, sess.RefreshExpiresAt.UTC().Format(time.RFC3339),
		sess.AccessToken, accessExp, now())
	return err
}

func (s *Store) DeleteSession(ctx context.Context, apiURL string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM account_sessions WHERE api_url = ?`, apiURL)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionLocked loads the session for apiURL inside a write
// transaction (BEGIN IMMEDIATE, so it also excludes other processes),
// calls fn, and persists what fn returns. This serializes refresh-token
// rotation across the CLI and `serve`: with single-use refresh tokens,
// two unsynchronized refreshers would log each other out.
//
// fn returning (nil, nil) means "no change". fn returning an error rolls
// back. ErrNotFound is returned if there is no session.
func (s *Store) UpdateSessionLocked(ctx context.Context, apiURL string,
	fn func(*AccountSession) (*AccountSession, error)) (*AccountSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := scanSession(tx.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM account_sessions WHERE api_url = ?`, apiURL))
	if err != nil {
		return nil, err
	}
	next, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return cur, tx.Commit()
	}
	if err := saveSession(ctx, tx, next); err != nil {
		return nil, err
	}
	return next, tx.Commit()
}
