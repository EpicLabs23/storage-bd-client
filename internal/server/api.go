package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"storage-bd-client/internal/account"
	"storage-bd-client/internal/execsafe"
	"storage-bd-client/internal/jobs"
	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

func (s *Server) apiRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { s.hub.serveSSE(w, r) })

	mux.HandleFunc("POST /api/account/login", s.login)
	mux.HandleFunc("POST /api/account/logout", s.logout)
	mux.HandleFunc("GET /api/account", s.getAccount)
	mux.HandleFunc("GET /api/quote", s.getQuote)
	mux.HandleFunc("POST /api/connect", s.connect)
	mux.HandleFunc("POST /api/credentials", s.addCred)

	mux.HandleFunc("POST /api/credentials/{c}/default", s.withCred(s.setDefault))
	mux.HandleFunc("POST /api/credentials/{c}/activate", s.withCred(s.activate))
	mux.HandleFunc("POST /api/credentials/{c}/test", s.withCred(s.testCred))
	mux.HandleFunc("DELETE /api/credentials/{c}", s.withCred(s.deleteCred))

	mux.HandleFunc("GET /api/credentials/{c}/sources", s.withCred(s.listSources))
	mux.HandleFunc("POST /api/credentials/{c}/sources", s.withCred(s.createSource))
	mux.HandleFunc("PUT /api/credentials/{c}/sources/{s}", s.withCred(s.updateSource))
	mux.HandleFunc("DELETE /api/credentials/{c}/sources/{s}", s.withCred(s.deleteSource))

	mux.HandleFunc("GET /api/credentials/{c}/schedule", s.withCred(s.getSchedule))
	mux.HandleFunc("PUT /api/credentials/{c}/schedule", s.withCred(s.putSchedule))
	mux.HandleFunc("DELETE /api/credentials/{c}/schedule", s.withCred(s.deleteSchedule))

	mux.HandleFunc("GET /api/credentials/{c}/retention", s.withCred(s.getRetention))
	mux.HandleFunc("PUT /api/credentials/{c}/retention", s.withCred(s.putRetention))
	mux.HandleFunc("DELETE /api/credentials/{c}/retention", s.withCred(s.deleteRetention))
	mux.HandleFunc("POST /api/credentials/{c}/retention/preview", s.withCred(s.previewRetention))
	mux.HandleFunc("POST /api/credentials/{c}/cleanup", s.withCred(s.cleanup))

	mux.HandleFunc("POST /api/credentials/{c}/backup", s.withCred(s.backup))
	mux.HandleFunc("POST /api/credentials/{c}/cancel", s.withCred(s.cancel))
	mux.HandleFunc("GET /api/credentials/{c}/jobs", s.withCred(s.listJobs))
	mux.HandleFunc("GET /api/credentials/{c}/snapshots", s.withCred(s.listSnapshots))
	mux.HandleFunc("GET /api/credentials/{c}/ls", s.withCred(s.ls))
	mux.HandleFunc("POST /api/credentials/{c}/restore", s.withCred(s.restore))
	mux.HandleFunc("POST /api/credentials/{c}/restore-db", s.withCred(s.restoreDB))

	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSetting)
	return mux
}

// decode reads a JSON body (max 1 MB, unknown fields rejected).
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func (s *Server) withCred(h func(http.ResponseWriter, *http.Request, *store.Credential)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, err := s.Store.GetCredentialByName(r.Context(), r.PathValue("c"))
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such repo on this machine")
			return
		} else if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		h(w, r, cred)
	}
}

// fail maps an error to a status. Most errors here are user-actionable
// messages (validation, storage.bd responses), so they're shown as-is.
func fail(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var running *store.ErrJobRunning
	switch {
	case errors.Is(err, errBusy), errors.As(err, &running):
		status = http.StatusConflict
	case errors.Is(err, account.ErrNotLoggedIn):
		status = http.StatusUnauthorized
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	}
	writeErr(w, status, err.Error())
}

func (s *Server) apiURL(raw string) (string, error) {
	if raw == "" {
		raw = s.DefaultAPIURL
	}
	return store.ValidateAPIURL(raw, false)
}

// ---- state ----

