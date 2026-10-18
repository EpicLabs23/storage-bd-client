// Package server is `storagebd serve`: the local web UI, its JSON API,
// and the backup scheduler.
//
// Security model. The UI can restore files to any path and read/write
// this machine's credentials, and storagebd often runs as root, so:
//   - Sign-in is a username and password (auth.go). Users are created
//     only from the terminal (`storagebd user add`), so whoever reaches
//     the page first can't claim it. Failed sign-ins are rate limited.
//   - The session lives server-side; the browser holds an opaque,
//     HttpOnly, SameSite=Strict cookie.
//   - Listens on loopback by default (remote use via an SSH tunnel). It
//     can be opened to the network (expose.go): that needs HTTPS, and
//     every sign-in then needs two-step verification.
//   - Host must be a loopback name, an IP, or a configured name (DNS-
//     rebinding defense). Mutating requests need a custom header and a
//     matching Origin (CSRF defense).
//   - Strict CSP and frame-ancestors 'none' (no inline script, no
//     clickjacking).
package server

import (
	"context"
	"crypto/tls"
	"embed"
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

	// From the ui-tls-cert / ui-tls-key / ui-hosts settings.
	TLSCertFile string
	TLSKeyFile  string
	Hosts       []string

	hub     *hub
	jobs    *jobManager
	fails   *failures
	exposed bool
}

// ServeState is what `storagebd ui-url` reads.
type ServeState struct {
	URL     string   `json:"url"`
	Also    []string `json:"also,omitempty"`
	Exposed bool     `json:"exposed"`
	PID     int      `json:"pid"`
}

// Ready describes the running page, for `serve`'s startup message.
type Ready struct {
	ServeState
	Port    int  // the port storagebd listens on
	Proxied bool // loopback listener behind a reverse proxy (ui-hosts)
}

// Run serves until ctx is cancelled. onReady receives where the page is.
func (s *Server) Run(ctx context.Context, addr string, onReady func(Ready)) error {
	haveTLS := s.TLSCertFile != "" || s.TLSKeyFile != ""
	if haveTLS && (s.TLSCertFile == "" || s.TLSKeyFile == "") {
		return errors.New("HTTPS needs both ui-tls-cert and ui-tls-key")
	}
	if err := ValidateListenAddr(addr, haveTLS); err != nil {
		return err
	}
	var certs *certReloader
	if haveTLS {
		var err error
		if certs, err = newCertReloader(s.TLSCertFile, s.TLSKeyFile); err != nil {
			return err
		}
	}
	s.hub = newHub()
	s.jobs = newJobManager(s)
	s.fails = newFailures()
	s.exposed = !isLoopbackListen(addr) || len(s.Hosts) > 0

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if certs != nil {
		ln = tls.NewListener(ln, certs.config())
	}
	st := s.pageURLs(ln.Addr().(*net.TCPAddr), certs != nil)
	if err := s.writeState(st); err != nil {
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
	go s.sweepSessions(ctx)

	if onReady != nil {
		a := ln.Addr().(*net.TCPAddr)
		onReady(Ready{ServeState: st, Port: a.Port, Proxied: a.IP.IsLoopback() && len(s.Hosts) > 0})
	}
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		s.jobs.wait()
		return nil
	}
	return err
}

// pageURLs works out the address(es) to tell the admin.
func (s *Server) pageURLs(a *net.TCPAddr, https bool) ServeState {
	st := ServeState{Exposed: s.exposed, PID: os.Getpid()}
	scheme := "http"
	if https {
		scheme = "https"
	}
	port := fmt.Sprint(a.Port)
	switch {
	case a.IP.IsLoopback() && len(s.Hosts) > 0:
		// Behind a reverse proxy: its address first, then the direct one.
		st.URL = "https://" + s.Hosts[0] + "/"
		st.Also = []string{scheme + "://" + net.JoinHostPort(a.IP.String(), port) + "/"}
	case a.IP.IsLoopback():
		st.URL = scheme + "://" + net.JoinHostPort(a.IP.String(), port) + "/"
	default:
		host := a.IP.String()
		if len(s.Hosts) > 0 {
			host = s.Hosts[0]
		} else if a.IP.IsUnspecified() {
			host = "<this-server's-address>"
		}
		st.URL = scheme + "://" + net.JoinHostPort(host, port) + "/"
	}
	return st
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
	ui := s.uiRoutes()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		if !s.allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			// Static assets only; the page itself asks /api/ui/status
			// whether to show the sign-in form.
			files.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) != "1" {
				writeErr(w, http.StatusForbidden, "missing request header")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
				writeErr(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/ui/") {
			ui.ServeHTTP(w, r)
			return
		}
		u, _ := s.currentUser(r)
		if u == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "please sign in", "code": signinRequired})
			return
		}
		api.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
