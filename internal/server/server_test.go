package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	"storage-bd-client/internal/uiauth"
)

type harness struct {
	base string // http://127.0.0.1:port
	fake *testutil.FakeStorage
	srv  *Server
}

const (
	testUser = "admin"
	testPass = "correct horse battery"
)

func start(t *testing.T, opts ...func(*Server)) *harness {
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
	hash, err := uiauth.HashPassword(testPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUIUser(context.Background(), testUser, hash); err != nil {
		t.Fatal(err)
	}
	repos := &repo.Manager{Store: st, DataDir: dir}
	s := &Server{
		Store: st, Accounts: &account.Manager{Store: st}, Repos: repos,
		Runner:  &jobs.Runner{Store: st, Repos: repos, Tools: dbdump.Tools{}, Host: "ui-test"},
		DataDir: dir, DefaultAPIURL: fake.API.URL, Version: "test",
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	for _, o := range opts {
		o(s)
	}
	go func() { done <- s.Run(ctx, "127.0.0.1:0", func(rd Ready) { ready <- rd.Also0() }) }()
	var page string
	select {
	case page = <-ready:
	case err := <-done:
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		<-done
	})
	u, _ := url.Parse(page)
	return &harness{base: u.Scheme + "://" + u.Host, fake: fake, srv: s}
}

// client returns an HTTP client signed in as testUser.
func (hs *harness) client(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 60 * time.Second, Transport: insecureTLS}
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, testUser, testPass)
	if code, out := call(t, c, "POST", hs.base+"/api/ui/login", body, csrf); code != 200 {
		t.Fatalf("ui login: %d %v", code, out)
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

	// No session: API refused with the code the page looks for.
	if code, out := call(t, anon, "GET", hs.base+"/api/state", "", nil); code != 401 || out["code"] != signinRequired {
		t.Fatalf("anon state: %d %v", code, out)
	}
	if code, out := call(t, anon, "GET", hs.base+"/api/ui/status", "", nil); code != 200 || out["signed_in"] != false || out["setup_required"] != false {
		t.Fatalf("anon status: %d %v", code, out)
	}
	resp, _ := anon.Get(hs.base + "/")
	resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP = %q", csp)
	}

	// Wrong password, unknown user: same answer.
	for _, b := range []string{`{"username":"admin","password":"wrong-password-1"}`, `{"username":"nobody","password":"wrong-password-1"}`} {
		if code, out := call(t, anon, "POST", hs.base+"/api/ui/login", b, csrf); code != 401 || out["error"] != "wrong username or password" {
			t.Fatalf("bad login %s: %d %v", b, code, out)
		}
	}
	// Login itself needs the CSRF header (no login CSRF).
	if code, _ := call(t, anon, "POST", hs.base+"/api/ui/login", `{"username":"admin","password":"x"}`, nil); code != 403 {
		t.Fatalf("login without csrf header: %d", code)
	}

	c := hs.client(t)
	if code, out := call(t, c, "GET", hs.base+"/api/state", "", nil); code != 200 || out["ui_user"] != testUser {
		t.Fatalf("state: %d %v", code, out)
	}
	// The cookie is HttpOnly + SameSite=Strict and isn't stored in the clear.
	u, _ := url.Parse(hs.base)
	ck := c.Jar.Cookies(u)
	if len(ck) != 1 {
		t.Fatalf("cookies: %v", ck)
	}
	if _, err := hs.srv.Store.GetUISession(context.Background(), ck[0].Value); err == nil {
		t.Fatal("session stored under the raw cookie value")
	}
	if _, err := hs.srv.Store.GetUISession(context.Background(), hashToken(ck[0].Value)); err != nil {
		t.Fatalf("session not stored by hash: %v", err)
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

	// Sign out ends the session server-side.
	if code, _ := call(t, c, "POST", hs.base+"/api/ui/logout", `{}`, csrf); code != 200 {
		t.Fatalf("logout: %d", code)
	}
	if _, err := hs.srv.Store.GetUISession(context.Background(), hashToken(ck[0].Value)); err == nil {
		t.Fatal("session survived logout")
	}
	replay := &http.Client{}
	req, _ := http.NewRequest("GET", hs.base+"/api/state", nil)
	req.AddCookie(ck[0])
	if r, _ := replay.Do(req); r.StatusCode != 401 {
		t.Fatalf("old cookie after logout: %d", r.StatusCode)
	}

	// ui-url state file is 0600 and holds the page address.
	st, err := ReadState(hs.srv.DataDir)
	if err != nil || st.URL != hs.base+"/" {
		t.Fatalf("state file: %+v %v", st, err)
	}
	info, _ := os.Stat(filepath.Join(hs.srv.DataDir, stateFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %o", info.Mode().Perm())
	}
}

func TestSignInThrottleAndPasswordChange(t *testing.T) {
	hs := start(t)
	anon := &http.Client{}
	bad := `{"username":"admin","password":"wrong-password-1"}`
	for i := 0; i < failPerUser; i++ {
		call(t, anon, "POST", hs.base+"/api/ui/login", bad, csrf)
	}
	// Locked now, even with the right password.
	good := fmt.Sprintf(`{"username":%q,"password":%q}`, testUser, testPass)
	if code, _ := call(t, anon, "POST", hs.base+"/api/ui/login", good, csrf); code != 429 {
		t.Fatalf("after %d failures: %d", failPerUser, code)
	}
	hs.srv.fails.reset("u:" + testUser + "@127.0.0.1")
	hs.srv.fails.reset("a:127.0.0.1")

	a, b := hs.client(t), hs.client(t)
	newPass := "an even longer passphrase"
	if code, out := call(t, a, "POST", hs.base+"/api/ui/password", `{"current_password":"nope","new_password":"`+newPass+`"}`, csrf); code != 400 {
		t.Fatalf("wrong current password: %d %v", code, out)
	}
	if code, out := call(t, a, "POST", hs.base+"/api/ui/password", `{"current_password":"`+testPass+`","new_password":"short"}`, csrf); code != 400 {
		t.Fatalf("short password: %d %v", code, out)
	}
	if code, out := call(t, a, "POST", hs.base+"/api/ui/password", `{"current_password":"`+testPass+`","new_password":"`+newPass+`"}`, csrf); code != 200 {
		t.Fatalf("change password: %d %v", code, out)
	}
	// The browser that changed it stays signed in; the other one is out.
	if code, _ := call(t, a, "GET", hs.base+"/api/state", "", nil); code != 200 {
		t.Fatalf("changer signed out: %d", code)
	}
	if code, _ := call(t, b, "GET", hs.base+"/api/state", "", nil); code != 401 {
		t.Fatalf("other browser still signed in: %d", code)
	}
	if code, _ := call(t, anon, "POST", hs.base+"/api/ui/login", fmt.Sprintf(`{"username":"admin","password":%q}`, newPass), csrf); code != 200 {
		t.Fatalf("login with new password: %d", code)
	}
}

func TestSetupRequiredWithoutUsers(t *testing.T) {
	hs := start(t)
	if err := hs.srv.Store.DeleteUIUser(context.Background(), testUser); err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, &http.Client{}, "GET", hs.base+"/api/ui/status", "", nil); code != 200 || out["setup_required"] != true {
		t.Fatalf("status: %d %v", code, out)
	}
}

