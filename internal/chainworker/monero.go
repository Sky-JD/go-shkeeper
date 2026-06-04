package chainworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const moneroAtomicDecimals = 12

type moneroRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonBigInt struct {
	value *big.Int
}

func (v jsonBigInt) MarshalJSON() ([]byte, error) {
	if v.value == nil {
		return []byte("0"), nil
	}
	return []byte(v.value.String()), nil
}

func (s *Server) moneroStatus(ctx context.Context) (map[string]any, error) {
	var info struct {
		Status       string `json:"status"`
		Synchronized bool   `json:"synchronized"`
		BusySyncing  bool   `json:"busy_syncing"`
		Height       int64  `json:"height"`
		TargetHeight int64  `json:"target_height"`
	}
	if err := s.moneroDaemonRPC(ctx, "get_info", map[string]any{}, &info); err != nil {
		return nil, err
	}
	delta := info.TargetHeight - info.Height
	if info.Synchronized || delta < 0 {
		delta = 0
	}
	return map[string]any{
		"status":        info.Status,
		"delta_blocks":  delta,
		"synchronized":  info.Synchronized,
		"busy_syncing":  info.BusySyncing,
		"height":        info.Height,
		"target_height": info.TargetHeight,
	}, nil
}

func (s *Server) moneroBalance(ctx context.Context) (decimal.Decimal, error) {
	var resp struct {
		UnlockedBalance json.Number `json:"unlocked_balance"`
	}
	if err := s.moneroWalletRPC(ctx, "get_balance", map[string]any{"account_index": 0}, &resp); err != nil {
		return decimal.Zero, err
	}
	return moneroAtomicToDecimal(resp.UnlockedBalance), nil
}

func (s *Server) newMoneroAddress(ctx context.Context) (string, error) {
	var resp struct {
		Address string `json:"address"`
	}
	if err := s.moneroWalletRPC(ctx, "create_address", map[string]any{"account_index": 0, "label": "shkeeper"}, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Address) == "" {
		return "", fmt.Errorf("monero-wallet-rpc returned empty address")
	}
	return resp.Address, nil
}

