package restic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type countingTokens struct {
	mu sync.Mutex
	n  int
}

func (c *countingTokens) Token(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return fmt.Sprintf("tok-%d", c.n), nil
}

func relayAddr(t *testing.T, r *Relay) (host, nonce string) {
	u := strings.TrimPrefix(r.RepoURL(), "rest:http://storagebd:")
	nonce, host, _ = strings.Cut(u, "@")
	return "http://" + strings.TrimSuffix(host, "/"), nonce
}

func TestRelayInjectsCurrentTokenAndRewritesPath(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+pass)
		mu.Unlock()
		if r.URL.Path == "/repo/data/abc" {
			http.Redirect(w, r, "https://s3.example/presigned", http.StatusFound)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	tokens := &countingTokens{}
	relay, err := StartRelay(upstream.URL, tokens)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	host, nonce := relayAddr(t, relay)

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, pass string) *http.Response {
		req, _ := http.NewRequest(method, host+path, nil)
		if pass != "" {
			req.SetBasicAuth("storagebd", pass)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if r := do("GET", "/config", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no nonce: %d", r.StatusCode)
	}
	if r := do("GET", "/config", "wrong"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad nonce: %d", r.StatusCode)
	}
	if r := do("GET", "/config", nonce); r.StatusCode != http.StatusOK {
		t.Fatalf("config: %d", r.StatusCode)
	}
	if r := do("POST", "/?create=true", nonce); r.StatusCode != http.StatusOK {
		t.Fatalf("create: %d", r.StatusCode)
	}
	r := do("GET", "/data/abc", nonce)
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "https://s3.example/presigned" {
		t.Fatalf("redirect not passed through: %d %q", r.StatusCode, r.Header.Get("Location"))
	}

	want := []string{"GET /repo/config tok-1", "POST /repo/?create=true tok-2", "GET /repo/data/abc tok-3"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("upstream saw %q, want %q", seen, want)
	}
}

func TestRelayListensOnLoopbackOnly(t *testing.T) {
	relay, err := StartRelay("http://127.0.0.1:1", &countingTokens{})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if !strings.Contains(relay.RepoURL(), "@127.0.0.1:") {
		t.Fatalf("repo URL %q", relay.RepoURL())
	}
}
