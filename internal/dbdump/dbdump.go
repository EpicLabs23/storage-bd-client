// Package dbdump turns a database source into a SQL stream for restic
// (`restic backup --stdin`), and a SQL stream back into a database
// (§03 "Database backup"): mysqldump/mariadb-dump and pg_dump in, mysql/
// mariadb and psql out. Plain SQL, never pre-compressed: compression
// would defeat restic's dedup between consecutive dumps.
//
// Passwords go through a 0600 option file (--defaults-extra-file) or
// PGPASSFILE. They never appear in argv or as a plain environment variable.
package dbdump

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"storage-bd-client/internal/execsafe"
	"storage-bd-client/internal/store"
)

// Tools are the resolved client binaries. Any may be empty if not
// installed; using a source that needs a missing one is an error then.
type Tools struct {
	MySQLDump string
	MySQL     string
	PgDump    string
	Psql      string
}

// DefaultTools finds the standard Debian/Ubuntu locations (MariaDB's
// own names first). Never $PATH, same reasoning as restic (§19).
func DefaultTools() Tools {
	first := func(c ...string) string { p, _ := execsafe.FirstUsable(c...); return p }
	return Tools{
		MySQLDump: first("/usr/bin/mariadb-dump", "/usr/bin/mysqldump", "/usr/local/bin/mariadb-dump", "/usr/local/bin/mysqldump"),
		MySQL:     first("/usr/bin/mariadb", "/usr/bin/mysql", "/usr/local/bin/mariadb", "/usr/local/bin/mysql"),
		PgDump:    first("/usr/bin/pg_dump", "/usr/local/bin/pg_dump"),
		Psql:      first("/usr/bin/psql", "/usr/local/bin/psql"),
	}
}

// ToolsFromSettings is DefaultTools with any *-path settings applied.
func ToolsFromSettings(ctx context.Context, s *store.Store) (Tools, error) {
	t := DefaultTools()
	for key, dst := range map[string]*string{
		store.SettingMySQLDumpPath: &t.MySQLDump,
		store.SettingMySQLPath:     &t.MySQL,
		store.SettingPgDumpPath:    &t.PgDump,
		store.SettingPsqlPath:      &t.Psql,
	} {
		v, err := s.GetSetting(ctx, key)
		if err == nil {
			*dst = v
		} else if !errors.Is(err, store.ErrNotFound) {
			return t, err
		}
	}
	return t, nil
}

func need(path, tool string) error {
	if path == "" {
		return fmt.Errorf("%s is not installed (or not in a standard location)", tool)
	}
	return execsafe.VerifyBinary(path)
}

// FileName is the name a source's dump gets inside its snapshot.
func FileName(src *store.Source) string {
	return src.Kind + "-" + src.DBName + ".sql"
}

// Command is a prepared dump/restore command. Cleanup removes its
// credential file and must always be called.
type Command struct {
	Cmd     *exec.Cmd
	Cleanup func()
	// Trailer is what a complete dump contains near its end. A dump that
	// exits 0 but lacks it is treated as truncated (§19).
	Trailer string
}

