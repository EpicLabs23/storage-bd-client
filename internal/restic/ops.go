package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Init creates the repository. Returns ErrRepoExists if it already
// exists (restic's own wording, as ecp-go also matches).
func (s *Session) Init(ctx context.Context) error {
	err := s.Run(ctx, []string{"init"}, RunOptions{})
	var re *Error
	if errors.As(err, &re) && strings.Contains(re.Stderr, "config file already exists") {
		return ErrRepoExists
	}
	return err
}

var ErrRepoExists = errors.New("repository already initialized")

// VerifyPassword confirms the session's password opens the repository.
// Returns ErrWrongPassword or ErrNoRepo (via errors.Is) when not.
func (s *Session) VerifyPassword(ctx context.Context) error {
	return s.Run(ctx, []string{"cat", "config"}, RunOptions{Stdout: io.Discard})
}

type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username"`
}

// Snapshots lists snapshots, optionally filtered to those carrying all
// of tags.
func (s *Session) Snapshots(ctx context.Context, tags ...string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	var out strings.Builder
	if err := s.Run(ctx, args, RunOptions{Stdout: &out}); err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal([]byte(out.String()), &snaps); err != nil {
		return nil, fmt.Errorf("could not parse restic snapshot list: %w", err)
	}
	return snaps, nil
}

// Progress is restic's --json "status" message.
type Progress struct {
	PercentDone      float64  `json:"percent_done"`
	TotalFiles       int64    `json:"total_files"`
	FilesDone        int64    `json:"files_done"`
	TotalBytes       int64    `json:"total_bytes"`
	BytesDone        int64    `json:"bytes_done"`
	ErrorCount       int64    `json:"error_count"`
	SecondsElapsed   int64    `json:"seconds_elapsed"`
	SecondsRemaining int64    `json:"seconds_remaining"`
	CurrentFiles     []string `json:"current_files"`
}

// FileError is one per-file problem restic reported but worked around
// (the stuff behind exit code 3).
type FileError struct {
	During  string `json:"during"`
	Item    string `json:"item"`
	Message string `json:"message"`
}

// BackupSummary is restic's --json "summary" message plus what we
// gathered along the way.
type BackupSummary struct {
	SnapshotID          string  `json:"snapshot_id"`
	FilesNew            int64   `json:"files_new"`
	FilesChanged        int64   `json:"files_changed"`
	FilesUnmodified     int64   `json:"files_unmodified"`
	DataAdded           int64   `json:"data_added"`
	TotalFilesProcessed int64   `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	TotalDuration       float64 `json:"total_duration"`

	// Partial is exit code 3: the snapshot exists but some files were
	// skipped (§19: "succeeded with warnings", not a failure).
	Partial bool        `json:"-"`
	Errors  []FileError `json:"-"`
}

type BackupOptions struct {
	Paths    []string // absolute, already canonicalized by the caller
	Stdin    io.Reader
	StdinAs  string // --stdin-filename, required with Stdin
	Tags     []string
	Excludes []string
	Host     string
	OnStatus func(Progress)
}

// Backup runs `restic backup --json`. Exit code 3 returns a summary with
// Partial set and a nil error.
func (s *Session) Backup(ctx context.Context, o BackupOptions) (*BackupSummary, error) {
	if (o.Stdin == nil) == (len(o.Paths) == 0) {
		return nil, errors.New("backup needs either paths or stdin, not both")
	}
	args := []string{"backup", "--json"}
	if o.Host != "" {
		args = append(args, "--host", o.Host)
	}
	for _, t := range o.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range o.Excludes {
		args = append(args, "--exclude", e)
	}
	if o.Stdin != nil {
		if o.StdinAs == "" {
			return nil, errors.New("stdin backup needs a filename")
		}
		args = append(args, "--stdin", "--stdin-filename", o.StdinAs)
	} else {
		// "--" so a path can never be parsed as a flag.
		args = append(append(args, "--"), o.Paths...)
	}

	summary := &BackupSummary{}
	gotSummary := false
	// stdout and stderr lines arrive on different goroutines.
	var errMu sync.Mutex
	addError := func(line []byte) {
		errMu.Lock()
		summary.Errors = append(summary.Errors, parseFileError(line))
		errMu.Unlock()
	}
	onStderr := func(line []byte) {
		var head struct {
			Type string `json:"message_type"`
		}
		if json.Unmarshal(line, &head) == nil && head.Type == "error" {
			addError(line)
		}
	}
	err := s.Run(ctx, args, RunOptions{Stdin: o.Stdin, OnStderrLine: onStderr, OnLine: func(line []byte) {
		var head struct {
			Type string `json:"message_type"`
		}
		if json.Unmarshal(line, &head) != nil {
			return
		}
		switch head.Type {
		case "status":
			if o.OnStatus != nil {
				var p Progress
				if json.Unmarshal(line, &p) == nil {
					o.OnStatus(p)
				}
			}
		case "error": // in case a restic version moves these to stdout
			addError(line)
		case "summary":
			if json.Unmarshal(line, summary) == nil {
				gotSummary = true
			}
		}
	}})

	var re *Error
	if errors.As(err, &re) && re.ExitCode == ExitPartial {
		summary.Partial = true
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if !gotSummary || summary.SnapshotID == "" {
		return nil, errors.New("restic finished without reporting a snapshot")
	}
	return summary, nil
}

// parseFileError handles both restic 0.16 ("error":{} — Go error values
// marshal empty) and 0.17+ ("error":{"message":...}).
func parseFileError(line []byte) FileError {
	var m struct {
		Error  json.RawMessage `json:"error"`
		During string          `json:"during"`
		Item   string          `json:"item"`
	}
	json.Unmarshal(line, &m)
	fe := FileError{During: m.During, Item: m.Item}
	var withMsg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(m.Error, &withMsg) == nil && withMsg.Message != "" {
		fe.Message = withMsg.Message
	} else if s := string(m.Error); s != "" && s != "{}" && s != "null" {
		fe.Message = s
	} else {
		fe.Message = "could not be read"
	}
	return fe
}

type RestoreOptions struct {
	SnapshotID string
	Target     string   // absolute directory, validated by the caller
	Includes   []string // paths within the snapshot; empty = everything
}

// Restore runs `restic restore` into Target.
func (s *Session) Restore(ctx context.Context, o RestoreOptions) error {
	if o.SnapshotID == "" || o.Target == "" {
		return errors.New("restore needs a snapshot and a target directory")
	}
	args := []string{"restore", "--target", o.Target}
	for _, inc := range o.Includes {
		args = append(args, "--include", inc)
	}
	args = append(args, "--", o.SnapshotID)
	return s.Run(ctx, args, RunOptions{Stdout: io.Discard})
}

// Dump streams one file from a snapshot to w (database restores pipe this
// into mysql/psql, §03).
func (s *Session) Dump(ctx context.Context, snapshotID, path string, w io.Writer) error {
	return s.Run(ctx, []string{"dump", "--", snapshotID, path}, RunOptions{Stdout: w})
}
