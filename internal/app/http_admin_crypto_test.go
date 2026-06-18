package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestAdminRatesBackfillsEnabledWalletRates(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"USDT", "BNB"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "USDT", "test-api-key"); err != nil {
		t.Fatalf("ensure USDT wallet: %v", err)
	}
	if err := store.EnsureWallet(ctx, "BNB", "test-api-key"); err != nil {
		t.Fatalf("ensure BNB wallet: %v", err)
	}
	if err := store.EnsureWallet(ctx, "-", "test-api-key"); err != nil {
		t.Fatalf("ensure placeholder wallet: %v", err)
	}
	if err := store.SetWalletEnabled(ctx, "BNB", false); err != nil {
		t.Fatalf("disable BNB wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/rates?fiat=USD", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("rates status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `"crypto":"USDT"`) {
		t.Fatalf("rates did not include enabled USDT wallet: %s", body)
	}
	if strings.Contains(body, `"crypto":"BNB"`) {
		t.Fatalf("rates should not include disabled BNB wallet: %s", body)
	}
	if strings.Contains(body, `"crypto":"-"`) {
		t.Fatalf("rates should not include placeholder wallet: %s", body)
	}
	if _, err := store.ExchangeRate(ctx, "USD", "USDT"); err != nil {
		t.Fatalf("enabled wallet rate was not created: %v", err)
	}
}

func TestAdminWalletsDoesNotLoadBalancesByDefault(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BNB"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BNB", "test-api-key"); err != nil {
		t.Fatalf("ensure BNB wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	var balanceSeen atomic.Bool
	var spendableSeen atomic.Bool
	var statusSeen atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BNB/status":
			statusSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
		case "/BNB/spendable":
			spendableSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":         "success",
				"balance":        "9",
				"cache_ready":    true,
				"balance_source": "evm_accounts_spendable",
			})
		case "/BNB/balance":
			balanceSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"balance": "9"})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BNB_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/wallets", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("wallets status=%d body=%s", res.Code, res.Body.String())
	}
	if balanceSeen.Load() {
		t.Fatalf("admin wallet list should not load live balances by default")
	}
	if statusSeen.Load() {
		t.Fatalf("admin wallet list should not load live worker status by default")
	}
	if !spendableSeen.Load() {
		t.Fatalf("admin wallet list should load cached spendable balance")
	}
	body := res.Body.String()
	for _, want := range []string{`"balance":"9"`, `"balance_source":"evm_accounts_spendable"`, `"cache_ready":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("wallet list missing %s: %s", want, body)
		}
	}
}

func TestAdminWalletDetailUsesCachedBalanceByDefault(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BNB-USDT"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BNB-USDT", "test-api-key"); err != nil {
		t.Fatalf("ensure BNB-USDT wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	var balanceSeen atomic.Bool
	var spendableSeen atomic.Bool
	var statusSeen atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BNB-USDT/status":
			statusSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
		case "/BNB-USDT/spendable":
			spendableSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":             "success",
				"balance":            "7.61",
				"max_single_account": "7.61",
				"cache_ready":        true,
				"balance_source":     "evm_accounts_spendable",
			})
		case "/BNB-USDT/balance":
			balanceSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"balance": "7.61"})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BNB_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/wallets/BNB-USDT", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("wallet detail status=%d body=%s", res.Code, res.Body.String())
	}
	if balanceSeen.Load() {
		t.Fatalf("admin wallet detail should not load live balances by default")
	}
	if statusSeen.Load() {
		t.Fatalf("admin wallet detail should not load live worker status by default")
	}
	if !spendableSeen.Load() {
		t.Fatalf("admin wallet detail should load cached spendable balance")
	}
	body := res.Body.String()
	for _, want := range []string{`"balance":"7.61"`, `"max_single_account":"7.61"`, `"balance_source":"evm_accounts_spendable"`, `"cache_ready":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("wallet detail missing %s: %s", want, body)
		}
	}
}

