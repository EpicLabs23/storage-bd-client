// Package update finds, downloads and verifies storagebd releases for
// `storagebd upgrade`. A release is trusted only if its SHA256SUMS carries a
// minisign signature from a key built into this binary (keys/*.pub), with the
// trusted comment "storagebd <version>"; the tarball must then match
// SHA256SUMS. Unlike get.sh, which is fetched from GitHub on every run, this
// check doesn't depend on anything GitHub serves.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const Repo = "EpicLabs23/storage-bd-client"

const (
	maxSums    = 64 << 10
	maxSig     = 4 << 10
	maxTarball = 512 << 20
)

//go:embed keys
var keyFS embed.FS

// ErrUnsigned means the release has no SHA256SUMS.minisig (v0.1.0 predates signing).
var ErrUnsigned = errors.New("release is not signed")

// ReleaseKeys returns the release signing keys built into this binary.
func ReleaseKeys() ([]PublicKey, error) {
	return loadKeys(keyFS)
}

func loadKeys(fsys fs.FS) ([]PublicKey, error) {
	names, err := fs.Glob(fsys, "keys/*.pub")
	if err != nil {
		return nil, err
	}
	var keys []PublicKey
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		k, err := ParsePublicKey(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

type Client struct {
	DownloadBase string // .../releases/download
	LatestURL    string // .../releases/latest (redirects to .../releases/tag/<version>)
	HTTP         *http.Client
	Keys         []PublicKey
}

func New(keys []PublicKey) *Client {
	return &Client{
		DownloadBase: "https://github.com/" + Repo + "/releases/download",
		LatestURL:    "https://github.com/" + Repo + "/releases/latest",
		HTTP:         &http.Client{Timeout: 15 * time.Minute},
		Keys:         keys,
	}
}

// Latest returns the tag GitHub marks as the latest release.
func (c *Client) Latest(ctx context.Context) (string, error) {
	hc := *c.HTTP
	hc.Timeout = 30 * time.Second
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.LatestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("finding the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
		return "", fmt.Errorf("finding the latest release: unexpected response %s", resp.Status)
	}
	v := path.Base(strings.TrimRight(loc, "/"))
	if !ValidVersion(v) {
		return "", fmt.Errorf("latest release has an unexpected tag %q", v)
	}
	return v, nil
}

// Download fetches version for linux/arch into dir, verifies it, unpacks it
// and returns the unpacked package directory (containing install.sh). It
// returns the id of the key that signed it.
func (c *Client) Download(ctx context.Context, version, arch, dir string) (pkgDir, keyID string, err error) {
	if !ValidVersion(version) {
		return "", "", fmt.Errorf("invalid version %q", version)
	}
	if len(c.Keys) == 0 {
		return "", "", errors.New("this storagebd build has no release signing key")
	}
	base := c.DownloadBase + "/" + version + "/"
	sums, err := c.get(ctx, base+"SHA256SUMS", maxSums)
	if err != nil {
		return "", "", err
	}
	sig, err := c.get(ctx, base+"SHA256SUMS.minisig", maxSig)
	if errors.Is(err, errNotFound) {
		return "", "", fmt.Errorf("%s: %w", version, ErrUnsigned)
	} else if err != nil {
		return "", "", err
	}
	trusted, key, err := Verify(c.Keys, sums, sig)
	if err != nil {
		return "", "", fmt.Errorf("%s SHA256SUMS: %w; not installing", version, err)
	}
	// Binds the signature to this tag, so an older signed SHA256SUMS can't
	// be served under a newer version.
	if trusted != "storagebd "+version {
		return "", "", fmt.Errorf("%s SHA256SUMS is signed for %q, not this version; not installing", version, trusted)
	}
	keyID = key.IDString()

	name := fmt.Sprintf("storagebd_%s_linux_%s", version, arch)
	want, ok := lookupSum(sums, name+".tar.gz")
	if !ok {
		return "", "", fmt.Errorf("%s.tar.gz is not listed in the release's SHA256SUMS", name)
	}
	tarPath := filepath.Join(dir, name+".tar.gz")
	if err := c.download(ctx, base+name+".tar.gz", tarPath, want); err != nil {
		return "", "", err
	}
	if err := extract(tarPath, dir, name); err != nil {
		return "", "", fmt.Errorf("unpacking %s: %w", name, err)
	}
	return filepath.Join(dir, name), keyID, nil
}

var errNotFound = errors.New("not found")

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("download %s: %w", url, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: larger than %d bytes", url, limit)
	}
	return b, nil
}

func (c *Client) download(ctx context.Context, url, dst, wantHex string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxTarball+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxTarball {
		return fmt.Errorf("download %s: larger than %d bytes", url, maxTarball)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(wantHex) {
		return fmt.Errorf("%s doesn't match the signed SHA256SUMS; not installing", path.Base(url))
	}
	return f.Close()
}

func lookupSum(sums []byte, file string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file && len(f[0]) == 64 {
			return f[0], true
		}
	}
	return "", false
}

// extract unpacks a release tarball: only regular files directly inside
// name/, nothing else (no links, devices, subdirectories or "..").
func extract(tarPath, dir, name string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	out := filepath.Join(dir, name)
	if err := os.Mkdir(out, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := strings.TrimSuffix(hdr.Name, "/")
		if clean == name && hdr.Typeflag == tar.TypeDir {
			continue
		}
		file, ok := strings.CutPrefix(clean, name+"/")
		if !ok || file == "" || file == "." || file == ".." || strings.Contains(file, "/") {
			return fmt.Errorf("unexpected entry %q", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("unexpected entry type for %q", hdr.Name)
		}
		total += hdr.Size
		if total > maxTarball {
			return errors.New("package too large")
		}
		mode := os.FileMode(0o644)
		if hdr.Mode&0o111 != 0 {
			mode = 0o755
		}
		w, err := os.OpenFile(filepath.Join(out, file), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, io.LimitReader(tr, hdr.Size)); err != nil {
			w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
}

var versionRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// ValidVersion reports whether v is a release tag like v1.2.3 or v1.2.3-rc1.
func ValidVersion(v string) bool { return versionRE.MatchString(v) }

// Compare orders release tags; a pre-release sorts before its release.
// Both must be ValidVersion.
func Compare(a, b string) int {
	ma, mb := versionRE.FindStringSubmatch(a), versionRE.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch pa, pb := ma[4], mb[4]; {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	default:
		return strings.Compare(pa, pb)
	}
}
