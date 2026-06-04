package chainworker

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestLightningWorkerLNDBridge(t *testing.T) {
	t.Setenv("LND_MACAROON_HEX", "abc123")
	invoiceHashRaw := []byte("01234567890123456789012345678901")
	invoiceHashHex := hex.EncodeToString(invoiceHashRaw)
	paymentHashRaw := []byte("abcdefghabcdefghabcdefghabcdefgh")
	paymentHashHex := hex.EncodeToString(paymentHashRaw)
	var invoiceValue any
	var seenMacaroon bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Grpc-Metadata-macaroon") == "abc123" {
			seenMacaroon = true
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/getinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"synced_to_chain":       true,
				"best_header_timestamp": "1780509000",
				"block_height":          900,
				"alias":                 "go-lnd",
				"identity_pubkey":       "pubkey",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/balance/channels":
			_ = json.NewEncoder(w).Encode(map[string]any{"balance": "2500"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode invoice request: %v", err)
			}
			invoiceValue = req["value"]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"r_hash":          base64.StdEncoding.EncodeToString(invoiceHashRaw),
				"payment_request": "lnbc42",
				"value":           "42",
				"expiry":          "3600",
				"state":           "OPEN",
				"creation_date":   "1780509000",
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/invoice/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"r_hash":          base64.StdEncoding.EncodeToString(invoiceHashRaw),
				"payment_request": "lnbc42",
				"value":           "42",
				"state":           "SETTLED",
				"settle_date":     "1780509010",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/transactions":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode payment request: %v", err)
			}
			if req["payment_request"] != "lnbc-dest" {
				t.Fatalf("unexpected payment request: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"payment_hash": base64.StdEncoding.EncodeToString(paymentHashRaw),
			})
		default:
			t.Fatalf("unexpected LND request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Server{
		cfg: Config{
			Module:         "BTC-LIGHTNING",
			FullnodeURL:    server.URL,
			RequestTimeout: 5 * time.Second,
		},
		client: server.Client(),
	}
	status, err := s.lightningStatus(t.Context())
	if err != nil {
		t.Fatalf("lightning status: %v", err)
	}
	if status["last_block_timestamp"] != int64(1780509000) || status["synced_to_chain"] != true {
		t.Fatalf("unexpected status: %+v", status)
	}
	balance, err := s.lightningBalance(t.Context())
	if err != nil || !balance.Equal(decimal.RequireFromString("0.000025")) {
		t.Fatalf("unexpected balance %s err=%v", balance, err)
	}
	paymentRequest, err := s.newLightningInvoice(t.Context(), decimal.RequireFromString("0.00000042"))
	if err != nil || paymentRequest != "lnbc42" {
		t.Fatalf("unexpected invoice %s err=%v", paymentRequest, err)
	}
	if invoiceValue != float64(42) {
		t.Fatalf("unexpected invoice value: %#v", invoiceValue)
	}
	transfers, err := s.lightningTransfersByTx(t.Context(), invoiceHashHex)
	if err != nil {
		t.Fatalf("lightning transfers: %v", err)
	}
	if len(transfers) != 1 || transfers[0].Address != "lnbc42" || transfers[0].Amount != "0.00000042" || transfers[0].Confirmations != 999 {
		t.Fatalf("unexpected transfers: %+v", transfers)
	}
	payout, err := s.broadcastLightningPayout(t.Context(), "lnbc-dest")
	if err != nil {
		t.Fatalf("lightning payout: %v", err)
	}
	if len(payout.TxIDs) != 1 || payout.TxIDs[0] != paymentHashHex {
		t.Fatalf("unexpected payout: %+v", payout)
	}
	if !seenMacaroon {
		t.Fatalf("expected macaroon header")
	}
}
