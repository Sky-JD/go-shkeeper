package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestFiroSparkJSONRPCAdapter(t *testing.T) {
	var sawSpendSpark bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		var result any
		switch req.Method {
		case "getsparkbalance":
			result = map[string]any{"availableBalance": 123456789}
		case "getnewsparkaddress":
			result = []string{"spark-address-1"}
		case "settxfee":
			result = true
		case "spendspark":
			if len(req.Params) != 1 {
				t.Fatalf("unexpected spendspark params: %+v", req.Params)
			}
			dest, ok := req.Params[0].(map[string]any)["spark-destination"].(map[string]any)
			if !ok {
				t.Fatalf("missing spendspark destination: %+v", req.Params)
			}
			amount, _ := decimalFromAny(dest["amount"])
			if !amount.Equal(decimal.RequireFromString("1.25")) || dest["subtractFee"] != false {
				t.Fatalf("unexpected spendspark payload: %+v", dest)
			}
			sawSpendSpark = true
			result = map[string]any{"txid": "spark-payout-tx"}
		case "getsparkcoinaddr":
			result = []map[string]any{
				{"address": "spark-receive", "amount": "1.25"},
				{"address": "spark-send", "amount": "0.5"},
			}
		case "gettransaction":
			result = map[string]any{
				"confirmations": 7,
				"details": []map[string]any{
					{"category": "receive", "amount": "1.25"},
					{"category": "spend", "amount": "-0.5"},
				},
			}
		case "getallsparkaddresses":
			result = map[string]string{"b": "spark-address-b", "a": "spark-address-a"}
		default:
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil})
	}))
	defer server.Close()

	t.Setenv("FIRO_RPC_HOST", strings.TrimPrefix(server.URL, "http://"))
	reg := &CryptoRegistry{httpClient: server.Client()}
	module := &CryptoModule{Name: "FIRO-SPARK", Adapter: "jsonrpc", HostEnv: "FIRO_RPC_HOST", UsernameEnv: "FIRO_USERNAME", PasswordEnv: "FIRO_PASSWORD"}

	balance, _, errText := reg.Balance(t.Context(), module)
	if errText != "" || !balance.Equal(decimal.RequireFromString("1.23456789")) {
		t.Fatalf("unexpected balance=%s err=%s", balance, errText)
	}
	address, err := reg.MakeAddress(t.Context(), module, decimal.Zero)
	if err != nil || address != "spark-address-1" {
		t.Fatalf("unexpected address=%s err=%v", address, err)
	}
	payout, err := reg.Payout(t.Context(), module, "spark-destination", decimal.RequireFromString("1.25"), "1000")
	if err != nil {
		t.Fatalf("spark payout: %v", err)
	}
	if !sawSpendSpark || payout["result"] == nil {
		t.Fatalf("spendspark was not used: saw=%v payout=%+v", sawSpendSpark, payout)
	}
	transfers, err := reg.TransfersByTx(t.Context(), module, "spark-tx")
	if err != nil {
		t.Fatalf("spark transfers: %v", err)
	}
	if len(transfers) != 2 || transfers[0].Address != "spark-receive" || transfers[0].Category != "receive" || transfers[1].Category != "send" || transfers[0].Confirmations != 7 {
		t.Fatalf("unexpected spark transfers: %+v", transfers)
	}
	addresses, err := reg.AllAddresses(t.Context(), module)
	if err != nil {
		t.Fatalf("spark addresses: %v", err)
	}
	list, ok := addresses.([]string)
	if !ok || len(list) != 2 || list[0] != "spark-address-a" || list[1] != "spark-address-b" {
		t.Fatalf("unexpected spark address list: %+v", addresses)
	}
}

func TestFiroJSONRPCAdapterFiltersSparkTransfers(t *testing.T) {
	longSparkAddress := strings.Repeat("s", 150)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		if req.Method != "gettransaction" {
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"confirmations": 3,
				"details": []map[string]any{
					{"address": "regular-firo-address", "amount": "2.5", "category": "receive"},
					{"address": longSparkAddress, "amount": "9.9", "category": "receive"},
				},
			},
			"error": nil,
		})
	}))
	defer server.Close()

	t.Setenv("FIRO_RPC_HOST", strings.TrimPrefix(server.URL, "http://"))
	reg := &CryptoRegistry{httpClient: server.Client()}
	module := &CryptoModule{Name: "FIRO", Adapter: "jsonrpc", HostEnv: "FIRO_RPC_HOST", UsernameEnv: "FIRO_USERNAME", PasswordEnv: "FIRO_PASSWORD"}

	transfers, err := reg.TransfersByTx(t.Context(), module, "firo-tx")
	if err != nil {
		t.Fatalf("firo transfers: %v", err)
	}
	if len(transfers) != 1 || transfers[0].Address != "regular-firo-address" || !transfers[0].Amount.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("unexpected FIRO transfers: %+v", transfers)
	}
}
