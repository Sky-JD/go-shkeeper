package chainworker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func (s *Server) lightningStatus(ctx context.Context) (map[string]any, error) {
	var info struct {
		SyncedToChain       bool   `json:"synced_to_chain"`
		BestHeaderTimestamp string `json:"best_header_timestamp"`
		BlockHeight         int64  `json:"block_height"`
		Alias               string `json:"alias"`
		IdentityPubkey      string `json:"identity_pubkey"`
	}
	if err := s.lightningJSON(ctx, http.MethodGet, "/v1/getinfo", nil, &info); err != nil {
		return nil, err
	}
	ts, _ := parseInt64(info.BestHeaderTimestamp)
	if ts == 0 && info.SyncedToChain {
		ts = time.Now().Unix()
	}
	return map[string]any{
		"last_block_timestamp": ts,
		"synced_to_chain":      info.SyncedToChain,
		"block_height":         info.BlockHeight,
		"alias":                info.Alias,
		"identity_pubkey":      info.IdentityPubkey,
	}, nil
}

func (s *Server) lightningBalance(ctx context.Context) (decimal.Decimal, error) {
	var resp struct {
		Balance json.Number `json:"balance"`
	}
	if err := s.lightningJSON(ctx, http.MethodGet, "/v1/balance/channels", nil, &resp); err != nil {
		return decimal.Zero, err
	}
	balance, ok := decimalFromAny(resp.Balance)
	if !ok {
		return decimal.Zero, errors.New("lnd returned invalid channel balance")
	}
	return decimalFromBaseUnits(balance, 8), nil
}

func (s *Server) newLightningInvoice(ctx context.Context, amount decimal.Decimal) (string, error) {
	valueSat := amountToBaseUnits(amount, 8).Int64()
	req := map[string]any{
		"value":  valueSat,
		"expiry": int64Env("LIGHTNING_INVOICE_TTL", 60*60*24*7),
	}
	var resp lightningInvoiceResponse
	if err := s.lightningJSON(ctx, http.MethodPost, "/v1/invoices", req, &resp); err != nil {
		return "", err
	}
	inv, err := lightningInvoiceFromResponse(resp)
	if err != nil {
		return "", err
	}
	if inv.Value.IsZero() {
		inv.Value = decimal.NewFromInt(valueSat)
	}
	if s.store != nil {
		if err := s.store.UpsertLightningInvoice(ctx, inv); err != nil {
			return "", err
		}
	}
	return inv.PaymentRequest, nil
}

func (s *Server) lightningAddresses(ctx context.Context) ([]string, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListLightningPaymentRequests(ctx)
}

func (s *Server) lightningTransfersByTx(ctx context.Context, txid string) ([]transferResult, error) {
	inv, err := s.lightningInvoiceByHash(ctx, txid)
	if err != nil {
		return nil, err
	}
	confirmations := int64(0)
	if strings.EqualFold(inv.State, "SETTLED") {
		confirmations = 999
	}
	if inv.PaymentRequest == "" {
		return nil, fmt.Errorf("lightning invoice has no payment_request: %s", txid)
	}
	return []transferResult{{
		Address:       inv.PaymentRequest,
		Amount:        decimalFromBaseUnits(inv.Value, 8).String(),
		Confirmations: confirmations,
		Category:      "receive",
	}}, nil
}

func (s *Server) broadcastLightningPayout(ctx context.Context, destination string) (broadcastResult, error) {
	paymentRequest, err := s.lightningPaymentRequest(ctx, destination)
	if err != nil {
		return broadcastResult{}, err
	}
	var resp struct {
		PaymentHash  string `json:"payment_hash"`
		PaymentError string `json:"payment_error"`
	}
	if err := s.lightningJSON(ctx, http.MethodPost, "/v1/channels/transactions", map[string]any{"payment_request": paymentRequest}, &resp); err != nil {
		return broadcastResult{}, err
	}
	if resp.PaymentError != "" {
		return broadcastResult{}, errors.New(resp.PaymentError)
	}
	txid := lightningHashToHex(resp.PaymentHash)
	if txid == "" {
		return broadcastResult{}, errors.New("lnd returned no payment hash")
	}
	return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
}

