package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

	// Web page exposure. Terminal-only (IsTerminalOnlySetting): the page
	// must not be able to open itself to the network. Read by `serve` at
	// start, so a change needs a restart.
	SettingUIListen  = "ui-listen"
	SettingUITLSCert = "ui-tls-cert"
	SettingUITLSKey  = "ui-tls-key"
	SettingUIHosts   = "ui-hosts"
)

var knownSettings = map[string]string{
	SettingResticPath:    "Absolute path of the restic binary to run",
	SettingMySQLDumpPath: "MySQL/MariaDB dump tool (default: mariadb-dump, then mysqldump)",
	SettingMySQLPath:     "MySQL/MariaDB client used for restores (default: mariadb, then mysql)",
	SettingPgDumpPath:    "pg_dump binary (default: /usr/bin/pg_dump)",
	SettingPsqlPath:      "psql binary used for restores (default: /usr/bin/psql)",
	SettingUIListen:      "Web page address, e.g. 0.0.0.0:7878 to open it to the network (default: 127.0.0.1:7878; needs ui-tls-cert/key unless loopback)",
	SettingUITLSCert:     "HTTPS certificate file (PEM, full chain) for the web page; reloaded when it changes",
	SettingUITLSKey:      "HTTPS private key file (PEM) for the web page",
	SettingUIHosts:       "Comma-separated host names the web page answers to, e.g. backup.example.com (IP addresses always work)",
}

// IsTerminalOnlySetting reports whether key may only be changed from the
// terminal, not from the web page.
func IsTerminalOnlySetting(key string) bool {
	switch key {
	case SettingUIListen, SettingUITLSCert, SettingUITLSKey, SettingUIHosts:
		return true
	}
	return false
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// ParseUIHosts splits and checks the ui-hosts value.
func ParseUIHosts(v string) ([]string, error) {
	var out []string
	for _, h := range strings.Split(v, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostnamePattern.MatchString(h) || len(h) > 253 {
			return nil, fmt.Errorf("ui-hosts: %q is not a host name (no ports, schemes or wildcards)", h)
		}
		out = append(out, h)
	}
	return out, nil
}

// ValidateUIListen accepts IP:port or :port (all addresses).
func ValidateUIListen(v string) error {
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return fmt.Errorf("ui-listen: %v (expected e.g. 0.0.0.0:7878)", err)
	}
	if host != "" && net.ParseIP(host) == nil {
		return fmt.Errorf("ui-listen: %q is not an IP address (use 0.0.0.0 for all, or this machine's address)", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("ui-listen: bad port %q", port)
	}
	return nil
}

func validateSetting(key, value string) error {
	switch key {
	case SettingUIListen:
		return ValidateUIListen(value)
	case SettingUITLSCert, SettingUITLSKey:
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path", key)
		}
	case SettingUIHosts:
		_, err := ParseUIHosts(value)
		return err
	}
	return nil
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
	if err := validateSetting(key, value); err != nil {
		return err
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
