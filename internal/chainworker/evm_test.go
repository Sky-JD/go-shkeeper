package chainworker

import (
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestAmountToBaseUnits(t *testing.T) {
	got := amountToBaseUnits(decimal.RequireFromString("1.234567"), 6).String()
	if got != "1234567" {
		t.Fatalf("unexpected base units: %s", got)
	}
	truncated := amountToBaseUnits(decimal.RequireFromString("1.23456789"), 6).String()
	if truncated != "1234567" {
		t.Fatalf("unexpected truncated base units: %s", truncated)
	}
}

func TestEVMCallData(t *testing.T) {
	address, err := evmAddressBytes("0x000000000000000000000000000000000000dead")
	if err != nil {
		t.Fatalf("evm address: %v", err)
	}
	balanceOf := hex.EncodeToString(evmCallData("balanceOf(address)", address))
	if !strings.HasPrefix(balanceOf, "70a08231") {
		t.Fatalf("unexpected balanceOf selector: %s", balanceOf[:8])
	}
	if len(balanceOf) != 8+64 {
		t.Fatalf("unexpected balanceOf length: %d", len(balanceOf))
	}
	if !strings.HasSuffix(balanceOf, "000000000000000000000000000000000000dead") {
		t.Fatalf("unexpected balanceOf address word: %s", balanceOf)
	}

	transfer := hex.EncodeToString(evmCallData("transfer(address,uint256)", address, amountToBaseUnits(decimal.NewFromInt(10), 18).Bytes()))
	if !strings.HasPrefix(transfer, "a9059cbb") {
		t.Fatalf("unexpected transfer selector: %s", transfer[:8])
	}
	if len(transfer) != 8+64+64 {
		t.Fatalf("unexpected transfer length: %d", len(transfer))
	}
}

func TestBNBTokenConfigEnvOverride(t *testing.T) {
	t.Setenv("BNB_USDT_CONTRACT", "0x0000000000000000000000000000000000000001")
	t.Setenv("BNB_USDT_DECIMALS", "6")
	contract, decimals, err := tokenConfig("BNB-USDT")
	if err != nil {
		t.Fatalf("token config: %v", err)
	}
	if contract != os.Getenv("BNB_USDT_CONTRACT") || decimals != 6 {
		t.Fatalf("unexpected token config: %s %d", contract, decimals)
	}
}

func TestSelectEVMPayoutPartsSplitsTokenAcrossGasFundedAccounts(t *testing.T) {
	candidates := []evmSpendableAccount{
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000001"},
			Balance:       decimal.RequireFromString("1.02"),
			NativeBalance: decimal.RequireFromString("0.00002"),
		},
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000002"},
			Balance:       decimal.RequireFromString("1.02"),
			NativeBalance: decimal.RequireFromString("0.00002"),
		},
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000003"},
			Balance:       decimal.RequireFromString("10"),
			NativeBalance: decimal.Zero,
		},
	}
	parts := selectEVMPayoutParts(candidates, decimal.RequireFromString("2.04"), decimal.RequireFromString("0.00001"), false, true)
	if len(parts) != 2 {
		t.Fatalf("expected split payout across 2 accounts, got %+v", parts)
	}
	if parts[0].Account.Address != "0x0000000000000000000000000000000000000001" || !parts[0].Amount.Equal(decimal.RequireFromString("1.02")) {
		t.Fatalf("unexpected first part: %+v", parts[0])
	}
	if parts[1].Account.Address != "0x0000000000000000000000000000000000000002" || !parts[1].Amount.Equal(decimal.RequireFromString("1.02")) {
		t.Fatalf("unexpected second part: %+v", parts[1])
	}
}

func TestSelectEVMPayoutPartsDoesNotSplitNativePayout(t *testing.T) {
	candidates := []evmSpendableAccount{
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000001"},
			Balance:       decimal.RequireFromString("1.03"),
			NativeBalance: decimal.RequireFromString("1.03"),
		},
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000002"},
			Balance:       decimal.RequireFromString("1.03"),
			NativeBalance: decimal.RequireFromString("1.03"),
		},
	}
	parts := selectEVMPayoutParts(candidates, decimal.RequireFromString("2.04"), decimal.RequireFromString("0.00001"), true, true)
	if len(parts) != 0 {
		t.Fatalf("native payout should not be split because each source needs its own gas, got %+v", parts)
	}
}

