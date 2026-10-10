package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"storage-bd-client/internal/jobs"
	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

// hub fans out events to every connected UI (SSE).
type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newHub() *hub { return &hub{subs: map[chan []byte]struct{}{}} }

// UIEvent is what the browser receives.
type UIEvent struct {
	Type       string             `json:"type"` // job-start, progress, source-start, source-done, warning, job-done
	Credential string             `json:"credential"`
	JobID      int64              `json:"job_id,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Source     string             `json:"source,omitempty"`
	Message    string             `json:"message,omitempty"`
	Status     string             `json:"status,omitempty"`
	Progress   *restic.Progress   `json:"progress,omitempty"`
	Result     *jobs.SourceResult `json:"result,omitempty"`
}

func (h *hub) publish(e UIEvent) {
	b, _ := json.Marshal(e)
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // slow client: drop rather than block a backup
		}
	}
}

func (h *hub) serveSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// running is a job this serve process started.
type running struct {
	Kind      string           `json:"kind"`
	JobID     int64            `json:"job_id"`
	Trigger   string           `json:"trigger"`
	StartedAt time.Time        `json:"started_at"`
	Source    string           `json:"source,omitempty"`
	Progress  *restic.Progress `json:"progress,omitempty"`
	cancel    context.CancelFunc
}

// jobManager runs jobs in the background, one per credential (the store's
// job lock still guards against other processes, e.g. a CLI backup).
type jobManager struct {
	s      *Server
	mu     sync.Mutex
	byCred map[string]*running
	wg     sync.WaitGroup
}

func newJobManager(s *Server) *jobManager {
	return &jobManager{s: s, byCred: map[string]*running{}}
}

var errBusy = errors.New("a job is already running for this repo")

func (m *jobManager) snapshot() map[string]running {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]running{}
	for k, v := range m.byCred {
		out[k] = *v
	}
	return out
}

// start runs fn in the background for cred. fn reports its own events
// through emit; start publishes job-done when fn returns.
func (m *jobManager) start(cred *store.Credential, kind, trigger string,
	fn func(ctx context.Context, emit func(jobs.Event)) (*store.Job, error)) error {
	m.mu.Lock()
	if _, busy := m.byCred[cred.Name]; busy {
		m.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{Kind: kind, Trigger: trigger, StartedAt: time.Now(), cancel: cancel}
	m.byCred[cred.Name] = r
	m.mu.Unlock()

	emit := func(e jobs.Event) {
		m.mu.Lock()
		switch e.Type {
		case "job-start":
			r.JobID = e.JobID
		case "source-start":
			r.Source, r.Progress = e.Source, nil
		case "phase":
			r.Source, r.Progress = e.Message, nil
		case "progress":
			r.Progress = e.Progress
		}
		m.mu.Unlock()
		m.s.hub.publish(UIEvent{Type: e.Type, Credential: cred.Name, JobID: e.JobID, Kind: kind,
			Source: e.Source, Message: e.Message, Progress: e.Progress, Result: e.Result})
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		job, err := fn(ctx, emit)
		m.mu.Lock()
		delete(m.byCred, cred.Name)
		m.mu.Unlock()

		ev := UIEvent{Type: "job-done", Credential: cred.Name, Kind: kind}
		if job != nil {
			ev.JobID, ev.Status, ev.Message = job.ID, job.Status, job.Error
		}
		if err != nil {
			ev.Status, ev.Message = store.StatusFailed, err.Error()
		}
		log.Printf("%s %s for %s: %s %s", trigger, kind, cred.Name, ev.Status, ev.Message)
		m.s.hub.publish(ev)
	}()
	return nil
}

func (m *jobManager) cancel(credName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byCred[credName]
	if ok {
		r.cancel()
	}
	return ok
}

func (m *jobManager) cancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.byCred {
		r.cancel()
	}
}

func (m *jobManager) wait() { m.wg.Wait() }

func (s *Server) startBackup(cred *store.Credential, trigger string, sources []string) error {
	return s.jobs.start(cred, store.JobBackup, trigger, func(ctx context.Context, emit func(jobs.Event)) (*store.Job, error) {
		job, _, err := s.Runner.Backup(ctx, cred, jobs.BackupOptions{Trigger: trigger, Sources: sources, OnEvent: emit})
		if err != nil {
			return job, err
		}
		// Retention runs as its own job record but inside the same UI slot,
		// so the repo stays "busy" until both are done.
		emit(jobs.Event{Type: "phase", Message: "Removing old snapshots…"})
		cj, d, cerr := s.Runner.AfterBackup(ctx, cred, job, jobs.CleanupOptions{Trigger: trigger})
		switch {
		case cerr != nil:
			emit(jobs.Event{Type: "warning", Message: "removing old snapshots failed: " + cerr.Error()})
		case cj != nil && cj.Status != store.StatusOK:
			emit(jobs.Event{Type: "warning", Message: "removing old snapshots " + cj.Status + ": " + cj.Error})
		case cj != nil && d.Removed > 0:
			emit(jobs.Event{Type: "warning", Message: fmt.Sprintf("removed %d old snapshot(s) by the cleanup policy", d.Removed)})
		}
		return job, nil
	})
}

// runScheduler starts due scheduled backups. A run that finds the repo
// busy is skipped (and logged) rather than queued: the next slot comes
// soon enough, and stacking runs would only pile up.
func (s *Server) runScheduler(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		s.scheduleTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) scheduleTick(ctx context.Context) {
	now := time.Now()
	due, err := s.Store.DueSchedules(ctx, now)
	if err != nil {
		log.Printf("scheduler: %v", err)
		return
	}
	for _, sch := range due {
		cred, err := s.Store.GetCredentialByID(ctx, sch.CredentialID)
		if err != nil {
			log.Printf("scheduler: credential %d: %v", sch.CredentialID, err)
			continue
		}
		// Advance first, so a failing or busy run doesn't retry every tick.
		if err := s.Store.MarkScheduleRun(ctx, sch, now); err != nil {
			log.Printf("scheduler: %v", err)
			continue
		}
		if err := s.startBackup(cred, store.TriggerSchedule, nil); err != nil {
			log.Printf("scheduler: skipped %s: %v", cred.Name, err)
			s.hub.publish(UIEvent{Type: "warning", Credential: cred.Name,
				Message: "scheduled backup skipped: " + err.Error()})
		}
	}
}
