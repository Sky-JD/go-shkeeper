package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type CryptoRegistry struct {
	cfg              Config
	store            *Store
	logger           *slog.Logger
	httpClient       *http.Client
	payoutHTTPClient *http.Client
	modules          map[string]*CryptoModule
	cacheMu          sync.Mutex
	cacheUntil       time.Time
	cacheValue       map[string]any
}

type CryptoModule struct {
	Name           string
	DisplayName    string
	Network        string
	Precision      int32
	Adapter        string
	DefaultOn      bool
	HostEnv        string
	PortEnv        string
	UsernameEnv    string
	PasswordEnv    string
	DefaultHost    string
	DefaultPort    string
	StatusInterval int64
}

type ChainTransfer struct {
	Address       string
	Amount        decimal.Decimal
	Confirmations int
	Category      string
}

func NewCryptoRegistry(cfg Config, store *Store, logger *slog.Logger) *CryptoRegistry {
	reg := &CryptoRegistry{
		cfg:              cfg,
		store:            store,
		logger:           logger,
		httpClient:       &http.Client{Timeout: cfg.RequestTimeout},
		payoutHTTPClient: &http.Client{Timeout: cfg.PayoutRequestTimeout},
		modules:          map[string]*CryptoModule{},
	}
	for _, def := range cryptoDefinitions() {
		if reg.enabledByConfig(def) {
			module := def
			reg.modules[module.Name] = &module
		}
	}
	return reg
}

func CryptoDefinitions() []CryptoModule {
	defs := cryptoDefinitions()
	out := make([]CryptoModule, len(defs))
	copy(out, defs)
	return out
}

