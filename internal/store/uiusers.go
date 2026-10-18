package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	MFA        bool // signed in (or later confirmed) with a second factor
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
		(token_hash, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent, mfa)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, ts, ts, sess.ExpiresAt.UTC().Format(time.RFC3339), sess.RemoteAddr, sess.UserAgent, boolInt(sess.MFA))
	return err
}

func (s *Store) GetUISession(ctx context.Context, tokenHash string) (*UISession, error) {
	var sess UISession
	var created, seen, exp string
	var mfa int
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent, mfa
		FROM ui_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&sess.TokenHash, &sess.UserID, &created, &seen, &exp, &sess.RemoteAddr, &sess.UserAgent, &mfa)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
	sess.LastSeenAt, _ = time.Parse(time.RFC3339, seen)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, exp)
	sess.MFA = mfa == 1
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

// UITOTP is a user's 2FA state. Secret may be set while Enabled is false:
// that's an enrollment waiting for its first code.
type UITOTP struct {
	Secret      string
	Enabled     bool
	BackupCodes []string // SHA-256 hashes, see uiauth.NewBackupCodes
	LastStep    int64
}

func (s *Store) GetUITOTP(ctx context.Context, userID int64) (*UITOTP, error) {
	var t UITOTP
	var enabled int
	var codes string
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret, totp_enabled, totp_backup_codes, totp_last_step FROM ui_users WHERE id = ?`, userID).
		Scan(&t.Secret, &enabled, &codes, &t.LastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled == 1
	if err := json.Unmarshal([]byte(codes), &t.BackupCodes); err != nil {
		return nil, fmt.Errorf("user %d: bad backup codes: %w", userID, err)
	}
	return &t, nil
}

// StartUITOTP stores a new, not yet confirmed secret. Refused while 2FA
// is on: replacing a working secret needs `disable` first.
func (s *Store) StartUITOTP(ctx context.Context, userID int64, secret string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE ui_users SET totp_secret = ?, updated_at = ? WHERE id = ? AND totp_enabled = 0`,
		secret, now(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTOTPAlreadyOn
	}
	return nil
}

var ErrTOTPAlreadyOn = errors.New("two-step sign-in is already on for this user; turn it off first to set it up again")

// EnableUITOTP turns 2FA on for the pending secret (confirmed with the
// code at step) and signs the user out everywhere except keepSession.
func (s *Store) EnableUITOTP(ctx context.Context, userID int64, secret string, step int64, backupHashes []string, keepSession string) error {
	codes, _ := json.Marshal(backupHashes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE ui_users SET totp_enabled = 1, totp_backup_codes = ?, totp_last_step = ?, updated_at = ?
		WHERE id = ? AND totp_enabled = 0 AND totp_secret = ?`, string(codes), step, now(), userID, secret)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("two-step sign-in setup changed meanwhile; start again")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ui_sessions WHERE user_id = ? AND token_hash != ?`, userID, keepSession); err != nil {
		return err
	}
	// The browser that just proved the code counts as 2FA-verified.
	if _, err := tx.ExecContext(ctx, `UPDATE ui_sessions SET mfa = 1 WHERE token_hash = ?`, keepSession); err != nil {
		return err
	}
	return tx.Commit()
}

// DisableUITOTP turns 2FA off and drops the user's 2FA-verified sessions
// claim (they become plain password sessions).
func (s *Store) DisableUITOTP(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE ui_users SET totp_enabled = 0, totp_secret = '', totp_backup_codes = '[]', totp_last_step = 0, updated_at = ?
		WHERE id = ?`, now(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ui_sessions SET mfa = 0 WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// UseUITOTPStep records a sign-in at step. It fails if that step (or a
// later one) was already used, so two requests racing with the same code
// can't both win.
func (s *Store) UseUITOTPStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE ui_users SET totp_last_step = ? WHERE id = ? AND totp_enabled = 1 AND totp_last_step < ?`,
		step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UseUIBackupCode removes the hash at index i, only if the list is still
// the one the caller read (two racing uses of one code: one wins).
func (s *Store) UseUIBackupCode(ctx context.Context, userID int64, read []string, i int) (bool, error) {
	if i < 0 || i >= len(read) {
		return false, nil
	}
	old, _ := json.Marshal(read)
	rest := append(append([]string{}, read[:i]...), read[i+1:]...)
	next, _ := json.Marshal(rest)
	res, err := s.db.ExecContext(ctx, `UPDATE ui_users SET totp_backup_codes = ? WHERE id = ? AND totp_enabled = 1 AND totp_backup_codes = ?`,
		string(next), userID, string(old))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