// writeSecretFile writes content to a new 0600 file in dir.
func writeSecretFile(dir, pattern, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Chmod(0o600); err == nil {
		_, err = f.WriteString(content)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// mysqlOptionFile builds a [client] option file. Values are double-quoted
// with \ and " escaped, per MySQL option-file syntax; Validate already
// rejected line breaks.
func mysqlOptionFile(src *store.Source) string {
	q := func(v string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	var b strings.Builder
	b.WriteString("[client]\n")
	b.WriteString("user=" + q(src.DBUser) + "\n")
	b.WriteString("password=" + q(src.DBPassword) + "\n")
	if src.DBHost != "" {
		b.WriteString("host=" + q(src.DBHost) + "\n")
	}
	if src.DBPort != 0 {
		b.WriteString("port=" + strconv.Itoa(src.DBPort) + "\n")
	}
	if src.DBSocket != "" {
		b.WriteString("socket=" + q(src.DBSocket) + "\n")
	}
	return b.String()
}

// pgPassFile builds a PGPASSFILE line matching any host/port/db for this
// user (the connection itself is pinned by argv), escaping : and \.
func pgPassFile(src *store.Source) string {
	esc := strings.NewReplacer(`\`, `\\`, `:`, `\:`)
	user := src.DBUser
	if user == "" {
		user = "*"
	} else {
		user = esc.Replace(user)
	}
	return "*:*:*:" + user + ":" + esc.Replace(src.DBPassword) + "\n"
}

func pgConnArgs(src *store.Source) []string {
	args := []string{"--no-password"}
	host := src.DBHost
	if host == "" && src.DBSocket != "" {
		// libpq takes a socket *directory* as host.
		host = dirOf(src.DBSocket)
	}
	if host != "" {
		args = append(args, "--host="+host)
	}
	if src.DBPort != 0 {
		args = append(args, "--port="+strconv.Itoa(src.DBPort))
	}
	if src.DBUser != "" {
		args = append(args, "--username="+src.DBUser)
	}
	return args
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return p
}

var minimalEnv = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8"}

// Dump prepares the dump command for src. tmpDir holds the credential
// file for the command's lifetime.
func (t Tools) Dump(ctx context.Context, src *store.Source, tmpDir string) (*Command, error) {
	switch src.Kind {
	case store.KindMySQL:
		if err := need(t.MySQLDump, "mysqldump/mariadb-dump"); err != nil {
			return nil, err
		}
		opt, err := writeSecretFile(tmpDir, "my-*.cnf", mysqlOptionFile(src))
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, t.MySQLDump,
			"--defaults-extra-file="+opt, // must be the first option
			"--single-transaction", "--quick", "--routines", "--triggers", "--hex-blob",
			"--no-tablespaces", "--default-character-set=utf8mb4",
			src.DBName)
		cmd.Env = minimalEnv
		return &Command{Cmd: cmd, Cleanup: func() { os.Remove(opt) }, Trailer: "-- Dump completed"}, nil

	case store.KindPostgres:
		if err := need(t.PgDump, "pg_dump"); err != nil {
			return nil, err
		}
		pass, err := writeSecretFile(tmpDir, "pgpass-*", pgPassFile(src))
		if err != nil {
			return nil, err
		}
		args := append(pgConnArgs(src),
			"--format=plain", "--clean", "--if-exists", "--no-owner", "--no-acl",
			"--dbname="+src.DBName)
		cmd := exec.CommandContext(ctx, t.PgDump, args...)
		cmd.Env = append([]string{"PGPASSFILE=" + pass, "PGCONNECT_TIMEOUT=15"}, minimalEnv...)
		return &Command{Cmd: cmd, Cleanup: func() { os.Remove(pass) }, Trailer: "-- PostgreSQL database dump complete"}, nil
	}
	return nil, fmt.Errorf("source %s is not a database", src.Name)
}

// Restore prepares the command that loads a SQL stream (its stdin) into
// database `into` using src's connection settings. Postgres restores run
// in a single transaction with ON_ERROR_STOP, so they apply fully or not
// at all. MySQL can't do that: DDL commits implicitly.
func (t Tools) Restore(ctx context.Context, src *store.Source, into, tmpDir string) (*Command, error) {
	target := *src
	target.DBName = into
	if err := target.Validate(); err != nil {
		return nil, err
	}
	switch src.Kind {
	case store.KindMySQL:
		if err := need(t.MySQL, "mysql/mariadb client"); err != nil {
			return nil, err
		}
		opt, err := writeSecretFile(tmpDir, "my-*.cnf", mysqlOptionFile(&target))
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, t.MySQL, "--defaults-extra-file="+opt,
			"--default-character-set=utf8mb4", "--batch", target.DBName)
		cmd.Env = minimalEnv
		return &Command{Cmd: cmd, Cleanup: func() { os.Remove(opt) }}, nil

	case store.KindPostgres:
		if err := need(t.Psql, "psql"); err != nil {
			return nil, err
		}
		pass, err := writeSecretFile(tmpDir, "pgpass-*", pgPassFile(&target))
		if err != nil {
			return nil, err
		}
		args := append(pgConnArgs(&target), "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1",
			"--single-transaction", "--dbname="+target.DBName)
		cmd := exec.CommandContext(ctx, t.Psql, args...)
		cmd.Env = append([]string{"PGPASSFILE=" + pass, "PGCONNECT_TIMEOUT=15"}, minimalEnv...)
		return &Command{Cmd: cmd, Cleanup: func() { os.Remove(pass) }}, nil
	}
	return nil, fmt.Errorf("source %s is not a database", src.Name)
}

// Reader runs a dump Command and exposes its stdout. At EOF it waits for
// the dump to exit and returns an error instead of io.EOF if it failed or
// the output lacks the completion trailer. restic.Session.Run turns that
// error into "abort before EOF", so no snapshot is committed.
type Reader struct {
	c      *Command
	stdout io.ReadCloser
	stderr *bytes.Buffer
	tail   []byte
	done   bool
	err    error
}

const tailKeep = 8 << 10 // pg_dump 18 adds \unrestrict after its trailer

func StartReader(c *Command) (*Reader, error) {
	stdout, err := c.Cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &bytes.Buffer{}
	c.Cmd.Stderr = &limitedWriter{w: stderr, n: 16 << 10}
	c.Cmd.WaitDelay = 10 * time.Second
	if err := c.Cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s: %w", c.Cmd.Path, err)
	}
	return &Reader{c: c, stdout: stdout, stderr: stderr}, nil
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.final()
	}
	n, err := r.stdout.Read(p)
	if n > 0 {
		r.tail = append(r.tail, p[:n]...)
		if len(r.tail) > tailKeep {
			r.tail = r.tail[len(r.tail)-tailKeep:]
		}
	}
	if err == io.EOF {
		r.done = true
		r.err = r.finish()
		if n > 0 {
			return n, nil
		}
		return 0, r.final()
	}
	return n, err
}

func (r *Reader) final() error {
	if r.err != nil {
		return r.err
	}
	return io.EOF
}

func (r *Reader) finish() error {
	tool := r.c.Cmd.Path
	if err := r.c.Cmd.Wait(); err != nil {
		msg := strings.TrimSpace(r.stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "/*!1") && strings.Contains(msg, "1064") {
			msg += " -- this looks like MariaDB's dump tool talking to a MySQL server; install MySQL's own " +
				"mysqldump and point storagebd at it: storagebd settings set mysqldump-path /path/to/mysqldump"
		}
		return fmt.Errorf("%s failed: %s", tool, msg)
	}
	if r.c.Trailer != "" && !bytes.Contains(r.tail, []byte(r.c.Trailer)) {
		return fmt.Errorf("%s output is incomplete (no %q trailer); treating the dump as failed", tool, r.c.Trailer)
	}
	return nil
}

// Close stops the dump if it is still running (e.g. restic failed first).
func (r *Reader) Close() {
	if !r.done && r.c.Cmd.Process != nil {
		r.c.Cmd.Process.Kill()
		r.c.Cmd.Wait()
	}
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := len(p)
		if k > l.n {
			k = l.n
		}
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
