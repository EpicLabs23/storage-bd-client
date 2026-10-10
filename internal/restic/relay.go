package restic

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// TokenSource returns a currently-valid storage.bd data token (§04),
// minting a new one when the cached one is near expiry.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Relay is a loopback-only HTTP reverse proxy between restic and the
// storage.bd data-plane proxy, alive for one restic invocation.
//
// Why not hand restic the data token directly (as ecp-go does)? Data
// tokens live 15 minutes and the proxy verifies expiry on every request,
// so any backup or restore longer than that fails midway. The relay
// attaches a fresh token per request instead. As a side effect neither the
// install secret nor any data token is ever in restic's environment: restic
// only knows a random per-run nonce that is useless outside this relay.
//
// Redirects (§03's presigned-S3 restore path) are passed back to restic
// untouched, so large blob downloads still go straight to S3. Go's HTTP
// client drops the Authorization header on a cross-host redirect, so the
// nonce never reaches S3.
type Relay struct {
	nonce    string
	listener net.Listener
	server   *http.Server
}

type tokenKey struct{}

// StartRelay listens on 127.0.0.1 (random port) and forwards to
// apiURL + "/repo".
func StartRelay(apiURL string, tokens TokenSource) (*Relay, error) {
	upstream, err := url.Parse(strings.TrimRight(apiURL, "/") + "/repo")
	if err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(nonceBytes)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = upstream.Scheme
			pr.Out.URL.Host = upstream.Host
			pr.Out.URL.Path = upstream.Path + pr.In.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = upstream.Host
			token := pr.In.Context().Value(tokenKey{}).(string)
			pr.Out.SetBasicAuth("storagebd", token)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) {
				log.Printf("storagebd relay: %s %s: %v", r.Method, r.URL.Path, err)
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(nonce)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// restic only ever sends clean, absolute repo paths; anything else
		// isn't restic.
		if !strings.HasPrefix(r.URL.Path, "/") || strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "//") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		token, err := tokens.Token(r.Context())
		if err != nil {
			log.Printf("storagebd relay: could not get a data token: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, token)))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	rl := &Relay{
		nonce:    nonce,
		listener: ln,
		server:   &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second},
	}
	go rl.server.Serve(ln)
	return rl, nil
}

// RepoURL is the RESTIC_REPOSITORY value pointing restic at this relay.
func (r *Relay) RepoURL() string {
	return "rest:http://storagebd:" + r.nonce + "@" + r.listener.Addr().String() + "/"
}

func (r *Relay) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.server.Shutdown(ctx)
}
