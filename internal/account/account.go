// Package account is the storage.bd account workflow shared by the CLI
// and (module 5) the local web UI: login/logout, keeping an access token
// fresh, and connecting this machine as a new D2C tenant.
package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"storage-bd-client/internal/api"
	"storage-bd-client/internal/store"
)

var ErrNotLoggedIn = errors.New("not logged in to storage.bd (run: storagebd login)")

type Manager struct {
	Store     *store.Store
	UserAgent string
}

func (m *Manager) client(apiURL string) *api.Client {
	return api.New(apiURL, m.UserAgent)
}

// Login signs in and persists the session for apiURL, replacing any
// earlier one.
func (m *Manager) Login(ctx context.Context, apiURL, email, password string, remember bool) error {
	email = strings.TrimSpace(strings.ToLower(email))
	sess, err := m.client(apiURL).AccountLogin(ctx, email, password, remember)
	if err != nil {
		return err
	}
	return m.Store.SaveSession(ctx, toStored(apiURL, email, sess))
}

// Logout revokes the session server-side (best effort) and always
// removes it locally. Credentials are untouched: backups keep working.
func (m *Manager) Logout(ctx context.Context, apiURL string) error {
	if _, err := m.Store.GetSession(ctx, apiURL); errors.Is(err, store.ErrNotFound) {
		return ErrNotLoggedIn
	} else if err != nil {
		return err
	}
	var remoteErr error
	if token, err := m.AccessToken(ctx, apiURL); err == nil {
		// Re-read after AccessToken: it may have just rotated the refresh token.
		if cur, err := m.Store.GetSession(ctx, apiURL); err == nil {
			remoteErr = m.client(apiURL).AccountLogout(ctx, &api.Session{AccessToken: token, RefreshToken: cur.RefreshToken})
		}
	}
	if err := m.Store.DeleteSession(ctx, apiURL); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if remoteErr != nil {
		return fmt.Errorf("logged out locally, but storage.bd could not revoke the session: %w", remoteErr)
	}
	return nil
}

// accessMargin is how long before expiry a cached access token is
// considered stale, so a token never expires mid-request.
const accessMargin = time.Minute

