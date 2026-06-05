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

	cfg := Config{Module: "BNB", FullnodeURL: fullnode.URL, Username: "worker", Password: "secret", RequestTimeout: 5}
	handler := NewServer(cfg, store, slog.New(slog.NewTextHandler(os.Stdout, nil))).Routes()
	req := httptest.NewRequest(http.MethodPost, "/BNB-USDT/balance", nil)
	req.SetBasicAuth("worker", "secret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"balance":"9"`) {
		t.Fatalf("balance status=%d body=%s", res.Code, res.Body.String())
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
	fromBlock, toBlock := server.evmDepositScanBlockRange(1000, latestAt, latestAt.Add(-20*time.Minute))
	if toBlock != 1000 || fromBlock <= 1 || fromBlock >= toBlock {
		t.Fatalf("unexpected scan block range: %d..%d", fromBlock, toBlock)
	}
}
