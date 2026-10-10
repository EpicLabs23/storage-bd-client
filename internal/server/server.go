// Package server is `storagebd serve`: the local web UI, its JSON API,
// and the backup scheduler.
//
// Security model. The UI can restore files to any path and read/write
// this machine's credentials, and storagebd often runs as root, so it's
// guarded against every local and browser-borne caller, not just the
// network:
//   - Listens on loopback only. Remote use is via an SSH tunnel.
//   - A random token, regenerated on every start, is required. It's
//     printed as a login URL and stored in <data>/serve.json (0600) for
//     `storagebd ui-url`. Visiting the URL swaps it for an HttpOnly,
//     SameSite=Strict cookie.
//   - Host must be a loopback name (DNS-rebinding defense). Mutating
//     requests need a custom header and a matching Origin (CSRF defense).
//   - Strict CSP and frame-ancestors 'none' (no inline script, no
//     clickjacking).
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"storage-bd-client/internal/account"
	"storage-bd-client/internal/jobs"
	"storage-bd-client/internal/repo"
	"storage-bd-client/internal/store"
)

//go:embed web
var webFS embed.FS

const (
	cookieName  = "storagebd_session"
	csrfHeader  = "X-Storagebd"
	stateFile   = "serve.json"
	DefaultAddr = "127.0.0.1:7878"
)

type Server struct {
	Store         *store.Store
	Accounts      *account.Manager
	Repos         *repo.Manager
	Runner        *jobs.Runner
	DataDir       string
	DefaultAPIURL string
	Version       string

	token string
	hub   *hub
	jobs  *jobManager
}

// ServeState is what `storagebd ui-url` reads.
type ServeState struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// ValidateListenAddr accepts only loopback IP addresses.
func ValidateListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing to listen on %q: the UI is loopback-only (use an SSH tunnel for remote access: ssh -L 7878:127.0.0.1:7878 host)", addr)
	}
	return nil
}

// Run serves until ctx is cancelled. onReady receives the login URL.
func (s *Server) Run(ctx context.Context, addr string, onReady func(loginURL string)) error {
	if err := ValidateListenAddr(addr); err != nil {
		return err
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	s.token = hex.EncodeToString(tok)
	s.hub = newHub()
	s.jobs = newJobManager(s)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	loginURL := fmt.Sprintf("http://%s/auth?t=%s", ln.Addr().String(), s.token)
	if err := s.writeState(ServeState{URL: loginURL, Token: s.token, PID: os.Getpid()}); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(filepath.Join(s.DataDir, stateFile))

	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		s.jobs.cancelAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	go s.runScheduler(ctx)

	if onReady != nil {
		onReady(loginURL)
	}
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		s.jobs.wait()
		return nil
	}
	return err
}

func (s *Server) writeState(st ServeState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	p := filepath.Join(s.DataDir, stateFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadState is used by `storagebd ui-url`.
func ReadState(dataDir string) (*ServeState, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, stateFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("storagebd serve is not running (start it with: storagebd serve)")
		}
		return nil, err
	}
	var st ServeState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func (s *Server) routes() http.Handler {
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	api := s.apiRoutes()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}

		switch {
		case r.URL.Path == "/auth":
			s.handleAuth(w, r)
			return
		case strings.HasPrefix(r.URL.Path, "/api/"):
			w.Header().Set("Cache-Control", "no-store")
			if !s.authed(r) {
				writeErr(w, http.StatusUnauthorized, "not signed in to this UI; open the link from: storagebd ui-url")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				if r.Header.Get(csrfHeader) != "1" {
					writeErr(w, http.StatusForbidden, "missing request header")
					return
				}
				if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
					writeErr(w, http.StatusForbidden, "cross-origin request refused")
					return
				}
			}
			api.ServeHTTP(w, r)
			return
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			if !s.authed(r) {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/locked.html"
				files.ServeHTTP(w, r2)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
		http.Error(w, "invalid or expired link; get a fresh one with: storagebd ui-url", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	// Redirect so the token leaves the address bar and history entry.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
