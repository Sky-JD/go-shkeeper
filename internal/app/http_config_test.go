package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestConfigCompatibilityEndpointsUseMariaDB(t *testing.T) {
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
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/BTC/calc-tx-fee/1.25" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"fee": "0.00002", "fee_satoshi": "2"})
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))

	handler := newTestHTTPHandler(t, store, cfg)
	setAdminPassword(t, store, cfg, "admin-password")

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/payment-gateway", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"token":"test-api-key"`) {
		t.Fatalf("gateway get status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payment-gateway", map[string]any{"enabled": false})
	if res.Code != http.StatusOK {
		t.Fatalf("gateway set status=%d body=%s", res.Code, res.Body.String())
	}
	wallet, err := store.WalletByCrypto(ctx, "BTC")
	if err != nil || wallet.Enabled {
		t.Fatalf("gateway enabled was not persisted: enabled=%v err=%v", wallet.Enabled, err)
	}

	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payment-gateway/token", map[string]any{"token": "new-api-key"})
	if res.Code != http.StatusOK {
		t.Fatalf("token set status=%d body=%s", res.Code, res.Body.String())
	}
	wallet, err = store.WalletByCrypto(ctx, "BTC")
	if err != nil || nullStringValue(wallet.APIKey) != "new-api-key" {
		t.Fatalf("token was not persisted: wallet=%+v err=%v", wallet, err)
	}

	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payout_destinations", map[string]any{"action": "add", "daddress": "bc1dest", "comment": "main"})
	if res.Code != http.StatusOK {
		t.Fatalf("destination add status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payout_destinations", map[string]any{"action": "list"})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "bc1dest") {
		t.Fatalf("destination list status=%d body=%s", res.Code, res.Body.String())
	}

	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/autopayout", map[string]any{
		"add":              "bc1dest",
		"fee":              "3",
		"policy":           "limit",
		"policyStatus":     true,
		"policyValue":      "10",
		"prespolicyOption": "percent",
		"prespolicyValue":  "20",
		"partiallPaid":     "90",
		"addedFee":         "110",
		"confirationNum":   2,
		"recalc":           30,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("autopayout status=%d body=%s", res.Code, res.Body.String())
	}
	wallet, err = store.WalletByCrypto(ctx, "BTC")
	if err != nil {
		t.Fatalf("load wallet after autopayout: %v", err)
	}
	if wallet.PPolicy != "limit" || wallet.PresPolicy != "percent" || nullStringValue(wallet.PresAmount) != "20" || wallet.Confirmations != 2 {
		t.Fatalf("autopayout was not persisted: %+v", wallet)
	}

	res = adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/exchange-rate", map[string]any{"fiat": "USD", "source": "manual", "rate": "65000", "fee": "1.5"})
	if res.Code != http.StatusOK {
		t.Fatalf("exchange-rate status=%d body=%s", res.Code, res.Body.String())
	}
	rate, err := store.ExchangeRate(ctx, "USD", "BTC")
	if err != nil || rate.Source != "manual" || !rate.Rate.Equal(decimal.NewFromInt(65000)) || !rate.Fee.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("exchange rate was not persisted: %+v err=%v", rate, err)
	}

	res = adminJSON(t, handler, http.MethodGet, "/api/v1/BTC/estimate-tx-fee/1.25?address=bc1dest", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"fee_satoshi":"2"`) {
		t.Fatalf("estimate fee status=%d body=%s", res.Code, res.Body.String())
	}

	payout := Payout{Amount: decimal.RequireFromString("2.5"), Crypto: "BTC", DestAddr: "bc1dest", ExternalID: sqlNullString("payout-config"), TaskID: sqlNullString("task-config"), Status: PayoutInProgress}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/BTC/payouts?amount=2.5", nil)
	req.Header.Set("X-Shkeeper-Api-Key", "new-api-key")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"success"`) || !strings.Contains(res.Body.String(), `"external_id":"payout-config"`) {
		t.Fatalf("payouts status=%d body=%s", res.Code, res.Body.String())
	}
}

func adminJSON(t *testing.T, handler *HTTPHandler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "admin-password")
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	return res
}