func (s *Server) moneroAddresses(ctx context.Context, crypto string) ([]string, error) {
	seen := map[string]struct{}{}
	if s.store != nil {
		accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if account.Address != "" {
				seen[account.Address] = struct{}{}
			}
		}
	}
	var resp struct {
		Address   string `json:"address"`
		Addresses []struct {
			Address string `json:"address"`
		} `json:"addresses"`
	}
	if err := s.moneroWalletRPC(ctx, "get_address", map[string]any{"account_index": 0}, &resp); err == nil {
		if resp.Address != "" {
			seen[resp.Address] = struct{}{}
		}
		for _, row := range resp.Addresses {
			if row.Address != "" {
				seen[row.Address] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for address := range seen {
		out = append(out, address)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) moneroTransfersByTx(ctx context.Context, txid string) ([]transferResult, error) {
	var resp struct {
		Transfer  moneroTransfer   `json:"transfer"`
		Transfers []moneroTransfer `json:"transfers"`
	}
	if err := s.moneroWalletRPC(ctx, "get_transfer_by_txid", map[string]any{"txid": txid}, &resp); err != nil {
		return nil, err
	}
	rows := resp.Transfers
	if len(rows) == 0 && (resp.Transfer.TxID != "" || resp.Transfer.Amount.String() != "") {
		rows = []moneroTransfer{resp.Transfer}
	}
	out := make([]transferResult, 0, len(rows))
	for _, row := range rows {
		category := moneroCategory(row.Type)
		confirmations := row.Confirmations
		if confirmations == 0 {
			confirmations = resp.Transfer.Confirmations
		}
		if len(row.Destinations) > 0 && category == "send" {
			for _, dest := range row.Destinations {
				out = append(out, transferResult{
					Address:       dest.Address,
					Amount:        moneroAtomicToDecimal(dest.Amount).String(),
					Confirmations: confirmations,
					Category:      category,
				})
			}
			continue
		}
		address := row.Address
		if address == "" && len(row.Destinations) > 0 {
			address = row.Destinations[0].Address
		}
		if address == "" {
			continue
		}
		out = append(out, transferResult{
			Address:       address,
			Amount:        moneroAtomicToDecimal(row.Amount).String(),
			Confirmations: confirmations,
			Category:      category,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("monero transaction has no address details: %s", txid)
	}
	return out, nil
}

func (s *Server) broadcastMoneroPayout(ctx context.Context, destination string, amount decimal.Decimal, fee string) (broadcastResult, error) {
	priority := int64Env("MONERO_TRANSFER_PRIORITY", 0)
	if strings.TrimSpace(fee) != "" {
		priority = int64EnvFromValue(fee, priority)
	}
	unlocked, err := s.moneroBalance(ctx)
	if err != nil {
		return broadcastResult{}, err
	}
	var resp struct {
		TxHash     string   `json:"tx_hash"`
		TxHashList []string `json:"tx_hash_list"`
	}
	if amount.GreaterThanOrEqual(unlocked) {
		err = s.moneroWalletRPC(ctx, "sweep_all", map[string]any{
			"address":       destination,
			"account_index": 0,
			"priority":      priority,
		}, &resp)
	} else {
		err = s.moneroWalletRPC(ctx, "transfer_split", map[string]any{
			"destinations": []map[string]any{{
				"address": destination,
				"amount":  jsonBigInt{value: amountToBaseUnits(amount, moneroAtomicDecimals)},
			}},
			"account_index": 0,
			"priority":      priority,
		}, &resp)
	}
	if err != nil {
		return broadcastResult{}, err
	}
	txids := resp.TxHashList
	if len(txids) == 0 && resp.TxHash != "" {
		txids = []string{resp.TxHash}
	}
	if len(txids) == 0 {
		return broadcastResult{}, fmt.Errorf("monero-wallet-rpc returned no tx hash")
	}
	return broadcastResult{Dest: destination, TxIDs: txids, Status: "success"}, nil
}

func (s *Server) moneroFeeDepositAccount(ctx context.Context) (string, decimal.Decimal, error) {
	addresses, err := s.moneroAddresses(ctx, "XMR")
	if err != nil {
		return "", decimal.Zero, err
	}
	address := ""
	if len(addresses) > 0 {
		address = addresses[0]
	}
	if address == "" {
		address, err = s.newMoneroAddress(ctx)
		if err != nil {
			return "", decimal.Zero, err
		}
	}
	balance, err := s.moneroBalance(ctx)
	return address, balance, err
}

type moneroTransfer struct {
	TxID          string       `json:"txid"`
	Address       string       `json:"address"`
	Amount        json.Number  `json:"amount"`
	Type          string       `json:"type"`
	Confirmations int64        `json:"confirmations"`
	Destinations  []moneroDest `json:"destinations"`
}

type moneroDest struct {
	Address string      `json:"address"`
	Amount  json.Number `json:"amount"`
}

func moneroCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "out", "pending", "pool":
		return "send"
	default:
		return "receive"
	}
}

func moneroAtomicToDecimal(value any) decimal.Decimal {
	amount, ok := decimalFromAny(value)
	if !ok {
		return decimal.Zero
	}
	return amount.Div(decimal.New(1, moneroAtomicDecimals))
}

func int64EnvFromValue(value string, fallback int64) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return fallback
	}
	return parsed.Truncate(0).BigInt().Int64()
}

func (s *Server) moneroWalletRPC(ctx context.Context, method string, params map[string]any, result any) error {
	if strings.TrimSpace(s.cfg.WalletRPCURL) == "" {
		return fmt.Errorf("MONERO_WALLET_RPC_URL is not configured")
	}
	return s.moneroRPC(ctx, s.cfg.WalletRPCURL, s.cfg.WalletRPCUser, s.cfg.WalletRPCPass, method, params, result)
}

func (s *Server) moneroDaemonRPC(ctx context.Context, method string, params map[string]any, result any) error {
	return s.moneroRPC(ctx, s.cfg.FullnodeURL, s.cfg.RPCUsername, s.cfg.RPCPassword, method, params, result)
}

func (s *Server) moneroRPC(ctx context.Context, endpoint string, username string, password string, method string, params map[string]any, result any) error {
	endpoint = normalizeMoneroRPCURL(endpoint)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "go-chain-worker",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("monero rpc %s returned %d", method, resp.StatusCode)
	}
	var payload struct {
		Result json.RawMessage `json:"result"`
		Error  *moneroRPCError `json:"error"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return err
	}
	if payload.Error != nil {
		return fmt.Errorf("monero rpc %s error %d: %s", method, payload.Error.Code, payload.Error.Message)
	}
	if result == nil {
		return nil
	}
	dec = json.NewDecoder(bytes.NewReader(payload.Result))
	dec.UseNumber()
	return dec.Decode(result)
}

func normalizeMoneroRPCURL(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return endpoint
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	if !strings.HasSuffix(endpoint, "/json_rpc") {
		endpoint += "/json_rpc"
	}
	return endpoint
}
