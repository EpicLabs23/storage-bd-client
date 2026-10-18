package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Opening the page to the network. It is "exposed" when it listens on a
// non-loopback address, or when ui-hosts names are set (a reverse proxy
// in front of a loopback listener). Exposed means:
//   - every sign-in needs two-step verification, and only 2FA-verified
//     sessions are accepted (auth.go);
//   - a non-loopback listener must serve HTTPS itself (the password and
//     session cookie would otherwise cross the network in the clear);
//   - Host may also be an IP literal or a ui-hosts name. DNS rebinding
//     needs an attacker-controlled *name*, so IP literals stay safe.

// ValidateListenAddr accepts loopback addresses, and any IP address when
// HTTPS is configured.
func ValidateListenAddr(addr string, haveTLS bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if host != "" && net.ParseIP(host) == nil {
		return fmt.Errorf("listen address %q: use an IP address (0.0.0.0 for all)", addr)
	}
	if !haveTLS {
		return fmt.Errorf("refusing to open the page to the network on %q without HTTPS: "+
			"set ui-tls-cert and ui-tls-key first (storagebd settings set ui-tls-cert /path/fullchain.pem), "+
			"or keep it on 127.0.0.1 and use an SSH tunnel", addr)
	}
	return nil
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// allowedHost is the DNS-rebinding check on the Host header.
func (s *Server) allowedHost(hostport string) bool {
	if isLoopbackHost(hostport) {
		return true
	}
	if !s.exposed {
		return false
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if net.ParseIP(host) != nil {
		return true
	}
	for _, h := range s.Hosts {
		if host == h {
			return true
		}
	}
	return false
}

// fromProxy reports whether forwarding headers may be believed: only
// with ui-hosts set (a proxy is configured) and from a loopback peer.
func (s *Server) fromProxy(r *http.Request) bool {
	if len(s.Hosts) == 0 {
		return false
	}
	ip := net.ParseIP(peerHost(r))
	return ip != nil && ip.IsLoopback()
}

func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientAddr is the address used for rate limiting and the sign-in log.
func (s *Server) clientAddr(r *http.Request) string {
	if s.fromProxy(r) {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return ip.String()
		}
	}
	return peerHost(r)
}

func (s *Server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (s.fromProxy(r) && r.Header.Get("X-Forwarded-Proto") == "https")
}

// sameOrigin compares Origin's host with Host. The scheme isn't compared,
// so it also works behind a TLS-terminating proxy; the custom request
// header is what stops cross-site requests anyway (it forces a CORS
// preflight, which this server never approves).
func sameOrigin(origin, host string) bool {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(origin, p) {
			return strings.EqualFold(origin[len(p):], host)
		}
	}
	return false
}

// ---- HTTPS ----

// certReloader serves the configured certificate and picks up a renewed
// one (e.g. certbot's) without a restart.
type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	mod     time.Time
	checked time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	c := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := checkKeyFile(keyFile); err != nil {
		return nil, err
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

// checkKeyFile refuses a private key that other users can read.
func checkKeyFile(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("ui-tls-key: %w", err)
	}
	if fi.Mode().Perm()&0o004 != 0 {
		return fmt.Errorf("ui-tls-key %s is readable by every user on this machine; chmod o-r it first", p)
	}
	return nil
}

func latestMod(paths ...string) time.Time {
	var t time.Time
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

func (c *certReloader) load() error {
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("HTTPS certificate: %w", err)
	}
	c.cert, c.mod, c.checked = &cert, latestMod(c.certFile, c.keyFile), time.Now()
	return nil
}

func (c *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) > 30*time.Second {
		c.checked = time.Now()
		if m := latestMod(c.certFile, c.keyFile); m.After(c.mod) {
			old := c.cert
			if err := c.load(); err != nil {
				c.cert = old
				log.Printf("ui: keeping the current HTTPS certificate: %v", err)
			} else {
				log.Printf("ui: loaded renewed HTTPS certificate from %s", c.certFile)
			}
		}
	}
	if c.cert == nil {
		return nil, errors.New("no certificate")
	}
	return c.cert, nil
}

func (c *certReloader) config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: c.get}
}
