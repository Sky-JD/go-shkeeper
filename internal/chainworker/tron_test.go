package chainworker

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestTRONAddressRoundTrip(t *testing.T) {
	address, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new tron account: %v", err)
	}
	payload, err := tronAddressBytes(address)
	if err != nil {
		t.Fatalf("decode tron address: %v", err)
	}
	if len(payload) != 21 || payload[0] != 0x41 {
		t.Fatalf("unexpected tron payload: %x", payload)
	}
	if encoded := base58Check(payload); encoded != address {
		t.Fatalf("round trip mismatch: %s != %s", encoded, address)
	}
	word, err := tronABIAddressParam(address)
	if err != nil {
		t.Fatalf("address param: %v", err)
	}
	if len(word) != 64 {
		t.Fatalf("unexpected ABI word length: %d", len(word))
	}
	if !strings.HasSuffix(word, tronAddressHexMust(t, address)[2:]) {
		t.Fatalf("ABI word should end with the 20-byte address: %s", word)
	}
}

func TestTRONTokenConfigEnvOverride(t *testing.T) {
	t.Setenv("TRON_USDT_CONTRACT", "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8")
	t.Setenv("TRON_USDT_DECIMALS", "8")
	contract, decimals, err := tronTokenConfig("USDT")
	if err != nil {
		t.Fatalf("tron token config: %v", err)
	}
	if contract != "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8" || decimals != 8 {
		t.Fatalf("unexpected tron token config: %s %d", contract, decimals)
	}
}

func TestTRONBalanceRetriesRateLimit(t *testing.T) {
	t.Setenv("TRON_USDT_CONTRACT", "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8")
	t.Setenv("TRON_RATE_LIMIT_RETRY_MS", "1")
	address, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new tron account: %v", err)
	}
	var calls atomic.Int32
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/triggerconstantcontract" {
			t.Fatalf("unexpected fullnode path: %s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"Error": "request rate exceeded the allowed_rps(3), suspended for 1 s"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"constant_result": []string{strings.Repeat("0", 58) + "989680"},
			"result":          map[string]any{"result": true},
		})
	}))
	defer fullnode.Close()

	server := NewServer(Config{
		Module:         "TRON",
		FullnodeURL:    fullnode.URL,
		FullnodeURLs:   []string{fullnode.URL},
		RequestTimeout: 2 * time.Second,
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	balance, err := server.tronBalance(t.Context(), "USDT", address)
	if err != nil {
		t.Fatalf("tron balance: %v", err)
	}
	if !balance.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("unexpected balance %s", balance)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one retry, got %d calls", calls.Load())
	}
}