func TestEVMPayoutSpendableSummaryIgnoresTokenAccountsWithoutGas(t *testing.T) {
	candidates := []evmSpendableAccount{
		{Balance: decimal.RequireFromString("1.02"), NativeBalance: decimal.RequireFromString("0.00002")},
		{Balance: decimal.RequireFromString("1.02"), NativeBalance: decimal.Zero},
		{Balance: decimal.RequireFromString("0.50"), NativeBalance: decimal.RequireFromString("0.00002")},
	}
	report := evmSpendableSummary(candidates, decimal.RequireFromString("0.00001"), false)
	if !report.Total.Equal(decimal.RequireFromString("1.52")) || !report.Max.Equal(decimal.RequireFromString("1.02")) || report.FundedAccountCount != 2 {
		t.Fatalf("unexpected token report: %+v", report)
	}
}

func TestEVMGasTopupSelectsFundingAccount(t *testing.T) {
	funders := []evmGasFundingAccount{
		{Account: Account{Address: "0x0000000000000000000000000000000000000001"}, Available: decimal.RequireFromString("1")},
		{Account: Account{Address: "0x0000000000000000000000000000000000000002"}, Available: decimal.RequireFromString("0.00002")},
		{Account: Account{Address: "0x0000000000000000000000000000000000000003"}, Available: decimal.RequireFromString("0.00003")},
	}
	idx := selectEVMGasFundingAccount(funders, decimal.RequireFromString("0.000025"), "0x0000000000000000000000000000000000000001")
	if idx != 2 {
		t.Fatalf("expected third account to fund gas top-up, got index %d", idx)
	}
	if got := evmGasTopupTarget(decimal.RequireFromString("0.00001")); !got.Equal(decimal.RequireFromString("0.00001")) {
		t.Fatalf("unexpected gas top-up target: %s", got)
	}
}

func TestEVMAutoTopupSpendableSummaryIncludesGasStarvedTokenBalance(t *testing.T) {
	candidates := []evmSpendableAccount{
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000001"},
			Balance:       decimal.RequireFromString("1.02"),
			NativeBalance: decimal.Zero,
		},
		{
			Account:       Account{Address: "0x0000000000000000000000000000000000000002"},
			Balance:       decimal.RequireFromString("1.02"),
			NativeBalance: decimal.RequireFromString("0.00002"),
		},
	}
	funders := []evmGasFundingAccount{
		{Account: Account{Address: "0x0000000000000000000000000000000000000003"}, Available: decimal.RequireFromString("0.001")},
	}
	report := evmAutoTopupSpendableSummary(candidates, decimal.RequireFromString("0.00001"), decimal.RequireFromString("0.0000021"), funders)
	if !report.Total.Equal(decimal.RequireFromString("2.04")) || report.FundedAccountCount != 2 || report.TopupAccountCount != 1 {
		t.Fatalf("unexpected auto top-up report: %+v", report)
	}
	if !report.TopupAmount.Equal(decimal.RequireFromString("0.00001")) || !report.TopupTransferFee.Equal(decimal.RequireFromString("0.0000021")) {
		t.Fatalf("unexpected auto top-up costs: %+v", report)
	}
}

