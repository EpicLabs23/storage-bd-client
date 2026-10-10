package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"storage-bd-client/internal/store"
)

// fakeAPI mimics the storage-bd-api endpoints the client uses, including
// single-use refresh tokens.
type fakeAPI struct {
	mu        sync.Mutex
	refresh   map[string]bool // live refresh tokens
	n         int
	refreshes int
	balanceOK bool
	tenants   []map[string]any
}

func jwtWithExp(exp time.Time) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return h + "." + p + ".sig"
}

func (f *fakeAPI) issue(w http.ResponseWriter, ttl time.Duration) {
	f.n++
	rt := fmt.Sprintf("rt-%d", f.n)
	f.refresh[rt] = true
	http.SetCookie(w, &http.Cookie{Name: "account_refresh_token", Value: rt, MaxAge: 86400, HttpOnly: true})
	json.NewEncoder(w).Encode(map[string]string{"access_token": jwtWithExp(time.Now().Add(ttl))})
}

func (f *fakeAPI) handler(accessTTL time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/account-login", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		if b["password"] != "right" {
			w.WriteHeader(http.StatusFailedDependency)
			w.Write([]byte(`{"message":"Invalid Credentials"}`))
			return
		}
		f.issue(w, accessTTL)
	})
	mux.HandleFunc("POST /auth/account-refresh", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ck, err := r.Cookie("account_refresh_token")
		if err != nil || !f.refresh[ck.Value] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		delete(f.refresh, ck.Value) // single use
		f.refreshes++
		f.issue(w, accessTTL)
	})
	mux.HandleFunc("POST /d2c/tenants", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		t := map[string]any{"tenant_id": "3f1c2b6e-8a4d-4c1e-9f0a-1b2c3d4e5f60", "install_id": "9a2e4c6b-1d3f-4a5b-8c7d-9e0f1a2b3c4d",
			"local_label": b["local_label"], "storage_gb": b["storage_gb"], "state": "suspended"}
		f.tenants = append(f.tenants, t)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"tenant": t, "install_id": t["install_id"],
			"install_secret": strings.Repeat("ab", 32)})
	})
	mux.HandleFunc("POST /d2c/tenants/{id}/activate", func(w http.ResponseWriter, r *http.Request) {
		if !f.balanceOK {
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte(`{"message":"Account balance (0) does not cover this plan's monthly cost (50)"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"tenant_id": r.PathValue("id"), "state": "active"})
	})
	mux.HandleFunc("GET /d2c/tenants", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.tenants)
	})
	mux.HandleFunc("GET /d2c/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"balance":"125.5"}`))
	})
	mux.HandleFunc("GET /public/pricing", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"price_per_gb":"5","currency":"BDT"}`))
	})
	mux.HandleFunc("POST /auth/account-logout", func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("account_refresh_token"); err == nil {
			f.mu.Lock()
			delete(f.refresh, ck.Value)
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusCreated)
	})
	return mux
}

func setup(t *testing.T, accessTTL time.Duration) (*Manager, *fakeAPI, string) {
	t.Helper()
	f := &fakeAPI{refresh: map[string]bool{}}
	srv := httptest.NewServer(f.handler(accessTTL))
	t.Cleanup(srv.Close)
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Manager{Store: s}, f, srv.URL
}

func TestLoginBadPassword(t *testing.T) {
	m, _, url := setup(t, 15*time.Minute)
	err := m.Login(context.Background(), url, "a@b.c", "wrong", true)
	if err == nil || err.Error() != "invalid email or password" {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessTokenCachedThenRotated(t *testing.T) {
	ctx := context.Background()

	// Long-lived access token: no refresh needed.
	m, f, url := setup(t, 15*time.Minute)
	if err := m.Login(ctx, url, " A@B.c ", "right", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := m.AccessToken(ctx, url); err != nil {
			t.Fatal(err)
		}
	}
	if f.refreshes != 0 {
		t.Fatalf("refreshed %d times with a fresh access token", f.refreshes)
	}
	sess, _ := m.Store.GetSession(ctx, url)
	if sess.Email != "a@b.c" {
		t.Fatalf("email not normalized: %q", sess.Email)
	}

	// Access token inside the expiry margin: every call must refresh and
	// persist the rotated refresh token, or the next call would 401.
	m2, f2, url2 := setup(t, 30*time.Second)
	if err := m2.Login(ctx, url2, "a@b.c", "right", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := m2.AccessToken(ctx, url2); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if f2.refreshes != 3 {
		t.Fatalf("refreshes = %d, want 3", f2.refreshes)
	}
}

func TestConcurrentAccessTokenNeverReusesRefreshToken(t *testing.T) {
	ctx := context.Background()
	m, _, url := setup(t, 30*time.Second)
	if err := m.Login(ctx, url, "a@b.c", "right", true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.AccessToken(ctx, url)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent refresh failed: %v", err)
		}
	}
}

func TestExpiredSessionIsDropped(t *testing.T) {
	ctx := context.Background()
	m, f, url := setup(t, 30*time.Second)
	m.Login(ctx, url, "a@b.c", "right", true)
	f.mu.Lock()
	f.refresh = map[string]bool{} // server-side revocation
	f.mu.Unlock()
	if _, err := m.AccessToken(ctx, url); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
	if _, err := m.Store.GetSession(ctx, url); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dead session kept: %v", err)
	}
}

func TestConnectInsufficientBalanceStillSaves(t *testing.T) {
	ctx := context.Background()
	m, _, url := setup(t, 15*time.Minute)
	m.Login(ctx, url, "a@b.c", "right", true)

	res, err := m.Connect(ctx, ConnectParams{APIURL: url, Name: "web1", Label: "web1", StorageGB: 10, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Activated || res.ActivationError == nil || !strings.Contains(res.ActivationError.Error(), "balance too low") {
		t.Fatalf("result = %+v", res)
	}
	c, err := m.Store.GetCredentialByName(ctx, "web1")
	if err != nil || c.AccountEmail != "a@b.c" || !c.IsDefault || len(c.ResticPassword) < 40 {
		t.Fatalf("stored credential = %+v, %v", c, err)
	}
}

func TestConnectActivates(t *testing.T) {
	ctx := context.Background()
	m, f, url := setup(t, 15*time.Minute)
	f.balanceOK = true
	m.Login(ctx, url, "a@b.c", "right", true)
	res, err := m.Connect(ctx, ConnectParams{APIURL: url, Name: "web1", Label: "web1", StorageGB: 10, Activate: true})
	if err != nil || !res.Activated {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestConnectRejectsExistingNameBeforeCreatingRemotely(t *testing.T) {
	ctx := context.Background()
	m, f, url := setup(t, 15*time.Minute)
	m.Login(ctx, url, "a@b.c", "right", true)
	m.Store.CreateCredential(ctx, &store.Credential{Name: "web1", APIURL: url,
		TenantID: "7d9e0f1a-2b3c-4d5e-8f6a-7b8c9d0e1f2a", InstallID: "9a2e4c6b-1d3f-4a5b-8c7d-9e0f1a2b3c4d",
		InstallSecret: strings.Repeat("cd", 32), ResticPassword: "aaaaaaaaaaaaaaaaaaaa"})
	if _, err := m.Connect(ctx, ConnectParams{APIURL: url, Name: "web1", Label: "x", StorageGB: 1}); err == nil {
		t.Fatal("expected name collision error")
	}
	if len(f.tenants) != 0 {
		t.Fatal("tenant was created remotely despite local name collision")
	}
}

func TestConnectOrphanCarriesSecret(t *testing.T) {
	ctx := context.Background()
	m, _, url := setup(t, 15*time.Minute)
	m.Login(ctx, url, "a@b.c", "right", true)
	// Same tenant already stored under another name: the local save
	// fails after the remote create, which must surface the secret.
	m.Store.CreateCredential(ctx, &store.Credential{Name: "old", APIURL: url,
		TenantID: "3f1c2b6e-8a4d-4c1e-9f0a-1b2c3d4e5f60", InstallID: "9a2e4c6b-1d3f-4a5b-8c7d-9e0f1a2b3c4d",
		InstallSecret: strings.Repeat("cd", 32), ResticPassword: "aaaaaaaaaaaaaaaaaaaa"})
	_, err := m.Connect(ctx, ConnectParams{APIURL: url, Name: "new", Label: "x", StorageGB: 1})
	var orphan *OrphanError
	if !errors.As(err, &orphan) || orphan.InstallSecret != strings.Repeat("ab", 32) {
		t.Fatalf("err = %v", err)
	}
}

func TestNotLoggedIn(t *testing.T) {
	m, _, url := setup(t, 15*time.Minute)
	if _, err := m.AccessToken(context.Background(), url); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v", err)
	}
}

func TestOverviewAndLogout(t *testing.T) {
	ctx := context.Background()
	m, f, url := setup(t, 15*time.Minute)
	m.Login(ctx, url, "a@b.c", "right", true)
	o, err := m.Overview(ctx, url)
	if err != nil || o.Balance != "125.5" || o.Currency != "BDT" {
		t.Fatalf("overview = %+v, %v", o, err)
	}
	if err := m.Logout(ctx, url); err != nil {
		t.Fatal(err)
	}
	if len(f.refresh) != 0 {
		t.Fatal("refresh token not revoked server-side")
	}
	if _, err := m.Store.GetSession(ctx, url); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session not removed locally")
	}
}