type credView struct {
	Name               string     `json:"name"`
	Default            bool       `json:"default"`
	APIURL             string     `json:"api_url"`
	TenantID           string     `json:"tenant_id"`
	AccountEmail       string     `json:"account_email"`
	RepoInitializedAt  *time.Time `json:"repo_initialized_at"`
	PasswordEscrowedAt *time.Time `json:"password_escrowed_at"`
	Schedule           *schedView `json:"schedule"`
	Retention          *retView   `json:"retention"`
	LastJob            *jobView   `json:"last_job"`
	LastSuccess        *time.Time `json:"last_success"`
	Running            *running   `json:"running"`
	SourceCount        int        `json:"source_count"`
}

type schedView struct {
	Enabled       bool       `json:"enabled"`
	Mode          string     `json:"mode"`
	DailyTime     string     `json:"daily_time"`
	IntervalHours int        `json:"interval_hours"`
	NextRunAt     *time.Time `json:"next_run_at"`
	LastRunAt     *time.Time `json:"last_run_at"`
}

func toSchedView(sc *store.Schedule) *schedView {
	return &schedView{Enabled: sc.Enabled, Mode: sc.Mode,
		DailyTime:     fmt.Sprintf("%02d:%02d", sc.DailyMinute/60, sc.DailyMinute%60),
		IntervalHours: sc.IntervalHours, NextRunAt: sc.NextRunAt, LastRunAt: sc.LastRunAt}
}

