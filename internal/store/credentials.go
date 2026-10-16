package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Credential is one storage.bd connect credential: which tenant/repo, the
// install credential used to mint data tokens (§04), and the restic
// password this machine holds as the primary copy (§05/§19).
type Credential struct {
	ID                 int64
	Name               string
	APIURL             string
	TenantID           string
	InstallID          string
	InstallSecret      string
	ResticPassword     string
	AccountEmail       string
	IsDefault          bool
	RepoInitializedAt  *time.Time
	PasswordEscrowedAt *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("name must be 1-63 chars of lowercase letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	return nil
}

// ValidateAPIURL accepts only a bare https origin (scheme + host[:port]).
// Plain http is allowed for loopback hosts (local dev), or anywhere when
// allowHTTP is set: the install secret and every data token cross this
// connection.
func ValidateAPIURL(raw string, allowHTTP bool) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid API URL %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("API URL must be just scheme://host[:port], got %q", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP && !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("refusing plain-http API URL %q: secrets would be sent unencrypted (use https, or --allow-http for a trusted network)", raw)
		}
	default:
		return "", fmt.Errorf("API URL must use https, got %q", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ValidateUUID(field, v string) error {
	if _, err := uuid.Parse(v); err != nil || len(v) != 36 {
		return fmt.Errorf("%s must be a UUID", field)
	}
	return nil
}

func ValidateInstallSecret(v string) error {
	if len(v) < 32 || len(v) > 256 || strings.ContainsAny(v, " \t\r\n") {
		return errors.New("space secret key looks malformed (expected the long hex string shown once at creation)")
	}
	return nil
}

func ValidateResticPassword(v string) error {
	if len(v) < 16 {
		return errors.New("restic password must be at least 16 characters")
	}
	if strings.ContainsAny(v, "\r\n") {
		return errors.New("restic password must not contain line breaks")
	}
	return nil
}

// GenerateResticPassword returns a full-entropy password (§19: generated
// locally, not a memorable passphrase).
func GenerateResticPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c *Credential) validate() error {
	if err := ValidateName(c.Name); err != nil {
		return err
	}
	if c.APIURL == "" {
		return errors.New("API URL is required")
	}
	if err := ValidateUUID("Space ID", c.TenantID); err != nil {
		return err
	}
	if err := ValidateUUID("Space access key ID", c.InstallID); err != nil {
		return err
	}
	if err := ValidateInstallSecret(c.InstallSecret); err != nil {
		return err
	}
	return ValidateResticPassword(c.ResticPassword)
}

const credentialCols = `id, name, api_url, tenant_id, install_id, install_secret, restic_password,
	account_email, is_default, repo_initialized_at, password_escrowed_at, created_at, updated_at`

func scanCredential(row interface{ Scan(...any) error }) (*Credential, error) {
	var c Credential
	var isDefault int
	var initAt, escrowAt sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&c.ID, &c.Name, &c.APIURL, &c.TenantID, &c.InstallID, &c.InstallSecret,
		&c.ResticPassword, &c.AccountEmail, &isDefault, &initAt, &escrowAt, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.IsDefault = isDefault == 1
	c.RepoInitializedAt = parseNullTime(initAt)
	c.PasswordEscrowedAt = parseNullTime(escrowAt)
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &c, nil
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// CreateCredential inserts c. The first credential on a machine becomes
// the default automatically, so a single-repo setup needs no extra step.
func (s *Store) CreateCredential(ctx context.Context, c *Credential) error {
	if err := c.validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		c.IsDefault = true
	}
	if c.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE credentials SET is_default = 0`); err != nil {
			return err
		}
	}

	ts := now()
	res, err := tx.ExecContext(ctx, `INSERT INTO credentials
		(name, api_url, tenant_id, install_id, install_secret, restic_password, account_email, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.APIURL, c.TenantID, c.InstallID, c.InstallSecret, c.ResticPassword, c.AccountEmail,
		boolInt(c.IsDefault), ts, ts)
	if err != nil {
		return translateUniqueErr(err)
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListCredentials(ctx context.Context) ([]*Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+credentialCols+` FROM credentials ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCredentialByName(ctx context.Context, name string) (*Credential, error) {
	return scanCredential(s.db.QueryRowContext(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE name = ?`, name))
}

func (s *Store) GetDefaultCredential(ctx context.Context) (*Credential, error) {
	return scanCredential(s.db.QueryRowContext(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE is_default = 1`))
}

// UpdateCredential writes every mutable field of c (looked up by c.ID).
// Callers load, modify, then save, so validation always sees the full row.
func (s *Store) UpdateCredential(ctx context.Context, c *Credential) error {
	if err := c.validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if c.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE credentials SET is_default = 0 WHERE id != ?`, c.ID); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE credentials SET
		name = ?, api_url = ?, tenant_id = ?, install_id = ?, install_secret = ?, restic_password = ?,
		account_email = ?, is_default = ?, repo_initialized_at = ?, password_escrowed_at = ?, updated_at = ?
		WHERE id = ?`,
		c.Name, c.APIURL, c.TenantID, c.InstallID, c.InstallSecret, c.ResticPassword,
		c.AccountEmail, boolInt(c.IsDefault), fmtNullTime(c.RepoInitializedAt), fmtNullTime(c.PasswordEscrowedAt),
		now(), c.ID)
	if err != nil {
		return translateUniqueErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetDefaultCredential makes name the default, clearing any other.
func (s *Store) SetDefaultCredential(ctx context.Context, name string) error {
	c, err := s.GetCredentialByName(ctx, name)
	if err != nil {
		return err
	}
	c.IsDefault = true
	return s.UpdateCredential(ctx, c)
}

// DeleteCredential removes name. If it was the default and others remain,
// the alphabetically-first remaining one is promoted so "the default"
// stays well-defined.
func (s *Store) DeleteCredential(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credentials SET is_default = 1
		WHERE id = (SELECT id FROM credentials ORDER BY name LIMIT 1)
		AND NOT EXISTS (SELECT 1 FROM credentials WHERE is_default = 1)`); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fmtNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func translateUniqueErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "credentials.name"):
		return errors.New("a credential with that name already exists")
	case strings.Contains(msg, "credentials.api_url"), strings.Contains(msg, "credentials.tenant_id"):
		return errors.New("that backup space is already saved on this machine")
	}
	return err
}