func TestTRONSpendableReportsTotalAndMaxAccount(t *testing.T) {
	t.Setenv("TRON_USDT_CONTRACT", "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8")
	t.Setenv("TRON_BALANCE_QUERY_INTERVAL_MS", "0")
	store := testStore(t)
	defer store.Close()
	firstAddress, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new first tron account: %v", err)
	}
	secondAddress, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new second tron account: %v", err)
	}
	ctx := t.Context()
	for _, address := range []string{firstAddress, secondAddress} {
		if err := store.AddAccount(ctx, &Account{Module: "TRON", Crypto: "USDT", Address: address, PrivateKeyHex: "v1:test"}); err != nil {
			t.Fatalf("add account %s: %v", address, err)
		}
	}
	balances := map[string]string{
		tronABIAddressParamMust(t, firstAddress):  strings.Repeat("0", 58) + "4c4b40",
		tronABIAddressParamMust(t, secondAddress): strings.Repeat("0", 58) + "989680",
	}
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/triggerconstantcontract" {
			t.Fatalf("unexpected fullnode path: %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode trigger request: %v", err)
		}
		parameter, _ := req["parameter"].(string)
		value := balances[parameter]
		if value == "" {
			value = strings.Repeat("0", 64)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"constant_result": []string{value},
			"result":          map[string]any{"result": true},
		})
	}))
	defer fullnode.Close()

	server := NewServer(Config{
		Module:         "TRON",
		FullnodeURL:    fullnode.URL,
		FullnodeURLs:   []string{fullnode.URL},
		RequestTimeout: 2 * time.Second,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report, err := server.tronSpendable(ctx, "USDT")
	if err != nil {
		t.Fatalf("tron spendable: %v", err)
	}
	if !report.Total.Equal(decimal.NewFromInt(15)) || !report.Max.Equal(decimal.NewFromInt(10)) || report.MaxAddress != secondAddress {
		t.Fatalf("unexpected report total=%s max=%s max_address=%s", report.Total, report.Max, report.MaxAddress)
	}
}

func TestTRONSpendableHTTPUsesCachedReport(t *testing.T) {
	t.Setenv("TRON_USDT_CONTRACT", "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8")
	t.Setenv("TRON_BALANCE_QUERY_INTERVAL_MS", "0")
	store := testStore(t)
	defer store.Close()
	firstAddress, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new first tron account: %v", err)
	}
	secondAddress, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new second tron account: %v", err)
	}
	ctx := t.Context()
	for _, address := range []string{firstAddress, secondAddress} {
		if err := store.AddAccount(ctx, &Account{Module: "TRON", Crypto: "USDT", Address: address, PrivateKeyHex: "v1:test"}); err != nil {
			t.Fatalf("add account %s: %v", address, err)
		}
	}
	balances := map[string]string{
		tronABIAddressParamMust(t, firstAddress):  strings.Repeat("0", 58) + "4c4b40",
		tronABIAddressParamMust(t, secondAddress): strings.Repeat("0", 58) + "989680",
	}
	var calls atomic.Int32
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode trigger request: %v", err)
		}
		parameter, _ := req["parameter"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"constant_result": []string{balances[parameter]},
			"result":          map[string]any{"result": true},
		})
	}))
	defer fullnode.Close()

	server := NewServer(Config{
		Module:         "TRON",
		FullnodeURL:    fullnode.URL,
		FullnodeURLs:   []string{fullnode.URL},
		Username:       "tron",
		Password:       "secret",
		RequestTimeout: 2 * time.Second,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := server.refreshTRONSpendable(ctx, "USDT"); err != nil {
		t.Fatalf("refresh spendable: %v", err)
	}
	initialCalls := calls.Load()
	handler := server.Routes()
	for _, path := range []string{"/USDT/balance", "/USDT/spendable"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.SetBasicAuth("tron", "secret")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body.String())
		}
		body := res.Body.String()
		if !strings.Contains(body, `"balance":"15"`) || !strings.Contains(body, `"cache_ready":true`) {
			t.Fatalf("%s returned unexpected cached body: %s", path, body)
		}
	}
	if _, err := server.broadcastTRONPayout(ctx, "USDT", firstAddress, decimal.NewFromInt(20)); err == nil || !strings.Contains(err.Error(), "no USDT account has enough balance") {
		t.Fatalf("expected cached insufficient-balance payout error, got %v", err)
	}
	if calls.Load() != initialCalls {
		t.Fatalf("cached HTTP requests should not call fullnode again: before=%d after=%d", initialCalls, calls.Load())
	}
}

func TestTRONBalanceRefreshCryptosHonorsDisabledTRONSelection(t *testing.T) {
	t.Setenv("SHKEEPER_CRYPTOS", "BNB,BNB-USDT")
	t.Setenv("TRON_BALANCE_REFRESH_CRYPTOS", "")
	if got := tronBalanceRefreshCryptos(); len(got) != 0 {
		t.Fatalf("TRON refresh list should be empty when SHKEEPER_CRYPTOS excludes TRON, got %v", got)
	}
}

func TestSignTRONTransaction(t *testing.T) {
	_, privateKeyHex, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new tron account: %v", err)
	}
	tx := map[string]any{"txID": "unit-test", "raw_data_hex": "0a02abcd"}
	signed, err := signTRONTransaction(tx, privateKeyHex)
	if err != nil {
		t.Fatalf("sign tron transaction: %v", err)
	}
	signatures, ok := signed["signature"].([]string)
	if !ok || len(signatures) != 1 {
		t.Fatalf("missing signature: %#v", signed["signature"])
	}
	if len(signatures[0]) != 130 {
		t.Fatalf("unexpected signature length: %d", len(signatures[0]))
	}
	if _, ok := tx["signature"]; ok {
		t.Fatalf("signTRONTransaction should not mutate input transaction")
	}
}

func TestParsePayoutRequests(t *testing.T) {
	requests, err := parsePayoutRequests([]byte(`[{"destination":"addr1","amount":"1.5"},{"dest":"addr2","amount":2}]`))
	if err != nil {
		t.Fatalf("parse payout requests: %v", err)
	}
	if len(requests) != 2 || requests[0].Destination != "addr1" || !requests[0].Amount.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("unexpected requests: %+v", requests)
	}
}

func TestTRONTopicAddress(t *testing.T) {
	topic := "000000000000000000000000a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	if got := tronTopicAddressHex(topic); got != "41a614f803b6fd780986a42c78ec9c7f77e6ded13c" {
		t.Fatalf("unexpected tron topic address: %s", got)
	}
	key, err := tronContractLogKey("41a614f803b6fd780986a42c78ec9c7f77e6ded13c")
	if err != nil {
		t.Fatalf("tron contract log key: %v", err)
	}
	if key != "a614f803b6fd780986a42c78ec9c7f77e6ded13c" {
		t.Fatalf("unexpected contract key: %s", key)
	}
}

