package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Known setting keys. Unknown keys are rejected so a typo doesn't
// silently store a setting nothing reads.
const (
	SettingResticPath    = "restic-path"
	SettingMySQLDumpPath = "mysqldump-path"
	SettingMySQLPath     = "mysql-path"
	SettingPgDumpPath    = "pg_dump-path"
	SettingPsqlPath      = "psql-path"
)

var knownSettings = map[string]string{
	SettingResticPath:    "absolute path of the restic binary to run",
	SettingMySQLDumpPath: "MySQL/MariaDB dump tool (default: mariadb-dump, then mysqldump)",
	SettingMySQLPath:     "MySQL/MariaDB client used for restores (default: mariadb, then mysql)",
	SettingPgDumpPath:    "pg_dump binary (default: /usr/bin/pg_dump)",
	SettingPsqlPath:      "psql binary used for restores (default: /usr/bin/psql)",
}

// IsBinarySetting reports whether key holds a path to an executable.
func IsBinarySetting(key string) bool {
	switch key {
	case SettingResticPath, SettingMySQLDumpPath, SettingMySQLPath, SettingPgDumpPath, SettingPsqlPath:
		return true
	}
	return false
}

func KnownSettings() map[string]string { return knownSettings }

var ErrUnknownSetting = errors.New("unknown setting")

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	if _, ok := knownSettings[key]; !ok {
		return "", ErrUnknownSetting
	}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if _, ok := knownSettings[key]; !ok {
		return ErrUnknownSetting
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now())
	return err
}

func (s *Store) UnsetSetting(ctx context.Context, key string) error {
	if _, ok := knownSettings[key]; !ok {
		return ErrUnknownSetting
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// MarkRepoInitialized / MarkPasswordEscrowed record repo setup progress
// without rewriting the rest of the row.
func (s *Store) MarkRepoInitialized(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET repo_initialized_at = ?, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), now(), id)
	return err
}

func (s *Store) MarkPasswordEscrowed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET password_escrowed_at = ?, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), now(), id)
	return err
}
