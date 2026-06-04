package chainworker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestMoneroRPCBridge(t *testing.T) {
	walletAuth := false
	daemonAuth := false
	seenTransferAmount := ""
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && user == "wallet-user" && pass == "wallet-pass" {
			walletAuth = true
		}
		var req struct {
			Method string           `json:"method"`
			Params *json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode wallet request: %v", err)
		}
		var result any
		switch req.Method {
		case "get_balance":
			result = map[string]any{"unlocked_balance": 5_000_000_000_000}
		case "create_address":
			result = map[string]any{"address": "xmr-created"}
		case "get_address":
			result = map[string]any{
				"address": "xmr-primary",
				"addresses": []map[string]any{
					{"address": "xmr-primary"},
					{"address": "xmr-created"},
				},
			}
		case "get_transfer_by_txid":
			result = map[string]any{
				"transfer": map[string]any{
					"txid":          "xmr-txid",
					"address":       "xmr-created",
					"amount":        1_500_000_000_000,
					"type":          "in",
					"confirmations": 7,
				},
			}
		case "transfer_split":
			var params struct {
				Destinations []struct {
					Amount json.Number `json:"amount"`
				} `json:"destinations"`
			}
			if req.Params == nil {
				t.Fatalf("missing transfer params")
			}
			dec := json.NewDecoder(bytesReader(*req.Params))
			dec.UseNumber()
			if err := dec.Decode(&params); err != nil {
				t.Fatalf("decode transfer params: %v", err)
			}
			if len(params.Destinations) != 1 {
				t.Fatalf("unexpected destinations: %+v", params.Destinations)
			}
			seenTransferAmount = params.Destinations[0].Amount.String()
			result = map[string]any{"tx_hash_list": []string{"xmr-payout"}}
		default:
			t.Fatalf("unexpected wallet method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "test"})
	}))
	defer wallet.Close()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && user == "daemon-user" && pass == "daemon-pass" {
			daemonAuth = true
		}
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode daemon request: %v", err)
		}
		if req.Method != "get_info" {
			t.Fatalf("unexpected daemon method: %s", req.Method)
		}
		result := map[string]any{"status": "OK", "synchronized": false, "busy_syncing": true, "height": 90, "target_height": 100}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "test"})
	}))
	defer daemon.Close()

	s := &Server{
		cfg: Config{
			Module:        "XMR",
			FullnodeURL:   daemon.URL,
			WalletRPCURL:  wallet.URL,
			RPCUsername:   "daemon-user",
			RPCPassword:   "daemon-pass",
			WalletRPCUser: "wallet-user",
			WalletRPCPass: "wallet-pass",
		},
		client: wallet.Client(),
	}

	status, err := s.moneroStatus(t.Context())
	if err != nil {
		t.Fatalf("monero status: %v", err)
	}
	if status["delta_blocks"] != int64(10) {
		t.Fatalf("unexpected monero status: %+v", status)
	}
	balance, err := s.moneroBalance(t.Context())
	if err != nil || !balance.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("unexpected monero balance %s err=%v", balance, err)
	}
	address, err := s.newMoneroAddress(t.Context())
	if err != nil || address != "xmr-created" {
		t.Fatalf("unexpected monero address %s err=%v", address, err)
	}
	addresses, err := s.moneroAddresses(t.Context(), "XMR")
	if err != nil || len(addresses) != 2 {
		t.Fatalf("unexpected monero addresses %+v err=%v", addresses, err)
	}
	transfers, err := s.moneroTransfersByTx(t.Context(), "xmr-txid")
	if err != nil || len(transfers) != 1 || transfers[0].Amount != "1.5" || transfers[0].Confirmations != 7 {
		t.Fatalf("unexpected monero transfers %+v err=%v", transfers, err)
	}
	payout, err := s.broadcastMoneroPayout(t.Context(), "xmr-dest", decimal.RequireFromString("1.5"), "2")
	if err != nil || len(payout.TxIDs) != 1 || payout.TxIDs[0] != "xmr-payout" {
		t.Fatalf("unexpected monero payout %+v err=%v", payout, err)
	}
	if seenTransferAmount != "1500000000000" {
		t.Fatalf("unexpected atomic amount: %s", seenTransferAmount)
	}
	if !walletAuth || !daemonAuth {
		t.Fatalf("expected wallet and daemon basic auth")
	}
}

func TestNormalizeMoneroRPCURL(t *testing.T) {
	if got := normalizeMoneroRPCURL("monero-wallet-rpc:2222"); got != "http://monero-wallet-rpc:2222/json_rpc" {
		t.Fatalf("unexpected normalized URL: %s", got)
	}
}

func bytesReader(raw json.RawMessage) *strings.Reader {
	return strings.NewReader(string(raw))
}
