package restic

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Exit codes restic documents (0.17 added 10/11/12; 0.16 reports those
// cases as 1 with a message, see classify).
const (
	ExitOK            = 0
	ExitFatal         = 1
	ExitPartial       = 3 // backup: some source files unreadable, snapshot still made
	ExitNoRepo        = 10
	ExitLockFailed    = 11
	ExitWrongPassword = 12
	ExitInterrupted   = 130
)

var (
	ErrNoRepo        = errors.New("repository does not exist")
	ErrWrongPassword = errors.New("wrong restic password for this repository")
	ErrLocked        = errors.New("repository is locked by another operation")
)

// Error is a failed restic run. Kind is one of the sentinel errors above
// when the failure was recognized, so callers can errors.Is it.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
	Kind     error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 2000 {
		msg = "..." + msg[len(msg)-2000:]
	}
	op := "restic"
	if len(e.Args) > 0 {
		op += " " + e.Args[0]
	}
	if e.Kind != nil {
		return fmt.Sprintf("%s: %v (exit %d)", op, e.Kind, e.ExitCode)
	}
	return fmt.Sprintf("%s failed (exit %d): %s", op, e.ExitCode, msg)
}

func (e *Error) Unwrap() error { return e.Kind }

func classify(code int, stderr string) error {
	switch {
	case code == ExitNoRepo || strings.Contains(stderr, "Is there a repository at the following location?"):
		return ErrNoRepo
	case code == ExitWrongPassword || strings.Contains(stderr, "wrong password or no key found"):
		return ErrWrongPassword
	case code == ExitLockFailed || strings.Contains(stderr, "repository is already locked"):
		return ErrLocked
	}
	return nil
}

// Session is one repository opened for one or more restic runs: a relay,
// a password file, and the environment pointing restic at both. Close it
// when done; that removes the password file and stops the relay.
type Session struct {
	bin          string
	relay        *Relay
	passwordFile string
	env          []string
}

// SessionConfig describes how to reach one repo.
type SessionConfig struct {
	Binary   string      // verified restic path
	APIURL   string      // storage.bd API base URL
	Tokens   TokenSource // data tokens for this repo's tenant
	Password string      // restic repo password
	DataDir  string      // client data dir: cache/ and tmp/ go under it
	CacheKey string      // per-repo cache subdirectory (tenant ID)
}

func OpenSession(c SessionConfig) (*Session, error) {
	tmpDir := filepath.Join(c.DataDir, "tmp")
	cacheDir := filepath.Join(c.DataDir, "cache", c.CacheKey)
	for _, d := range []string{tmpDir, cacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}

	// §19: the password reaches restic only via a 0600 file, never argv or
	// a plain env var. It lives for this session only.
	f, err := os.CreateTemp(tmpDir, "pw-*")
	if err != nil {
		return nil, err
	}
	passwordFile := f.Name()
	if err := f.Chmod(0o600); err == nil {
		_, err = f.WriteString(c.Password)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(passwordFile)
		return nil, err
	}

	relay, err := StartRelay(c.APIURL, c.Tokens)
	if err != nil {
		os.Remove(passwordFile)
		return nil, err
	}

	return &Session{
		bin:          c.Binary,
		relay:        relay,
		passwordFile: passwordFile,
		// §19: explicit, minimal environment, nothing inherited.
		env: []string{
			"RESTIC_REPOSITORY=" + relay.RepoURL(),
			"RESTIC_PASSWORD_FILE=" + passwordFile,
			"RESTIC_CACHE_DIR=" + filepath.Join(c.DataDir, "cache", c.CacheKey),
			"TMPDIR=" + tmpDir,
			"HOME=" + c.DataDir,
			"PATH=/usr/bin:/bin",
			// Emit --json status lines even though stdout isn't a TTY.
			"RESTIC_PROGRESS_FPS=1",
		},
	}, nil
}

func (s *Session) Close() error {
	err := s.relay.Close()
	if rerr := os.Remove(s.passwordFile); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}

