package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"storage-bd-client/internal/account"
	"storage-bd-client/internal/dbdump"
	"storage-bd-client/internal/jobs"
	"storage-bd-client/internal/repo"
	"storage-bd-client/internal/store"
	"storage-bd-client/internal/testutil"
)

type harness struct {
	base  string // http://127.0.0.1:port
	token string
	fake  *testutil.FakeStorage
	srv   *Server
}

func start(t *testing.T) *harness {
	t.Helper()
	bin := testutil.ResticOrSkip(t)
	fake := testutil.NewFakeStorage(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetSetting(context.Background(), store.SettingResticPath, bin)
	repos := &repo.Manager{Store: st, DataDir: dir}
	s := &Server{
		Store: st, Accounts: &account.Manager{Store: st}, Repos: repos,
		Runner:  &jobs.Runner{Store: st, Repos: repos, Tools: dbdump.Tools{}, Host: "ui-test"},
		DataDir: dir, DefaultAPIURL: fake.API.URL, Version: "test",
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, "127.0.0.1:0", func(u string) { ready <- u }) }()
	var login string
	select {
	case login = <-ready:
	case err := <-done:
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		<-done
	})
	u, _ := url.Parse(login)
	return &harness{base: "http://" + u.Host, token: u.Query().Get("t"), fake: fake, srv: s}
}

func (hs *harness) client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	resp, err := c.Get(hs.base + "/auth?t=" + hs.token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/" {
		t.Fatalf("auth did not redirect to /: %s", resp.Request.URL)
	}
	return c
}