func TestTRONMultiserverEndpoints(t *testing.T) {
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getnowblock":
			writeJSON(w, http.StatusOK, map[string]any{
				"block_header": map[string]any{
					"raw_data": map[string]any{
						"number":    12345,
						"timestamp": timeNowMillis(),
					},
				},
			})
		case "/wallet/getnodeinfo":
			writeJSON(w, http.StatusOK, map[string]any{"configNodeInfo": map[string]any{"codeVersion": "unit-test"}})
		default:
			t.Fatalf("unexpected fullnode path: %s", r.URL.Path)
		}
	}))
	defer fullnode.Close()

	cfg := Config{
		Module:         "TRON",
		Username:       "worker",
		Password:       "secret",
		FullnodeURL:    fullnode.URL,
		FullnodeURLs:   []string{fullnode.URL},
		RequestTimeout: 2 * time.Second,
	}
	server := NewServer(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/TRX/multiserver/status", nil)
	req.SetBasicAuth("worker", "secret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"codeVersion":"unit-test"`) {
		t.Fatalf("status code=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/TRX/multiserver/change/0", nil)
	req.SetBasicAuth("worker", "secret")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || server.fullnodeURL() != fullnode.URL {
		t.Fatalf("change code=%d active=%s body=%s", res.Code, server.fullnodeURL(), res.Body.String())
	}
}

func TestTRONLatestBlockTimestampUsesRecentCacheAfterFullnodeError(t *testing.T) {
	var calls atomic.Int32
	wantMillis := timeNowMillis()
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/getnowblock" {
			t.Fatalf("unexpected fullnode path: %s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			writeJSON(w, http.StatusOK, map[string]any{
				"block_header": map[string]any{
					"raw_data": map[string]any{"timestamp": wantMillis},
				},
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "error"})
	}))
	defer fullnode.Close()

	server := NewServer(Config{
		Module:         "TRON",
		FullnodeURL:    fullnode.URL,
		FullnodeURLs:   []string{fullnode.URL},
		RequestTimeout: 2 * time.Second,
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	first, err := server.latestBlockTimestamp(t.Context())
	if err != nil {
		t.Fatalf("first latest block timestamp: %v", err)
	}
	second, err := server.latestBlockTimestamp(t.Context())
	if err != nil {
		t.Fatalf("cached latest block timestamp: %v", err)
	}
	if !first.Equal(second) || first.UnixMilli() != wantMillis {
		t.Fatalf("unexpected cached timestamp first=%s second=%s want_ms=%d", first, second, wantMillis)
	}
}

func TestTRONActivationStatusReportsInactiveAccounts(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	inactiveAddress, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new inactive tron account: %v", err)
	}
	activeAddress, _, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new active tron account: %v", err)
	}
	ctx := t.Context()
	for _, address := range []string{inactiveAddress, activeAddress} {
		if err := store.AddAccount(ctx, &Account{Module: "TRON", Crypto: "USDT", Address: address, PrivateKeyHex: "v1:test"}); err != nil {
			t.Fatalf("add account %s: %v", address, err)
		}
	}
	activeHex := tronAddressHexMust(t, activeAddress)
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/getaccount" {
			t.Fatalf("unexpected fullnode path: %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode getaccount request: %v", err)
		}
		if req["address"] == activeHex {
			writeJSON(w, http.StatusOK, map[string]any{"address": activeHex})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	defer fullnode.Close()

	server := NewServer(Config{
		Module:         "TRON",
		FullnodeURL:    fullnode.URL,
		RequestTimeout: 2 * time.Second,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report, err := server.tronActivationStatus(ctx, "USDT")
	if err != nil {
		t.Fatalf("activation status: %v", err)
	}
	if report["total"] != 2 || report["checked"] != 2 || report["active"] != 1 || report["inactive"] != 1 {
		t.Fatalf("unexpected activation report: %+v", report)
	}
	sample, ok := report["sample_inactive"].([]string)
	if !ok || len(sample) != 1 || sample[0] != inactiveAddress {
		t.Fatalf("unexpected inactive sample: %+v", report["sample_inactive"])
	}
}

func tronAddressHexMust(t *testing.T, address string) string {
	t.Helper()
	value, err := tronAddressHex(address)
	if err != nil {
		t.Fatalf("tron address hex: %v", err)
	}
	return value
}

func tronABIAddressParamMust(t *testing.T, address string) string {
	t.Helper()
	value, err := tronABIAddressParam(address)
	if err != nil {
		t.Fatalf("tron ABI address param: %v", err)
	}
	return value
}

func timeNowMillis() int64 {
	return time.Now().UnixMilli()
}
