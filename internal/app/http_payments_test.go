package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestTxIDsFromPayoutResult(t *testing.T) {
	got := txIDsFromAny(map[string]any{
		"dest":  "addr1",
		"txids": []any{"tx1", "tx2"},
	})
	if len(got) != 2 || got[0] != "tx1" || got[1] != "tx2" {
		t.Fatalf("unexpected txids: %+v", got)
	}
	queues := payoutTxIDsByDestination([]any{
		map[string]any{"dest": "addr1", "txids": []any{"tx1"}},
		map[string]any{"dest": "addr1", "txids": []any{"tx2"}},
	})
	if first := popPayoutTxIDs(queues, "addr1"); len(first) != 1 || first[0] != "tx1" {
		t.Fatalf("unexpected first txids: %+v", first)
	}
	if second := popPayoutTxIDs(queues, "addr1"); len(second) != 1 || second[0] != "tx2" {
		t.Fatalf("unexpected second txids: %+v", second)
	}
	got = txIDsFromAny(map[string]any{"result": map[string]any{"txid": "single-tx"}})
	if len(got) != 1 || got[0] != "single-tx" {
		t.Fatalf("single txid result was not parsed: %+v", got)
	}
}

func TestPayoutPersistsBeforeWorkerSuccess(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/BTC/payout/bc1success/1.25" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"task_id": "task-success",
			"result":  map[string]any{"txids": []string{"tx-success"}},
		})
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payout", map[string]any{
		"destination":  "bc1success",
		"amount":       "1.25",
		"external_id":  "payout-success",
		"callback_url": "https://merchant.test/payout",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("payout status=%d body=%s", res.Code, res.Body.String())
	}
	payout, err := store.PayoutByExternalID(ctx, "BTC", "payout-success")
	if err != nil {
		t.Fatalf("load payout: %v", err)
	}
	if payout.Status != PayoutInProgress || nullStringValue(payout.TaskID) != "task-success" || len(payout.Transactions) != 1 || payout.Transactions[0].TxID != "tx-success" {
		t.Fatalf("payout was not persisted with worker result: %+v", payout)
	}
	if payout.DestAddr != "bc1success" || !payout.Amount.Equal(decimal.RequireFromString("1.25")) || nullStringValue(payout.CallbackURL) != "https://merchant.test/payout" {
		t.Fatalf("payout request fields were not persisted: %+v", payout)
	}
}

func TestPayoutPersistsBitcoinLikeWorkerSuccess(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"LTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "LTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/LTC/payout/ltc-success/3.5/2" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "SUCCESS",
			"task_id": "ltc-task-success",
			"result":  map[string]any{"dest": "ltc-success", "txids": []string{"ltc-tx-success"}},
		})
	}))
	defer backend.Close()
	t.Setenv("LTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/LTC/payout", map[string]any{
		"destination": "ltc-success",
		"amount":      "3.5",
		"fee":         "2",
		"external_id": "ltc-payout-success",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("payout status=%d body=%s", res.Code, res.Body.String())
	}
	payout, err := store.PayoutByExternalID(ctx, "LTC", "ltc-payout-success")
	if err != nil {
		t.Fatalf("load payout: %v", err)
	}
	if payout.Status != PayoutInProgress || nullStringValue(payout.TaskID) != "ltc-task-success" || len(payout.Transactions) != 1 || payout.Transactions[0].TxID != "ltc-tx-success" {
		t.Fatalf("LTC payout was not persisted with worker result: %+v", payout)
	}
}

func TestPayoutWorkerFailureMarksMariaDBRowFail(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/BTC/payout/bc1fail/2.5" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		http.Error(w, "worker rejected payout", http.StatusBadGateway)
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payout", map[string]any{
		"destination": "bc1fail",
		"amount":      "2.5",
		"external_id": "payout-fail",
	})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("payout status=%d body=%s", res.Code, res.Body.String())
	}
	payout, err := store.PayoutByExternalID(ctx, "BTC", "payout-fail")
	if err != nil {
		t.Fatalf("load failed payout: %v", err)
	}
	if payout.Status != PayoutFail || nullStringValue(payout.Success) != "No" || !strings.Contains(nullStringValue(payout.Error), "worker rejected payout") {
		t.Fatalf("worker failure was not persisted as FAIL: %+v", payout)
	}
}