func (s *Server) lightningFeeDepositAccount(ctx context.Context) (string, decimal.Decimal, error) {
	lnurl := firstNonEmpty(getenv("LIGHTNING_LNURL", ""), getenv("LNBITS_LNURL", ""))
	balance, err := s.lightningBalance(ctx)
	return lnurl, balance, err
}

func (s *Server) lightningInvoiceByHash(ctx context.Context, txid string) (LightningInvoice, error) {
	txid = strings.TrimSpace(txid)
	if txid == "" {
		return LightningInvoice{}, errors.New("r_hash is required")
	}
	if s.store != nil {
		if inv, err := s.store.LightningInvoice(ctx, txid); err == nil {
			if fresh, err := s.lightningInvoiceFromLND(ctx, txid); err == nil {
				fresh.PaymentRequest = firstNonEmpty(fresh.PaymentRequest, inv.PaymentRequest)
				if fresh.Value.IsZero() {
					fresh.Value = inv.Value
				}
				_ = s.store.UpsertLightningInvoice(ctx, fresh)
				return fresh, nil
			}
			return inv, nil
		}
	}
	return s.lightningInvoiceFromLND(ctx, txid)
}

func (s *Server) lightningInvoiceFromLND(ctx context.Context, rHash string) (LightningInvoice, error) {
	var resp lightningInvoiceResponse
	if err := s.lightningJSON(ctx, http.MethodGet, "/v1/invoice/"+rHash, nil, &resp); err != nil {
		return LightningInvoice{}, err
	}
	inv, err := lightningInvoiceFromResponse(resp)
	if err != nil {
		return LightningInvoice{}, err
	}
	if inv.RHash == "" {
		inv.RHash = strings.ToLower(rHash)
	}
	return inv, nil
}

func (s *Server) lightningPaymentRequest(ctx context.Context, destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("payment request is required")
	}
	if strings.HasPrefix(strings.ToLower(destination), "lnurl") {
		return s.lightningPaymentRequestFromLNURL(ctx, destination)
	}
	return destination, nil
}

