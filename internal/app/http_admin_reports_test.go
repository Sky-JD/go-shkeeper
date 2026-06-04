package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestAdminReportRoutesUseMariaDB(t *testing.T) {
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
		Addr:          "bc1report",
		ExternalID:    "report-order",
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
		t.Fatalf("add address: %v", err)
	}
	tx := Transaction{InvoiceID: invoice.ID, TxID: "report-tx", Crypto: "BTC", AmountCrypto: decimal.RequireFromString("0.0002"), AmountFiat: decimal.NewFromInt(10)}
	if err := store.AddTransaction(ctx, &tx); err != nil {
		t.Fatalf("add tx: %v", err)
	}
	payout := Payout{Amount: decimal.NewFromInt(3), Crypto: "BTC", DestAddr: "bc1dest-report", ExternalID: sqlNullString("report-payout"), TaskID: sqlNullString("report-task"), Status: PayoutInProgress}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if err := store.AddPayoutTx(ctx, payout.ID, "report-payout-tx"); err != nil {
		t.Fatalf("add payout tx: %v", err)
	}

	handler := newTestHTTPHandler(t, store, cfg)
	user, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}

	checkAdminRoute(t, handler, user, http.MethodGet, "/BTC/get-rate?source=manual", "", `"BTC":"50000"`)
	checkAdminRoute(t, handler, user, http.MethodGet, "/wallet/BTC", "", "Manage BTC")
	checkAdminRoute(t, handler, user, http.MethodGet, "/payout/BTC", "", "Create payout")
	checkAdminRoute(t, handler, user, http.MethodGet, "/rates", "", "BTC")
	checkAdminRoute(t, handler, user, http.MethodGet, "/transactions", "", "report-tx")
	checkAdminRoute(t, handler, user, http.MethodGet, "/parts/transactions?txid=report-tx", "", "bc1report")
	checkAdminRoute(t, handler, user, http.MethodGet, "/payouts", "", "report-payout-tx")
	checkAdminRoute(t, handler, user, http.MethodGet, "/parts/payouts?download=csv", "", "report-payout-tx")

	form := url.Values{}
	form.Set("rates__BTC__source", "manual")
	form.Set("rates__BTC__rate", "51000")
	form.Set("rates__BTC__fee", "1")
	form.Set("rates__BTC__fixed_fee", "0.5")
	form.Set("rates__BTC__fee_policy", "PERCENT_OR_MINIMAL_FIXED_FEE")
	req := httptest.NewRequest(http.MethodPost, "/rates/USD", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusFound {
		t.Fatalf("rates post status=%d body=%s", res.Code, res.Body.String())
	}
	rate, err := store.ExchangeRate(ctx, "USD", "BTC")
	if err != nil || !rate.Rate.Equal(decimal.NewFromInt(51000)) || rate.FeePolicy != "PERCENT_OR_MINIMAL_FIXED_FEE" {
		t.Fatalf("rate update not persisted: %+v err=%v", rate, err)
	}

	localeForm := url.Values{"locale": []string{"zh-CN"}}
	req = httptest.NewRequest(http.MethodPost, "/settings/locale", strings.NewReader(localeForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusFound || len(res.Result().Cookies()) == 0 || res.Result().Cookies()[0].Value != "zh_CN" {
		t.Fatalf("locale post status=%d cookies=%+v", res.Code, res.Result().Cookies())
	}

	if err := store.UpsertSetting(ctx, "WalletEncryptionPersistentStatus", "1"); err != nil {
		t.Fatalf("set wallet encryption pending: %v", err)
	}
	checkAdminRoute(t, handler, user, http.MethodGet, "/unlock", "", "Wallet encryption")
	unlockForm := url.Values{"encryption": []string{"1"}, "key": []string{"secret-key"}, "key2": []string{"secret-key"}, "confirmation": []string{"1"}}
	req = httptest.NewRequest(http.MethodPost, "/unlock", strings.NewReader(unlockForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusFound {
		t.Fatalf("unlock post status=%d body=%s", res.Code, res.Body.String())
	}
	if status, err := store.Setting(ctx, "WalletEncryptionPersistentStatus"); err != nil || status != "3" {
		t.Fatalf("wallet encryption status=%q err=%v", status, err)
	}
}

func checkAdminRoute(t *testing.T, handler *HTTPHandler, user User, method, path, body, want string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), want) {
		t.Fatalf("%s %s status=%d want=%q body=%s", method, path, res.Code, want, res.Body.String())
	}
}