func TestAdminPayoutQuoteReturnsDisabledStatusForDisabledCrypto(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BNB"}
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/payout-quote?crypto=USDT&amount=10", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("quote status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `"status":"disabled"`) || !strings.Contains(body, `"balance_source":"disabled"`) {
		t.Fatalf("disabled quote should return structured disabled payload: %s", body)
	}
}

func TestAdminPayoutQuoteCopiesTRONCacheMetadata(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"USDT"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "USDT", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/USDT/spendable":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":             "success",
				"balance":            "10",
				"max_single_account": "5",
				"cache_ready":        true,
				"cache_stale":        false,
				"balance_source":     "wallet_cache",
				"refreshed_at":       "2026-06-06T00:00:00Z",
			})
		case "/USDT/calc-tx-fee/10":
			_ = json.NewEncoder(w).Encode(map[string]any{"fee": "100", "fee_sun": 100000000})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("TRON_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/payout-quote?crypto=USDT&amount=10", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("quote status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`"balance":"10"`,
		`"max_single_account":"5"`,
		`"cache_ready":true`,
		`"balance_source":"wallet_cache"`,
		`"fee":"100"`,
		`"fee_asset":"TRX"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("quote missing %s: %s", want, body)
		}
	}
}

func TestAdminPayoutQuoteSkipsUnsupportedBackendFeeEstimate(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BNB"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BNB", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BNB/spendable":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":                 "success",
				"balance":                "1",
				"spendable":              "0.99999",
				"max_single_account":     "0.99999",
				"required_native_per_tx": "0.00001",
				"fee_asset":              "BNB",
				"balance_source":         "evm_accounts_spendable",
			})
		case "/BNB/calc-tx-fee/0.001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":                 "success",
				"fee":                    "0.0000021",
				"required_native_per_tx": "0.0000021",
				"fee_asset":              "BNB",
				"gas_limit":              21000,
				"estimated_gas":          true,
			})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BNB_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/payout-quote?crypto=BNB&amount=0.001", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("quote status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{`"balance":"1"`, `"spendable":"0.99999"`, `"fee":"0.0000021"`, `"fee_asset":"BNB"`, `"gas_limit":21000`} {
		if !strings.Contains(body, want) {
			t.Fatalf("quote missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "fee_error") {
		t.Fatalf("unsupported fee estimate should not become fee_error: %s", body)
	}
}

func TestAdminPayoutQuoteUsesSplitSpendableForEVMToken(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BNB-USDT"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BNB-USDT", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BNB-USDT/spendable":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":                 "success",
				"balance":                "2.04",
				"spendable":              "2.04",
				"max_single_account":     "1.02",
				"direct_spendable":       "1.02",
				"required_native_per_tx": "0.00001",
				"fee_asset":              "BNB",
				"can_split_payout":       true,
				"auto_gas_topup":         true,
				"gas_topup_target":       "0.000012",
				"gas_topup_amount":       "0.000012",
				"gas_topup_transfer_fee": "0.0000021",
				"gas_funding_balance":    "0.001",
				"balance_source":         "evm_accounts_spendable",
			})
		case "/BNB-USDT/calc-tx-fee/2.04":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":                        "success",
				"fee":                           "0.0000034504",
				"required_native_per_tx":        "0.0000034504",
				"fee_asset":                     "BNB",
				"gas_limit":                     34504,
				"estimated_gas":                 true,
				"gas_topup_target":              "0.0000034504",
				"gas_topup_amount":              "0.0000034504",
				"gas_topup_transfer_fee":        "0.0000021",
				"gas_topup_transfer_fee_per_tx": "0.0000021",
			})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BNB_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/payout-quote?crypto=BNB-USDT&amount=2.04", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("quote status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`"balance":"2.04"`,
		`"max_single_account":"2.04"`,
		`"single_account_max":"1.02"`,
		`"can_split_payout":true`,
		`"auto_gas_topup":true`,
		`"direct_spendable":"1.02"`,
		`"gas_topup_target":"0.0000034504"`,
		`"gas_topup_amount":"0.0000034504"`,
		`"gas_topup_transfer_fee":"0.0000021"`,
		`"gas_funding_balance":"0.001"`,
		`"fee":"0.0000034504"`,
		`"fee_asset":"BNB"`,
		`"gas_limit":34504`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("quote missing %s: %s", want, body)
		}
	}
}

func TestAdminRatesPostBackfillsMissingRateRows(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"USDT"}
	ctx := t.Context()
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/admin/rates", map[string]any{
		"fiat": "USD",
		"rates": []map[string]string{{
			"crypto":     "USDT",
			"source":     "manual",
			"rate":       "1.5",
			"fee":        "3",
			"fixed_fee":  "0.25",
			"fee_policy": "FIXED_FEE",
		}},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("rates post status=%d body=%s", res.Code, res.Body.String())
	}
	rate, err := store.ExchangeRate(ctx, "USD", "USDT")
	if err != nil {
		t.Fatalf("missing rate row was not created: %v", err)
	}
	if rate.Source != "manual" || !rate.Rate.Equal(decimal.RequireFromString("1.5")) || rate.FeePolicy != "FIXED_FEE" {
		t.Fatalf("unexpected rate settings: %+v", rate)
	}
}

func TestAdminCryptosSavesDesiredWalletSet(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure BTC wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodPost, "/api/v1/admin/cryptos", map[string]any{
		"cryptos": []string{"BNB-USDT", "BNB"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("cryptos status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `"command":"deploy/shkeeperctl.sh set-cryptos BNB,BNB-USDT"`) {
		t.Fatalf("response did not include manager command: %s", body)
	}
	if !strings.Contains(body, `"short_command":"shkeeperctl set-cryptos BNB,BNB-USDT"`) {
		t.Fatalf("response did not include short manager command: %s", body)
	}
	for _, crypto := range []string{"BNB", "BNB-USDT"} {
		wallet, err := store.WalletByCrypto(ctx, crypto)
		if err != nil || !wallet.Enabled {
			t.Fatalf("%s wallet not enabled: wallet=%+v err=%v", crypto, wallet, err)
		}
	}
	btc, err := store.WalletByCrypto(ctx, "BTC")
	if err != nil || btc.Enabled {
		t.Fatalf("BTC wallet should be disabled: wallet=%+v err=%v", btc, err)
	}
	desired, err := store.Setting(ctx, desiredCryptosSettingName)
	if err != nil || desired != "BNB,BNB-USDT" {
		t.Fatalf("desired cryptos not persisted: desired=%q err=%v", desired, err)
	}
}

func TestAdminWalletImportAcceptsLegacyAddressMap(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	if _, err := store.DB().Exec("DROP TABLE IF EXISTS chain_account"); err != nil {
		t.Fatalf("drop chain_account: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	legacyAddress := "0x0000000000000000000000000000000000000abc"
	legacyJSON := `{"0x0000000000000000000000000000000000000abc":{"public_address":"0x0000000000000000000000000000000000000abc","secret":"plain-private-key"}}`
	res := adminJSON(t, handler, http.MethodPost, "/api/v1/admin/wallet-import", map[string]any{
		"module":           "BNB",
		"default_crypto":   "BNB-USDT",
		"account_password": "0123456789abcdef0123456789abcdef",
		"json":             legacyJSON,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"rows":1`) {
		t.Fatalf("import did not report one row: %s", res.Body.String())
	}
	var module, crypto, address, secret string
	err := store.DB().QueryRow("SELECT module, crypto, address, private_key_hex FROM chain_account WHERE address = ?", legacyAddress).Scan(&module, &crypto, &address, &secret)
	if err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("imported account was not written")
		}
		t.Fatalf("query chain_account: %v", err)
	}
	if module != "BNB" || crypto != "BNB-USDT" || address != legacyAddress || !strings.HasPrefix(secret, "v1:") {
		t.Fatalf("unexpected imported account module=%s crypto=%s address=%s secret=%s", module, crypto, address, secret)
	}
	if _, err := store.WalletByCrypto(t.Context(), "BNB-USDT"); err != nil {
		t.Fatalf("import did not ensure wallet: %v", err)
	}
}