// AccessToken returns a valid account access token for apiURL, refreshing
// (and persisting the rotated refresh token) when needed.
func (m *Manager) AccessToken(ctx context.Context, apiURL string) (string, error) {
	sess, err := m.Store.UpdateSessionLocked(ctx, apiURL, func(cur *store.AccountSession) (*store.AccountSession, error) {
		if cur.AccessToken != "" && time.Until(cur.AccessExpiresAt) > accessMargin {
			return nil, nil
		}
		if time.Now().After(cur.RefreshExpiresAt) {
			return nil, errSessionExpired
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fresh, err := m.client(apiURL).AccountRefresh(rctx, cur.RefreshToken)
		if err != nil {
			if s := api.StatusOf(err); s == http.StatusUnauthorized || s == http.StatusForbidden {
				return nil, errSessionExpired
			}
			return nil, err
		}
		return toStored(apiURL, cur.Email, fresh), nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotLoggedIn
	}
	if errors.Is(err, errSessionExpired) {
		// Dead session: drop it so `account` stops reporting a login.
		_ = m.Store.DeleteSession(ctx, apiURL)
		return "", errors.New("storage.bd session expired (run: storagebd login)")
	}
	if err != nil {
		return "", err
	}
	return sess.AccessToken, nil
}

var errSessionExpired = errors.New("session expired")

func toStored(apiURL, email string, s *api.Session) *store.AccountSession {
	return &store.AccountSession{
		APIURL: apiURL, Email: email,
		RefreshToken: s.RefreshToken, RefreshExpiresAt: s.RefreshExpiresAt,
		AccessToken: s.AccessToken, AccessExpiresAt: jwtExpiry(s.AccessToken),
	}
}

// jwtExpiry reads the exp claim without verifying the signature. It is
// only used to decide when to refresh our own cached token; the API
// verifies the token on every call. Falls back to 10 minutes (the API
// issues 15).
func jwtExpiry(token string) time.Time {
	fallback := time.Now().Add(10 * time.Minute)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fallback
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return fallback
	}
	return time.Unix(claims.Exp, 0)
}

// Overview is the account's state as shown by `storagebd account` and
// the UI's account page.
type Overview struct {
	APIURL   string
	Email    string
	Balance  string
	Currency string
	Tenants  []api.Tenant
}

func (m *Manager) Overview(ctx context.Context, apiURL string) (*Overview, error) {
	token, err := m.AccessToken(ctx, apiURL)
	if err != nil {
		return nil, err
	}
	sess, err := m.Store.GetSession(ctx, apiURL)
	if err != nil {
		return nil, err
	}
	c := m.client(apiURL)
	balance, err := c.Balance(ctx, token)
	if err != nil {
		return nil, err
	}
	tenants, err := c.ListTenants(ctx, token)
	if err != nil {
		return nil, err
	}
	o := &Overview{APIURL: apiURL, Email: sess.Email, Balance: balance, Tenants: tenants, Currency: "BDT"}
	if p, err := c.Pricing(ctx); err == nil && p.Currency != "" {
		o.Currency = p.Currency
	}
	return o, nil
}

// Quote is the monthly price of a plan size, from the public rate.
type Quote struct {
	StorageGB  int
	PricePerGB string
	Monthly    string
	Currency   string
}

func (m *Manager) Quote(ctx context.Context, apiURL string, storageGB int) (*Quote, error) {
	p, err := m.client(apiURL).Pricing(ctx)
	if err != nil {
		return nil, err
	}
	q := &Quote{StorageGB: storageGB, PricePerGB: p.PricePerGB.String(), Currency: p.Currency}
	if rate, err := p.PricePerGB.Float64(); err == nil {
		q.Monthly = fmt.Sprintf("%.2f", rate*float64(storageGB))
	}
	return q, nil
}

type ConnectParams struct {
	APIURL    string
	Name      string // local credential name
	Label     string // tenant label shown in the storage.bd dashboard
	StorageGB int
	Activate  bool
}

type ConnectResult struct {
	Credential *store.Credential
	Activated  bool
	// ActivationError is set when the tenant was created and saved but
	// could not be activated (most often 402: balance too low). The
	// connection itself still succeeded.
	ActivationError error
}

// OrphanError is returned when storage.bd created the tenant but this
// machine failed to save its credential. The install secret is shown
// only once, so it's carried here for the caller to show the user.
type OrphanError struct {
	TenantID, InstallID, InstallSecret string
	Err                                error
}

func (e *OrphanError) Error() string {
	return fmt.Sprintf("backup space %s was created on storage.bd but could not be saved locally: %v", e.TenantID, e.Err)
}

func (e *OrphanError) Unwrap() error { return e.Err }

// Connect creates a new tenant for this machine, stores its credential
// with a freshly generated restic password, and optionally activates it
// (which charges the first month).
func (m *Manager) Connect(ctx context.Context, p ConnectParams) (*ConnectResult, error) {
	if err := store.ValidateName(p.Name); err != nil {
		return nil, err
	}
	if p.StorageGB < 1 {
		return nil, errors.New("storage size must be at least 1 GB")
	}
	if p.Label = strings.TrimSpace(p.Label); p.Label == "" {
		return nil, errors.New("label is required")
	}
	// Check the local name before creating anything remote: a name
	// collision after creation would leave a tenant with no local owner.
	if _, err := m.Store.GetCredentialByName(ctx, p.Name); err == nil {
		return nil, fmt.Errorf("a credential named %q already exists on this machine", p.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	sess, err := m.Store.GetSession(ctx, p.APIURL)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotLoggedIn
	} else if err != nil {
		return nil, err
	}
	password, err := store.GenerateResticPassword()
	if err != nil {
		return nil, err
	}
	token, err := m.AccessToken(ctx, p.APIURL)
	if err != nil {
		return nil, err
	}

	c := m.client(p.APIURL)
	created, err := c.CreateTenant(ctx, token, p.Label, p.StorageGB)
	if err != nil {
		return nil, err
	}
	cred := &store.Credential{
		Name: p.Name, APIURL: p.APIURL, TenantID: created.Tenant.TenantID, InstallID: created.InstallID,
		InstallSecret: created.InstallSecret, ResticPassword: password, AccountEmail: sess.Email,
	}
	if err := m.Store.CreateCredential(ctx, cred); err != nil {
		return nil, &OrphanError{TenantID: created.Tenant.TenantID, InstallID: created.InstallID,
			InstallSecret: created.InstallSecret, Err: err}
	}

	res := &ConnectResult{Credential: cred}
	if p.Activate {
		if _, err := c.ActivateTenant(ctx, token, cred.TenantID); err != nil {
			res.ActivationError = activationError(err)
		} else {
			res.Activated = true
		}
	}
	return res, nil
}

// Activate starts (and charges for) the first period of a stored
// credential's tenant.
func (m *Manager) Activate(ctx context.Context, cred *store.Credential) error {
	token, err := m.AccessToken(ctx, cred.APIURL)
	if err != nil {
		return err
	}
	if _, err := m.client(cred.APIURL).ActivateTenant(ctx, token, cred.TenantID); err != nil {
		return activationError(err)
	}
	return nil
}

func activationError(err error) error {
	switch api.StatusOf(err) {
	case http.StatusPaymentRequired:
		return fmt.Errorf("balance too low to activate: %w. Top up at your storage.bd dashboard, then run: storagebd activate", err)
	case http.StatusConflict:
		return fmt.Errorf("already active or not activatable: %w", err)
	}
	return err
}

// TestCredential checks that storage.bd accepts cred by minting a data
// token, the same exchange every backup starts with.
func (m *Manager) TestCredential(ctx context.Context, cred *store.Credential) error {
	_, err := m.client(cred.APIURL).DataToken(ctx, cred.TenantID, cred.InstallID, cred.InstallSecret)
	return err
}

var nonNameChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// DefaultName derives a credential name from the hostname.
func DefaultName() string {
	h, _ := os.Hostname()
	n := strings.Trim(nonNameChars.ReplaceAllString(strings.ToLower(h), "-"), "-._")
	if len(n) > 63 {
		n = n[:63]
	}
	if n == "" {
		n = "this-machine"
	}
	return n
}

// DefaultLabel is the tenant label shown in the storage.bd dashboard.
func DefaultLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "storagebd client"
	}
	return h
}