func (s *Server) lightningPaymentRequestFromLNURL(ctx context.Context, lnurl string) (string, error) {
	baseURL := strings.TrimRight(firstNonEmpty(getenv("LNBITS_URL", ""), getenv("LIGHTNING_LNBITS_URL", "")), "/")
	apiKey := firstNonEmpty(getenv("LNBITS_ADMIN_KEY", ""), getenv("LNBITS_ADMIN_API_KEY", ""))
	if baseURL == "" || apiKey == "" {
		return "", errors.New("LNURL payout requires LNBITS_URL and LNBITS_ADMIN_KEY")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	var scan struct {
		Callback    string      `json:"callback"`
		MinSendable json.Number `json:"minSendable"`
	}
	if err := s.lightningExternalJSON(ctx, http.MethodPost, baseURL+"/api/v1/lnurlscan", apiKey, map[string]any{"lnurl": lnurl}, &scan); err != nil {
		return "", err
	}
	if scan.Callback == "" {
		return "", errors.New("LNbits returned no LNURL callback")
	}
	amount := firstNonEmpty(getenv("LIGHTNING_LNURL_MSAT", ""), scan.MinSendable.String())
	callback := scan.Callback
	if strings.Contains(callback, "?") {
		callback += "&amount=" + amount
	} else {
		callback += "?amount=" + amount
	}
	var pay struct {
		PaymentRequest string `json:"pr"`
	}
	if err := s.lightningExternalJSON(ctx, http.MethodGet, callback, "", nil, &pay); err != nil {
		return "", err
	}
	if pay.PaymentRequest == "" {
		return "", errors.New("LNURL callback returned no payment request")
	}
	return pay.PaymentRequest, nil
}

func (s *Server) lightningJSON(ctx context.Context, method string, path string, body any, out any) error {
	url := strings.TrimRight(normalizeHTTPURL(s.cfg.FullnodeURL), "/") + path
	macaroon, err := lightningMacaroonHex()
	if err != nil {
		return err
	}
	return s.lightningDoJSON(ctx, method, url, macaroon, body, out)
}

func (s *Server) lightningExternalJSON(ctx context.Context, method string, url string, apiKey string, body any, out any) error {
	return s.lightningDoJSON(ctx, method, url, apiKey, body, out)
}

func (s *Server) lightningDoJSON(ctx context.Context, method string, url string, authValue string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authValue != "" {
		if strings.Contains(url, "/api/v1/lnurlscan") {
			req.Header.Set("X-API-KEY", authValue)
		} else {
			req.Header.Set("Grpc-Metadata-macaroon", authValue)
		}
	}
	client := s.client
	if boolLike(getenv("LND_TLS_SKIP_VERIFY", "")) {
		client = &http.Client{Timeout: s.cfg.RequestTimeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, string(data))
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	return dec.Decode(out)
}

type lightningInvoiceResponse struct {
	RHash          string      `json:"r_hash"`
	PaymentRequest string      `json:"payment_request"`
	Value          json.Number `json:"value"`
	Expiry         string      `json:"expiry"`
	State          string      `json:"state"`
	CreationDate   string      `json:"creation_date"`
	SettleDate     string      `json:"settle_date"`
	Settled        bool        `json:"settled"`
}

func lightningInvoiceFromResponse(resp lightningInvoiceResponse) (LightningInvoice, error) {
	rHash := lightningHashToHex(resp.RHash)
	if rHash == "" {
		return LightningInvoice{}, errors.New("lnd invoice returned no r_hash")
	}
	value, ok := decimalFromAny(resp.Value)
	if !ok {
		value = decimal.Zero
	}
	state := resp.State
	if state == "" && resp.Settled {
		state = "SETTLED"
	}
	return LightningInvoice{
		RHash:          rHash,
		PaymentRequest: resp.PaymentRequest,
		Value:          value,
		Expiry:         resp.Expiry,
		State:          state,
		CreationDate:   resp.CreationDate,
		SettleDate:     resp.SettleDate,
	}, nil
}

func lightningHashToHex(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, err := hex.DecodeString(value); err == nil && len(value)%2 == 0 {
		return strings.ToLower(value)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return value
	}
	return hex.EncodeToString(raw)
}

func lightningMacaroonHex() (string, error) {
	if value := firstNonEmpty(getenv("LND_MACAROON_HEX", ""), getenv("LND_ADMIN_MACAROON_HEX", "")); value != "" {
		return value, nil
	}
	file := firstNonEmpty(getenv("LND_MACAROON_FILE", ""), defaultMacaroonFile())
	if file == "" {
		return "", nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes.TrimSpace(raw)), nil
}

func defaultMacaroonFile() string {
	shared := getenv("LND_SHARED_DIR", "")
	if shared == "" {
		return ""
	}
	network := getenv("LND_NETWORK", "mainnet")
	return filepath.Join(shared, "data", "chain", "bitcoin", network, "admin.macaroon")
}

func parseInt64(value string) (int64, error) {
	i, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return i.Truncate(0).BigInt().Int64(), nil
}

func boolLike(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func lightningAmountFromRequest(r *http.Request) decimal.Decimal {
	if r.Body == nil {
		return decimal.Zero
	}
	var req map[string]any
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return decimal.Zero
	}
	amount, ok := decimalFromAny(firstValue(req, "amount", "amount_crypto", "value"))
	if !ok || amount.LessThan(decimal.Zero) {
		return decimal.Zero
	}
	return amount
}
