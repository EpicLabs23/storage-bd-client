// Package repo opens a stored credential's restic repository: resolves
// the restic binary, mints data tokens, and on first use initializes the
// repo and escrows its password with storage.bd (§05/§19). Shared by the
// CLI and (module 5) the web UI / scheduler.
package repo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"storage-bd-client/internal/api"
	"storage-bd-client/internal/restic"
	"storage-bd-client/internal/store"
)

type Manager struct {
	Store     *store.Store
	DataDir   string
	UserAgent string
}

// ResticBinary resolves and version-checks the restic binary.
func (m *Manager) ResticBinary(ctx context.Context) (path, version string, err error) {
	explicit, err := m.Store.GetSetting(ctx, store.SettingResticPath)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", "", err
	}
	if path, err = restic.Locate(explicit); err != nil {
		return "", "", err
	}
	version, err = restic.Version(ctx, path)
	return path, version, err
}

// tokenSource caches one tenant's data token and re-mints it shortly
// before expiry. Safe for the relay's concurrent requests.
type tokenSource struct {
	client *api.Client
	cred   *store.Credential

	mu      sync.Mutex
	token   string
	expires time.Time
}

// refreshMargin: re-mint this long before expiry, so a token is never
// sent with only seconds left (an upload can take a while to start).
const refreshMargin = 2 * time.Minute

func (t *tokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expires) > refreshMargin {
		return t.token, nil
	}
	dt, err := t.client.DataToken(ctx, t.cred.TenantID, t.cred.InstallID, t.cred.InstallSecret)
	if err != nil {
		return "", err
	}
	ttl := time.Duration(dt.ExpiresIn) * time.Second
	if ttl <= refreshMargin {
		// Defensive: never cache a token we'd immediately consider stale.
		ttl = refreshMargin + 30*time.Second
	}
	t.token, t.expires = dt.AccessToken, time.Now().Add(ttl)
	return t.token, nil
}

// Opened is a ready-to-use repository session. Warnings are non-fatal
// problems from setup (e.g. escrow failed and will be retried).
type Opened struct {
	*restic.Session
	Warnings []string
}

// Open prepares cred's repository and returns a session for restic runs.
// On first use it runs `restic init` (or, if the repo already exists,
// verifies this machine's password opens it), then escrows the password.
func (m *Manager) Open(ctx context.Context, cred *store.Credential) (*Opened, error) {
	bin, _, err := m.ResticBinary(ctx)
	if err != nil {
		return nil, err
	}
	client := api.New(cred.APIURL, m.UserAgent)
	tokens := &tokenSource{client: client, cred: cred}

	// Mint once up front: a bad credential or suspended tenant should be
	// a clear error, not a restic "repository does not exist".
	if _, err := tokens.Token(ctx); err != nil {
		return nil, err
	}

	sess, err := restic.OpenSession(restic.SessionConfig{
		Binary: bin, APIURL: cred.APIURL, Tokens: tokens, Password: cred.ResticPassword,
		DataDir: m.DataDir, CacheKey: cred.TenantID,
	})
	if err != nil {
		return nil, err
	}
	opened := &Opened{Session: sess}
	if err := m.ensureReady(ctx, cred, client, tokens, opened); err != nil {
		sess.Close()
		return nil, err
	}
	return opened, nil
}

func (m *Manager) ensureReady(ctx context.Context, cred *store.Credential, client *api.Client,
	tokens *tokenSource, o *Opened) error {
	if cred.RepoInitializedAt == nil {
		err := o.Init(ctx)
		if errors.Is(err, restic.ErrRepoExists) {
			// Initialized before: by an earlier run that crashed before
			// recording it, or by another machine. Either way it's only
			// usable if our password opens it.
			if verr := o.VerifyPassword(ctx); verr != nil {
				if errors.Is(verr, restic.ErrWrongPassword) {
					return fmt.Errorf("repo for %q already exists and this machine's restic password does not open it. "+
						"If it was set up elsewhere, store that password: storagebd credential update %s --restic-password", cred.Name, cred.Name)
				}
				return verr
			}
		} else if err != nil {
			return err
		}
		if err := m.Store.MarkRepoInitialized(ctx, cred.ID); err != nil {
			return err
		}
		now := time.Now()
		cred.RepoInitializedAt = &now
	}

	if cred.PasswordEscrowedAt == nil {
		// Not fatal: this machine's copy is the primary (§05), and refusing
		// to back up because the escrow endpoint hiccupped would leave the
		// customer with no backup at all. Retried on every run until it works.
		token, err := tokens.Token(ctx)
		if err == nil {
			err = client.EscrowPassword(ctx, token, cred.ResticPassword)
		}
		if err != nil {
			o.Warnings = append(o.Warnings, fmt.Sprintf(
				"could not escrow the restic password with storage.bd (will retry next run): %v. "+
					"Until it succeeds, this machine holds the only copy (storagebd credential show %s --reveal).", err, cred.Name))
			return nil
		}
		if err := m.Store.MarkPasswordEscrowed(ctx, cred.ID); err != nil {
			return err
		}
		now := time.Now()
		cred.PasswordEscrowedAt = &now
	}
	return nil
}
