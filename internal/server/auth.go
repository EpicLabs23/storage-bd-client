package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"storage-bd-client/internal/store"
	"storage-bd-client/internal/uiauth"
)

// Web UI sign-in: users created with `storagebd user add`, an argon2id
// password check, and a server-side session behind an HttpOnly,
// SameSite=Strict cookie.
const (
	sessionIdle = 12 * time.Hour     // signed out after this long without a request
	sessionMax  = 7 * 24 * time.Hour // and after this long regardless

	// Failed sign-ins. Per username+address, so a remote guesser can't
	// lock the real admin out by naming them; plus a looser per-address
	// cap against trying many usernames.
	failWindow  = 15 * time.Minute
	failPerUser = 5
	failPerAddr = 20
)

type ctxKey int

const userKey ctxKey = 0

// signinRequired is the `code` the page looks for to show the sign-in form.
const signinRequired = "signin_required"

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// currentUser resolves the session cookie, enforcing both expiries.
func (s *Server) currentUser(r *http.Request) (*store.UIUser, *store.UISession) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, nil
	}
	ctx := r.Context()
	sess, err := s.Store.GetUISession(ctx, hashToken(c.Value))
	if err != nil {
		return nil, nil
	}
	now := time.Now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > sessionIdle {
		s.Store.DeleteUISession(ctx, sess.TokenHash)
		return nil, nil
	}
	u, err := s.Store.GetUIUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, nil
	}
	// last_seen_at has 1 s resolution; skip the write on rapid requests.
	if now.Sub(sess.LastSeenAt) > time.Minute {
		s.Store.TouchUISession(ctx, sess.TokenHash)
	}
	return u, sess
}

func userFrom(ctx context.Context) *store.UIUser {
	u, _ := ctx.Value(userKey).(*store.UIUser)
	return u
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

// ---- throttle ----

type failures struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func newFailures() *failures { return &failures{m: map[string][]time.Time{}} }

func (f *failures) recent(key string, now time.Time) []time.Time {
	ts := f.m[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > failWindow {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(f.m, key)
	} else {
		f.m[key] = ts
	}
	return ts
}

// blocked reports how long until the next attempt is allowed (0 = now).
func (f *failures) blocked(userKey, addrKey string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	var wait time.Duration
	for _, k := range []struct {
		key string
		max int
	}{{userKey, failPerUser}, {addrKey, failPerAddr}} {
		if ts := f.recent(k.key, now); len(ts) >= k.max {
			if d := failWindow - now.Sub(ts[len(ts)-k.max]); d > wait {
				wait = d
			}
		}
	}
	return wait
}

func (f *failures) add(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		f.m[k] = append(f.recent(k, now), now)
	}
}

func (f *failures) reset(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
}

// ---- handlers ----

func (s *Server) uiRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ui/status", s.uiStatus)
	mux.HandleFunc("POST /api/ui/login", s.uiLogin)
	mux.HandleFunc("POST /api/ui/logout", s.uiLogout)
	mux.HandleFunc("POST /api/ui/password", s.uiPassword)
	return mux
}

func (s *Server) uiStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUIUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{"setup_required": n == 0, "signed_in": false}
	if u, _ := s.currentUser(r); u != nil {
		out["signed_in"] = true
		out["username"] = u.Username
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) uiLogin(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	b.Username = strings.ToLower(strings.TrimSpace(b.Username))
	addr := remoteHost(r)
	uKey, aKey := "u:"+b.Username+"@"+addr, "a:"+addr
	if wait := s.fails.blocked(uKey, aKey); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "too many failed sign-ins; try again in "+wait.Round(time.Minute).String())
		return
	}

	u, err := s.Store.GetUIUser(r.Context(), b.Username)
	hash := ""
	if err == nil {
		hash = u.PasswordHash
	} else if !errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !uiauth.VerifyPassword(b.Password, hash) || u == nil {
		s.fails.add(uKey, aKey)
		log.Printf("ui: failed sign-in for %q from %s", b.Username, addr)
		writeErr(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	s.fails.reset(uKey)

	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	value := base64.RawURLEncoding.EncodeToString(tok)
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	if err := s.Store.CreateUISession(r.Context(), &store.UISession{
		TokenHash: hashToken(value), UserID: u.ID, ExpiresAt: time.Now().Add(sessionMax),
		RemoteAddr: addr, UserAgent: ua,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setSessionCookie(w, r, value, int(sessionMax.Seconds()))
	log.Printf("ui: %s signed in from %s", u.Username, addr)
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
}

func (s *Server) uiLogout(w http.ResponseWriter, r *http.Request) {
	if _, sess := s.currentUser(r); sess != nil {
		s.Store.DeleteUISession(r.Context(), sess.TokenHash)
	}
	s.setSessionCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) uiPassword(w http.ResponseWriter, r *http.Request) {
	u, sess := s.currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "please sign in", "code": signinRequired})
		return
	}
	var b struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	addr := remoteHost(r)
	uKey, aKey := "u:"+u.Username+"@"+addr, "a:"+addr
	if wait := s.fails.blocked(uKey, aKey); wait > 0 {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts; try again in "+wait.Round(time.Minute).String())
		return
	}
	if !uiauth.VerifyPassword(b.Current, u.PasswordHash) {
		s.fails.add(uKey, aKey)
		writeErr(w, http.StatusBadRequest, "current password is wrong")
		return
	}
	if err := uiauth.ValidatePassword(b.New); err != nil {
		fail(w, err)
		return
	}
	hash, err := uiauth.HashPassword(b.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.SetUIUserPassword(r.Context(), u.ID, hash, sess.TokenHash); err != nil {
		fail(w, err)
		return
	}
	log.Printf("ui: %s changed their password from %s", u.Username, addr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sweepSessions drops expired sessions now and then hourly.
func (s *Server) sweepSessions(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.Store.DeleteStaleUISessions(ctx, sessionIdle)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
