package chainworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type bitcoinRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) bitcoinStatus(ctx context.Context) (map[string]any, error) {
	var info struct {
		Headers int64 `json:"headers"`
		Blocks  int64 `json:"blocks"`
	}
	if err := s.bitcoinRPC(ctx, "getblockchaininfo", []any{}, &info); err != nil {
		return nil, err
	}
	delta := info.Headers - info.Blocks
	if delta < 0 {
		delta = 0
	}
	return map[string]any{"delta_blocks": delta, "headers": info.Headers, "blocks": info.Blocks}, nil
}

func (s *Server) bitcoinLatestBlockTimestamp(ctx context.Context) (time.Time, error) {
	var hash string
	if err := s.bitcoinRPC(ctx, "getbestblockhash", []any{}, &hash); err != nil {
		return time.Time{}, err
	}
	var header struct {
		Time int64 `json:"time"`
	}
	if err := s.bitcoinRPC(ctx, "getblockheader", []any{hash}, &header); err != nil {
		return time.Time{}, err
	}
	if header.Time <= 0 {
		return time.Time{}, fmt.Errorf("bitcoin-like rpc returned no block timestamp")
	}
	return time.Unix(header.Time, 0), nil
}

func (s *Server) bitcoinBalance(ctx context.Context) (decimal.Decimal, error) {
	var balance decimal.Decimal
	if err := s.bitcoinRPC(ctx, "getbalance", []any{"*", 1}, &balance); err != nil {
		return decimal.Zero, err
	}
	return balance, nil
}

func (s *Server) newBitcoinAddress(ctx context.Context) (string, error) {
	var address string
	if err := s.bitcoinRPC(ctx, "getnewaddress", []any{}, &address); err != nil {
		return "", err
	}
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("bitcoin rpc returned empty address")
	}
	return address, nil
}

func (s *Server) newFiroSparkAddress(ctx context.Context) (string, error) {
	var addresses []string
	if err := s.bitcoinRPC(ctx, "getnewsparkaddress", []any{}, &addresses); err != nil {
		return "", err
	}
	if len(addresses) == 0 || strings.TrimSpace(addresses[0]) == "" {
		return "", fmt.Errorf("firo spark rpc returned empty address")
	}
	return addresses[0], nil
}