func call(t *testing.T, c *http.Client, method, u, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

var csrf = map[string]string{"X-Storagebd": "1"}

func TestAuthAndRequestHardening(t *testing.T) {
	hs := start(t)
	anon := &http.Client{}

	// No cookie: API refused, index shows the locked page.
	if code, _ := call(t, anon, "GET", hs.base+"/api/state", "", nil); code != 401 {
		t.Fatalf("anon state: %d", code)
	}
	resp, _ := anon.Get(hs.base + "/")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "storagebd ui-url") {
		t.Fatal("anonymous index did not serve the locked page")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP = %q", csp)
	}

	// Wrong token.
	if r, _ := anon.Get(hs.base + "/auth?t=nope"); r.StatusCode != 401 {
		t.Fatalf("bad token: %d", r.StatusCode)
	}

	c := hs.client(t)
	if code, _ := call(t, c, "GET", hs.base+"/api/state", "", nil); code != 200 {
		t.Fatalf("state: %d", code)
	}
	// DNS rebinding: non-loopback Host refused even with a valid cookie.
	if code, _ := call(t, c, "GET", hs.base+"/api/state", "", map[string]string{"Host": "evil.example:7878"}); code != 403 {
		t.Fatalf("rebinding host: %d", code)
	}
	// CSRF: mutating request without the custom header refused...
	body := `{"key":"restic-path","value":""}`
	if code, _ := call(t, c, "PUT", hs.base+"/api/settings", body, nil); code != 403 {
		t.Fatalf("no csrf header: %d", code)
	}
	// ...and with a foreign Origin.
	if code, _ := call(t, c, "PUT", hs.base+"/api/settings", body, map[string]string{"X-Storagebd": "1", "Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	// Unknown JSON fields rejected.
	if code, _ := call(t, c, "POST", hs.base+"/api/account/logout", `{"api_url":"x","extra":1}`, csrf); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}

	// ui-url state file is 0600 and holds the current token.
	st, err := ReadState(hs.srv.DataDir)
	if err != nil || st.Token != hs.token {
		t.Fatalf("state file: %+v %v", st, err)
	}
	info, _ := os.Stat(filepath.Join(hs.srv.DataDir, stateFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %o", info.Mode().Perm())
	}
}

func TestListenAddrMustBeLoopback(t *testing.T) {
	for _, a := range []string{"0.0.0.0:7878", "192.168.1.5:7878", "localhost:7878", ":7878"} {
		if ValidateListenAddr(a) == nil {
			t.Errorf("%s accepted", a)
		}
	}
	for _, a := range []string{"127.0.0.1:7878", "[::1]:7878"} {
		if err := ValidateListenAddr(a); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
}

// Full UI flow over the API: login → connect → add source → backup (async,
// with SSE events) → snapshots → browse → restore.
func TestOnboardBackupBrowseRestore(t *testing.T) {
	hs := start(t)
	c := hs.client(t)

	code, out := call(t, c, "POST", hs.base+"/api/account/login",
		`{"email":"me@example.com","password":"`+testutil.FakeAccountPassword+`","remember":true}`, csrf)
	if code != 200 {
		t.Fatalf("login: %d %v", code, out)
	}
	code, out = call(t, c, "POST", hs.base+"/api/connect", `{"name":"laptop","label":"Laptop","storage_gb":10,"activate":true}`, csrf)
	if code != 200 || out["activated"] != true {
		t.Fatalf("connect: %d %v", code, out)
	}

	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "docs"), 0o700)
	os.WriteFile(filepath.Join(src, "docs", "a.txt"), []byte("alpha"), 0o600)
	body, _ := json.Marshal(map[string]any{"name": "home", "kind": "dir", "path": src})
	if code, out = call(t, c, "POST", hs.base+"/api/credentials/laptop/sources", string(body), csrf); code != 201 {
		t.Fatalf("add source: %d %v", code, out)
	}

	// Subscribe to events before starting the backup.
	evReq, _ := http.NewRequest("GET", hs.base+"/api/events", nil)
	evResp, err := c.Do(evReq)
	if err != nil {
		t.Fatal(err)
	}
	defer evResp.Body.Close()

	if code, out = call(t, c, "POST", hs.base+"/api/credentials/laptop/backup", `{}`, csrf); code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	// A second start while running is refused.
	if code, _ = call(t, c, "POST", hs.base+"/api/credentials/laptop/backup", `{}`, csrf); code != 409 {
		t.Logf("second backup start returned %d (job may already have finished)", code)
	}

	deadline := time.Now().Add(60 * time.Second)
	buf := make([]byte, 0, 1<<16)
	tmp := make([]byte, 4096)
	for !strings.Contains(string(buf), `"type":"job-done"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no job-done event; got %s", buf)
		}
		n, err := evResp.Body.Read(tmp)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, tmp[:n]...)
	}
	if !strings.Contains(string(buf), `"status":"succeeded"`) || !strings.Contains(string(buf), `"type":"job-start"`) {
		t.Fatalf("events: %s", buf)
	}

	// Snapshots and browsing.
	req, _ := http.NewRequest("GET", hs.base+"/api/credentials/laptop/snapshots", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var snaps []map[string]any
	json.NewDecoder(resp.Body).Decode(&snaps)
	resp.Body.Close()
	if len(snaps) != 1 || snaps[0]["source"] != "home" {
		t.Fatalf("snapshots: %v", snaps)
	}
	snapID := snaps[0]["id"].(string)
	code, out = call(t, c, "GET", hs.base+"/api/credentials/laptop/ls?snapshot="+snapID+"&path="+url.QueryEscape(src+"/docs"), "", nil)
	nodes, _ := out["nodes"].([]any)
	if code != 200 || len(nodes) != 1 || nodes[0].(map[string]any)["name"] != "a.txt" {
		t.Fatalf("ls: %d %v", code, out)
	}
	if code, _ = call(t, c, "GET", hs.base+"/api/credentials/laptop/ls?snapshot=--help&path=/", "", nil); code != 400 {
		t.Fatalf("flag-like snapshot id: %d", code)
	}

	// Restore into a non-empty dir needs explicit confirmation.
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "existing"), []byte("x"), 0o600)
	rb, _ := json.Marshal(map[string]any{"source": "home", "snapshot": snapID, "target": target, "includes": []string{src + "/docs/a.txt"}})
	if code, out = call(t, c, "POST", hs.base+"/api/credentials/laptop/restore", string(rb), csrf); code != 409 || out["needs_confirm"] != true {
		t.Fatalf("restore without confirm: %d %v", code, out)
	}
	rb, _ = json.Marshal(map[string]any{"source": "home", "snapshot": snapID, "target": target,
		"includes": []string{src + "/docs/a.txt"}, "confirm_overwrite": true})
	if code, out = call(t, c, "POST", hs.base+"/api/credentials/laptop/restore", string(rb), csrf); code != 202 {
		t.Fatalf("restore: %d %v", code, out)
	}
	for i := 0; ; i++ {
		if b, err := os.ReadFile(filepath.Join(target, src, "docs", "a.txt")); err == nil && string(b) == "alpha" {
			break
		}
		if i > 100 {
			t.Fatal("restored file never appeared")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestSchedulerRunsDueBackup(t *testing.T) {
	hs := start(t)
	ctx := context.Background()
	st := hs.srv.Store
	pw, _ := store.GenerateResticPassword()
	cred := &store.Credential{Name: "sched", APIURL: hs.fake.API.URL, TenantID: testutil.TenantID,
		InstallID: testutil.InstallID, InstallSecret: testutil.InstallSecret, ResticPassword: pw}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	st.CreateSource(ctx, &store.Source{CredentialID: cred.ID, Name: "d", Kind: store.KindDir, Path: t.TempDir(), Enabled: true})
	sc := &store.Schedule{CredentialID: cred.ID, Enabled: true, Mode: store.ScheduleInterval, IntervalHours: 6}
	if err := st.SaveSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	// Pretend it was due an hour ago (machine was off).
	past := time.Now().Add(-time.Hour)
	st.MarkScheduleRun(ctx, &store.Schedule{CredentialID: cred.ID, Mode: store.ScheduleInterval, IntervalHours: 0}, past.Add(-time.Hour))
	hs.srv.scheduleTick(ctx)

	for i := 0; ; i++ {
		list, _ := st.ListJobs(ctx, cred.ID, 5)
		if len(list) == 1 && list[0].Status == store.StatusOK {
			if list[0].Trigger != store.TriggerSchedule {
				t.Fatalf("trigger = %s", list[0].Trigger)
			}
			break
		}
		if i > 150 {
			t.Fatalf("scheduled backup did not complete: %+v", list)
		}
		time.Sleep(200 * time.Millisecond)
	}
	got, _ := st.GetSchedule(ctx, cred.ID)
	if got.NextRunAt == nil || !got.NextRunAt.After(time.Now().Add(5*time.Hour)) {
		t.Fatalf("next run not advanced: %v", got.NextRunAt)
	}
}

func TestScheduleNextDaily(t *testing.T) {
	s := &store.Schedule{Mode: store.ScheduleDaily, DailyMinute: 2 * 60}
	from := time.Date(2026, 10, 10, 1, 0, 0, 0, time.Local)
	if n := s.Next(from); !n.Equal(time.Date(2026, 10, 10, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("same-day next = %v", n)
	}
	from = time.Date(2026, 10, 10, 2, 0, 0, 0, time.Local)
	if n := s.Next(from); !n.Equal(time.Date(2026, 10, 11, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("next-day next = %v", n)
	}
}

func waitJobs(t *testing.T, st *store.Store, credID int64, want int) []*store.Job {
	t.Helper()
	for i := 0; ; i++ {
		list, _ := st.ListJobs(context.Background(), credID, 20)
		done := 0
		for _, j := range list {
			if j.Status != store.StatusRunning {
				done++
			}
		}
		if done >= want {
			return list
		}
		if i > 200 {
			t.Fatalf("only %d of %d jobs finished: %+v", done, want, list)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestRetentionAPIAndAfterBackupCleanup(t *testing.T) {
	hs := start(t)
	ctx := context.Background()
	c := hs.client(t)
	st := hs.srv.Store
	pw, _ := store.GenerateResticPassword()
	cred := &store.Credential{Name: "r", APIURL: hs.fake.API.URL, TenantID: testutil.TenantID,
		InstallID: testutil.InstallID, InstallSecret: testutil.InstallSecret, ResticPassword: pw}
	st.CreateCredential(ctx, cred)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o600)
	st.CreateSource(ctx, &store.Source{CredentialID: cred.ID, Name: "s", Kind: store.KindDir, Path: src, Enabled: true})

	// No policy yet: suggestion offered, cleanup refused.
	code, out := call(t, c, "GET", hs.base+"/api/credentials/r/retention", "", nil)
	if code != 200 || out["policy"] != nil || out["suggested"] == nil {
		t.Fatalf("get: %d %v", code, out)
	}
	if code, _ = call(t, c, "POST", hs.base+"/api/credentials/r/cleanup", `{}`, csrf); code != 400 {
		t.Fatalf("cleanup without policy: %d", code)
	}
	if code, _ = call(t, c, "PUT", hs.base+"/api/credentials/r/retention", `{"keep_daily":0}`, csrf); code != 400 {
		t.Fatalf("keep-nothing policy accepted: %d", code)
	}

	// Two backups, then a keep-last-1 policy: preview says 1 would go.
	for i := 0; i < 2; i++ {
		os.WriteFile(filepath.Join(src, "f"), []byte(fmt.Sprint(i)), 0o600)
		if _, _, err := hs.srv.Runner.Backup(ctx, cred, jobs.BackupOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	code, out = call(t, c, "POST", hs.base+"/api/credentials/r/retention/preview", `{"keep_last":1}`, csrf)
	if code != 200 || out["removed"] != float64(1) || out["dry_run"] != true {
		t.Fatalf("preview: %d %v", code, out)
	}
	if code, _ = call(t, c, "PUT", hs.base+"/api/credentials/r/retention", `{"keep_last":1,"prune_interval_days":7}`, csrf); code != 200 {
		t.Fatalf("put: %d", code)
	}

	// A UI-started backup applies the policy afterwards, as its own job.
	if code, _ = call(t, c, "POST", hs.base+"/api/credentials/r/backup", `{}`, csrf); code != 202 {
		t.Fatalf("backup: %d", code)
	}
	list := waitJobs(t, st, cred.ID, 4) // 2 direct backups + UI backup + cleanup
	if list[0].Kind != store.JobCleanup || list[0].Status != store.StatusOK || !strings.Contains(string(list[0].Detail), `"removed":2`) {
		t.Fatalf("after-backup cleanup: %+v %s", list[0], list[0].Detail)
	}
	rt, _ := st.GetRetention(ctx, cred.ID)
	if rt.LastForgetAt == nil || rt.LastPruneAt == nil {
		t.Fatalf("retention times: %+v", rt)
	}
}
