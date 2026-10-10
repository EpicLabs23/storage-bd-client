package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	KindDir      = "dir"
	KindMySQL    = "mysql"
	KindPostgres = "postgres"
)

// Source is one thing to back up into a credential's repo: a directory,
// or a MySQL/MariaDB or Postgres database dumped with the native tool.
type Source struct {
	ID           int64
	CredentialID int64
	Name         string
	Kind         string
	Enabled      bool

	// dir
	Path     string
	Excludes []string

	// mysql / postgres. Empty host = the local default (unix socket).
	DBName     string
	DBHost     string
	DBPort     int
	DBSocket   string
	DBUser     string
	DBPassword string
}

func (s *Source) IsDB() bool { return s.Kind == KindMySQL || s.Kind == KindPostgres }

// These values reach dump/restore tools as --flag=value arguments or
// option-file lines, so they're restricted to characters that can't
// break out of either (no leading '-', no whitespace, no quotes/newlines).
var (
	dbNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_$.-]{0,63}$`)
	dbUserPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,79}$`)
	dbHostPattern = regexp.MustCompile(`^[A-Za-z0-9_\[][A-Za-z0-9_.:\[\]-]{0,252}$`)
)

func (s *Source) Validate() error {
	if err := ValidateName(s.Name); err != nil {
		return fmt.Errorf("source %w", err)
	}
	switch s.Kind {
	case KindDir:
		if !filepath.IsAbs(s.Path) {
			return errors.New("directory path must be absolute")
		}
		s.Path = filepath.Clean(s.Path)
		for _, e := range s.Excludes {
			if strings.TrimSpace(e) == "" || strings.ContainsAny(e, "\r\n") {
				return fmt.Errorf("invalid exclude pattern %q", e)
			}
		}
	case KindMySQL, KindPostgres:
		if !dbNamePattern.MatchString(s.DBName) {
			return fmt.Errorf("invalid database name %q", s.DBName)
		}
		if s.DBUser == "" && s.Kind == KindMySQL {
			return errors.New("database user is required")
		}
		if s.DBUser != "" && !dbUserPattern.MatchString(s.DBUser) {
			return fmt.Errorf("invalid database user %q", s.DBUser)
		}
		if s.DBHost != "" && !dbHostPattern.MatchString(s.DBHost) {
			return fmt.Errorf("invalid database host %q", s.DBHost)
		}
		if s.DBPort < 0 || s.DBPort > 65535 {
			return fmt.Errorf("invalid database port %d", s.DBPort)
		}
		if s.DBSocket != "" && !filepath.IsAbs(s.DBSocket) {
			return errors.New("database socket must be an absolute path")
		}
		if strings.ContainsAny(s.DBPassword, "\r\n\x00") {
			return errors.New("database password must not contain line breaks")
		}
	default:
		return fmt.Errorf("unknown source kind %q", s.Kind)
	}
	return nil
}

const sourceCols = `id, credential_id, name, kind, path, excludes, db_name, db_host, db_port, db_socket,
	db_user, db_password, enabled`

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var s Source
	var excludes string
	var enabled int
	err := row.Scan(&s.ID, &s.CredentialID, &s.Name, &s.Kind, &s.Path, &excludes, &s.DBName, &s.DBHost,
		&s.DBPort, &s.DBSocket, &s.DBUser, &s.DBPassword, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled == 1
	if err := json.Unmarshal([]byte(excludes), &s.Excludes); err != nil {
		return nil, fmt.Errorf("source %s: corrupt excludes: %w", s.Name, err)
	}
	return &s, nil
}

func (st *Store) CreateSource(ctx context.Context, s *Source) error {
	if err := s.Validate(); err != nil {
		return err
	}
	excludes, _ := json.Marshal(nonNil(s.Excludes))
	ts := now()
	res, err := st.db.ExecContext(ctx, `INSERT INTO sources
		(credential_id, name, kind, path, excludes, db_name, db_host, db_port, db_socket, db_user, db_password,
		 enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.CredentialID, s.Name, s.Kind, s.Path, string(excludes), s.DBName, s.DBHost, s.DBPort, s.DBSocket,
		s.DBUser, s.DBPassword, boolInt(s.Enabled), ts, ts)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("a source named %q already exists for this credential", s.Name)
		}
		return err
	}
	s.ID, err = res.LastInsertId()
	return err
}

func (st *Store) UpdateSource(ctx context.Context, s *Source) error {
	if err := s.Validate(); err != nil {
		return err
	}
	excludes, _ := json.Marshal(nonNil(s.Excludes))
	res, err := st.db.ExecContext(ctx, `UPDATE sources SET name = ?, path = ?, excludes = ?, db_name = ?,
		db_host = ?, db_port = ?, db_socket = ?, db_user = ?, db_password = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		s.Name, s.Path, string(excludes), s.DBName, s.DBHost, s.DBPort, s.DBSocket, s.DBUser, s.DBPassword,
		boolInt(s.Enabled), now(), s.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("a source named %q already exists for this credential", s.Name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (st *Store) ListSources(ctx context.Context, credentialID int64) ([]*Source, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE credential_id = ? ORDER BY name`,
		credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (st *Store) GetSource(ctx context.Context, credentialID int64, name string) (*Source, error) {
	return scanSource(st.db.QueryRowContext(ctx,
		`SELECT `+sourceCols+` FROM sources WHERE credential_id = ? AND name = ?`, credentialID, name))
}

func (st *Store) DeleteSource(ctx context.Context, credentialID int64, name string) error {
	res, err := st.db.ExecContext(ctx, `DELETE FROM sources WHERE credential_id = ? AND name = ?`, credentialID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