func TestFilterAccountsByActivityBalances(t *testing.T) {
	accounts := []Account{
		{Address: "0x0000000000000000000000000000000000000001"},
		{Address: "0x0000000000000000000000000000000000000002"},
		{Address: "0x0000000000000000000000000000000000000003"},
	}
	filtered := filterAccountsByActivityBalances(accounts, map[string]decimal.Decimal{
		"0x0000000000000000000000000000000000000002": decimal.RequireFromString("7.61"),
		"0x0000000000000000000000000000000000000001": decimal.RequireFromString("1.02"),
		"0x0000000000000000000000000000000000000003": decimal.Zero,
	})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 active accounts, got %+v", filtered)
	}
	if filtered[0].Address != "0x0000000000000000000000000000000000000002" || filtered[1].Address != "0x0000000000000000000000000000000000000001" {
		t.Fatalf("accounts should be ordered by local activity balance: %+v", filtered)
	}
	if empty := filterAccountsByActivityBalances(accounts, nil); len(empty) != 0 {
		t.Fatalf("empty local activity should skip network candidates, got %+v", empty)
	}
}

func TestEVMActualFeeSkipsReceiptLookupWhenWaitDisabled(t *testing.T) {
	var rpcCount atomic.Int32
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCount.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": nil})
	}))
	defer fullnode.Close()

	t.Setenv("EVM_PAYOUT_RECEIPT_WAIT_SECONDS", "0")
	server := &Server{cfg: Config{FullnodeURL: fullnode.URL}, client: fullnode.Client()}
	if fee, ok := server.evmActualFeeForTxs(t.Context(), []string{"0xabc"}, big.NewInt(1)); ok || !fee.IsZero() {
		t.Fatalf("receipt fee should be skipped by default, fee=%s ok=%v", fee, ok)
	}
	if got := rpcCount.Load(); got != 0 {
		t.Fatalf("receipt lookup should not call RPC when disabled, got %d calls", got)
	}
}