func TestPayoutInvalidCallbackDoesNotCallWorkerOrCreateRow(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	if err := store.EnsureWallet(t.Context(), "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	called := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payout", map[string]any{
		"destination":  "bc1badcallback",
		"amount":       "1",
		"external_id":  "payout-bad-callback",
		"callback_url": "ftp://merchant.test/payout",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("payout status=%d body=%s", res.Code, res.Body.String())
	}
	if called {
		t.Fatalf("worker was called for invalid callback_url")
	}
	if _, err := store.PayoutByExternalID(t.Context(), "BTC", "payout-bad-callback"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid callback payout row should not exist: %v", err)
	}
}

func TestPayoutDisabledWalletDoesNotCallWorkerOrCreateRow(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	if err := store.SetWalletEnabled(ctx, "BTC", false); err != nil {
		t.Fatalf("disable wallet: %v", err)
	}
	called := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/payout", map[string]any{
		"destination": "bc1disabled",
		"amount":      "1",
		"external_id": "payout-disabled-wallet",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("payout status=%d body=%s", res.Code, res.Body.String())
	}
	if called {
		t.Fatalf("worker was called for disabled wallet")
	}
	if _, err := store.PayoutByExternalID(ctx, "BTC", "payout-disabled-wallet"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled wallet payout row should not exist: %v", err)
	}
}

func TestMultiPayoutDuplicateExternalIDDoesNotCallWorkerOrCreateRows(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	if err := store.EnsureWallet(t.Context(), "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	called := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/BTC/multipayout", []map[string]any{
		{"destination": "bc1dup1", "amount": "1", "external_id": "payout-dup"},
		{"destination": "bc1dup2", "amount": "2", "external_id": "payout-dup"},
	})
	if res.Code != http.StatusConflict {
		t.Fatalf("multipayout status=%d body=%s", res.Code, res.Body.String())
	}
	if called {
		t.Fatalf("worker was called for duplicate external_id list")
	}
	if _, err := store.PayoutByExternalID(t.Context(), "BTC", "payout-dup"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate multipayout rows should not exist: %v", err)
	}
}

func TestWalletNotifyRecordsOutgoingTransaction(t *testing.T) {
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
	if _, err := store.DB().Exec("UPDATE " + store.table("exchange_rate") + " SET source = 'manual', rate = 100 WHERE crypto = 'BTC' AND fiat = 'USD'"); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/BTC/transaction/outgoing-tx" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([][]any{{"bc1outgoing", "0.12", 6, "send"}})
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	t.Setenv("SHKEEPER_BTC_BACKEND_KEY", "backend-key")

	handler := newTestHTTPHandler(t, store, cfg)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/walletnotify/BTC/outgoing-tx", nil)
		req.Header.Set("X-Shkeeper-Backend-Key", "backend-key")
		res := httptest.NewRecorder()
		handler.Routes().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("walletnotify status=%d body=%s", res.Code, res.Body.String())
		}
	}

	order, err := store.GetOrder(ctx, "outgoing:outgoing-tx")
	if err != nil {
		t.Fatalf("load outgoing order: %v", err)
	}
	if len(order.Invoices) != 1 || order.Invoices[0].Invoice.Status != InvoiceOutgoing || len(order.Invoices[0].Transactions) != 1 {
		t.Fatalf("outgoing order was not recorded correctly: %+v", order)
	}
	tx := order.Invoices[0].Transactions[0]
	if tx.NeedMoreConfirmations || !tx.CallbackConfirmed || !tx.AmountFiat.Equal(decimal.RequireFromString("12")) {
		t.Fatalf("unexpected outgoing transaction: %+v", tx)
	}
}

func TestValidBackendKeyFallsBackToGlobalKey(t *testing.T) {
	t.Setenv("SHKEEPER_BNB_USDT_BACKEND_KEY", "")
	t.Setenv("SHKEEPER_BTC_BACKEND_KEY", "")
	t.Setenv("SHKEEPER_BACKEND_KEY", "global-backend")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/walletnotify/BNB-USDT/tx", nil)
	req.Header.Set("X-Shkeeper-Backend-Key", "global-backend")
	handler := &HTTPHandler{}
	if !handler.validBackendKey(req, &CryptoModule{Name: "BNB-USDT"}) {
		t.Fatalf("global backend key should be accepted when module-specific key is unset")
	}
}
