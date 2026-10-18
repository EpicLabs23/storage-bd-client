package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"storage-bd-client/internal/store"
	"storage-bd-client/internal/uiauth"
)

// Web UI sign-in: users created with `storagebd user add`, an argon2id
// password check, an optional TOTP second step (required when the page is
// exposed, see expose.go), and a server-side session behind an HttpOnly,
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

// Error `code`s the page acts on.
const (
	signinRequired = "signin_required" // show the sign-in form
	totpRequired   = "totp_required"   // password was right; ask for the 2FA code
)

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
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
	// Open to the network: a session from before 2FA was set up (or one
	// signed in while the page was loopback-only) isn't enough.
	if s.exposed && !sess.MFA {
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
		HttpOnly: true, Secure: s.isHTTPS(r), SameSite: http.SameSiteStrictMode,
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
	mux.HandleFunc("POST /api/ui/2fa/setup", s.ui2FASetup)
	mux.HandleFunc("POST /api/ui/2fa/confirm", s.ui2FAConfirm)
	mux.HandleFunc("POST /api/ui/2fa/disable", s.ui2FADisable)
	return mux
}

func (s *Server) uiStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUIUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{"setup_required": n == 0, "signed_in": false, "exposed": s.exposed}
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
		Code     string `json:"code"` // 2FA code or backup code, second step
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	b.Username = strings.ToLower(strings.TrimSpace(b.Username))
	addr := s.clientAddr(r)
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

	// Second step. Only reached with the right password, so it doesn't
	// reveal to a guesser whether a user has 2FA.
	mfa := false
	if u.TOTPEnabled {
		if strings.TrimSpace(b.Code) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "enter the code from your authenticator app", "code": totpRequired})
			return
		}
		ok, usedBackup, err := s.checkSecondFactor(r.Context(), u.ID, b.Code)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			s.fails.add(uKey, aKey)
			log.Printf("ui: wrong 2FA code for %q from %s", u.Username, addr)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong or already used code", "code": totpRequired})
			return
		}
		if usedBackup {
			log.Printf("ui: %s used a backup code", u.Username)
		}
		mfa = true
	} else if s.exposed {
		writeErr(w, http.StatusForbidden, "this page is open to the network, so two-step sign-in is required. "+
			"Turn it on for "+u.Username+" on the server: sudo storagebd user 2fa enable "+u.Username)
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
		RemoteAddr: addr, UserAgent: ua, MFA: mfa,
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
	u, sess := s.requireUser(w, r)
	if u == nil {
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
	if !s.recheckPassword(w, r, u, b.Current) {
		return
	}
	addr := s.clientAddr(r)
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

// requireUser is currentUser for the /api/ui/ routes that need a session.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (*store.UIUser, *store.UISession) {
	u, sess := s.currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "please sign in", "code": signinRequired})
	}
	return u, sess
}

// recheckPassword asks for the password again before a sensitive change,
// under the same rate limit as sign-in. It writes the error response.
func (s *Server) recheckPassword(w http.ResponseWriter, r *http.Request, u *store.UIUser, password string) bool {
	addr := s.clientAddr(r)
	uKey, aKey := "u:"+u.Username+"@"+addr, "a:"+addr
	if wait := s.fails.blocked(uKey, aKey); wait > 0 {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts; try again in "+wait.Round(time.Minute).String())
		return false
	}
	if !uiauth.VerifyPassword(password, u.PasswordHash) {
		s.fails.add(uKey, aKey)
		writeErr(w, http.StatusBadRequest, "current password is wrong")
		return false
	}
	return true
}

// checkSecondFactor accepts a current TOTP code (each time step once) or
// an unused backup code (consumed).
func (s *Server) checkSecondFactor(ctx context.Context, userID int64, code string) (ok, usedBackup bool, err error) {
	t, err := s.Store.GetUITOTP(ctx, userID)
	if err != nil || !t.Enabled {
		return false, false, err
	}
	if uiauth.LooksLikeTOTP(code) {
		step, match := uiauth.CheckTOTP(t.Secret, code, time.Now(), t.LastStep)
		if !match {
			return false, false, nil
		}
		ok, err := s.Store.UseUITOTPStep(ctx, userID, step)
		return ok, false, err
	}
	i := uiauth.MatchBackupCode(code, t.BackupCodes)
	if i < 0 {
		return false, false, nil
	}
	ok, err = s.Store.UseUIBackupCode(ctx, userID, t.BackupCodes, i)
	return ok, true, err
}

// ---- 2FA setup from the page ----

// ui2FASetup starts enrollment: a new secret (not active until confirmed)
// with its QR code. Needs the password again.
func (s *Server) ui2FASetup(w http.ResponseWriter, r *http.Request) {
	u, _ := s.requireUser(w, r)
	if u == nil {
		return
	}
	var b struct {
		Password string `json:"password"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if !s.recheckPassword(w, r, u, b.Password) {
		return
	}
	secret, err := uiauth.NewTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.StartUITOTP(r.Context(), u.ID, secret); err != nil {
		fail(w, err)
		return
	}
	uri := uiauth.TOTPURI(totpAccount(u.Username), secret)
	svg, err := uiauth.QRSVG(uri)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret, "uri": uri,
		"qr": "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)),
	})
}

// totpAccount is the label in the authenticator app: user@host, so
// several servers are told apart.
func totpAccount(username string) string {
	host, _ := os.Hostname()
	if host == "" {
		return username
	}
	return username + "@" + host
}

func (s *Server) ui2FAConfirm(w http.ResponseWriter, r *http.Request) {
	u, sess := s.requireUser(w, r)
	if u == nil {
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	t, err := s.Store.GetUITOTP(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	if t.Enabled || t.Secret == "" {
		writeErr(w, http.StatusBadRequest, "start the setup again")
		return
	}
	step, ok := uiauth.CheckTOTP(t.Secret, b.Code, time.Now(), 0)
	if !ok {
		writeErr(w, http.StatusBadRequest, "that code doesn't match; check the phone's clock and try the next code")
		return
	}
	codes, hashes, err := uiauth.NewBackupCodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.EnableUITOTP(r.Context(), u.ID, t.Secret, step, hashes, sess.TokenHash); err != nil {
		fail(w, err)
		return
	}
	log.Printf("ui: %s turned on two-step sign-in from %s", u.Username, s.clientAddr(r))
	writeJSON(w, http.StatusOK, map[string]any{"backup_codes": codes})
}

func (s *Server) ui2FADisable(w http.ResponseWriter, r *http.Request) {
	u, _ := s.requireUser(w, r)
	if u == nil {
		return
	}
	if s.exposed {
		writeErr(w, http.StatusForbidden, "two-step sign-in is required while this page is open to the network; "+
			"turn it off on the server instead: sudo storagebd user 2fa disable "+u.Username)
		return
	}
	var b struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if !s.recheckPassword(w, r, u, b.Password) {
		return
	}
	ok, _, err := s.checkSecondFactor(r.Context(), u.ID, b.Code)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		addr := s.clientAddr(r)
		s.fails.add("u:"+u.Username+"@"+addr, "a:"+addr)
		writeErr(w, http.StatusBadRequest, "wrong or already used code")
		return
	}
	if err := s.Store.DisableUITOTP(r.Context(), u.ID); err != nil {
		fail(w, err)
		return
	}
	log.Printf("ui: %s turned off two-step sign-in from %s", u.Username, s.clientAddr(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
