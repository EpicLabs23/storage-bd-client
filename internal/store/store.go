// Package store is the client's persistent local state, in SQLite via the
// pure-Go modernc.org/sqlite driver (no cgo, so the binary stays static).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations. The file is forced to 0600 before anything is
// written to it.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open database %s: %w", path, err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}

	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(15000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer at a time is all SQLite does anyway; a single connection
	// also keeps the pragmas above applied to every statement.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// migrations are append-only: never edit an applied entry, add a new one.
var migrations = []string{
	// 1: storage.bd connect credentials. One row per tenant/repo this
	// machine backs up to. install_secret and restic_password are stored
	// in plaintext, protected by the 0700 data dir / 0600 file (§19's
	// trust model) — an at-rest key would have to live next to them.
	`CREATE TABLE credentials (
		id                   INTEGER PRIMARY KEY,
		name                 TEXT NOT NULL UNIQUE,
		api_url              TEXT NOT NULL,
		tenant_id            TEXT NOT NULL,
		install_id           TEXT NOT NULL,
		install_secret       TEXT NOT NULL,
		restic_password      TEXT NOT NULL,
		account_email        TEXT NOT NULL DEFAULT '',
		is_default           INTEGER NOT NULL DEFAULT 0,
		repo_initialized_at  TEXT,
		password_escrowed_at TEXT,
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL
	);
	CREATE UNIQUE INDEX credentials_one_default ON credentials(is_default) WHERE is_default = 1;
	CREATE UNIQUE INDEX credentials_tenant ON credentials(api_url, tenant_id);`,

	// 2: storage.bd account login, one per API URL. The refresh token is
	// single-use (rotated on every refresh), so the CLI and `serve` must
	// share it through here rather than each holding their own copy. The
	// access token is cached too, so most calls skip the refresh entirely.
	`CREATE TABLE account_sessions (
		api_url            TEXT PRIMARY KEY,
		email              TEXT NOT NULL,
		refresh_token      TEXT NOT NULL,
		refresh_expires_at TEXT NOT NULL,
		access_token       TEXT NOT NULL DEFAULT '',
		access_expires_at  TEXT,
		updated_at         TEXT NOT NULL
	);`,

	// 3: runtime-changeable client settings (restic path for now; schedule
	// and UI settings later). Validated per key in settings.go.
	`CREATE TABLE settings (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,

	// 4: what to back up (sources) and what ran (jobs). db_password sits
	// next to the repo credentials under the same 0700/0600 protection.
	// jobs_one_running is the durable per-repo job lock (§19): at most one
	// running job per credential, enforced by SQLite across processes.
	`CREATE TABLE sources (
		id            INTEGER PRIMARY KEY,
		credential_id INTEGER NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
		name          TEXT NOT NULL,
		kind          TEXT NOT NULL CHECK (kind IN ('dir', 'mysql', 'postgres')),
		path          TEXT NOT NULL DEFAULT '',
		excludes      TEXT NOT NULL DEFAULT '[]',
		db_name       TEXT NOT NULL DEFAULT '',
		db_host       TEXT NOT NULL DEFAULT '',
		db_port       INTEGER NOT NULL DEFAULT 0,
		db_socket     TEXT NOT NULL DEFAULT '',
		db_user       TEXT NOT NULL DEFAULT '',
		db_password   TEXT NOT NULL DEFAULT '',
		enabled       INTEGER NOT NULL DEFAULT 1,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL,
		UNIQUE (credential_id, name)
	);
	CREATE TABLE jobs (
		id            INTEGER PRIMARY KEY,
		credential_id INTEGER NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
		kind          TEXT NOT NULL,
		trigger       TEXT NOT NULL,
		status        TEXT NOT NULL,
		pid           INTEGER NOT NULL,
		pid_start     TEXT NOT NULL,
		started_at    TEXT NOT NULL,
		finished_at   TEXT,
		detail        TEXT NOT NULL DEFAULT '{}',
		error         TEXT NOT NULL DEFAULT ''
	);
	CREATE UNIQUE INDEX jobs_one_running ON jobs(credential_id) WHERE status = 'running';
	CREATE INDEX jobs_by_credential ON jobs(credential_id, id DESC);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this client supports (%d); upgrade storagebd",
			current, len(migrations))
	}

	for i := current; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d failed: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