func TestBNBTokenBalanceFallsBackToModuleAccounts(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := t.Context()
	if err := store.AddAccount(ctx, &Account{
		Module:        "BNB",
		Crypto:        "BNB",
		Address:       "0x000000000000000000000000000000000000dEaD",
		PrivateKeyHex: "v1:test",
	}); err != nil {
		t.Fatalf("add bnb account: %v", err)
	}
	t.Setenv("BNB_USDT_CONTRACT", "0x0000000000000000000000000000000000000001")
	t.Setenv("BNB_USDT_DECIMALS", "18")
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		if req.Method != "eth_call" {
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x7ce66c50e2840000"})
	}))
	defer fullnode.Close()

	cfg := Config{Module: "BNB", FullnodeURL: fullnode.URL, Username: "worker", Password: "secret", RequestTimeout: 5 * time.Second}
	handler := NewServer(cfg, store, slog.New(slog.NewTextHandler(os.Stdout, nil))).Routes()
	req := httptest.NewRequest(http.MethodPost, "/BNB-USDT/balance", nil)
	req.SetBasicAuth("worker", "secret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"balance":"9"`) {
		t.Fatalf("balance status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestEVMSpendableUsesAsyncCacheForHTTPQuote(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := t.Context()
	account := Account{
		Module:        "BNB",
		Crypto:        "BNB-USDT",
		Address:       "0x000000000000000000000000000000000000dEaD",
		PrivateKeyHex: "v1:test",
	}
	if err := store.AddAccount(ctx, &account); err != nil {
		t.Fatalf("add bnb-usdt account: %v", err)
	}
	t.Setenv("BNB_USDT_CONTRACT", "0x0000000000000000000000000000000000000001")
	t.Setenv("BNB_USDT_DECIMALS", "18")
	t.Setenv("EVM_SPENDABLE_CACHE_SECONDS", "60")

	firstRPCStarted := make(chan struct{}, 1)
	releaseFirstRPC := make(chan struct{})
	var rpcCount atomic.Int32
	var blockedFirstRPC atomic.Bool
	fullnode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCount.Add(1)
		if blockedFirstRPC.CompareAndSwap(false, true) {
			firstRPCStarted <- struct{}{}
			<-releaseFirstRPC
		}
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		result := "0x0"
		switch req.Method {
		case "eth_gasPrice":
			result = "0x3b9aca00"
		case "eth_call":
			result = "0xde0b6b3a7640000"
		case "eth_getBalance":
			result = "0x2386f26fc10000"
		default:
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer fullnode.Close()

	server := NewServer(Config{Module: "BNB", FullnodeURL: fullnode.URL, Username: "worker", Password: "secret", RequestTimeout: time.Second}, store, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	handler := server.Routes()
	req := httptest.NewRequest(http.MethodGet, "/BNB-USDT/spendable", nil)
	req.SetBasicAuth("worker", "secret")
	res := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(res, req)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("spendable should return cached warming response quickly, took %s", elapsed)
	}
	if res.Code != http.StatusOK {
		t.Fatalf("spendable status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{`"cache_ready":false`, `"balance_source":"warming"`, `"refreshing":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("warming response missing %s: %s", want, body)
		}
	}
	select {
	case <-firstRPCStarted:
	case <-time.After(time.Second):
		t.Fatalf("async refresh did not start")
	}
	close(releaseFirstRPC)

	accounts := []Account{account}
	deadline := time.Now().Add(2 * time.Second)
	for {
		entry, ok := server.loadEVMSpendableCache("BNB-USDT", accounts)
		if ok && entry.Payload != nil && !entry.RefreshedAt.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for evm spendable cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
	beforeSecondRequest := rpcCount.Load()
	req = httptest.NewRequest(http.MethodGet, "/BNB-USDT/spendable", nil)
	req.SetBasicAuth("worker", "secret")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("cached spendable status=%d body=%s", res.Code, res.Body.String())
	}
	body = res.Body.String()
	for _, want := range []string{`"cache_ready":true`, `"balance":"1"`, `"balance_source":"evm_accounts_spendable"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("cached response missing %s: %s", want, body)
		}
	}
	if got := rpcCount.Load(); got != beforeSecondRequest {
		t.Fatalf("cached response should not call fullnode again, before=%d after=%d", beforeSecondRequest, got)
	}
}

func TestEVMRPCFallsBackForArchiveReadErrors(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]any{"code": -32602, "message": "Archive requests require a personal token"},
		})
	}))
	defer primary.Close()
	fallbackCalls := atomic.Int32{}
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x10"})
	}))
	defer fallback.Close()

	server := NewServer(Config{
		Module:       "BNB",
		FullnodeURL:  primary.URL,
		FullnodeURLs: []string{primary.URL, fallback.URL},
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	var blockHex string
	if err := server.rpc(t.Context(), "eth_blockNumber", []any{}, &blockHex); err != nil {
		t.Fatalf("rpc fallback failed: %v", err)
	}
	if blockHex != "0x10" || fallbackCalls.Load() != 1 {
		t.Fatalf("unexpected fallback result block=%s calls=%d", blockHex, fallbackCalls.Load())
	}
	if server.evmReadNodeURL() != fallback.URL {
		t.Fatalf("successful fallback should become active read endpoint: %s", server.evmReadNodeURL())
	}
	if server.fullnodeURL() != primary.URL {
		t.Fatalf("read fallback must not change broadcast endpoint: %s", server.fullnodeURL())
	}
}

func TestEVMRPCDoesNotFallbackForSendRawTransaction(t *testing.T) {
	primaryCalls := atomic.Int32{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]any{"code": -32000, "message": "temporary failure"},
		})
	}))
	defer primary.Close()
	fallbackCalls := atomic.Int32{}
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0xsent"})
	}))
	defer fallback.Close()

	server := NewServer(Config{
		Module:       "BNB",
		FullnodeURL:  primary.URL,
		FullnodeURLs: []string{primary.URL, fallback.URL},
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	var txid string
	if err := server.rpc(t.Context(), "eth_sendRawTransaction", []any{"0xraw"}, &txid); err == nil {
		t.Fatalf("send raw transaction should return primary error without fallback")
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 0 {
		t.Fatalf("unexpected call counts: primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestSignLegacyEVMTransaction(t *testing.T) {
	to, err := evmAddressBytes("0x000000000000000000000000000000000000dead")
	if err != nil {
		t.Fatalf("evm address: %v", err)
	}
	_, privateKeyHex, err := newBNBAccount()
	if err != nil {
		t.Fatalf("new bnb account: %v", err)
	}
	raw, txHash, err := signLegacyEVMTransaction(privateKeyHex, 1, big.NewInt(3_000_000_000), 21_000, to, big.NewInt(1), nil, big.NewInt(56))
	if err != nil {
		t.Fatalf("sign evm transaction: %v", err)
	}
	if len(raw) == 0 || raw[0] < 0xc0 {
		t.Fatalf("unexpected raw RLP: %x", raw)
	}
	if len(txHash) != 32 {
		t.Fatalf("unexpected tx hash length: %d", len(txHash))
	}
}

func TestEVMTopicAddress(t *testing.T) {
	topic := "0x000000000000000000000000000000000000000000000000000000000000dead"
	if got := evmTopicAddress(topic); got != "0x000000000000000000000000000000000000dead" {
		t.Fatalf("unexpected topic address: %s", got)
	}
}

func TestEVMDepositScannerHelpers(t *testing.T) {
	address := "0xf9e546a0a9e06a3de7ca7e1aae30f040819314a4"
	wantTopic := "0x000000000000000000000000f9e546a0a9e06a3de7ca7e1aae30f040819314a4"
	if got := evmTransferToTopic(address); got != wantTopic {
		t.Fatalf("unexpected transfer topic: %s", got)
	}
	chunks := evmTopicChunks([]string{"a", "b", "c"}, 2)
	if len(chunks) != 2 || len(chunks[0]) != 2 || len(chunks[1]) != 1 {
		t.Fatalf("unexpected topic chunks: %+v", chunks)
	}
	sorted := evmSortedTopics(map[string]DepositInvoiceAddress{"b": {}, "a": {}})
	if strings.Join(sorted, ",") != "a,b" {
		t.Fatalf("unexpected sorted topics: %+v", sorted)
	}

	server := NewServer(Config{
		Module:                      "BNB",
		DepositScanMinConfirmations: 1,
		DepositScanStartMargin:      10 * time.Minute,
		EVMAverageBlockSeconds:      3,
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	if !server.evmDepositScannerOwnsCrypto("BNB-USDT") || server.evmDepositScannerOwnsCrypto("TRON-USDT") {
		t.Fatalf("unexpected crypto ownership")
	}
	latestAt := time.Unix(1_000_000, 0)
	fromBlock, toBlock := server.evmDepositScanBlockRange(10_000, latestAt, latestAt.Add(-20*time.Minute))
	if toBlock != 10_000 || fromBlock <= 1 || fromBlock >= toBlock {
		t.Fatalf("unexpected scan block range: %d..%d", fromBlock, toBlock)
	}
}

func TestEVMDepositScanStartBlockSkipsStaleCursorBeforeActiveWindow(t *testing.T) {
	server := NewServer(Config{
		Module:                      "BNB",
		DepositScanMinConfirmations: 1,
		DepositScanStartMargin:      10 * time.Minute,
		EVMAverageBlockSeconds:      3,
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	latestAt := time.Unix(1_000_000, 0)
	earliest := latestAt.Add(-20 * time.Minute)
	windowFromBlock, _ := server.evmDepositScanBlockRange(10_000, latestAt, earliest)
	if windowFromBlock <= 1_001 {
		t.Fatalf("test setup needs active window after stale cursor, window=%d", windowFromBlock)
	}

	fromBlock, gotWindow := server.evmDepositScanStartBlock(10_000, latestAt, earliest, 1_000, true, false)
	if gotWindow != windowFromBlock {
		t.Fatalf("unexpected active window: got %d want %d", gotWindow, windowFromBlock)
	}
	if fromBlock != windowFromBlock {
		t.Fatalf("stale cursor should resume from active invoice window: got %d want %d", fromBlock, windowFromBlock)
	}
}

func TestEVMDepositReconcileRechecksActiveWindowWhenCursorAhead(t *testing.T) {
	server := NewServer(Config{
		Module:                      "BNB",
		DepositScanMinConfirmations: 1,
		DepositScanStartMargin:      10 * time.Minute,
		EVMAverageBlockSeconds:      1,
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	latestAt := time.Unix(1_000_000, 0)
	earliest := latestAt.Add(-20 * time.Minute)
	windowFromBlock, _ := server.evmDepositScanBlockRange(10_000, latestAt, earliest)
	cursorBlock := int64(20_000)

	fromBlock, gotWindow := server.evmDepositScanStartBlock(10_000, latestAt, earliest, cursorBlock, true, true)
	if gotWindow != windowFromBlock {
		t.Fatalf("unexpected active window: got %d want %d", gotWindow, windowFromBlock)
	}
	if fromBlock != windowFromBlock {
		t.Fatalf("active unpaid invoices should be rechecked even after cursor advanced: got %d want %d", fromBlock, windowFromBlock)
	}
}

func TestEVMDepositIncrementalScanStartsAfterCursor(t *testing.T) {
	server := NewServer(Config{
		Module:                      "BNB",
		DepositScanMinConfirmations: 1,
		DepositScanStartMargin:      10 * time.Minute,
		EVMAverageBlockSeconds:      1,
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	latestAt := time.Unix(1_000_000, 0)
	earliest := latestAt.Add(-20 * time.Minute)
	cursorBlock := int64(9_900)

	fromBlock, _ := server.evmDepositScanStartBlock(10_000, latestAt, earliest, cursorBlock, true, false)
	if fromBlock != cursorBlock+1 {
		t.Fatalf("incremental scan should start after cursor: got %d want %d", fromBlock, cursorBlock+1)
	}
}

func TestEVMDepositScanBlockRangeCoversFastBNBBlocks(t *testing.T) {
	server := NewServer(Config{
		Module:                      "BNB",
		DepositScanMinConfirmations: 1,
		DepositScanStartMargin:      10 * time.Minute,
	}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	latestAt := time.Unix(1_000_000, 0)
	earliest := latestAt.Add(-2 * time.Hour)
	fromBlock, _ := server.evmDepositScanBlockRange(107_231_812, latestAt, earliest)
	paidBlock := int64(107_221_462)
	if fromBlock > paidBlock {
		t.Fatalf("BNB active-window scan should cover fast recent payment blocks: from=%d paid=%d", fromBlock, paidBlock)
	}
}

func TestEVMTransferLogsFiltersRecipientTopics(t *testing.T) {
	var gotFilter map[string]any
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		params, ok := req["params"].([]any)
		if !ok || len(params) != 1 {
			t.Fatalf("unexpected params: %#v", req["params"])
		}
		gotFilter, ok = params[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected filter: %#v", params[0])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "go-chain-worker",
			"result":  []any{},
		})
	}))
	defer rpc.Close()

	server := NewServer(Config{Module: "BNB", FullnodeURL: rpc.URL}, nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	recipients := []string{
		"0x00000000000000000000000000000000000000000000000000000000000000a1",
		"0x00000000000000000000000000000000000000000000000000000000000000b2",
	}
	if _, err := server.evmTransferLogs(t.Context(), "0x55D398326F99059fF775485246999027B3197955", 10, 20, recipients); err != nil {
		t.Fatalf("evmTransferLogs: %v", err)
	}
	if gotFilter["address"] != "0x55d398326f99059ff775485246999027b3197955" || gotFilter["fromBlock"] != "0xa" || gotFilter["toBlock"] != "0x14" {
		t.Fatalf("unexpected filter bounds/address: %#v", gotFilter)
	}
	topics, ok := gotFilter["topics"].([]any)
	if !ok || len(topics) != 3 {
		t.Fatalf("unexpected topics: %#v", gotFilter["topics"])
	}
	if topics[0] != evmTransferTopic || topics[1] != nil {
		t.Fatalf("unexpected topic prefix: %#v", topics)
	}
	gotRecipients, ok := topics[2].([]any)
	if !ok || len(gotRecipients) != 2 || gotRecipients[0] != recipients[0] || gotRecipients[1] != recipients[1] {
		t.Fatalf("unexpected recipient topics: %#v", topics[2])
	}
}
