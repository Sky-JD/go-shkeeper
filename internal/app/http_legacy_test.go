package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestLegacyOperationalEndpoints(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	if err := store.EnsureExchangeRate(ctx, "BTC", "USD"); err != nil {
		t.Fatalf("ensure rate: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE " + store.table("exchange_rate") + " SET source = 'manual', rate = 50000 WHERE crypto = 'BTC' AND fiat = 'USD'"); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	invoice := Invoice{
		Crypto:        "BTC",
		Addr:          "bc1invoice",
		ExternalID:    "legacy-transaction",
		Fiat:          "USD",
		CallbackURL:   "https://example.test/callback",
		AmountFiat:    decimal.NewFromInt(10),
		AmountCrypto:  decimal.RequireFromString("0.0002"),
		ExchangeRate:  decimal.NewFromInt(50000),
		BalanceFiat:   decimal.Zero,
		BalanceCrypto: decimal.Zero,
		Status:        InvoiceUnpaid,
	}
	if err := store.CreateInvoice(ctx, &invoice); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if err := store.AddInvoiceAddress(ctx, invoice.ID, "BTC", invoice.Addr); err != nil {
		t.Fatalf("add invoice address: %v", err)
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BTC/generate-address":
			_ = json.NewEncoder(w).Encode(map[string]any{"address": "bc1generated"})
		case "/BTC/dump":
			username, password, ok := r.BasicAuth()
			if !ok || username != "worker-user" || password != "worker-pass" {
				t.Fatalf("backup used wrong worker auth: ok=%v username=%s password=%s", ok, username, password)
			}
			w.Header().Set("Content-Type", "application/json")
			account := map[string]string{"address": "bc1generated"}
			if r.URL.Query().Get("include_private_key") == "1" {
				account["private_key"] = "plain-private-key"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "accounts": []map[string]string{account}})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	t.Setenv("SHKEEPER_BTC_BACKEND_KEY", "backend-key")

	handler := newTestHTTPHandler(t, store, cfg)
	setAdminPassword(t, store, cfg, "admin-password")

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/generate-address", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "bc1generated") {
		t.Fatalf("generate-address status=%d body=%s", res.Code, res.Body.String())
	}

	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/transaction", map[string]any{"txid": "legacy-tx", "addr": "bc1invoice", "amount": "0.0002", "confirmations": 6})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"success"`) {
		t.Fatalf("transaction status=%d body=%s", res.Code, res.Body.String())
	}
	order, err := store.GetOrder(ctx, "legacy-transaction")
	if err != nil || len(order.Invoices) != 1 || len(order.Invoices[0].Transactions) != 1 {
		t.Fatalf("legacy transaction was not persisted in order details: order=%+v err=%v", order, err)
	}

	res = adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/server", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), strings.TrimPrefix(backend.URL, "http://")) {
		t.Fatalf("server details status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/server/key", map[string]any{"username": "worker-user", "password": "worker-pass"})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"key":"worker-user:worker-pass"`) {
		t.Fatalf("server key status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/server/host", map[string]any{"host": backend.URL})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), strings.TrimPrefix(backend.URL, "http://")) {
		t.Fatalf("server host status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/server", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"key":"worker-user:worker-pass"`) || !strings.Contains(res.Body.String(), strings.TrimPrefix(backend.URL, "http://")) {
		t.Fatalf("server details after update status=%d body=%s", res.Code, res.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/BTC/decrypt", nil)
	req.Header.Set("X-Shkeeper-Backend-Key", "backend-key")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"key":""`) {
		t.Fatalf("decrypt status=%d body=%s", res.Code, res.Body.String())
	}

	res = adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/backup", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "bc1generated") {
		t.Fatalf("backup status=%d body=%s", res.Code, res.Body.String())
	}

	res = adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/backup?include_private_key=1", nil)
	if res.Code != http.StatusForbidden {
		t.Fatalf("plaintext backup without admin session should be forbidden, status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/BTC/backup?include_private_key=1", nil)
	req.SetBasicAuth("admin", "admin-password")
	req.AddCookie(&http.Cookie{Name: "shkeeper_session", Value: "session"})
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("plaintext backup with forged session should be forbidden, status=%d body=%s", res.Code, res.Body.String())
	}

	user, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("load admin user: %v", err)
	}
	loginRes := httptest.NewRecorder()
	handler.auth.Login(loginRes, user)
	sessionCookie := responseCookie(loginRes, "shkeeper_session")
	if sessionCookie == nil {
		t.Fatalf("login did not create session cookie")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/BTC/backup?include_private_key=1", nil)
	req.AddCookie(sessionCookie)
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "plain-private-key") {
		t.Fatalf("plaintext backup status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/test-callback-receiver", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("test callback status=%d body=%s", res.Code, res.Body.String())
	}
}
