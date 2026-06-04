package chainworker

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func tronAddressHexMust(t *testing.T, address string) string {
	t.Helper()
	value, err := tronAddressHex(address)
	if err != nil {
		t.Fatalf("tron address hex: %v", err)
	}
	return value
}

func timeNowMillis() int64 {
	return time.Now().UnixMilli()
}
