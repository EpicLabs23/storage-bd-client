package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"storage-bd-client/internal/store"
	"storage-bd-client/internal/uiauth"
)

var insecureTLS = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

// Also0 is the direct (non-proxy) URL, which is what tests talk to.
func (r Ready) Also0() string {
	if len(r.Also) > 0 {
		return r.Also[0]
	}
	return r.URL
}

func loginBody(pass, code string) string {
	return fmt.Sprintf(`{"username":%q,"password":%q,"code":%q}`, testUser, pass, code)
}

func newJar() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: insecureTLS, Timeout: 30 * time.Second}
}

// enable2FA turns 2FA on for testUser straight in the store; returns the secret.
func enable2FA(t *testing.T, s *Server) string {
	t.Helper()
	ctx := context.Background()
	u, _ := s.Store.GetUIUser(ctx, testUser)
	secret, _ := uiauth.NewTOTPSecret()
	if err := s.Store.StartUITOTP(ctx, u.ID, secret); err != nil {
		t.Fatal(err)
	}
	_, hashes, _ := uiauth.NewBackupCodes()
	if err := s.Store.EnableUITOTP(ctx, u.ID, secret, 0, hashes, ""); err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestTwoStepFromThePage(t *testing.T) {
	hs := start(t)
	c := hs.client(t)
	other := hs.client(t)

	// Setup needs the password again.
	if code, _ := call(t, c, "POST", hs.base+"/api/ui/2fa/setup", `{"password":"wrong-password-1"}`, csrf); code != 400 {
		t.Fatalf("setup with wrong password: %d", code)
	}
	code, out := call(t, c, "POST", hs.base+"/api/ui/2fa/setup", fmt.Sprintf(`{"password":%q}`, testPass), csrf)
	if code != 200 || !strings.HasPrefix(out["qr"].(string), "data:image/svg+xml;base64,") || !strings.HasPrefix(out["uri"].(string), "otpauth://totp/") {
		t.Fatalf("setup: %d %v", code, out)
	}
	secret := out["secret"].(string)
	// Not active until confirmed: password alone still signs in.
	if code, _ := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, ""), csrf); code != 200 {
		t.Fatalf("login before confirm: %d", code)
	}
	if code, _ := call(t, c, "POST", hs.base+"/api/ui/2fa/confirm", `{"code":"000000"}`, csrf); code != 400 {
		t.Fatalf("confirm with wrong code: %d", code)
	}
	now := uiauth.TOTPCode(secret, time.Now())
	code, out = call(t, c, "POST", hs.base+"/api/ui/2fa/confirm", fmt.Sprintf(`{"code":%q}`, now), csrf)
	if code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	backups := out["backup_codes"].([]any)
	if len(backups) != uiauth.BackupCodeCount {
		t.Fatalf("backup codes: %v", backups)
	}
	// The confirming browser stays in; the other is signed out.
	if code, out := call(t, c, "GET", hs.base+"/api/state", "", nil); code != 200 || out["ui_user_2fa"] != true {
		t.Fatalf("confirming browser: %d %v", code, out)
	}
	if code, _ := call(t, other, "GET", hs.base+"/api/state", "", nil); code != 401 {
		t.Fatalf("other browser after enabling 2FA: %d", code)
	}
	// Setup again while on: refused (would replace a working secret).
	if code, _ := call(t, c, "POST", hs.base+"/api/ui/2fa/setup", fmt.Sprintf(`{"password":%q}`, testPass), csrf); code != 400 {
		t.Fatalf("re-setup while on: %d", code)
	}

	// Sign-in now asks for the code after the right password only.
	if code, out := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody("wrong-password-1", ""), csrf); code != 401 || out["code"] == totpRequired {
		t.Fatalf("wrong password must not reveal 2FA: %d %v", code, out)
	}
	if code, out := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, ""), csrf); code != 401 || out["code"] != totpRequired {
		t.Fatalf("password only: %d %v", code, out)
	}
	// The code used to confirm can't be replayed.
	if code, _ := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, now), csrf); code != 401 {
		t.Fatalf("replayed confirm code: %d", code)
	}
	// A backup code works once.
	b0 := backups[0].(string)
	if code, _ := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, b0), csrf); code != 200 {
		t.Fatalf("backup code: %d", code)
	}
	if code, _ := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, b0), csrf); code != 401 {
		t.Fatalf("backup code reused: %d", code)
	}
	// Disable needs password + code.
	if code, _ := call(t, c, "POST", hs.base+"/api/ui/2fa/disable", fmt.Sprintf(`{"password":%q,"code":"zzzz-zzzz-zzzz"}`, testPass), csrf); code != 400 {
		t.Fatalf("disable with bad code: %d", code)
	}
	if code, out := call(t, c, "POST", hs.base+"/api/ui/2fa/disable", fmt.Sprintf(`{"password":%q,"code":%q}`, testPass, backups[1]), csrf); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	if code, _ := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, ""), csrf); code != 200 {
		t.Fatalf("password-only after disable: %d", code)
	}
}