func TestAdminWalletImportNormalizesPastedLegacyJSON(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	if _, err := store.DB().Exec("DROP TABLE IF EXISTS chain_account"); err != nil {
		t.Fatalf("drop chain_account: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	legacyJSON := `如
` + "```json" + `
{
  “0xb986F8EdE0073822d63f7610057c447581a25F08”： {
    “public_address”： “0xb986F8EdE0073822d63f7610057c447581a25F08”，
    “secret”： “a85f20f23969563a5a141d4efa389ac760739edc0cd5ad789225cffa64dcc7e1”
  }
}
` + "```" + `
`
	res := adminJSON(t, handler, http.MethodPost, "/api/v1/admin/wallet-import", map[string]any{
		"module":           "BNB",
		"default_crypto":   "BNB-USDT",
		"account_password": "0123456789abcdef0123456789abcdef",
		"json":             legacyJSON,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"rows":1`) {
		t.Fatalf("import did not report one row: %s", res.Body.String())
	}
	var module, crypto, address, secret string
	err := store.DB().QueryRow("SELECT module, crypto, address, private_key_hex FROM chain_account WHERE address = ?", "0xb986F8EdE0073822d63f7610057c447581a25F08").Scan(&module, &crypto, &address, &secret)
	if err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("normalized pasted legacy wallet was not written")
		}
		t.Fatalf("query chain_account: %v", err)
	}
	if module != "BNB" || crypto != "BNB-USDT" || address != "0xb986F8EdE0073822d63f7610057c447581a25F08" || !strings.HasPrefix(secret, "v1:") {
		t.Fatalf("unexpected imported account module=%s crypto=%s address=%s secret=%s", module, crypto, address, secret)
	}
}

