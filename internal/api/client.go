// Package api is the client for storage-bd-api's account (D2C) and
// data-token endpoints. It never talks to the data-plane proxy itself;
// restic does that (module 3).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Account refresh token cookie name, set by POST /auth/account-login and
// rotated by /auth/account-refresh.
const accountRefreshCookie = "account_refresh_token"

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

func New(baseURL, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: userAgent,
	}
}

// Error is a non-2xx response. Message is the API's own text when it
// sent one (Nest's {message} shape), else the HTTP status text.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("storage.bd: %s (HTTP %d)", e.Message, e.Status)
}

func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

type request struct {
	method, path string
	bearer       string
	basicPass    string // data-plane routes take the data token as Basic-auth password
	cookie       *http.Cookie
	body         any
}

func (c *Client) do(ctx context.Context, r request, out any) (*http.Response, error) {
	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.BaseURL+r.path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	if r.basicPass != "" {
		req.SetBasicAuth("storagebd", r.basicPass)
	}
	if r.cookie != nil {
		req.AddCookie(r.cookie)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach storage.bd at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, &Error{Status: resp.StatusCode, Message: errorMessage(resp, data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("unexpected response from storage.bd %s: %w", r.path, err)
		}
	}
	return resp, nil
}

func errorMessage(resp *http.Response, data []byte) string {
	var body struct {
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(data, &body) == nil && len(body.Message) > 0 {
		var s string
		if json.Unmarshal(body.Message, &s) == nil && s != "" {
			return s
		}
		var list []string
		if json.Unmarshal(body.Message, &list) == nil && len(list) > 0 {
			return strings.Join(list, "; ")
		}
	}
	return http.StatusText(resp.StatusCode)
}

// Session is an account login. AccessToken is short-lived (15 min) and
// kept in memory only; RefreshToken is what gets persisted.
type Session struct {
	AccessToken      string
	RefreshToken     string
	RefreshExpiresAt time.Time
}

func sessionFrom(resp *http.Response, accessToken string) (*Session, error) {
	for _, ck := range resp.Cookies() {
		if ck.Name == accountRefreshCookie && ck.Value != "" {
			s := &Session{AccessToken: accessToken, RefreshToken: ck.Value}
			switch {
			case ck.MaxAge > 0:
				s.RefreshExpiresAt = time.Now().Add(time.Duration(ck.MaxAge) * time.Second)
			case !ck.Expires.IsZero():
				s.RefreshExpiresAt = ck.Expires
			default:
				s.RefreshExpiresAt = time.Now().Add(24 * time.Hour)
			}
			return s, nil
		}
	}
	return nil, errors.New("storage.bd did not return a refresh token")
}

// AccountLogin signs in a D2C account. remember=true gets the 30-day
// refresh window instead of 24h.
func (c *Client) AccountLogin(ctx context.Context, email, password string, remember bool) (*Session, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	resp, err := c.do(ctx, request{method: http.MethodPost, path: "/auth/account-login",
		body: map[string]any{"email": email, "password": password, "remember": remember}}, &out)
	if err != nil {
		// The API answers bad credentials with 424, not 401.
		if s := StatusOf(err); s == http.StatusFailedDependency || s == http.StatusUnauthorized {
			return nil, errors.New("invalid email or password")
		}
		return nil, err
	}
	return sessionFrom(resp, out.AccessToken)
}

// AccountRefresh rotates the refresh token: the old one stops working,
// so the caller must persist the returned session.
func (c *Client) AccountRefresh(ctx context.Context, refreshToken string) (*Session, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	resp, err := c.do(ctx, request{method: http.MethodPost, path: "/auth/account-refresh",
		cookie: &http.Cookie{Name: accountRefreshCookie, Value: refreshToken}}, &out)
	if err != nil {
		return nil, err
	}
	return sessionFrom(resp, out.AccessToken)
}

// AccountLogout revokes the access token and refresh token server-side.
func (c *Client) AccountLogout(ctx context.Context, s *Session) error {
	_, err := c.do(ctx, request{method: http.MethodPost, path: "/auth/account-logout",
		bearer: s.AccessToken, cookie: &http.Cookie{Name: accountRefreshCookie, Value: s.RefreshToken}}, nil)
	return err
}

// Tenant is the subset of storage-bd-api's Tenant row the client uses.
// The API returns more (S3 location, escrow envelope); those are
// deliberately not decoded.
type Tenant struct {
	TenantID      string    `json:"tenant_id"`
	InstallID     string    `json:"install_id"`
	LocalLabel    string    `json:"local_label"`
	StorageGB     int       `json:"storage_gb"`
	State         string    `json:"state"`
	SuspendReason *string   `json:"suspend_reason"`
	NextBillingAt time.Time `json:"next_billing_at"`
	EscrowedAt    *string   `json:"restic_password_escrowed_at"`
	CreatedAt     time.Time `json:"createdAt"`
}

func (c *Client) ListTenants(ctx context.Context, accessToken string) ([]Tenant, error) {
	var out []Tenant
	_, err := c.do(ctx, request{method: http.MethodGet, path: "/d2c/tenants", bearer: accessToken}, &out)
	return out, err
}

// CreatedTenant carries the install secret, which the API returns exactly
// once. The caller must store it before doing anything else.
type CreatedTenant struct {
	Tenant        Tenant `json:"tenant"`
	InstallID     string `json:"install_id"`
	InstallSecret string `json:"install_secret"`
}

// CreateTenant creates a D2C tenant in state `suspended`; ActivateTenant
// is what starts (and charges for) its first period.
func (c *Client) CreateTenant(ctx context.Context, accessToken, label string, storageGB int) (*CreatedTenant, error) {
	var out CreatedTenant
	_, err := c.do(ctx, request{method: http.MethodPost, path: "/d2c/tenants", bearer: accessToken,
		body: map[string]any{"local_label": label, "storage_gb": storageGB}}, &out)
	if err != nil {
		return nil, err
	}
	if out.InstallSecret == "" || out.Tenant.TenantID == "" {
		return nil, errors.New("storage.bd created the backup space but returned no space access key")
	}
	return &out, nil
}

// ActivateTenant debits one month at the current rate from the account
// balance. 402 means the balance doesn't cover it.
func (c *Client) ActivateTenant(ctx context.Context, accessToken, tenantID string) (*Tenant, error) {
	var out Tenant
	_, err := c.do(ctx, request{method: http.MethodPost, path: "/d2c/tenants/" + url.PathEscape(tenantID) + "/activate",
		bearer: accessToken}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Balance(ctx context.Context, accessToken string) (string, error) {
	var out struct {
		Balance json.Number `json:"balance"`
	}
	_, err := c.do(ctx, request{method: http.MethodGet, path: "/d2c/billing/balance", bearer: accessToken}, &out)
	return out.Balance.String(), err
}

type Pricing struct {
	PricePerGB json.Number `json:"price_per_gb"`
	Currency   string      `json:"currency"`
}

func (c *Client) Pricing(ctx context.Context) (*Pricing, error) {
	var out Pricing
	_, err := c.do(ctx, request{method: http.MethodGet, path: "/public/pricing"}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DataToken exchanges an install credential for a short-lived data-plane
// token (§04). restic receives it as the Basic-auth password in the repo
// URL.
type DataToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (c *Client) DataToken(ctx context.Context, tenantID, installID, installSecret string) (*DataToken, error) {
	var out DataToken
	_, err := c.do(ctx, request{method: http.MethodPost, path: "/auth/data-token",
		body: map[string]string{"tenant_id": tenantID, "install_id": installID, "install_secret": installSecret}}, &out)
	if err != nil {
		if StatusOf(err) == http.StatusUnauthorized {
			return nil, errors.New("storage.bd rejected this space access key (wrong space secret key, the key was replaced or revoked, or the Space ID doesn't belong to this key)")
		}
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("storage.bd returned an empty data token")
	}
	return &out, nil
}

// EscrowPassword submits the repo's restic password for KMS-envelope
// escrow (§05). Called once after init (and after a rotation). The
// password crosses the wire exactly here; never log the request.
func (c *Client) EscrowPassword(ctx context.Context, dataToken, password string) error {
	_, err := c.do(ctx, request{method: http.MethodPut, path: "/escrow/password", basicPass: dataToken,
		body: map[string]string{"password": password}}, nil)
	return err
}