type jobView struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Trigger    string          `json:"trigger"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Error      string          `json:"error"`
	Detail     json.RawMessage `json:"detail"`
}

func toJobView(j *store.Job) *jobView {
	return &jobView{j.ID, j.Kind, j.Trigger, j.Status, j.StartedAt, j.FinishedAt, j.Error, j.Detail}
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	creds, err := s.Store.ListCredentials(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	runningJobs := s.jobs.snapshot()
	views := []credView{}
	for _, c := range creds {
		v := credView{Name: c.Name, Default: c.IsDefault, APIURL: c.APIURL, TenantID: c.TenantID,
			AccountEmail: c.AccountEmail, RepoInitializedAt: c.RepoInitializedAt, PasswordEscrowedAt: c.PasswordEscrowedAt}
		if sc, err := s.Store.GetSchedule(ctx, c.ID); err == nil {
			v.Schedule = toSchedView(sc)
		}
		if rt, err := s.Store.GetRetention(ctx, c.ID); err == nil {
			v.Retention = toRetView(rt)
		}
		if list, err := s.Store.ListJobs(ctx, c.ID, 50); err == nil {
			if len(list) > 0 {
				v.LastJob = toJobView(list[0])
			}
			for _, j := range list {
				if j.Kind == store.JobBackup && (j.Status == store.StatusOK || j.Status == store.StatusPartial) {
					v.LastSuccess = j.FinishedAt
					break
				}
			}
		}
		if srcs, err := s.Store.ListSources(ctx, c.ID); err == nil {
			v.SourceCount = len(srcs)
		}
		if rj, ok := runningJobs[c.Name]; ok {
			v.Running = &rj
		}
		views = append(views, v)
	}

	type sessView struct {
		APIURL string `json:"api_url"`
		Email  string `json:"email"`
	}
	sessions := []sessView{}
	if list, err := s.Store.ListSessions(ctx); err == nil {
		for _, ss := range list {
			sessions = append(sessions, sessView{ss.APIURL, ss.Email})
		}
	}
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.Version, "default_api_url": s.DefaultAPIURL, "hostname": host,
		"default_name": account.DefaultName(), "running_as_root": os.Geteuid() == 0,
		"credentials": views, "sessions": sessions,
	})
}

// ---- account ----

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIURL   string `json:"api_url"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	apiURL, err := s.apiURL(body.APIURL)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.Accounts.Login(r.Context(), apiURL, body.Email, body.Password, body.Remember); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"api_url": apiURL})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIURL string `json:"api_url"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.Accounts.Logout(r.Context(), body.APIURL); err != nil && !errors.Is(err, account.ErrNotLoggedIn) {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	apiURL, err := s.apiURL(r.URL.Query().Get("api_url"))
	if err != nil {
		fail(w, err)
		return
	}
	o, err := s.Accounts.Overview(r.Context(), apiURL)
	if err != nil {
		fail(w, err)
		return
	}
	local := map[string]string{}
	if creds, err := s.Store.ListCredentials(r.Context()); err == nil {
		for _, c := range creds {
			if c.APIURL == apiURL {
				local[c.TenantID] = c.Name
			}
		}
	}
	type tv struct {
		TenantID      string    `json:"tenant_id"`
		Label         string    `json:"label"`
		StorageGB     int       `json:"storage_gb"`
		State         string    `json:"state"`
		SuspendReason *string   `json:"suspend_reason"`
		NextBillingAt time.Time `json:"next_billing_at"`
		Local         string    `json:"local_credential"`
	}
	tenants := []tv{}
	for _, t := range o.Tenants {
		tenants = append(tenants, tv{t.TenantID, t.LocalLabel, t.StorageGB, t.State, t.SuspendReason, t.NextBillingAt, local[t.TenantID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_url": o.APIURL, "email": o.Email, "balance": o.Balance,
		"currency": o.Currency, "tenants": tenants})
}

func (s *Server) getQuote(w http.ResponseWriter, r *http.Request) {
	apiURL, err := s.apiURL(r.URL.Query().Get("api_url"))
	if err != nil {
		fail(w, err)
		return
	}
	gb, _ := strconv.Atoi(r.URL.Query().Get("gb"))
	if gb < 1 {
		fail(w, errors.New("storage must be at least 1 GB"))
		return
	}
	q, err := s.Accounts.Quote(r.Context(), apiURL, gb)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIURL    string `json:"api_url"`
		Name      string `json:"name"`
		Label     string `json:"label"`
		StorageGB int    `json:"storage_gb"`
		Activate  bool   `json:"activate"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	apiURL, err := s.apiURL(body.APIURL)
	if err != nil {
		fail(w, err)
		return
	}
	res, err := s.Accounts.Connect(r.Context(), account.ConnectParams{APIURL: apiURL, Name: body.Name,
		Label: body.Label, StorageGB: body.StorageGB, Activate: body.Activate})
	var orphan *account.OrphanError
	if errors.As(err, &orphan) {
		// One-time secret: hand it to the user rather than lose it.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(),
			"tenant_id": orphan.TenantID, "install_id": orphan.InstallID, "install_secret": orphan.InstallSecret})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"name": res.Credential.Name, "activated": res.Activated}
	if res.ActivationError != nil {
		out["activation_error"] = res.ActivationError.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- credentials ----

// addCred stores a credential the user created in the storage.bd
// dashboard (the web counterpart of `credential add`). It's checked with
// storage.bd before saving, so a mistyped ID or secret fails here rather
// than on the first backup. No account sign-in is needed.
func (s *Server) addCred(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIURL         string `json:"api_url"`
		Name           string `json:"name"`
		TenantID       string `json:"tenant_id"`
		InstallID      string `json:"install_id"`
		InstallSecret  string `json:"install_secret"`
		ResticPassword string `json:"restic_password"` // empty: new repo, generate one
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	apiURL, err := s.apiURL(strings.TrimSpace(body.APIURL))
	if err != nil {
		fail(w, err)
		return
	}
	name, tenantID, installID := strings.TrimSpace(body.Name), strings.TrimSpace(body.TenantID), strings.TrimSpace(body.InstallID)
	secret := strings.TrimSpace(body.InstallSecret)
	for _, err := range []error{store.ValidateName(name), store.ValidateUUID("Space ID", tenantID),
		store.ValidateUUID("Space access key ID", installID),
		store.ValidateInstallSecret(secret)} {
		if err != nil {
			fail(w, err)
			return
		}
	}
	password := body.ResticPassword
	if password == "" {
		if password, err = store.GenerateResticPassword(); err != nil {
			fail(w, err)
			return
		}
	} else if err := store.ValidateResticPassword(password); err != nil {
		fail(w, err)
		return
	}
	c := &store.Credential{Name: name, APIURL: apiURL, TenantID: tenantID, InstallID: installID,
		InstallSecret: secret, ResticPassword: password}
	if sess, err := s.Store.GetSession(r.Context(), apiURL); err == nil {
		c.AccountEmail = sess.Email
	}
	if err := s.Accounts.TestCredential(r.Context(), c); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.CreateCredential(r.Context(), c); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": c.Name})
}

func (s *Server) setDefault(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if err := s.Store.SetDefaultCredential(r.Context(), c.Name); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) activate(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if err := s.Accounts.Activate(r.Context(), c); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) testCred(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if err := s.Accounts.TestCredential(r.Context(), c); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteCred(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var body struct {
		ConfirmName string `json:"confirm_name"`
	}
	if err := decode(r, &body); err != nil || body.ConfirmName != c.Name {
		fail(w, errors.New("type the repo name to confirm"))
		return
	}
	if err := s.Store.DeleteCredential(r.Context(), c.Name); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- sources ----

type sourceView struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Enabled     bool     `json:"enabled"`
	Path        string   `json:"path,omitempty"`
	Excludes    []string `json:"excludes,omitempty"`
	DBName      string   `json:"db_name,omitempty"`
	DBHost      string   `json:"db_host,omitempty"`
	DBPort      int      `json:"db_port,omitempty"`
	DBSocket    string   `json:"db_socket,omitempty"`
	DBUser      string   `json:"db_user,omitempty"`
	HasPassword bool     `json:"has_password"`
}

// sourceBody is create/update input. DBPassword nil on update = keep.
type sourceBody struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Enabled    *bool    `json:"enabled"`
	Path       string   `json:"path"`
	Excludes   []string `json:"excludes"`
	DBName     string   `json:"db_name"`
	DBHost     string   `json:"db_host"`
	DBPort     int      `json:"db_port"`
	DBSocket   string   `json:"db_socket"`
	DBUser     string   `json:"db_user"`
	DBPassword *string  `json:"db_password"`
}

func (b *sourceBody) apply(src *store.Source) {
	src.Name, src.Path, src.Excludes = b.Name, b.Path, b.Excludes
	src.DBName, src.DBHost, src.DBPort, src.DBSocket, src.DBUser = b.DBName, b.DBHost, b.DBPort, b.DBSocket, b.DBUser
	if b.Enabled != nil {
		src.Enabled = *b.Enabled
	}
	if b.DBPassword != nil {
		src.DBPassword = *b.DBPassword
	}
}

func checkDir(src *store.Source) error {
	if src.Kind != store.KindDir {
		return nil
	}
	info, err := os.Stat(src.Path)
	if err != nil {
		return fmt.Errorf("%s: %w", src.Path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src.Path)
	}
	return nil
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	srcs, err := s.Store.ListSources(r.Context(), c.ID)
	if err != nil {
		fail(w, err)
		return
	}
	out := []sourceView{}
	for _, x := range srcs {
		out = append(out, sourceView{x.Name, x.Kind, x.Enabled, x.Path, x.Excludes, x.DBName, x.DBHost,
			x.DBPort, x.DBSocket, x.DBUser, x.DBPassword != ""})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b sourceBody
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	src := &store.Source{CredentialID: c.ID, Kind: b.Kind, Enabled: true}
	b.apply(src)
	if err := src.Validate(); err != nil {
		fail(w, err)
		return
	}
	if err := checkDir(src); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.CreateSource(r.Context(), src); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": src.Name})
}

func (s *Server) updateSource(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	src, err := s.Store.GetSource(r.Context(), c.ID, r.PathValue("s"))
	if err != nil {
		fail(w, err)
		return
	}
	var b sourceBody
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if b.Kind != "" && b.Kind != src.Kind {
		fail(w, errors.New("a source's kind can't be changed; add a new source instead"))
		return
	}
	b.apply(src)
	if err := src.Validate(); err != nil {
		fail(w, err)
		return
	}
	if err := checkDir(src); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.UpdateSource(r.Context(), src); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": src.Name})
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if err := s.Store.DeleteSource(r.Context(), c.ID, r.PathValue("s")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- schedule ----

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	sc, err := s.Store.GetSchedule(r.Context(), c.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	} else if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSchedView(sc))
}

func (s *Server) putSchedule(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b struct {
		Enabled       bool   `json:"enabled"`
		Mode          string `json:"mode"`
		DailyTime     string `json:"daily_time"`
		IntervalHours int    `json:"interval_hours"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	sc := &store.Schedule{CredentialID: c.ID, Enabled: b.Enabled, Mode: b.Mode, IntervalHours: b.IntervalHours, DailyMinute: 120}
	if b.Mode == store.ScheduleDaily {
		t, err := time.Parse("15:04", b.DailyTime)
		if err != nil {
			fail(w, errors.New("daily time must be HH:MM"))
			return
		}
		sc.DailyMinute = t.Hour()*60 + t.Minute()
	}
	if sc.IntervalHours == 0 {
		sc.IntervalHours = 24
	}
	if err := s.Store.SaveSchedule(r.Context(), sc); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSchedView(sc))
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if err := s.Store.DeleteSchedule(r.Context(), c.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- jobs ----

func (s *Server) backup(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b struct {
		Sources []string `json:"sources"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if err := s.startBackup(c, store.TriggerManual, b.Sources); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if !s.jobs.cancel(c.Name) {
		fail(w, errors.New("nothing is running for this repo in this UI"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 50
	}
	list, err := s.Store.ListJobs(r.Context(), c.ID, limit)
	if err != nil {
		fail(w, err)
		return
	}
	out := []*jobView{}
	for _, j := range list {
		out = append(out, toJobView(j))
	}
	writeJSON(w, http.StatusOK, out)
}

// openRepo opens c's repo for a short read-only operation, bounded so a
// hung storage.bd can't pin the request forever.
func (s *Server) openRepo(r *http.Request, c *store.Credential, timeout time.Duration) (context.Context, context.CancelFunc, func(), *restic.Session, error) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	o, err := s.Repos.Open(ctx, c)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	return ctx, cancel, func() { o.Close() }, o.Session, nil
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	ctx, cancel, closeFn, sess, err := s.openRepo(r, c, 2*time.Minute)
	if err != nil {
		fail(w, err)
		return
	}
	defer cancel()
	defer closeFn()
	tags := []string{jobs.TagClient}
	if src := r.URL.Query().Get("source"); src != "" {
		tags = append(tags, jobs.SourceTag(src))
	}
	snaps, err := sess.Snapshots(ctx, tags...)
	if err != nil {
		fail(w, err)
		return
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	type sv struct {
		restic.Snapshot
		Source string `json:"source"`
	}
	out := []sv{}
	for _, sn := range snaps {
		v := sv{Snapshot: sn}
		for _, t := range sn.Tags {
			if strings.HasPrefix(t, "source:") {
				v.Source = strings.TrimPrefix(t, "source:")
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ls(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	q := r.URL.Query()
	snap, dir := q.Get("snapshot"), q.Get("path")
	if snap == "" || strings.ContainsAny(snap, "/ ") || strings.HasPrefix(snap, "-") {
		fail(w, errors.New("invalid snapshot id"))
		return
	}
	if dir == "" {
		dir = "/"
	}
	if !strings.HasPrefix(dir, "/") {
		fail(w, errors.New("path must be absolute"))
		return
	}
	ctx, cancel, closeFn, sess, err := s.openRepo(r, c, 2*time.Minute)
	if err != nil {
		fail(w, err)
		return
	}
	defer cancel()
	defer closeFn()
	nodes, truncated, err := sess.Ls(ctx, snap, filepath.Clean(dir), 5000)
	if err != nil {
		fail(w, err)
		return
	}
	if nodes == nil {
		nodes = []restic.Node{}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if (nodes[i].Type == "dir") != (nodes[j].Type == "dir") {
			return nodes[i].Type == "dir"
		}
		return nodes[i].Name < nodes[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.Clean(dir), "nodes": nodes, "truncated": truncated})
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b struct {
		Source           string   `json:"source"`
		Snapshot         string   `json:"snapshot"`
		Target           string   `json:"target"`
		Includes         []string `json:"includes"`
		ConfirmOverwrite bool     `json:"confirm_overwrite"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if !filepath.IsAbs(b.Target) {
		fail(w, errors.New("restore target must be an absolute path"))
		return
	}
	target := filepath.Clean(b.Target)
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 && !b.ConfirmOverwrite {
		writeJSON(w, http.StatusConflict, map[string]any{"error": target + " is not empty; files from the snapshot will overwrite files there",
			"needs_confirm": true})
		return
	}
	err := s.jobs.start(c, store.JobRestore, store.TriggerManual, func(ctx context.Context, emit func(jobs.Event)) (*store.Job, error) {
		job, _, err := s.Runner.RestoreFiles(ctx, c, jobs.RestoreOptions{Source: b.Source, SnapshotID: b.Snapshot,
			Target: target, Includes: b.Includes})
		return job, err
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) restoreDB(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b struct {
		Source   string `json:"source"`
		Snapshot string `json:"snapshot"`
		Into     string `json:"into"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	err := s.jobs.start(c, store.JobRestoreDB, store.TriggerManual, func(ctx context.Context, emit func(jobs.Event)) (*store.Job, error) {
		job, _, err := s.Runner.RestoreDB(ctx, c, jobs.RestoreDBOptions{Source: b.Source, SnapshotID: b.Snapshot, Into: b.Into})
		return job, err
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// ---- settings ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	type setting struct {
		Key         string `json:"key"`
		Value       string `json:"value"`
		Description string `json:"description"`
	}
	out := []setting{}
	for k, d := range store.KnownSettings() {
		v, _ := s.Store.GetSetting(r.Context(), k)
		out = append(out, setting{k, v, d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	resticInfo := map[string]string{}
	if bin, ver, err := s.Repos.ResticBinary(r.Context()); err != nil {
		resticInfo["error"] = err.Error()
	} else {
		resticInfo["path"], resticInfo["version"] = bin, ver
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": out, "restic": resticInfo})
}

func (s *Server) putSetting(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if b.Value == "" {
		if err := s.Store.UnsetSetting(r.Context(), b.Key); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if store.IsBinarySetting(b.Key) {
		if err := execsafe.VerifyBinary(b.Value); err != nil {
			fail(w, err)
			return
		}
	}
	if b.Key == store.SettingResticPath {
		if _, err := restic.Version(r.Context(), b.Value); err != nil {
			fail(w, err)
			return
		}
	}
	if err := s.Store.SetSetting(r.Context(), b.Key, b.Value); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- retention ----

type retView struct {
	Enabled           bool       `json:"enabled"`
	KeepLast          int        `json:"keep_last"`
	KeepDaily         int        `json:"keep_daily"`
	KeepWeekly        int        `json:"keep_weekly"`
	KeepMonthly       int        `json:"keep_monthly"`
	KeepYearly        int        `json:"keep_yearly"`
	PruneIntervalDays int        `json:"prune_interval_days"`
	LastForgetAt      *time.Time `json:"last_forget_at"`
	LastPruneAt       *time.Time `json:"last_prune_at"`
}

func toRetView(r *store.Retention) *retView {
	return &retView{r.Enabled, r.KeepLast, r.KeepDaily, r.KeepWeekly, r.KeepMonthly, r.KeepYearly,
		r.PruneIntervalDays, r.LastForgetAt, r.LastPruneAt}
}

type retBody struct {
	KeepLast          int `json:"keep_last"`
	KeepDaily         int `json:"keep_daily"`
	KeepWeekly        int `json:"keep_weekly"`
	KeepMonthly       int `json:"keep_monthly"`
	KeepYearly        int `json:"keep_yearly"`
	PruneIntervalDays int `json:"prune_interval_days"`
}

func (b *retBody) policy(credID int64) *store.Retention {
	days := b.PruneIntervalDays
	if days == 0 {
		days = 7
	}
	return &store.Retention{CredentialID: credID, Enabled: true, KeepLast: b.KeepLast, KeepDaily: b.KeepDaily,
		KeepWeekly: b.KeepWeekly, KeepMonthly: b.KeepMonthly, KeepYearly: b.KeepYearly, PruneIntervalDays: days}
}

func (s *Server) getRetention(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	rt, err := s.Store.GetRetention(r.Context(), c.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"policy": nil, "suggested": toRetView(store.DefaultRetention(c.ID))})
		return
	} else if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": toRetView(rt), "suggested": toRetView(store.DefaultRetention(c.ID))})
}

func (s *Server) putRetention(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b retBody
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	pol := b.policy(c.ID)
	if err := s.Store.SaveRetention(r.Context(), pol); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRetView(pol))
}

func (s *Server) deleteRetention(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	if err := s.Store.DeleteRetention(r.Context(), c.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// previewRetention runs forget --dry-run with the posted (unsaved) policy,
// so the UI can show the effect before the user saves it.
func (s *Server) previewRetention(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b retBody
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	d, err := s.Runner.PreviewCleanup(ctx, c, b.policy(c.ID))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) cleanup(w http.ResponseWriter, r *http.Request, c *store.Credential) {
	var b struct {
		Prune bool `json:"prune"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if rt, err := s.Store.GetRetention(r.Context(), c.ID); err != nil || !rt.Enabled {
		fail(w, jobs.ErrNoRetention)
		return
	}
	err := s.jobs.start(c, store.JobCleanup, store.TriggerManual, func(ctx context.Context, emit func(jobs.Event)) (*store.Job, error) {
		job, _, err := s.Runner.Cleanup(ctx, c, jobs.CleanupOptions{Trigger: store.TriggerManual, ForcePrune: b.Prune, OnEvent: emit})
		return job, err
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}
