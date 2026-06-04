package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCompatBackendEndpoints(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC-LIGHTNING"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC-LIGHTNING", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	if err := store.EnsureExchangeRate(ctx, "BTC-LIGHTNING", "USD"); err != nil {
		t.Fatalf("ensure rate: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE " + store.table("exchange_rate") + " SET source = 'manual', rate = 50000 WHERE crypto = 'BTC-LIGHTNING' AND fiat = 'USD'"); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BTC-LIGHTNING/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"last_block_timestamp": 1780510000})
		case "/BTC-LIGHTNING/balance":
			_ = json.NewEncoder(w).Encode(map[string]any{"balance": "0.25"})
		case "/BTC-LIGHTNING/fee-deposit-account":
			_ = json.NewEncoder(w).Encode(map[string]any{"account": "lnurl1fee"})
		case "/BTC-LIGHTNING/task/task-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "SUCCESS", "result": map[string]any{"txids": []string{"tx1"}}})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BTC_LIGHTNING_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))

	handler := newTestHTTPHandler(t, store, cfg)
	tests := []struct {
		name string
		path string
		auth string
		want int
	}{
		{name: "status", path: "/api/v1/BTC-LIGHTNING/status", auth: "api", want: http.StatusOK},
		{name: "fee", path: "/api/v1/BTC-LIGHTNING/fee-deposit-address", auth: "api", want: http.StatusOK},
		{name: "task", path: "/api/v1/BTC-LIGHTNING/task/task-1", auth: "basic", want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.auth == "api" {
				req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
			} else {
				setAdminPassword(t, store, cfg, "admin-password")
				req.SetBasicAuth("admin", "admin-password")
			}
			res := httptest.NewRecorder()
			handler.Routes().ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("unexpected status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestDecryptionKeyAndMetricsCompatEndpoints(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	if err := store.EnsureWallet(t.Context(), "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	handler := newTestHTTPHandler(t, store, cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/decryption-key", strings.NewReader("key=unused"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("decryption no-hash status=%d body=%s", res.Code, res.Body.String())
	}

	hash, err := NewAuthManager(cfg, store, nil).HashPassword("wallet-key")
	if err != nil {
		t.Fatalf("hash wallet key: %v", err)
	}
	if err := store.UpsertSetting(t.Context(), "WalletEncryptionPasswordHash", string(hash)); err != nil {
		t.Fatalf("save wallet hash: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/decryption-key", strings.NewReader(`{"key":"wallet-key"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("decryption valid status=%d body=%s", res.Code, res.Body.String())
	}
	runtimeStatus, err := store.Setting(t.Context(), "WalletEncryptionRuntimeStatus")
	if err != nil || runtimeStatus != "3" {
		t.Fatalf("unexpected runtime status=%s err=%v", runtimeStatus, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("shkeeper", "shkeeper")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "go_shkeeper_wallets_total") {
		t.Fatalf("metrics status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestHealthAndReadyEndpoints(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	handler := newTestHTTPHandler(t, store, cfg)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"ready"`) {
		t.Fatalf("readyz status=%d body=%s", res.Code, res.Body.String())
	}
}

func newTestHTTPHandler(t *testing.T, store *Store, cfg Config) *HTTPHandler {
	t.Helper()
	logger := testLogger()
	return NewHTTPHandler(cfg, store, NewCryptoRegistry(cfg, store, logger), NewRateService(cfg, store, logger), NewAuthManager(cfg, store, logger), logger)
}

func setAdminPassword(t *testing.T, store *Store, cfg Config, password string) {
	t.Helper()
	auth := NewAuthManager(cfg, store, testLogger())
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	user, err := store.UserByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if user.Passhash.Valid && user.Passhash.String != "" {
		return
	}
	if err := store.SetInitialPassword(t.Context(), hash); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
}

func TestDecimalFromBaseUnitsCompat(t *testing.T) {
	if got := decimalFromBaseUnits(decimal.NewFromInt(123456789), 8); !got.Equal(decimal.RequireFromString("1.23456789")) {
		t.Fatalf("unexpected decimal from base units: %s", got)
	}
}