// Behind a reverse proxy (ui-hosts set): exposed, so 2FA is required even
// though the listener is loopback, and old password-only sessions die.
func TestExposedBehindProxy(t *testing.T) {
	hs := start(t, func(s *Server) { s.Hosts = []string{"backup.example.test"} })
	// A session made the way it would have been before exposure.
	plainSess := func() *http.Client {
		c := newJar()
		tok := "pre-exposure-session-token-value"
		u, _ := hs.srv.Store.GetUIUser(context.Background(), testUser)
		if err := hs.srv.Store.CreateUISession(context.Background(), storeSession(tok, u.ID, false)); err != nil {
			t.Fatal(err)
		}
		bu, _ := url.Parse(hs.base)
		c.Jar.SetCookies(bu, []*http.Cookie{{Name: cookieName, Value: tok}})
		return c
	}
	plain := plainSess()
	if code, _ := call(t, plain, "GET", hs.base+"/api/state", "", nil); code != 401 {
		t.Fatalf("password-only session accepted while exposed: %d", code)
	}

	// No 2FA yet: right password is refused with the fix.
	if code, out := call(t, newJar(), "POST", hs.base+"/api/ui/login", loginBody(testPass, ""), csrf); code != 403 || !strings.Contains(out["error"].(string), "user 2fa enable") {
		t.Fatalf("exposed without 2FA: %d %v", code, out)
	}
	secret := enable2FA(t, hs.srv)
	c := newJar()
	hdr := map[string]string{"X-Storagebd": "1", "Host": "backup.example.test", "Origin": "https://backup.example.test",
		"X-Forwarded-Proto": "https", "X-Real-IP": "203.0.113.9"}
	req, _ := http.NewRequest("POST", hs.base+"/api/ui/login", strings.NewReader(loginBody(testPass, uiauth.TOTPCode(secret, time.Now()))))
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
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login via proxy: %d", resp.StatusCode)
	}
	sc := resp.Header.Get("Set-Cookie")
	if !strings.Contains(sc, "Secure") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Strict") {
		t.Fatalf("cookie behind https proxy: %s", sc)
	}
	// The sign-in was recorded with the client's address from the proxy.
	ck := resp.Cookies()[0]
	sess, err := hs.srv.Store.GetUISession(context.Background(), hashToken(ck.Value))
	if err != nil || sess.RemoteAddr != "203.0.113.9" || !sess.MFA {
		t.Fatalf("session: %+v %v", sess, err)
	}
	// Host allow-list: the configured name and IPs pass, other names don't.
	authed := &http.Client{Transport: insecureTLS}
	for host, want := range map[string]int{"backup.example.test": 200, "198.51.100.7:7878": 200, "evil.example": 403} {
		r2, _ := http.NewRequest("GET", hs.base+"/api/state", nil)
		r2.Host = host
		r2.AddCookie(ck)
		res, err := authed.Do(r2)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("Host %s: %d, want %d", host, res.StatusCode, want)
		}
	}
	// Turning 2FA off from the page is refused while exposed.
	r3, _ := http.NewRequest("POST", hs.base+"/api/ui/2fa/disable", strings.NewReader(`{"password":"x","code":"y"}`))
	r3.Header.Set("X-Storagebd", "1")
	r3.AddCookie(ck)
	if res, _ := authed.Do(r3); res.StatusCode != 403 {
		t.Fatalf("disable while exposed: %d", res.StatusCode)
	}
	// Terminal-only settings can't be changed from the page.
	r4, _ := http.NewRequest("PUT", hs.base+"/api/settings", strings.NewReader(`{"key":"ui-listen","value":"0.0.0.0:7878"}`))
	r4.Header.Set("X-Storagebd", "1")
	r4.AddCookie(ck)
	if res, _ := authed.Do(r4); res.StatusCode != 403 {
		t.Fatalf("ui-listen from the page: %d", res.StatusCode)
	}
}

// HTTPS straight from storagebd, with a renewed certificate picked up.
func TestHTTPSAndCertReload(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeCert(t, certFile, keyFile, "first")
	os.Chmod(keyFile, 0o644)
	if _, err := newCertReloader(certFile, keyFile); err == nil {
		t.Fatal("world-readable key accepted")
	}
	os.Chmod(keyFile, 0o600)

	hs := start(t, func(s *Server) { s.TLSCertFile, s.TLSKeyFile = certFile, keyFile })
	if !strings.HasPrefix(hs.base, "https://") {
		t.Fatalf("base %s", hs.base)
	}
	c := hs.client(t) // signs in over TLS
	if code, _ := call(t, c, "GET", hs.base+"/api/state", "", nil); code != 200 {
		t.Fatalf("state over https: %d", code)
	}

	cn := func() string {
		conn, err := tls.Dial("tcp", strings.TrimPrefix(hs.base, "https://"), &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
	}
	if got := cn(); got != "first" {
		t.Fatalf("cert %s", got)
	}
	writeCert(t, certFile, keyFile, "second")
	future := time.Now().Add(time.Minute)
	os.Chtimes(certFile, future, future)
	r := mustReloader(t, hs.srv, certFile, keyFile)
	r.mu.Lock()
	r.checked = time.Time{}
	r.mu.Unlock()
	got, err := r.get(nil)
	if err != nil || got.Leaf == nil || got.Leaf.Subject.CommonName != "second" {
		t.Fatalf("not reloaded: %+v %v", got, err)
	}
}

func mustReloader(t *testing.T, _ *Server, certFile, keyFile string) *certReloader {
	t.Helper()
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	r.mod = time.Time{} // as if loaded before the renewal
	return r
}

func writeCert(t *testing.T, certFile, keyFile, cn string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
}

func storeSession(tok string, userID int64, mfa bool) *store.UISession {
	return &store.UISession{TokenHash: hashToken(tok), UserID: userID, ExpiresAt: time.Now().Add(time.Hour), MFA: mfa}
}
