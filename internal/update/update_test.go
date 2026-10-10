package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/blake2b"
)

// testdata/reference.* were made with an independent minisign implementation
// (aead.dev/minisign): minisign -S -t "storagebd v9.9.9" -m SHA256SUMS
func TestVerifyReferenceSignature(t *testing.T) {
	pubText, _ := os.ReadFile("testdata/reference.pub")
	data, _ := os.ReadFile("testdata/reference.SHA256SUMS")
	sig, _ := os.ReadFile("testdata/reference.SHA256SUMS.minisig")
	key, err := ParsePublicKey(string(pubText))
	if err != nil {
		t.Fatal(err)
	}
	trusted, used, err := Verify([]PublicKey{key}, data, sig)
	if err != nil {
		t.Fatal(err)
	}
	if trusted != "storagebd v9.9.9" || used.IDString() != key.IDString() {
		t.Fatalf("trusted=%q key=%s", trusted, used.IDString())
	}
	if !strings.Contains(string(pubText), key.IDString()) {
		t.Fatalf("key id %s not in %q", key.IDString(), pubText)
	}

	if _, _, err := Verify([]PublicKey{key}, append(bytes.Clone(data), 'x'), sig); err == nil {
		t.Fatal("tampered data verified")
	}
	lines := strings.Split(string(sig), "\n")
	lines[2] = "trusted comment: storagebd v9.9.10"
	if _, _, err := Verify([]PublicKey{key}, data, []byte(strings.Join(lines, "\n"))); err == nil {
		t.Fatal("tampered trusted comment verified")
	}
	other := newSigner(t)
	if _, _, err := Verify([]PublicKey{other.pub}, data, sig); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("other key: %v", err)
	}
}

func TestVerifyRefusesLegacyUnhashed(t *testing.T) {
	s := newSigner(t)
	sig := s.sign([]byte("data"), "storagebd v1.0.0")
	lines := strings.Split(string(sig), "\n")
	raw, _ := base64.StdEncoding.DecodeString(lines[1])
	copy(raw, "Ed")
	lines[1] = base64.StdEncoding.EncodeToString(raw)
	if _, _, err := Verify([]PublicKey{s.pub}, []byte("data"), []byte(strings.Join(lines, "\n"))); err == nil {
		t.Fatal("legacy Ed signature accepted")
	}
}

