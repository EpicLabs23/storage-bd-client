package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
)

// UIUser is someone who can sign in to the `serve` web page. Users are
// created from the terminal only.
type UIUser struct {
	ID           int64
	Username     string
	PasswordHash string
	TOTPEnabled  bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UISession is one signed-in browser. Only the SHA-256 of the cookie
// value is stored.
type UISession struct {
	TokenHash  string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RemoteAddr string
	UserAgent  string
}

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

func ValidateUsername(u string) error {
	if !usernamePattern.MatchString(u) {
		return errors.New("username must be 1-32 chars of lowercase letters, digits, '.', '_' or '-', starting with a letter")
	}
	return nil
}

const uiUserCols = `id, username, password_hash, totp_enabled, created_at, updated_at`

func scanUIUser(row interface{ Scan(...any) error }) (*UIUser, error) {
	var u UIUser
	var totp int
	var created, updated string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &totp, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = totp == 1
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &u, nil
}

func (s *Store) CreateUIUser(ctx context.Context, username, passwordHash string) (*UIUser, error) {
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO ui_users (username, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, username, passwordHash, ts, ts)
	if err != nil {
		if strings.Contains(err.Error(), "ui_users.username") {
			return nil, errors.New("a user with that name already exists")
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetUIUserByID(ctx, id)
}

func (s *Store) GetUIUser(ctx context.Context, username string) (*UIUser, error) {
	return scanUIUser(s.db.QueryRowContext(ctx, `SELECT `+uiUserCols+` FROM ui_users WHERE username = ?`, username))
}

func (s *Store) GetUIUserByID(ctx context.Context, id int64) (*UIUser, error) {
	return scanUIUser(s.db.QueryRowContext(ctx, `SELECT `+uiUserCols+` FROM ui_users WHERE id = ?`, id))
}

func (s *Store) ListUIUsers(ctx context.Context) ([]*UIUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+uiUserCols+` FROM ui_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UIUser
	for rows.Next() {
		u, err := scanUIUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CountUIUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ui_users`).Scan(&n)
	return n, err
}

// SetUIUserPassword replaces the password and signs the user out
// everywhere except keepSession (the token hash of the browser that made
// the change; "" signs out all of them).
func (s *Store) SetUIUserPassword(ctx context.Context, id int64, passwordHash, keepSession string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE ui_users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ui_sessions WHERE user_id = ? AND token_hash != ?`, id, keepSession); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUIUser removes the user and (by cascade) all their sessions.
func (s *Store) DeleteUIUser(ctx context.Context, username string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ui_users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUISessionsForUser signs a user out everywhere.
func (s *Store) DeleteUISessionsForUser(ctx context.Context, id int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ui_sessions WHERE user_id = ?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) CreateUISession(ctx context.Context, sess *UISession) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO ui_sessions
		(token_hash, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, ts, ts, sess.ExpiresAt.UTC().Format(time.RFC3339), sess.RemoteAddr, sess.UserAgent)
	return err
}

func (s *Store) GetUISession(ctx context.Context, tokenHash string) (*UISession, error) {
	var sess UISession
	var created, seen, exp string
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent
		FROM ui_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&sess.TokenHash, &sess.UserID, &created, &seen, &exp, &sess.RemoteAddr, &sess.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
	sess.LastSeenAt, _ = time.Parse(time.RFC3339, seen)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, exp)
	return &sess, nil
}

func (s *Store) TouchUISession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ui_sessions SET last_seen_at = ? WHERE token_hash = ?`, now(), tokenHash)
	return err
}

func (s *Store) DeleteUISession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ui_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteStaleUISessions removes sessions past their absolute expiry or
// idle longer than idle.
func (s *Store) DeleteStaleUISessions(ctx context.Context, idle time.Duration) error {
	t := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `DELETE FROM ui_sessions WHERE expires_at < ? OR last_seen_at < ?`,
		t.Format(time.RFC3339), t.Add(-idle).Format(time.RFC3339))
	return err
}
