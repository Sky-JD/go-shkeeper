package chainworker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestBitcoinRPCBridge(t *testing.T) {
	seenAuth := false
	seenSetFee := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && user == "rpcuser" && pass == "rpcpass" {
			seenAuth = true
		}
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		var result any
		switch req.Method {
		case "getblockchaininfo":
			result = map[string]any{"headers": 105, "blocks": 100}
		case "getbalance":
			result = "1.25"
		case "getnewaddress":
			result = "bc1qgenerated"
		case "listreceivedbyaddress":
			result = []map[string]any{{"address": "bc1qreceived"}}
		case "gettransaction":
			result = map[string]any{
				"confirmations": 3,
				"details": []map[string]any{{
					"address":  "bc1qreceived",
					"amount":   "0.5",
					"category": "receive",
				}},
			}
		case "estimatesmartfee":
			result = map[string]any{"feerate": "0.00002000", "blocks": 6}
		case "settxfee":
			seenSetFee = true
			result = true
		case "sendtoaddress":
			result = "btc-txid"
		case "getbestblockhash":
			result = "best-block-hash"
		case "getblockheader":
			result = map[string]any{"time": 1780531200}
		default:
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "test"})
	}))
	defer server.Close()

	s := &Server{cfg: Config{Module: "BTC", FullnodeURL: server.URL, RPCUsername: "rpcuser", RPCPassword: "rpcpass"}, client: server.Client()}

	status, err := s.bitcoinStatus(t.Context())
	if err != nil {
		t.Fatalf("bitcoin status: %v", err)
	}
	if status["delta_blocks"] != int64(5) {
		t.Fatalf("unexpected status: %+v", status)
	}
	balance, err := s.bitcoinBalance(t.Context())
	if err != nil || !balance.Equal(decimal.RequireFromString("1.25")) {
		t.Fatalf("unexpected balance %s err=%v", balance, err)
	}
	address, err := s.newBitcoinAddress(t.Context())
	if err != nil || address != "bc1qgenerated" {
		t.Fatalf("unexpected address %s err=%v", address, err)
	}
	transfers, err := s.bitcoinTransfersByTx(t.Context(), "btc-txid")
	if err != nil || len(transfers) != 1 || transfers[0].Confirmations != 3 {
		t.Fatalf("unexpected transfers %+v err=%v", transfers, err)
	}
	fee, err := s.bitcoinEstimateTxFee(t.Context())
	if err != nil || fee["fee_satoshi"] != "2" {
		t.Fatalf("unexpected fee %+v err=%v", fee, err)
	}
	result, err := s.broadcastBitcoinPayout(t.Context(), "bc1qdest", decimal.RequireFromString("0.1"), "2")
	if err != nil || len(result.TxIDs) != 1 || result.TxIDs[0] != "btc-txid" {
		t.Fatalf("unexpected payout %+v err=%v", result, err)
	}
	if !seenAuth || !seenSetFee {
		t.Fatalf("expected basic auth and settxfee to be used")
	}
	ts, err := s.bitcoinLatestBlockTimestamp(t.Context())
	if err != nil || !ts.Equal(time.Unix(1780531200, 0)) {
		t.Fatalf("unexpected latest block timestamp %s err=%v", ts, err)
	}
}

func TestFiroSparkWorkerRPCBridge(t *testing.T) {
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
		case "getallsparkaddresses":
			result = map[string]string{"b": "spark-address-b", "a": "spark-address-a"}
		case "settxfee":
			result = true
		case "spendspark":
			dest, ok := req.Params[0].(map[string]any)["spark-destination"].(map[string]any)
			if !ok || dest["subtractFee"] != false {
				t.Fatalf("unexpected spendspark payload: %+v", req.Params)
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
		default:
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "test"})
	}))
	defer server.Close()

	s := &Server{cfg: Config{Module: "FIRO", FullnodeURL: server.URL}, client: server.Client()}
	balance, err := s.firoSparkBalance(t.Context())
	if err != nil || !balance.Equal(decimal.RequireFromString("1.23456789")) {
		t.Fatalf("unexpected spark balance %s err=%v", balance, err)
	}
	address, err := s.newFiroSparkAddress(t.Context())
	if err != nil || address != "spark-address-1" {
		t.Fatalf("unexpected spark address %s err=%v", address, err)
	}
	addresses, err := s.firoSparkAddresses(t.Context())
	if err != nil || len(addresses) != 2 || addresses[0] != "spark-address-a" || addresses[1] != "spark-address-b" {
		t.Fatalf("unexpected spark addresses %+v err=%v", addresses, err)
	}
	payout, err := s.broadcastFiroSparkPayout(t.Context(), "spark-destination", decimal.RequireFromString("1.25"), "1000")
	if err != nil || !sawSpendSpark || len(payout.TxIDs) != 1 || payout.TxIDs[0] != "spark-payout-tx" {
		t.Fatalf("unexpected spark payout %+v err=%v saw=%v", payout, err, sawSpendSpark)
	}
	transfers, err := s.firoSparkTransfersByTx(t.Context(), "spark-tx")
	if err != nil || len(transfers) != 2 || transfers[0].Address != "spark-receive" || transfers[1].Category != "send" || transfers[0].Confirmations != 7 {
		t.Fatalf("unexpected spark transfers %+v err=%v", transfers, err)
	}
}
