package testutil

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FakeAccountPassword is the only password the fake's account login accepts.
const FakeAccountPassword = "demo-password"

const fakePricePerGB = 5.0

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func fakeUUID() string {
	h := randHex(16)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

// fakeJWT is unsigned; the client only decodes exp to schedule refreshes.
func fakeJWT(ttl time.Duration) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(ttl).Unix())) + ".x"
}

func (f *FakeStorage) issueSession(w http.ResponseWriter) {
	rt := randHex(16)
	f.refresh[rt] = true
	http.SetCookie(w, &http.Cookie{Name: "account_refresh_token", Value: rt, MaxAge: 30 * 86400, HttpOnly: true})
	json.NewEncoder(w).Encode(map[string]string{"access_token": fakeJWT(15 * time.Minute)})
}

// serveAccount implements the D2C account endpoints storagebd uses:
// login/refresh/logout, tenants list/create/activate, balance, pricing.
// Bearer tokens aren't verified (any non-empty one passes).
func (f *FakeStorage) serveAccount(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	bearer := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	path := r.URL.Path

	switch {
	case path == "/public/pricing" && r.Method == http.MethodGet:
		json.NewEncoder(w).Encode(map[string]any{"price_per_gb": fmt.Sprint(fakePricePerGB), "currency": "BDT"})

	case path == "/auth/account-login" && r.Method == http.MethodPost:
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["password"] != FakeAccountPassword {
			w.WriteHeader(http.StatusFailedDependency)
			json.NewEncoder(w).Encode(map[string]string{"message": "Invalid Credentials"})
			return
		}
		f.issueSession(w)

	case path == "/auth/account-refresh" && r.Method == http.MethodPost:
		ck, err := r.Cookie("account_refresh_token")
		if err != nil || !f.refresh[ck.Value] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		delete(f.refresh, ck.Value)
		f.issueSession(w)

	case path == "/auth/account-logout" && r.Method == http.MethodPost:
		if ck, err := r.Cookie("account_refresh_token"); err == nil {
			delete(f.refresh, ck.Value)
		}
		w.WriteHeader(http.StatusCreated)

	case !bearer && (strings.HasPrefix(path, "/d2c/")):
		w.WriteHeader(http.StatusUnauthorized)

	case path == "/d2c/billing/balance" && r.Method == http.MethodGet:
		json.NewEncoder(w).Encode(map[string]string{"balance": fmt.Sprintf("%.2f", f.Balance)})

	case path == "/d2c/tenants" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, t := range f.tenants {
			out = append(out, t)
		}
		json.NewEncoder(w).Encode(out)

	case path == "/d2c/tenants" && r.Method == http.MethodPost:
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		id := TenantID // first tenant maps onto the fake's single repo
		if _, used := f.tenants[id]; used {
			id = fakeUUID()
		}
		t := map[string]any{"tenant_id": id, "install_id": InstallID, "local_label": b["local_label"],
			"storage_gb": b["storage_gb"], "state": "suspended", "suspend_reason": nil,
			"next_billing_at": time.Now().AddDate(0, 1, 0), "createdAt": time.Now()}
		f.tenants[id] = t
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"tenant": t, "install_id": InstallID, "install_secret": InstallSecret})

	case strings.HasPrefix(path, "/d2c/tenants/") && strings.HasSuffix(path, "/activate") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/d2c/tenants/"), "/activate")
		t, ok := f.tenants[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gb, _ := t["storage_gb"].(float64)
		cost := gb * fakePricePerGB
		if f.Balance < cost {
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(map[string]string{"message": fmt.Sprintf("Account balance (%.2f) does not cover this plan's monthly cost (%.2f)", f.Balance, cost)})
			return
		}
		f.Balance -= cost
		t["state"] = "active"
		json.NewEncoder(w).Encode(t)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