func TestLoadKeys(t *testing.T) {
	pub, _ := os.ReadFile("testdata/reference.pub")
	keys, err := loadKeys(fstest.MapFS{
		"keys/README.md":  {Data: []byte("not a key")},
		"keys/a.pub":      {Data: pub},
		"keys/other.text": {Data: []byte("ignored")},
	})
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%d err=%v", len(keys), err)
	}
	if _, err := loadKeys(fstest.MapFS{"keys/bad.pub": {Data: []byte("nope")}}); err == nil {
		t.Fatal("bad key accepted")
	}
	// The embedded set must at least parse (it may be empty before the first signed release).
	if _, err := ReleaseKeys(); err != nil {
		t.Fatal(err)
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.0", 0},
		{"v0.1.1", "v0.1.0", 1},
		{"v0.2.0", "v0.10.0", -1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.1.0-rc1", "v0.1.0", -1},
		{"v0.1.0-rc2", "v0.1.0-rc1", 1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, v := range []string{"dev", "0.1.0", "v0.1", "v0.1.0-dirty+x", "v0.1.0/../x"} {
		if ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = true", v)
		}
	}
}

func TestDownloadVerifiesAndUnpacks(t *testing.T) {
	s := newSigner(t)
	rel := newRelease(t, s, "v0.2.0", map[string]string{"install.sh": "#!/bin/sh\necho hi\n", "storagebd": "bin"})
	c := rel.client(s.pub)

	if v, err := c.Latest(context.Background()); err != nil || v != "v0.2.0" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
	dir := t.TempDir()
	pkg, keyID, err := c.Download(context.Background(), "v0.2.0", "amd64", dir)
	if err != nil {
		t.Fatal(err)
	}
	if keyID != s.pub.IDString() {
		t.Fatalf("keyID = %s", keyID)
	}
	b, err := os.ReadFile(filepath.Join(pkg, "install.sh"))
	if err != nil || !strings.Contains(string(b), "echo hi") {
		t.Fatalf("install.sh: %q %v", b, err)
	}
	if info, _ := os.Stat(filepath.Join(pkg, "install.sh")); info.Mode().Perm() != 0o755 {
		t.Fatalf("install.sh mode %v", info.Mode())
	}
}

func TestDownloadRefuses(t *testing.T) {
	ctx := context.Background()
	s := newSigner(t)
	files := map[string]string{"install.sh": "x"}

	t.Run("tampered tarball", func(t *testing.T) {
		rel := newRelease(t, s, "v0.2.0", files)
		rel.files["/v0.2.0/storagebd_v0.2.0_linux_amd64.tar.gz"] = tarball(t, "storagebd_v0.2.0_linux_amd64", map[string]string{"install.sh": "evil"})
		_, _, err := rel.client(s.pub).Download(ctx, "v0.2.0", "amd64", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "doesn't match") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tampered sums", func(t *testing.T) {
		rel := newRelease(t, s, "v0.2.0", files)
		rel.files["/v0.2.0/SHA256SUMS"] = append(rel.files["/v0.2.0/SHA256SUMS"], '\n')
		if _, _, err := rel.client(s.pub).Download(ctx, "v0.2.0", "amd64", t.TempDir()); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		rel := newRelease(t, s, "v0.2.0", files)
		if _, _, err := rel.client(newSigner(t).pub).Download(ctx, "v0.2.0", "amd64", t.TempDir()); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("signature for another version", func(t *testing.T) {
		old := newRelease(t, s, "v0.1.5", files)
		rel := newRelease(t, s, "v0.2.0", files)
		// v0.2.0 served with v0.1.5's genuinely signed files.
		rel.files["/v0.2.0/SHA256SUMS"] = old.files["/v0.1.5/SHA256SUMS"]
		rel.files["/v0.2.0/SHA256SUMS.minisig"] = old.files["/v0.1.5/SHA256SUMS.minisig"]
		_, _, err := rel.client(s.pub).Download(ctx, "v0.2.0", "amd64", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "signed for") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsigned", func(t *testing.T) {
		rel := newRelease(t, s, "v0.2.0", files)
		delete(rel.files, "/v0.2.0/SHA256SUMS.minisig")
		_, _, err := rel.client(s.pub).Download(ctx, "v0.2.0", "amd64", t.TempDir())
		if !errors.Is(err, ErrUnsigned) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no keys", func(t *testing.T) {
		rel := newRelease(t, s, "v0.2.0", files)
		c := rel.client(s.pub)
		c.Keys = nil
		if _, _, err := c.Download(ctx, "v0.2.0", "amd64", t.TempDir()); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestExtractRejectsUnsafeEntries(t *testing.T) {
	name := "storagebd_v0.2.0_linux_amd64"
	for _, bad := range []*tar.Header{
		{Name: name + "/../evil", Typeflag: tar.TypeReg},
		{Name: "other/install.sh", Typeflag: tar.TypeReg},
		{Name: name + "/sub/file", Typeflag: tar.TypeReg},
		{Name: name + "/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		bad.Mode = 0o644
		tw.WriteHeader(bad)
		tw.Close()
		gz.Close()
		dir := t.TempDir()
		p := filepath.Join(dir, "x.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o600)
		if err := extract(p, dir, name); err == nil {
			t.Errorf("%s (type %c) accepted", bad.Name, bad.Typeflag)
		}
	}
}

// ---- helpers ----

type signer struct {
	pub  PublicKey
	priv ed25519.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var id [8]byte
	rand.Read(id[:])
	return signer{pub: PublicKey{ID: id, Key: pk}, priv: sk}
}

// sign writes a minisign "ED" signature file.
func (s signer) sign(data []byte, trusted string) []byte {
	h := blake2b.Sum512(data)
	sig := ed25519.Sign(s.priv, h[:])
	global := ed25519.Sign(s.priv, append(bytes.Clone(sig), trusted...))
	line := append(append([]byte("ED"), s.pub.ID[:]...), sig...)
	return fmt.Appendf(nil, "untrusted comment: test\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(line), trusted, base64.StdEncoding.EncodeToString(global))
}

type release struct {
	srv   *httptest.Server
	files map[string][]byte
}

func newRelease(t *testing.T, s signer, version string, contents map[string]string) *release {
	t.Helper()
	r := &release{files: map[string][]byte{}}
	name := "storagebd_" + version + "_linux_amd64"
	tgz := tarball(t, name, contents)
	sum := sha256.Sum256(tgz)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + ".tar.gz\n")
	r.files["/"+version+"/"+name+".tar.gz"] = tgz
	r.files["/"+version+"/SHA256SUMS"] = sums
	r.files["/"+version+"/SHA256SUMS.minisig"] = s.sign(sums, "storagebd "+version)
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/latest" {
			http.Redirect(w, req, "/tag/"+version, http.StatusFound)
			return
		}
		b, ok := r.files[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *release) client(keys ...PublicKey) *Client {
	c := New(keys)
	c.DownloadBase = r.srv.URL
	c.LatestURL = r.srv.URL + "/latest"
	return c
}

func tarball(t *testing.T, name string, contents map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for f, body := range contents {
		mode := int64(0o644)
		if f == "install.sh" || f == "storagebd" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name + "/" + f, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