func TestAdminWalletImportAcceptsSingleLegacyBNBWalletObject(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	if _, err := store.DB().Exec("DROP TABLE IF EXISTS chain_account"); err != nil {
		t.Fatalf("drop chain_account: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)

	legacyAddress := "0x0000000000000000000000000000000000000002"
	legacyJSON := `{"public_address":"0x0000000000000000000000000000000000000002","secret":"plain-private-key"}`
	res := adminJSON(t, handler, http.MethodPost, "/api/v1/admin/wallet-import", map[string]any{
		"module":           "BNB",
		"default_crypto":   "BNB-USDT",
		"account_password": "0123456789abcdef0123456789abcdef",
		"json":             legacyJSON,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"rows":1`) {
		t.Fatalf("import did not report one row: %s", res.Body.String())
	}
	var module, crypto, address, secret string
	err := store.DB().QueryRow("SELECT module, crypto, address, private_key_hex FROM chain_account WHERE address = ?", legacyAddress).Scan(&module, &crypto, &address, &secret)
	if err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("single legacy wallet object was not written")
		}
		t.Fatalf("query chain_account: %v", err)
	}
	if module != "BNB" || crypto != "BNB-USDT" || address != legacyAddress || !strings.HasPrefix(secret, "v1:") {
		t.Fatalf("unexpected imported account module=%s crypto=%s address=%s secret=%s", module, crypto, address, secret)
	}
}

func TestAdminWalletDetailChecksTRONStatusBeforeBalance(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"USDT"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "USDT", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	setAdminPassword(t, store, cfg, "admin-password")

	var balanceSeen atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/USDT/status":
			if balanceSeen.Load() {
				writeJSON(w, http.StatusBadGateway, map[string]any{"status": "error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"last_block_timestamp": time.Now().Unix()})
		case "/USDT/balance":
			balanceSeen.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"balance": "0"})
		case "/USDT/activation-status":
			_ = json.NewEncoder(w).Encode(map[string]any{"required": true, "total": 0, "checked": 0, "inactive": 0})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("TRON_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))
	handler := newTestHTTPHandler(t, store, cfg)

	res := adminJSON(t, handler, http.MethodGet, "/api/v1/admin/wallets/USDT", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("wallet detail status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, `"status":"Synced"`) {
		t.Fatalf("wallet detail did not check status before balance: %s", body)
	}
	if !strings.Contains(body, `"activation"`) {
		t.Fatalf("wallet detail did not include activation status: %s", body)
	}
}