// RunOptions controls one restic invocation.
type RunOptions struct {
	// Stdin is copied to restic's stdin. If reading it fails (e.g. a
	// database dump exited non-zero), restic is interrupted *before* it
	// sees EOF, so `backup --stdin` never commits a snapshot of truncated
	// data, and Run returns that read error.
	Stdin  io.Reader
	Stdout io.Writer    // raw stdout (e.g. `dump`); mutually exclusive with OnLine
	OnLine func([]byte) // called per stdout line (--json output)
	// OnStderrLine is called per stderr line. restic writes --json
	// "error" messages to stderr, not stdout.
	OnStderrLine func([]byte)
}

// abortGrace is how long restic gets to exit after SIGINT when its input
// failed, before it is killed.
var abortGrace = 10 * time.Second

// stderrLimit caps how much stderr is kept for error messages.
const stderrLimit = 64 << 10

// Run executes restic with args. Cancelling ctx sends SIGINT first so
// restic can remove its repository lock, then kills it after 30s.
// Exit code 3 is returned as an *Error too; callers that accept partial
// success check for it.
func (s *Session) Run(ctx context.Context, args []string, opts RunOptions) error {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	cmd.Env = s.env
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second

	stderr := &tailBuffer{max: stderrLimit}
	cmd.Stderr = stderr
	if opts.OnStderrLine != nil {
		lw := &lineWriter{fn: opts.OnStderrLine}
		defer lw.flush()
		cmd.Stderr = io.MultiWriter(stderr, lw)
	}

	var stdout io.ReadCloser
	if opts.OnLine != nil {
		var err error
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return err
		}
	} else {
		cmd.Stdout = opts.Stdout
	}

	var stdinPipe io.WriteCloser
	if opts.Stdin != nil {
		var err error
		if stdinPipe, err = cmd.StdinPipe(); err != nil {
			return err
		}
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start restic: %w", err)
	}

	stdinDone := make(chan error, 1)
	procDone := make(chan struct{}) // closed once cmd.Wait has returned
	killed := false                 // written before stdinDone is sent, read after
	if stdinPipe != nil {
		go func() {
			src := &readErrCapture{r: opts.Stdin}
			io.Copy(stdinPipe, src) // a write error just means restic exited early
			if src.err != nil {
				// Producer failed: stop restic while its stdin is still open,
				// so it never sees EOF and can't commit a snapshot of the
				// truncated stream. SIGINT first (restic drops its lock); but
				// restic >= 0.17 keeps blocking on a stdin read after SIGINT,
				// so SIGKILL after a grace period. Wait closes the pipe.
				cmd.Process.Signal(os.Interrupt)
				select {
				case <-procDone:
				case <-time.After(abortGrace):
					cmd.Process.Kill()
					killed = true
				}
				stdinDone <- src.err
				return
			}
			stdinPipe.Close()
			stdinDone <- nil
		}()
	} else {
		stdinDone <- nil
	}

	if stdout != nil {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			opts.OnLine(sc.Bytes())
		}
		io.Copy(io.Discard, stdout) // drain if a line was oversized
	}
	err := cmd.Wait()
	close(procDone)
	if stdinErr := <-stdinDone; stdinErr != nil {
		if killed {
			// A killed restic leaves its (non-exclusive) lock behind.
			// `unlock` only removes locks that are stale, never live ones.
			uctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			s.Run(uctx, []string{"unlock"}, RunOptions{Stdout: io.Discard})
			cancel()
		}
		return fmt.Errorf("input for restic failed, backup aborted: %w", stdinErr)
	}
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	code := exitErr.ExitCode()
	if ctx.Err() != nil {
		code = ExitInterrupted
	}
	return &Error{Args: args, ExitCode: code, Stderr: stderr.String(), Kind: classify(code, stderr.String())}
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return t.buf.String() }

// lineWriter calls fn for each complete line written to it. exec copies
// stderr from a single goroutine, so no locking is needed.
type lineWriter struct {
	buf []byte
	fn  func([]byte)
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.fn(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 4<<20 { // pathological line: drop rather than grow forever
		l.buf = l.buf[:0]
	}
	return len(p), nil
}

func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		l.fn(l.buf)
		l.buf = nil
	}
}

// readErrCapture records the first non-EOF read error, so Run can tell a
// failing producer apart from restic closing its stdin.
type readErrCapture struct {
	r   io.Reader
	err error
}

func (c *readErrCapture) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil && err != io.EOF && c.err == nil {
		c.err = err
	}
	return n, err
}