func (r *CryptoRegistry) EnsureCurrencies(ctx context.Context) error {
	for _, c := range r.Modules() {
		if err := r.store.EnsureWallet(ctx, c.Name, r.cfg.SuggestedWalletAPIKey); err != nil {
			return err
		}
		for _, fiat := range r.cfg.Fiats {
			if err := r.store.EnsureExchangeRate(ctx, c.Name, fiat); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *CryptoRegistry) Modules() []*CryptoModule {
	out := make([]*CryptoModule, 0, len(r.modules))
	for _, c := range r.modules {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *CryptoRegistry) Module(name string) (*CryptoModule, bool) {
	c, ok := r.modules[strings.ToUpper(name)]
	return c, ok
}

func (r *CryptoRegistry) AvailableCryptos(ctx context.Context) (map[string]any, error) {
	r.cacheMu.Lock()
	if time.Now().Before(r.cacheUntil) && r.cacheValue != nil {
		value := r.cacheValue
		r.cacheMu.Unlock()
		return value, nil
	}
	r.cacheMu.Unlock()

	filtered := make([]string, 0)
	cryptoList := make([]map[string]string, 0)
	for _, c := range r.Modules() {
		wallet, err := r.store.WalletByCrypto(ctx, c.Name)
		if err != nil || !wallet.Enabled {
			continue
		}
		status := r.Status(ctx, c)
		if status == "Offline" {
			continue
		}
		if r.cfg.DisableCryptoWhenLags && status != "Synced" {
			continue
		}
		filtered = append(filtered, c.Name)
		cryptoList = append(cryptoList, map[string]string{"name": c.Name, "display_name": c.DisplayName})
	}
	sort.Strings(filtered)
	sort.Slice(cryptoList, func(i, j int) bool { return cryptoList[i]["name"] < cryptoList[j]["name"] })
	value := map[string]any{"filtered": filtered, "crypto_list": cryptoList}

	r.cacheMu.Lock()
	r.cacheValue = value
	r.cacheUntil = time.Now().Add(60 * time.Second)
	r.cacheMu.Unlock()
	return value, nil
}

func (r *CryptoRegistry) Status(ctx context.Context, c *CryptoModule) string {
	switch c.Adapter {
	case "jsonrpc":
		var resp struct {
			Result struct {
				Headers              int64           `json:"headers"`
				Blocks               int64           `json:"blocks"`
				VerificationProgress decimal.Decimal `json:"verificationprogress"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := r.rpc(ctx, c, "getblockchaininfo", []any{}, &resp); err != nil || resp.Error != nil {
			return "Offline"
		}
		if resp.Result.Headers == resp.Result.Blocks {
			return "Synced"
		}
		progress := resp.Result.VerificationProgress.Mul(decimal.NewFromInt(100))
		return fmt.Sprintf("Sync In Progress (%s%%)", progress.Round(2).String())
	default:
		var payload map[string]any
		if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/status", nil, &payload); err != nil {
			return "Offline"
		}
		if delta, ok := decimalFromAny(payload["delta_blocks"]); ok {
			if delta.LessThanOrEqual(decimal.NewFromInt(12)) {
				return "Synced"
			}
			return fmt.Sprintf("Sync In Progress (%s blocks behind)", delta.Round(0).String())
		}
		if ts, ok := decimalFromAny(payload["last_block_timestamp"]); ok {
			now := decimal.NewFromInt(time.Now().Unix())
			if c.Network == "XRP" {
				ts = ts.Add(decimal.NewFromInt(946684800))
			}
			interval := decimal.NewFromInt(c.StatusInterval)
			if interval.IsZero() {
				interval = decimal.NewFromInt(12)
			}
			delta := now.Sub(ts).Abs()
			if delta.LessThan(interval.Mul(decimal.NewFromInt(10))) {
				return "Synced"
			}
			return fmt.Sprintf("Sync In Progress (%s blocks behind)", delta.Div(interval).Round(0).String())
		}
		return "Synced"
	}
}

func (r *CryptoRegistry) Balance(ctx context.Context, c *CryptoModule) (decimal.Decimal, string, string) {
	if c.Adapter == "jsonrpc" {
		if c.Name == "FIRO-SPARK" {
			return r.firoSparkBalance(ctx, c)
		}
		var resp struct {
			Result decimal.Decimal `json:"result"`
			Error  any             `json:"error"`
		}
		if err := r.rpc(ctx, c, "getbalance", []any{"*", 1}, &resp); err != nil || resp.Error != nil {
			return decimal.Zero, "wallet_api", fmt.Sprint(err)
		}
		return resp.Result, "wallet_api", ""
	}
	var payload map[string]any
	if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/balance", nil, &payload); err != nil {
		return decimal.Zero, "wallet_api", err.Error()
	}
	source := strings.TrimSpace(anyString(payload["balance_source"]))
	if source == "" {
		source = "wallet_api"
	}
	errText := strings.TrimSpace(anyString(payload["balance_error"]))
	if amount, ok := decimalFromAny(payload["balance"]); ok {
		return amount, source, errText
	}
	return decimal.Zero, source, firstNonEmptyString(errText, "balance field is missing")
}

func (r *CryptoRegistry) ActivationStatus(ctx context.Context, c *CryptoModule) map[string]any {
	if c.Adapter == "jsonrpc" || c.Network != "TRX" {
		return map[string]any{"required": false, "module": c.Network, "crypto": c.Name}
	}
	var payload map[string]any
	path := "/" + c.Name + "/activation-status"
	if err := r.backendJSON(ctx, c, http.MethodGet, path, nil, &payload); err != nil {
		if err := r.backendJSON(ctx, c, http.MethodPost, path, nil, &payload); err != nil {
			return map[string]any{
				"required": true,
				"module":   c.Network,
				"crypto":   c.Name,
				"error":    err.Error(),
			}
		}
	}
	return payload
}

func (r *CryptoRegistry) MakeAddress(ctx context.Context, c *CryptoModule, amount decimal.Decimal) (string, error) {
	if c.Adapter == "jsonrpc" {
		if c.Name == "FIRO-SPARK" {
			return r.firoSparkAddress(ctx, c)
		}
		var resp struct {
			Result string `json:"result"`
			Error  any    `json:"error"`
		}
		if err := r.rpc(ctx, c, "getnewaddress", []any{}, &resp); err != nil {
			return "", err
		}
		if resp.Error != nil {
			return "", fmt.Errorf("rpc getnewaddress: %v", resp.Error)
		}
		return resp.Result, nil
	}
	path := "/" + c.Name + "/generate-address"
	var body any
	if c.Name == "BTC-LIGHTNING" {
		body = map[string]string{"amount": amount.String()}
	}
	var payload map[string]any
	if err := r.backendJSON(ctx, c, http.MethodPost, path, body, &payload); err != nil {
		return "", err
	}
	for _, key := range []string{"address", "base58check_address", "payment_request"} {
		if v, ok := payload[key].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", errors.New("address field is missing in backend response")
}

func (r *CryptoRegistry) TransfersByTx(ctx context.Context, c *CryptoModule, txid string) ([]ChainTransfer, error) {
	if c.Adapter == "jsonrpc" {
		if c.Name == "FIRO-SPARK" {
			return r.firoSparkTransfersByTx(ctx, c, txid)
		}
		if c.Name == "FIRO" {
			return r.firoTransfersByTx(ctx, c, txid)
		}
		var resp struct {
			Result struct {
				Confirmations int `json:"confirmations"`
				Details       []struct {
					Address  string          `json:"address"`
					Amount   decimal.Decimal `json:"amount"`
					Category string          `json:"category"`
				} `json:"details"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := r.rpc(ctx, c, "gettransaction", []any{txid}, &resp); err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc gettransaction: %v", resp.Error)
		}
		out := make([]ChainTransfer, 0, len(resp.Result.Details))
		for _, detail := range resp.Result.Details {
			if detail.Address == "" {
				continue
			}
			out = append(out, ChainTransfer{Address: detail.Address, Amount: detail.Amount, Confirmations: resp.Result.Confirmations, Category: detail.Category})
		}
		return out, nil
	}
	var payload any
	if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/transaction/"+txid, nil, &payload); err != nil {
		return nil, err
	}
	return parseTransfers(payload)
}

func (r *CryptoRegistry) Confirmations(ctx context.Context, c *CryptoModule, txid string) (int, error) {
	txs, err := r.TransfersByTx(ctx, c, txid)
	if err != nil {
		return 0, err
	}
	if len(txs) == 0 {
		return 0, errors.New("transaction has no transfer details")
	}
	return txs[0].Confirmations, nil
}

func (r *CryptoRegistry) Payout(ctx context.Context, c *CryptoModule, destination string, amount decimal.Decimal, fee string) (map[string]any, error) {
	if c.Adapter == "jsonrpc" {
		if c.Name == "FIRO-SPARK" {
			return r.firoSparkPayout(ctx, c, destination, amount, fee)
		}
		var resp map[string]any
		if fee != "" && fee != "0" {
			btcPerKB, _ := decimal.NewFromString(fee)
			btcPerKB = btcPerKB.Div(decimal.NewFromInt(100000))
			_ = r.rpc(ctx, c, "settxfee", []any{btcPerKB.StringFixed(8)}, &resp)
		}
		if err := r.rpc(ctx, c, "sendtoaddress", []any{destination, amount.String(), "", "", false}, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	}
	path := "/" + c.Name + "/payout/" + url.PathEscape(destination) + "/" + url.PathEscape(amount.String())
	if backendPayoutUsesPathFee(c) && fee != "" {
		path += "/" + url.PathEscape(fee)
	}
	var payload map[string]any
	if err := r.backendJSONWithClient(ctx, r.payoutHTTPClient, c, http.MethodPost, path, nil, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func backendPayoutUsesPathFee(c *CryptoModule) bool {
	switch c.Name {
	case "BTC", "BTC-LIGHTNING", "LTC", "DOGE", "FIRO", "FIRO-SPARK":
		return true
	default:
		return false
	}
}

func (r *CryptoRegistry) MultiPayout(ctx context.Context, c *CryptoModule, payouts any) (map[string]any, error) {
	var payload map[string]any
	if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/multipayout", payouts, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (r *CryptoRegistry) Task(ctx context.Context, c *CryptoModule, id string) (map[string]any, error) {
	var payload map[string]any
	if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/task/"+id, nil, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (r *CryptoRegistry) FeeDepositAddress(ctx context.Context, c *CryptoModule) (string, error) {
	if c.Adapter == "jsonrpc" {
		return "", fmt.Errorf("%s fee deposit address is not supported by direct JSON-RPC adapter", c.Name)
	}
	var payload map[string]any
	if err := r.backendJSON(ctx, c, http.MethodGet, "/"+c.Name+"/fee-deposit-account", nil, &payload); err != nil {
		if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/fee-deposit-account", nil, &payload); err != nil {
			return "", err
		}
	}
	for _, key := range []string{"account", "fee_deposit_address", "address"} {
		if value, ok := payload[key].(string); ok && value != "" {
			return value, nil
		}
	}
	return "", errors.New("fee deposit account response has no address")
}

func (r *CryptoRegistry) Spendable(ctx context.Context, c *CryptoModule) (map[string]any, error) {
	if c.Adapter == "jsonrpc" {
		balance, _, errText := r.Balance(ctx, c)
		if errText != "" {
			return nil, errors.New(errText)
		}
		return map[string]any{
			"status":             "success",
			"crypto":             c.Name,
			"balance":            balance.String(),
			"max_single_account": balance.String(),
		}, nil
	}
	var payload map[string]any
	path := "/" + c.Name + "/spendable"
	if err := r.backendJSON(ctx, c, http.MethodGet, path, nil, &payload); err != nil {
		if err := r.backendJSON(ctx, c, http.MethodPost, path, nil, &payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func (r *CryptoRegistry) EstimateTxFee(ctx context.Context, c *CryptoModule, amount decimal.Decimal, address string) (map[string]any, error) {
	if c.Adapter == "jsonrpc" {
		var resp struct {
			Result struct {
				FeeRate decimal.Decimal `json:"feerate"`
				Errors  []string        `json:"errors"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := r.rpc(ctx, c, "estimatesmartfee", []any{6}, &resp); err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc estimatesmartfee: %v", resp.Error)
		}
		if !resp.Result.FeeRate.GreaterThan(decimal.Zero) {
			return nil, fmt.Errorf("fee estimation returned no feerate: %v", resp.Result.Errors)
		}
		satPerByte := resp.Result.FeeRate.Mul(decimal.NewFromInt(100000)).Round(0)
		return map[string]any{
			"status":      "success",
			"amount":      amount.String(),
			"fee":         resp.Result.FeeRate.String(),
			"fee_satoshi": satPerByte.String(),
			"address":     address,
		}, nil
	}
	if !supportsBackendFeeEstimate(c) {
		return map[string]any{
			"status":  "success",
			"amount":  amount.String(),
			"fee":     "0",
			"address": address,
		}, nil
	}
	path := "/" + c.Name + "/calc-tx-fee/" + url.PathEscape(amount.String())
	if address != "" {
		path += "?address=" + url.QueryEscape(address)
	}
	var payload map[string]any
	if err := r.backendJSON(ctx, c, http.MethodGet, path, nil, &payload); err != nil {
		if err := r.backendJSON(ctx, c, http.MethodPost, path, nil, &payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func supportsBackendFeeEstimate(c *CryptoModule) bool {
	if c.Network == "TRX" {
		return true
	}
	switch strings.ToUpper(c.Network) {
	case "ETH", "BNB", "MATIC", "AVAX", "ARBETH", "OPETH":
		return true
	}
	switch c.Name {
	case "BTC", "LTC", "DOGE", "FIRO", "FIRO-SPARK":
		return true
	default:
		return false
	}
}

func (r *CryptoRegistry) ServerDetails(ctx context.Context, c *CryptoModule) map[string]any {
	user, pass := r.moduleAuth(ctx, c)
	return map[string]any{"status": "success", "key": user + ":" + pass, "host": r.moduleHost(ctx, c)}
}

func (r *CryptoRegistry) Backup(ctx context.Context, c *CryptoModule, includePrivateKey bool) ([]byte, string, error) {
	if c.Adapter == "jsonrpc" {
		payload := map[string]any{
			"status":  "error",
			"message": c.Name + " direct JSON-RPC wallet backup must be run on the wallet node",
		}
		data, err := json.Marshal(payload)
		return data, "application/json", err
	}
	path := "/" + c.Name + "/dump"
	if includePrivateKey {
		path += "?include_private_key=1"
	}
	data, contentType, err := r.backendRaw(ctx, c, http.MethodGet, path, nil)
	if err != nil {
		data, contentType, err = r.backendRaw(ctx, c, http.MethodPost, path, nil)
	}
	return data, contentType, err
}

func (r *CryptoRegistry) AllAddresses(ctx context.Context, c *CryptoModule) (any, error) {
	if c.Adapter == "jsonrpc" {
		if c.Name == "FIRO-SPARK" {
			return r.firoSparkAddresses(ctx, c)
		}
		var resp struct {
			Result []struct {
				Address string `json:"address"`
			} `json:"result"`
		}
		if err := r.rpc(ctx, c, "listreceivedbyaddress", []any{0, true}, &resp); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(resp.Result))
		for _, row := range resp.Result {
			out = append(out, row.Address)
		}
		return out, nil
	}
	var payload any
	path := "/" + c.Name + "/get_all_addresses"
	if c.Network == "TRX" {
		path = "/" + c.Name + "/addresses"
	}
	if err := r.backendJSON(ctx, c, http.MethodGet, path, nil, &payload); err != nil {
		if err := r.backendJSON(ctx, c, http.MethodPost, "/"+c.Name+"/get_all_addresses", nil, &payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func (r *CryptoRegistry) backendRaw(ctx context.Context, c *CryptoModule, method string, path string, body any) ([]byte, string, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+r.moduleHost(ctx, c)+path, reader)
	if err != nil {
		return nil, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	user, pass := r.moduleAuth(ctx, c)
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", readErr
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("backend %s returned %d: %s", req.URL.String(), resp.StatusCode, string(data))
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	return data, contentType, nil
}

func (r *CryptoRegistry) firoSparkBalance(ctx context.Context, c *CryptoModule) (decimal.Decimal, string, string) {
	var resp struct {
		Result struct {
			AvailableBalance json.Number `json:"availableBalance"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := r.rpc(ctx, c, "getsparkbalance", []any{}, &resp); err != nil || resp.Error != nil {
		return decimal.Zero, "wallet_api", fmt.Sprint(err)
	}
	atomic, ok := decimalFromAny(resp.Result.AvailableBalance)
	if !ok {
		return decimal.Zero, "wallet_api", "availableBalance field is missing"
	}
	return decimalFromBaseUnits(atomic, 8), "wallet_api", ""
}

func (r *CryptoRegistry) firoSparkAddress(ctx context.Context, c *CryptoModule) (string, error) {
	var resp struct {
		Result []string `json:"result"`
		Error  any      `json:"error"`
	}
	if err := r.rpc(ctx, c, "getnewsparkaddress", []any{}, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("rpc getnewsparkaddress: %v", resp.Error)
	}
	if len(resp.Result) == 0 || resp.Result[0] == "" {
		return "", errors.New("rpc getnewsparkaddress returned no address")
	}
	return resp.Result[0], nil
}

func (r *CryptoRegistry) firoSparkPayout(ctx context.Context, c *CryptoModule, destination string, amount decimal.Decimal, fee string) (map[string]any, error) {
	var resp map[string]any
	if fee != "" && fee != "0" {
		btcPerKB, _ := decimal.NewFromString(fee)
		btcPerKB = btcPerKB.Div(decimal.NewFromInt(100000))
		_ = r.rpc(ctx, c, "settxfee", []any{btcPerKB.StringFixed(8)}, &resp)
	}
	amountFloat, _ := amount.Float64()
	params := []any{map[string]any{
		destination: map[string]any{
			"amount":      amountFloat,
			"memo":        "",
			"subtractFee": false,
		},
	}}
	if err := r.rpc(ctx, c, "spendspark", params, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (r *CryptoRegistry) firoTransfersByTx(ctx context.Context, c *CryptoModule, txid string) ([]ChainTransfer, error) {
	transfers, err := r.bitcoinLikeTransfersByTx(ctx, c, txid)
	if err != nil {
		return nil, err
	}
	out := make([]ChainTransfer, 0, len(transfers))
	for _, transfer := range transfers {
		if transfer.Address != "" && len(transfer.Address) < 140 {
			out = append(out, transfer)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("transaction %s has no regular FIRO transfers", txid)
	}
	return out, nil
}

func (r *CryptoRegistry) firoSparkTransfersByTx(ctx context.Context, c *CryptoModule, txid string) ([]ChainTransfer, error) {
	var sparkResp struct {
		Result []struct {
			Address string          `json:"address"`
			Amount  decimal.Decimal `json:"amount"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := r.rpc(ctx, c, "getsparkcoinaddr", []any{txid}, &sparkResp); err != nil {
		return nil, err
	}
	if sparkResp.Error != nil {
		return nil, fmt.Errorf("rpc getsparkcoinaddr: %v", sparkResp.Error)
	}
	if len(sparkResp.Result) == 0 {
		return nil, fmt.Errorf("transaction %s has no FIRO-SPARK transfers", txid)
	}
	txResp, err := r.bitcoinLikeTransaction(ctx, c, txid)
	if err != nil {
		return nil, err
	}
	out := make([]ChainTransfer, 0, len(sparkResp.Result))
	for i, transfer := range sparkResp.Result {
		if transfer.Address == "" {
			continue
		}
		category := "receive"
		amount := transfer.Amount
		if i < len(txResp.Result.Details) {
			detail := txResp.Result.Details[i]
			if detail.Category == "spend" {
				category = "send"
				amount = detail.Amount
			} else if detail.Category != "" {
				category = detail.Category
			}
		}
		out = append(out, ChainTransfer{Address: transfer.Address, Amount: amount, Confirmations: txResp.Result.Confirmations, Category: category})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("transaction %s has no FIRO-SPARK transfer details", txid)
	}
	return out, nil
}

func (r *CryptoRegistry) firoSparkAddresses(ctx context.Context, c *CryptoModule) ([]string, error) {
	var resp struct {
		Result map[string]string `json:"result"`
		Error  any               `json:"error"`
	}
	if err := r.rpc(ctx, c, "getallsparkaddresses", []any{}, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc getallsparkaddresses: %v", resp.Error)
	}
	out := make([]string, 0, len(resp.Result))
	for _, address := range resp.Result {
		if address != "" {
			out = append(out, address)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (r *CryptoRegistry) bitcoinLikeTransfersByTx(ctx context.Context, c *CryptoModule, txid string) ([]ChainTransfer, error) {
	resp, err := r.bitcoinLikeTransaction(ctx, c, txid)
	if err != nil {
		return nil, err
	}
	out := make([]ChainTransfer, 0, len(resp.Result.Details))
	for _, detail := range resp.Result.Details {
		if detail.Address == "" {
			continue
		}
		out = append(out, ChainTransfer{Address: detail.Address, Amount: detail.Amount, Confirmations: resp.Result.Confirmations, Category: detail.Category})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("transaction %s has no transfer details", txid)
	}
	return out, nil
}

func (r *CryptoRegistry) bitcoinLikeTransaction(ctx context.Context, c *CryptoModule, txid string) (bitcoinLikeTransactionResponse, error) {
	var resp bitcoinLikeTransactionResponse
	if err := r.rpc(ctx, c, "gettransaction", []any{txid}, &resp); err != nil {
		return resp, err
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("rpc gettransaction: %v", resp.Error)
	}
	return resp, nil
}

type bitcoinLikeTransactionResponse struct {
	Result struct {
		Confirmations int `json:"confirmations"`
		Details       []struct {
			Address  string          `json:"address"`
			Amount   decimal.Decimal `json:"amount"`
			Category string          `json:"category"`
		} `json:"details"`
	} `json:"result"`
	Error any `json:"error"`
}

func (r *CryptoRegistry) backendJSON(ctx context.Context, c *CryptoModule, method string, path string, body any, out any) error {
	return r.backendJSONWithClient(ctx, r.httpClient, c, method, path, body, out)
}

func (r *CryptoRegistry) backendJSONWithClient(ctx context.Context, client *http.Client, c *CryptoModule, method string, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+r.moduleHost(ctx, c)+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	user, pass := r.moduleAuth(ctx, c)
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	if client == nil {
		client = r.httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("backend %s returned %d: %s", req.URL.String(), resp.StatusCode, string(data))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (r *CryptoRegistry) rpc(ctx context.Context, c *CryptoModule, method string, params []any, out any) error {
	payload := map[string]any{"jsonrpc": "1.0", "id": "shkeeper-go", "method": method, "params": params}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+r.moduleHost(ctx, c), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	user, pass := r.moduleAuth(ctx, c)
	req.SetBasicAuth(user, pass)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("rpc %s returned %d: %s", method, resp.StatusCode, string(data))
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	return dec.Decode(out)
}

func (c *CryptoModule) host() string {
	host := os.Getenv(c.HostEnv)
	if host == "" {
		host = c.DefaultHost
	}
	port := os.Getenv(c.PortEnv)
	if port == "" {
		port = c.DefaultPort
	}
	if port == "" || strings.Contains(host, ":") {
		return host
	}
	return host + ":" + port
}

func (c *CryptoModule) auth() (string, string) {
	user := os.Getenv(c.UsernameEnv)
	pass := os.Getenv(c.PasswordEnv)
	if user == "" {
		user = "shkeeper"
	}
	if pass == "" {
		pass = "shkeeper"
	}
	return user, pass
}

func (r *CryptoRegistry) moduleHost(ctx context.Context, c *CryptoModule) string {
	if r.store != nil {
		if host, err := r.store.WalletServerHost(ctx, c.Name); err == nil {
			if host = strings.TrimSpace(host); host != "" {
				return host
			}
		}
	}
	return c.host()
}

func (r *CryptoRegistry) moduleAuth(ctx context.Context, c *CryptoModule) (string, string) {
	if r.store != nil {
		if wallet, err := r.store.WalletByCrypto(ctx, c.Name); err == nil {
			if key := strings.TrimSpace(nullStringValue(wallet.ServerKey)); key != "" {
				return parseServerAuthKey(key, c)
			}
		}
	}
	return c.auth()
}

func parseServerAuthKey(key string, c *CryptoModule) (string, string) {
	key = strings.TrimSpace(key)
	if user, pass, ok := strings.Cut(key, ":"); ok {
		return strings.TrimSpace(user), strings.TrimSpace(pass)
	}
	user, _ := c.auth()
	return user, key
}

func (r *CryptoRegistry) enabledByConfig(c CryptoModule) bool {
	envKey := strings.ReplaceAll(c.Name, "-", "_") + "_WALLET"
	value := strings.ToLower(os.Getenv(envKey))
	if value == "disabled" {
		return false
	}
	if len(r.cfg.CryptoAllowList) > 0 {
		for _, item := range r.cfg.CryptoAllowList {
			if item == c.Name {
				return true
			}
		}
		return false
	}
	if c.DefaultOn {
		return true
	}
	return value == "enabled"
}

func cryptoDefinitions() []CryptoModule {
	return []CryptoModule{
		{Name: "BTC", DisplayName: "Bitcoin", Network: "BTC", Precision: 8, Adapter: "backend", DefaultOn: true, HostEnv: "BTC_API_SERVER_HOST", PortEnv: "BTC_SERVER_PORT", UsernameEnv: "BTC_USERNAME", PasswordEnv: "BTC_PASSWORD", DefaultHost: "btc-worker", DefaultPort: "6000", StatusInterval: 600},
		{Name: "LTC", DisplayName: "Litecoin", Network: "LTC", Precision: 8, Adapter: "backend", DefaultOn: true, HostEnv: "LTC_API_SERVER_HOST", PortEnv: "LTC_SERVER_PORT", UsernameEnv: "LTC_USERNAME", PasswordEnv: "LTC_PASSWORD", DefaultHost: "ltc-worker", DefaultPort: "6000", StatusInterval: 150},
		{Name: "DOGE", DisplayName: "Dogecoin", Network: "DOGE", Precision: 8, Adapter: "backend", DefaultOn: true, HostEnv: "DOGE_API_SERVER_HOST", PortEnv: "DOGE_SERVER_PORT", UsernameEnv: "DOGE_USERNAME", PasswordEnv: "DOGE_PASSWORD", DefaultHost: "doge-worker", DefaultPort: "6000", StatusInterval: 60},
		{Name: "FIRO", DisplayName: "Firo", Network: "FIRO", Precision: 8, Adapter: "backend", HostEnv: "FIRO_API_SERVER_HOST", PortEnv: "FIRO_SERVER_PORT", UsernameEnv: "FIRO_USERNAME", PasswordEnv: "FIRO_PASSWORD", DefaultHost: "firo-worker", DefaultPort: "6000"},
		{Name: "FIRO-SPARK", DisplayName: "Firo Spark", Network: "FIRO", Precision: 8, Adapter: "backend", HostEnv: "FIRO_API_SERVER_HOST", PortEnv: "FIRO_SERVER_PORT", UsernameEnv: "FIRO_USERNAME", PasswordEnv: "FIRO_PASSWORD", DefaultHost: "firo-worker", DefaultPort: "6000"},
		{Name: "ETH", DisplayName: "Ethereum", Network: "ETH", Precision: 8, Adapter: "backend", HostEnv: "ETHEREUM_API_SERVER_HOST", PortEnv: "ETHEREUM_SERVER_PORT", UsernameEnv: "ETH_USERNAME", PasswordEnv: "ETH_PASSWORD", DefaultHost: "eth-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "ETH-USDT", DisplayName: "ERC20 USDT", Network: "ETH", Precision: 8, Adapter: "backend", HostEnv: "ETHEREUM_API_SERVER_HOST", PortEnv: "ETHEREUM_SERVER_PORT", UsernameEnv: "ETH_USERNAME", PasswordEnv: "ETH_PASSWORD", DefaultHost: "eth-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "ETH-USDC", DisplayName: "ERC20 USDC", Network: "ETH", Precision: 8, Adapter: "backend", HostEnv: "ETHEREUM_API_SERVER_HOST", PortEnv: "ETHEREUM_SERVER_PORT", UsernameEnv: "ETH_USERNAME", PasswordEnv: "ETH_PASSWORD", DefaultHost: "eth-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "ETH-PYUSD", DisplayName: "ERC20 PYUSD", Network: "ETH", Precision: 8, Adapter: "backend", HostEnv: "ETHEREUM_API_SERVER_HOST", PortEnv: "ETHEREUM_SERVER_PORT", UsernameEnv: "ETH_USERNAME", PasswordEnv: "ETH_PASSWORD", DefaultHost: "eth-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "TRX", DisplayName: "Tron TRX", Network: "TRX", Precision: 8, Adapter: "backend", HostEnv: "TRON_API_SERVER_HOST", PortEnv: "TRON_API_SERVER_PORT", UsernameEnv: "TRX_USERNAME", PasswordEnv: "TRX_PASSWORD", DefaultHost: "tron-worker", DefaultPort: "6000", StatusInterval: 3},
		{Name: "USDT", DisplayName: "TRC20 USDT", Network: "TRX", Precision: 8, Adapter: "backend", HostEnv: "TRON_API_SERVER_HOST", PortEnv: "TRON_API_SERVER_PORT", UsernameEnv: "USDT_USERNAME", PasswordEnv: "USDT_PASSWORD", DefaultHost: "tron-worker", DefaultPort: "6000", StatusInterval: 3},
		{Name: "USDC", DisplayName: "TRC20 USDC", Network: "TRX", Precision: 8, Adapter: "backend", HostEnv: "TRON_API_SERVER_HOST", PortEnv: "TRON_API_SERVER_PORT", UsernameEnv: "USDC_USERNAME", PasswordEnv: "USDC_PASSWORD", DefaultHost: "tron-worker", DefaultPort: "6000", StatusInterval: 3},
		{Name: "BNB", DisplayName: "BNB", Network: "BNB", Precision: 8, Adapter: "backend", HostEnv: "BNB_API_SERVER_HOST", PortEnv: "BNB_SERVER_PORT", UsernameEnv: "BNB_USERNAME", PasswordEnv: "BNB_PASSWORD", DefaultHost: "bnb-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "BNB-USDT", DisplayName: "BEP20 USDT", Network: "BNB", Precision: 8, Adapter: "backend", HostEnv: "BNB_API_SERVER_HOST", PortEnv: "BNB_SERVER_PORT", UsernameEnv: "BNB_USERNAME", PasswordEnv: "BNB_PASSWORD", DefaultHost: "bnb-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "BNB-USDC", DisplayName: "BEP20 USDC", Network: "BNB", Precision: 8, Adapter: "backend", HostEnv: "BNB_API_SERVER_HOST", PortEnv: "BNB_SERVER_PORT", UsernameEnv: "BNB_USERNAME", PasswordEnv: "BNB_PASSWORD", DefaultHost: "bnb-worker", DefaultPort: "6000", StatusInterval: 12},
		{Name: "MATIC", DisplayName: "Polygon MATIC", Network: "MATIC", Precision: 8, Adapter: "backend", HostEnv: "POLYGON_API_SERVER_HOST", PortEnv: "POLYGON_SERVER_PORT", UsernameEnv: "POLYGON_USERNAME", PasswordEnv: "POLYGON_PASSWORD", DefaultHost: "polygon-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "POLYGON-USDT", DisplayName: "POLYGON ERC20 USDT", Network: "MATIC", Precision: 8, Adapter: "backend", HostEnv: "POLYGON_API_SERVER_HOST", PortEnv: "POLYGON_SERVER_PORT", UsernameEnv: "POLYGON_USERNAME", PasswordEnv: "POLYGON_PASSWORD", DefaultHost: "polygon-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "POLYGON-USDC", DisplayName: "POLYGON ERC20 USDC", Network: "MATIC", Precision: 8, Adapter: "backend", HostEnv: "POLYGON_API_SERVER_HOST", PortEnv: "POLYGON_SERVER_PORT", UsernameEnv: "POLYGON_USERNAME", PasswordEnv: "POLYGON_PASSWORD", DefaultHost: "polygon-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "AVAX", DisplayName: "Avalanche AVAX", Network: "AVAX", Precision: 8, Adapter: "backend", HostEnv: "AVALANCHE_API_SERVER_HOST", PortEnv: "AVALANCHE_SERVER_PORT", UsernameEnv: "AVALANCHE_USERNAME", PasswordEnv: "AVALANCHE_PASSWORD", DefaultHost: "avalanche-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "AVALANCHE-USDT", DisplayName: "AVALANCHE ERC20 USDT", Network: "AVAX", Precision: 8, Adapter: "backend", HostEnv: "AVALANCHE_API_SERVER_HOST", PortEnv: "AVALANCHE_SERVER_PORT", UsernameEnv: "AVALANCHE_USERNAME", PasswordEnv: "AVALANCHE_PASSWORD", DefaultHost: "avalanche-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "AVALANCHE-USDC", DisplayName: "AVALANCHE ERC20 USDC", Network: "AVAX", Precision: 8, Adapter: "backend", HostEnv: "AVALANCHE_API_SERVER_HOST", PortEnv: "AVALANCHE_SERVER_PORT", UsernameEnv: "AVALANCHE_USERNAME", PasswordEnv: "AVALANCHE_PASSWORD", DefaultHost: "avalanche-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "SOL", DisplayName: "Solana", Network: "SOL", Precision: 8, Adapter: "backend", HostEnv: "SOLANA_API_SERVER_HOST", PortEnv: "SOLANA_SERVER_PORT", UsernameEnv: "SOLANA_USERNAME", PasswordEnv: "SOLANA_PASSWORD", DefaultHost: "solana-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "SOLANA-USDT", DisplayName: "SOLANA SPL USDT", Network: "SOL", Precision: 8, Adapter: "backend", HostEnv: "SOLANA_API_SERVER_HOST", PortEnv: "SOLANA_SERVER_PORT", UsernameEnv: "SOLANA_USERNAME", PasswordEnv: "SOLANA_PASSWORD", DefaultHost: "solana-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "SOLANA-USDC", DisplayName: "SOLANA SPL USDC", Network: "SOL", Precision: 8, Adapter: "backend", HostEnv: "SOLANA_API_SERVER_HOST", PortEnv: "SOLANA_SERVER_PORT", UsernameEnv: "SOLANA_USERNAME", PasswordEnv: "SOLANA_PASSWORD", DefaultHost: "solana-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "SOLANA-PYUSD", DisplayName: "SOLANA SPL PYUSD", Network: "SOL", Precision: 8, Adapter: "backend", HostEnv: "SOLANA_API_SERVER_HOST", PortEnv: "SOLANA_SERVER_PORT", UsernameEnv: "SOLANA_USERNAME", PasswordEnv: "SOLANA_PASSWORD", DefaultHost: "solana-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "XRP", DisplayName: "XRP", Network: "XRP", Precision: 8, Adapter: "backend", HostEnv: "XRP_API_SERVER_HOST", PortEnv: "XRP_SERVER_PORT", UsernameEnv: "XRP_USERNAME", PasswordEnv: "XRP_PASSWORD", DefaultHost: "xrp-worker", DefaultPort: "6000", StatusInterval: 4},
		{Name: "BTC-LIGHTNING", DisplayName: "BTC Lightning", Network: "BTC", Precision: 8, Adapter: "backend", HostEnv: "BTC_LIGHTNING_API_SERVER_HOST", PortEnv: "BTC_LIGHTNING_SERVER_PORT", UsernameEnv: "BTC_LIGHTNING_USERNAME", PasswordEnv: "BTC_LIGHTNING_PASSWORD", DefaultHost: "btc-lightning-worker", DefaultPort: "6000", StatusInterval: 600},
		{Name: "ARBETH", DisplayName: "Arbitrum ETH", Network: "ARBETH", Precision: 8, Adapter: "backend", HostEnv: "ARBITRUM_API_SERVER_HOST", PortEnv: "ARBITRUM_SERVER_PORT", UsernameEnv: "ARB_USERNAME", PasswordEnv: "ARB_PASSWORD", DefaultHost: "arbitrum-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "ARB-USDC", DisplayName: "ARBITRUM ERC20 USDC", Network: "ARBETH", Precision: 8, Adapter: "backend", HostEnv: "ARBITRUM_API_SERVER_HOST", PortEnv: "ARBITRUM_SERVER_PORT", UsernameEnv: "ARB_USERNAME", PasswordEnv: "ARB_PASSWORD", DefaultHost: "arbitrum-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "ARB-PYUSD", DisplayName: "ARBITRUM ERC20 PYUSD", Network: "ARBETH", Precision: 8, Adapter: "backend", HostEnv: "ARBITRUM_API_SERVER_HOST", PortEnv: "ARBITRUM_SERVER_PORT", UsernameEnv: "ARB_USERNAME", PasswordEnv: "ARB_PASSWORD", DefaultHost: "arbitrum-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "ARB-TOKEN", DisplayName: "ARBITRUM ERC20 TOKEN", Network: "ARBETH", Precision: 8, Adapter: "backend", HostEnv: "ARBITRUM_API_SERVER_HOST", PortEnv: "ARBITRUM_SERVER_PORT", UsernameEnv: "ARB_USERNAME", PasswordEnv: "ARB_PASSWORD", DefaultHost: "arbitrum-worker", DefaultPort: "6000", StatusInterval: 2},
		{Name: "OPETH", DisplayName: "Optimism ETH", Network: "OPETH", Precision: 8, Adapter: "backend", HostEnv: "OPTIMISM_API_SERVER_HOST", PortEnv: "OPTIMISM_SERVER_PORT", UsernameEnv: "OP_USERNAME", PasswordEnv: "OP_PASSWORD", DefaultHost: "optimism-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "OP-USDT", DisplayName: "OPTIMISM ERC20 USDT", Network: "OPETH", Precision: 8, Adapter: "backend", HostEnv: "OPTIMISM_API_SERVER_HOST", PortEnv: "OPTIMISM_SERVER_PORT", UsernameEnv: "OP_USERNAME", PasswordEnv: "OP_PASSWORD", DefaultHost: "optimism-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "OP-USDC", DisplayName: "OPTIMISM ERC20 USDC", Network: "OPETH", Precision: 8, Adapter: "backend", HostEnv: "OPTIMISM_API_SERVER_HOST", PortEnv: "OPTIMISM_SERVER_PORT", UsernameEnv: "OP_USERNAME", PasswordEnv: "OP_PASSWORD", DefaultHost: "optimism-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "OP-TOKEN", DisplayName: "OPTIMISM ERC20 TOKEN", Network: "OPETH", Precision: 8, Adapter: "backend", HostEnv: "OPTIMISM_API_SERVER_HOST", PortEnv: "OPTIMISM_SERVER_PORT", UsernameEnv: "OP_USERNAME", PasswordEnv: "OP_PASSWORD", DefaultHost: "optimism-worker", DefaultPort: "6000", StatusInterval: 1},
		{Name: "XMR", DisplayName: "Monero XMR", Network: "XMR", Precision: 12, Adapter: "backend", HostEnv: "MONERO_API_SERVER_HOST", PortEnv: "MONERO_SERVER_PORT", UsernameEnv: "MONERO_USERNAME", PasswordEnv: "MONERO_PASSWORD", DefaultHost: "xmr-worker", DefaultPort: "6000", StatusInterval: 120},
	}
}

func parseTransfers(payload any) ([]ChainTransfer, error) {
	if obj, ok := payload.(map[string]any); ok {
		if status, _ := obj["status"].(string); strings.EqualFold(status, "error") {
			return nil, fmt.Errorf("%v", obj)
		}
		if txs, ok := obj["transactions"]; ok {
			return parseTransfers(txs)
		}
		if txs, ok := obj["result"]; ok {
			return parseTransfers(txs)
		}
	}
	rows, ok := payload.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected transaction response: %T", payload)
	}
	out := make([]ChainTransfer, 0, len(rows))
	for _, row := range rows {
		switch v := row.(type) {
		case []any:
			if len(v) < 3 {
				continue
			}
			amount, _ := decimalFromAny(v[1])
			conf := intFromAny(v[2])
			category := "receive"
			if len(v) > 3 {
				category, _ = v[3].(string)
			}
			addr, _ := v[0].(string)
			out = append(out, ChainTransfer{Address: addr, Amount: amount, Confirmations: conf, Category: category})
		case map[string]any:
			addr, _ := firstString(v, "addr", "address", "to_address")
			amount, _ := decimalFromAny(firstAny(v, "amount", "amount_crypto"))
			conf := intFromAny(firstAny(v, "confirmations"))
			category, _ := firstString(v, "category")
			if category == "" {
				category = "receive"
			}
			out = append(out, ChainTransfer{Address: addr, Amount: amount, Confirmations: conf, Category: category})
		}
	}
	return out, nil
}

func firstAny(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			return v, true
		}
	}
	return "", false
}

func decimalFromAny(v any) (decimal.Decimal, bool) {
	switch x := v.(type) {
	case nil:
		return decimal.Zero, false
	case decimal.Decimal:
		return x, true
	case json.Number:
		d, err := decimal.NewFromString(string(x))
		return d, err == nil
	case string:
		d, err := decimal.NewFromString(x)
		return d, err == nil
	case float64:
		return decimal.NewFromFloat(x), true
	case int:
		return decimal.NewFromInt(int64(x)), true
	case int64:
		return decimal.NewFromInt(x), true
	default:
		return decimal.Zero, false
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		i, _ := strconv.Atoi(string(x))
		return i
	case string:
		i, _ := strconv.Atoi(x)
		return i
	default:
		return 0
	}
}

func decimalFromBaseUnits(value decimal.Decimal, decimals int32) decimal.Decimal {
	return value.Div(decimal.New(1, decimals))
}