func TestListenAddr(t *testing.T) {
	for _, a := range []string{"0.0.0.0:7878", "192.168.1.5:7878", ":7878"} {
		if ValidateListenAddr(a, false) == nil {
			t.Errorf("%s accepted without HTTPS", a)
		}
		if err := ValidateListenAddr(a, true); err != nil {
			t.Errorf("%s with HTTPS: %v", a, err)
		}
	}
	if ValidateListenAddr("localhost:7878", true) == nil {
		t.Error("host name accepted")
	}
	for _, a := range []string{"127.0.0.1:7878", "[::1]:7878"} {
		if err := ValidateListenAddr(a, false); err != nil {
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

// A space created in the storage.bd dashboard is added without signing in,
// and a wrong secret is refused before anything is saved.
func TestAddDashboardCredential(t *testing.T) {
	hs := start(t)
	c := hs.client(t)
	add := func(secret, pw string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"name": "dash", "tenant_id": testutil.TenantID,
			"install_id": testutil.InstallID, "install_secret": secret, "restic_password": pw})
		return call(t, c, "POST", hs.base+"/api/credentials", string(b), csrf)
	}

	if code, out := add(strings.Repeat("cd", 32), ""); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "rejected") {
		t.Fatalf("wrong secret: %d %v", code, out)
	}
	if code, out := add(testutil.InstallSecret, "short"); code != 400 {
		t.Fatalf("short password: %d %v", code, out)
	}
	if creds, _ := hs.srv.Store.ListCredentials(context.Background()); len(creds) != 0 {
		t.Fatalf("saved despite errors: %d", len(creds))
	}

	if code, out := add(" "+testutil.InstallSecret+"\n", ""); code != 201 || out["name"] != "dash" {
		t.Fatalf("add: %d %v", code, out)
	}
	cred, err := hs.srv.Store.GetCredentialByName(context.Background(), "dash")
	if err != nil {
		t.Fatal(err)
	}
	if !cred.IsDefault || cred.InstallSecret != testutil.InstallSecret || len(cred.ResticPassword) < 16 {
		t.Fatalf("stored credential: default=%v secret ok=%v pw len=%d", cred.IsDefault, cred.InstallSecret == testutil.InstallSecret, len(cred.ResticPassword))
	}
	if code, _ := add(testutil.InstallSecret, ""); code != 400 {
		t.Fatalf("duplicate accepted: %d", code)
	}
}
