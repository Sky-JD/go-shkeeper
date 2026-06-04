package chainworker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"
)

func TestXRPAddressCodecRoundTrip(t *testing.T) {
	classic := "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf"
	tag := uint32(4294967295)
	xaddr, err := xrpClassicToXAddress(classic, &tag, false)
	if err != nil {
		t.Fatalf("classic to x-address: %v", err)
	}
	decoded, err := xrpXAddressToClassic(xaddr)
	if err != nil {
		t.Fatalf("x-address to classic: %v", err)
	}
	if decoded.ClassicAddress != classic || decoded.Tag == nil || *decoded.Tag != tag || decoded.Testnet {
		t.Fatalf("unexpected decoded x-address: %+v from %s", decoded, xaddr)
	}
	noTag, err := xrpClassicToXAddress(classic, nil, true)
	if err != nil {
		t.Fatalf("classic to testnet x-address: %v", err)
	}
	decoded, err = xrpXAddressToClassic(noTag)
	if err != nil {
		t.Fatalf("decode testnet x-address: %v", err)
	}
	if decoded.ClassicAddress != classic || decoded.Tag != nil || !decoded.Testnet {
		t.Fatalf("unexpected testnet x-address decode: %+v", decoded)
	}
}

func TestXRPWorkerRPCBridge(t *testing.T) {
	t.Setenv("XRP_ACCOUNT_ADDRESS", "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf")
	t.Setenv("XRP_ACCOUNT_SECRET", "s-redacted")
	var seenSubmit map[string]any
	var txAddress string
	tag := uint32(12345)
	txAddress, err := xrpClassicToXAddress("rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf", &tag, false)
	if err != nil {
		t.Fatalf("x-address: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		var result any
		switch req.Method {
		case "ledger":
			result = map[string]any{
				"ledger": map[string]any{
					"close_time":   810000000,
					"ledger_index": 100,
				},
			}
		case "account_info":
			result = map[string]any{
				"account_data": map[string]any{"Balance": "25000000"},
			}
		case "tx":
			result = map[string]any{
				"TransactionType": "Payment",
				"Account":         "rSender111111111111111111111111111111",
				"Destination":     "rGWrZyQqhTp9Xu7G5Pkayo7bXjH4k4QYpf",
				"DestinationTag":  tag,
				"Amount":          "1500000",
				"ledger_index":    99,
				"validated":       true,
				"meta":            map[string]any{"TransactionResult": "tesSUCCESS"},
			}
		case "submit":
			if len(req.Params) != 1 {
				t.Fatalf("unexpected submit params: %+v", req.Params)
			}
			tx, ok := req.Params[0]["tx_json"].(map[string]any)
			if !ok {
				t.Fatalf("missing submit tx_json: %+v", req.Params[0])
			}
			seenSubmit = tx
			result = map[string]any{
				"engine_result": "tesSUCCESS",
				"tx_json":       map[string]any{"hash": "XRP-SUBMIT-HASH"},
			}
		default:
			t.Fatalf("unexpected xrp method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "status": "success"})
	}))
	defer server.Close()

	s := &Server{
		cfg: Config{
			Module:      "XRP",
			FullnodeURL: server.URL,
		},
		client: server.Client(),
	}
	status, err := s.xrpStatus(t.Context())
	if err != nil {
		t.Fatalf("xrp status: %v", err)
	}
	if status["last_block_timestamp"] != int64(810000000) {
		t.Fatalf("unexpected status: %+v", status)
	}
	balance, err := s.xrpBalance(t.Context())
	if err != nil || !balance.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("unexpected balance %s err=%v", balance, err)
	}
	address, privateKeyHex, err := s.newXRPAddress(t.Context())
	if err != nil || privateKeyHex != "" {
		t.Fatalf("unexpected generated xrp address=%s key=%s err=%v", address, privateKeyHex, err)
	}
	if _, err := xrpXAddressToClassic(address); err != nil {
		t.Fatalf("generated address is not an x-address: %s err=%v", address, err)
	}
	transfers, err := s.xrpTransfersByTx(t.Context(), "xrp-txid")
	if err != nil {
		t.Fatalf("xrp transfers: %v", err)
	}
	if len(transfers) != 1 || transfers[0].Address != txAddress || transfers[0].Amount != "1.5" || transfers[0].Confirmations != 2 || transfers[0].Category != "receive" {
		t.Fatalf("unexpected transfers: %+v", transfers)
	}
	payout, err := s.broadcastXRPPayout(t.Context(), txAddress, decimal.RequireFromString("1.5"), "0.000012")
	if err != nil {
		t.Fatalf("xrp payout: %v", err)
	}
	if len(payout.TxIDs) != 1 || payout.TxIDs[0] != "XRP-SUBMIT-HASH" {
		t.Fatalf("unexpected payout: %+v", payout)
	}
	if seenSubmit["Amount"] != "1500000" || seenSubmit["Fee"] != "12" || seenSubmit["DestinationTag"] != float64(tag) {
		t.Fatalf("unexpected submit tx: %+v", seenSubmit)
	}
}