func (s *Server) bitcoinAddresses(ctx context.Context, crypto string) ([]string, error) {
	if strings.EqualFold(crypto, "FIRO-SPARK") {
		return s.firoSparkAddresses(ctx)
	}
	seen := map[string]struct{}{}
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if account.Address != "" {
			seen[account.Address] = struct{}{}
		}
	}
	var received []struct {
		Address string `json:"address"`
	}
	if err := s.bitcoinRPC(ctx, "listreceivedbyaddress", []any{0, true}, &received); err == nil {
		for _, row := range received {
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

func (s *Server) firoSparkAddresses(ctx context.Context) ([]string, error) {
	var resp map[string]string
	if err := s.bitcoinRPC(ctx, "getallsparkaddresses", []any{}, &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp))
	for _, address := range resp {
		if address != "" {
			out = append(out, address)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) bitcoinTransfersByTx(ctx context.Context, txid string) ([]transferResult, error) {
	var tx struct {
		Confirmations int64 `json:"confirmations"`
		Details       []struct {
			Address  string          `json:"address"`
			Amount   decimal.Decimal `json:"amount"`
			Category string          `json:"category"`
		} `json:"details"`
	}
	if err := s.bitcoinRPC(ctx, "gettransaction", []any{txid}, &tx); err != nil {
		return nil, err
	}
	out := make([]transferResult, 0, len(tx.Details))
	for _, detail := range tx.Details {
		if detail.Address == "" {
			continue
		}
		out = append(out, transferResult{
			Address:       detail.Address,
			Amount:        detail.Amount.String(),
			Confirmations: tx.Confirmations,
			Category:      detail.Category,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bitcoin transaction has no address details: %s", txid)
	}
	return out, nil
}

func (s *Server) firoTransfersByTx(ctx context.Context, txid string) ([]transferResult, error) {
	transfers, err := s.bitcoinTransfersByTx(ctx, txid)
	if err != nil {
		return nil, err
	}
	out := make([]transferResult, 0, len(transfers))
	for _, transfer := range transfers {
		if transfer.Address != "" && len(transfer.Address) < 140 {
			out = append(out, transfer)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("firo transaction has no regular transfer details: %s", txid)
	}
	return out, nil
}

func (s *Server) firoSparkTransfersByTx(ctx context.Context, txid string) ([]transferResult, error) {
	var sparkResp []struct {
		Address string          `json:"address"`
		Amount  decimal.Decimal `json:"amount"`
	}
	if err := s.bitcoinRPC(ctx, "getsparkcoinaddr", []any{txid}, &sparkResp); err != nil {
		return nil, err
	}
	if len(sparkResp) == 0 {
		return nil, fmt.Errorf("firo spark transaction has no transfer details: %s", txid)
	}
	var tx struct {
		Confirmations int64 `json:"confirmations"`
		Details       []struct {
			Amount   decimal.Decimal `json:"amount"`
			Category string          `json:"category"`
		} `json:"details"`
	}
	if err := s.bitcoinRPC(ctx, "gettransaction", []any{txid}, &tx); err != nil {
		return nil, err
	}
	out := make([]transferResult, 0, len(sparkResp))
	for i, transfer := range sparkResp {
		if transfer.Address == "" {
			continue
		}
		category := "receive"
		amount := transfer.Amount
		if i < len(tx.Details) {
			detail := tx.Details[i]
			if detail.Category == "spend" {
				category = "send"
				amount = detail.Amount
			} else if detail.Category != "" {
				category = detail.Category
			}
		}
		out = append(out, transferResult{
			Address:       transfer.Address,
			Amount:        amount.String(),
			Confirmations: tx.Confirmations,
			Category:      category,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("firo spark transaction has no usable transfer details: %s", txid)
	}
	return out, nil
}

func (s *Server) broadcastBitcoinPayout(ctx context.Context, destination string, amount decimal.Decimal, fee string) (broadcastResult, error) {
	if strings.TrimSpace(fee) != "" {
		if err := s.setBitcoinTxFee(ctx, fee); err != nil {
			return broadcastResult{}, err
		}
	}
	var txid string
	if err := s.bitcoinRPC(ctx, "sendtoaddress", []any{destination, amount.String(), "", "", false}, &txid); err != nil {
		return broadcastResult{}, err
	}
	if txid == "" {
		return broadcastResult{}, fmt.Errorf("bitcoin rpc returned empty txid")
	}
	return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
}

func (s *Server) broadcastFiroSparkPayout(ctx context.Context, destination string, amount decimal.Decimal, fee string) (broadcastResult, error) {
	if strings.TrimSpace(fee) != "" {
		if err := s.setBitcoinTxFee(ctx, fee); err != nil {
			return broadcastResult{}, err
		}
	}
	amountFloat, _ := amount.Float64()
	var resp any
	if err := s.bitcoinRPC(ctx, "spendspark", []any{map[string]any{
		destination: map[string]any{
			"amount":      amountFloat,
			"memo":        "",
			"subtractFee": false,
		},
	}}, &resp); err != nil {
		return broadcastResult{}, err
	}
	txid := ""
	switch value := resp.(type) {
	case string:
		txid = value
	case map[string]any:
		if item, ok := value["txid"].(string); ok {
			txid = item
		}
	}
	if txid == "" {
		return broadcastResult{Dest: destination, Status: "success"}, nil
	}
	return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
}

func (s *Server) bitcoinEstimateTxFee(ctx context.Context) (map[string]any, error) {
	moduleKey := strings.ReplaceAll(strings.ToUpper(s.cfg.Module), "-", "_") + "_ESTIMATE_TARGET_BLOCKS"
	target := int64Env(moduleKey, int64Env("BTC_ESTIMATE_TARGET_BLOCKS", 6))
	if target <= 0 {
		target = 6
	}
	var resp struct {
		FeeRate decimal.Decimal `json:"feerate"`
		Errors  []string        `json:"errors"`
		Blocks  int64           `json:"blocks"`
	}
	if err := s.bitcoinRPC(ctx, "estimatesmartfee", []any{target}, &resp); err != nil {
		return nil, err
	}
	if resp.FeeRate.IsZero() {
		return nil, fmt.Errorf("bitcoin fee estimation returned no feerate: %v", resp.Errors)
	}
	satPerByte := resp.FeeRate.Mul(decimal.NewFromInt(100000)).Round(0)
	if satPerByte.LessThan(decimal.NewFromInt(1)) {
		satPerByte = decimal.NewFromInt(1)
	}
	return map[string]any{
		"fee":         resp.FeeRate.String(),
		"fee_satoshi": satPerByte.String(),
		"blocks":      resp.Blocks,
	}, nil
}

func (s *Server) setBitcoinTxFee(ctx context.Context, satPerByte string) error {
	fee, err := decimal.NewFromString(strings.TrimSpace(satPerByte))
	if err != nil {
		return err
	}
	if !fee.GreaterThan(decimal.Zero) {
		return nil
	}
	btcPerKB := fee.Div(decimal.NewFromInt(100000))
	var ignored any
	return s.bitcoinRPC(ctx, "settxfee", []any{btcPerKB.StringFixed(8)}, &ignored)
}

func (s *Server) firoSparkBalance(ctx context.Context) (decimal.Decimal, error) {
	var resp struct {
		AvailableBalance json.Number `json:"availableBalance"`
	}
	if err := s.bitcoinRPC(ctx, "getsparkbalance", []any{}, &resp); err != nil {
		return decimal.Zero, err
	}
	atomic, ok := decimalFromAny(resp.AvailableBalance)
	if !ok {
		return decimal.Zero, fmt.Errorf("firo spark balance response has no availableBalance")
	}
	return decimalFromBaseUnits(atomic, 8), nil
}

func (s *Server) bitcoinFeeDepositAccount(ctx context.Context) (string, decimal.Decimal, error) {
	var address string
	if err := s.bitcoinRPC(ctx, "getrawchangeaddress", []any{}, &address); err != nil {
		address, err = s.newBitcoinAddress(ctx)
		if err != nil {
			return "", decimal.Zero, err
		}
	}
	balance, err := s.bitcoinBalance(ctx)
	return address, balance, err
}

func (s *Server) bitcoinRPC(ctx context.Context, method string, params []any, result any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      "go-chain-worker",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.fullnodeURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.RPCUsername != "" || s.cfg.RPCPassword != "" {
		req.SetBasicAuth(s.cfg.RPCUsername, s.cfg.RPCPassword)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("bitcoin rpc %s returned %d", method, resp.StatusCode)
	}
	var payload struct {
		Result json.RawMessage  `json:"result"`
		Error  *bitcoinRPCError `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if payload.Error != nil {
		return fmt.Errorf("bitcoin rpc %s error %d: %s", method, payload.Error.Code, payload.Error.Message)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(payload.Result, result)
}
