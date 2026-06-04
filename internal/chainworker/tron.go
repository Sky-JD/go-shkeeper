package chainworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/shopspring/decimal"
)

const tronNativeDecimals = 6

func (s *Server) tronBalance(ctx context.Context, crypto string, address string) (decimal.Decimal, error) {
	if crypto == "TRX" {
		addressHex, err := tronAddressHex(address)
		if err != nil {
			return decimal.Zero, err
		}
		var resp struct {
			Balance jsonNumber `json:"balance"`
		}
		if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getaccount", map[string]any{
			"address": addressHex,
			"visible": false,
		}, &resp); err != nil {
			return decimal.Zero, err
		}
		sun, ok := resp.Balance.Decimal()
		if !ok {
			return decimal.Zero, nil
		}
		return sun.Div(decimal.New(1, tronNativeDecimals)), nil
	}
	contract, decimals, err := tronTokenConfig(crypto)
	if err != nil {
		return decimal.Zero, err
	}
	addressHex, err := tronAddressHex(address)
	if err != nil {
		return decimal.Zero, err
	}
	contractHex, err := tronAddressHex(contract)
	if err != nil {
		return decimal.Zero, err
	}
	addressParam, err := tronABIAddressParam(address)
	if err != nil {
		return decimal.Zero, err
	}
	var resp struct {
		ConstantResult []string `json:"constant_result"`
		Result         struct {
			Result  bool   `json:"result"`
			Message string `json:"message"`
		} `json:"result"`
	}
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/triggerconstantcontract", map[string]any{
		"owner_address":     addressHex,
		"contract_address":  contractHex,
		"function_selector": "balanceOf(address)",
		"parameter":         addressParam,
		"visible":           false,
	}, &resp); err != nil {
		return decimal.Zero, err
	}
	if !resp.Result.Result && resp.Result.Message != "" {
		return decimal.Zero, fmt.Errorf("tron balanceOf failed: %s", decodeTRONMessage(resp.Result.Message))
	}
	if len(resp.ConstantResult) == 0 {
		return decimal.Zero, nil
	}
	value, err := hexToDecimal("0x" + strings.TrimPrefix(resp.ConstantResult[0], "0x"))
	if err != nil {
		return decimal.Zero, err
	}
	return value.Div(decimal.New(1, int32(decimals))), nil
}

func (s *Server) broadcastTRONPayout(ctx context.Context, crypto string, destination string, amount decimal.Decimal) (broadcastResult, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return broadcastResult{}, err
	}
	if len(accounts) == 0 && crypto != "TRX" {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
		if err != nil {
			return broadcastResult{}, err
		}
	}
	for _, account := range accounts {
		balance, err := s.tronBalance(ctx, crypto, account.Address)
		if err != nil || balance.LessThan(amount) {
			continue
		}
		txid, err := s.signAndBroadcastTRON(ctx, account, crypto, destination, amount)
		if err != nil {
			return broadcastResult{}, err
		}
		return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
	}
	return broadcastResult{}, fmt.Errorf("no %s account has enough balance for payout", crypto)
}

func (s *Server) signAndBroadcastTRON(ctx context.Context, account Account, crypto string, destination string, amount decimal.Decimal) (string, error) {
	var tx map[string]any
	var err error
	if crypto == "TRX" {
		tx, err = s.createTRXTransfer(ctx, account.Address, destination, amount)
	} else {
		tx, err = s.createTRC20Transfer(ctx, account.Address, crypto, destination, amount)
	}
	if err != nil {
		return "", err
	}
	privateKeyHex, err := decryptSecret(s.cfg.AccountPassword, account.PrivateKeyHex)
	if err != nil {
		return "", err
	}
	signed, err := signTRONTransaction(tx, privateKeyHex)
	if err != nil {
		return "", err
	}
	return s.broadcastTRON(ctx, signed)
}

func (s *Server) createTRXTransfer(ctx context.Context, owner string, destination string, amount decimal.Decimal) (map[string]any, error) {
	ownerHex, err := tronAddressHex(owner)
	if err != nil {
		return nil, err
	}
	destHex, err := tronAddressHex(destination)
	if err != nil {
		return nil, err
	}
	amountSun, err := bigIntToInt64(amountToBaseUnits(amount, tronNativeDecimals))
	if err != nil {
		return nil, err
	}
	var tx map[string]any
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/createtransaction", map[string]any{
		"owner_address": ownerHex,
		"to_address":    destHex,
		"amount":        amountSun,
		"visible":       false,
	}, &tx); err != nil {
		return nil, err
	}
	if err := ensureTRONTransaction(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

func (s *Server) createTRC20Transfer(ctx context.Context, owner string, crypto string, destination string, amount decimal.Decimal) (map[string]any, error) {
	ownerHex, err := tronAddressHex(owner)
	if err != nil {
		return nil, err
	}
	contract, decimals, err := tronTokenConfig(crypto)
	if err != nil {
		return nil, err
	}
	contractHex, err := tronAddressHex(contract)
	if err != nil {
		return nil, err
	}
	destParam, err := tronABIAddressParam(destination)
	if err != nil {
		return nil, err
	}
	amountParam := hex.EncodeToString(leftPad(amountToBaseUnits(amount, decimals).Bytes(), 32))
	var resp struct {
		Result struct {
			Result  bool   `json:"result"`
			Message string `json:"message"`
		} `json:"result"`
		Transaction map[string]any `json:"transaction"`
	}
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/triggersmartcontract", map[string]any{
		"owner_address":     ownerHex,
		"contract_address":  contractHex,
		"function_selector": "transfer(address,uint256)",
		"parameter":         destParam + amountParam,
		"fee_limit":         int64Env("TRON_TOKEN_FEE_LIMIT_SUN", 100_000_000),
		"call_value":        0,
		"visible":           false,
	}, &resp); err != nil {
		return nil, err
	}
	if !resp.Result.Result {
		return nil, fmt.Errorf("tron trigger transfer failed: %s", decodeTRONMessage(resp.Result.Message))
	}
	if err := ensureTRONTransaction(resp.Transaction); err != nil {
		return nil, err
	}
	return resp.Transaction, nil
}

func signTRONTransaction(tx map[string]any, privateKeyHex string) (map[string]any, error) {
	rawHex, ok := tx["raw_data_hex"].(string)
	if !ok || rawHex == "" {
		return nil, fmt.Errorf("tron transaction has no raw_data_hex")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(rawHex, "0x"))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	r, s, recoveryID, err := recoverableSignature(privateKeyHex, hash[:])
	if err != nil {
		return nil, err
	}
	signature := append(append(append([]byte{}, r...), s...), recoveryID)
	signed := make(map[string]any, len(tx)+1)
	for k, v := range tx {
		signed[k] = v
	}
	signed["signature"] = []string{hex.EncodeToString(signature)}
	return signed, nil
}

func (s *Server) broadcastTRON(ctx context.Context, signed map[string]any) (string, error) {
	var resp struct {
		Result  bool   `json:"result"`
		TxID    string `json:"txid"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/broadcasttransaction", signed, &resp); err != nil {
		return "", err
	}
	if !resp.Result {
		return "", fmt.Errorf("tron broadcast failed: %s %s", resp.Code, decodeTRONMessage(resp.Message))
	}
	if resp.TxID != "" {
		return resp.TxID, nil
	}
	if txid, ok := signed["txID"].(string); ok && txid != "" {
		return txid, nil
	}
	return "", fmt.Errorf("tron broadcast did not return txid")
}

func (s *Server) tronTransfersByTx(ctx context.Context, crypto string, txid string) ([]transferResult, error) {
	latest, err := s.latestBlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.accountSet(ctx, crypto)
	if err != nil {
		return nil, err
	}
	if crypto == "TRX" {
		return s.tronNativeTransfersByTx(ctx, txid, latest, accounts)
	}
	return s.tronTokenTransfersByTx(ctx, crypto, txid, latest, accounts)
}

func (s *Server) tronNativeTransfersByTx(ctx context.Context, txid string, latest int64, accounts map[string]struct{}) ([]transferResult, error) {
	info, err := s.tronTransactionInfo(ctx, txid)
	if err != nil {
		return nil, err
	}
	var tx struct {
		RawData struct {
			Contract []struct {
				Type      string `json:"type"`
				Parameter struct {
					Value struct {
						OwnerAddress string     `json:"owner_address"`
						ToAddress    string     `json:"to_address"`
						Amount       jsonNumber `json:"amount"`
					} `json:"value"`
				} `json:"parameter"`
			} `json:"contract"`
		} `json:"raw_data"`
	}
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/gettransactionbyid", map[string]any{"value": txid}, &tx); err != nil {
		return nil, err
	}
	out := make([]transferResult, 0)
	for _, contract := range tx.RawData.Contract {
		if contract.Type != "TransferContract" {
			continue
		}
		fromHex, err := tronAddressHex(contract.Parameter.Value.OwnerAddress)
		if err != nil {
			fromHex = strings.ToLower(contract.Parameter.Value.OwnerAddress)
		}
		toHex, err := tronAddressHex(contract.Parameter.Value.ToAddress)
		if err != nil {
			toHex = strings.ToLower(contract.Parameter.Value.ToAddress)
		}
		_, fromOwned := accounts[strings.ToLower(fromHex)]
		_, toOwned := accounts[strings.ToLower(toHex)]
		category := transferCategory(fromOwned, toOwned)
		if category == "" {
			continue
		}
		amountSun, ok := contract.Parameter.Value.Amount.Decimal()
		if !ok {
			continue
		}
		toAddress, _ := tronBase58FromHex(toHex)
		out = append(out, transferResult{
			Address:       toAddress,
			Amount:        decimalFromBaseUnits(amountSun, tronNativeDecimals).String(),
			Confirmations: confirmations(latest, info.BlockNumber),
			Category:      category,
		})
	}
	return out, nil
}

func (s *Server) tronTokenTransfersByTx(ctx context.Context, crypto string, txid string, latest int64, accounts map[string]struct{}) ([]transferResult, error) {
	contract, decimals, err := tronTokenConfig(crypto)
	if err != nil {
		return nil, err
	}
	contractKey, err := tronContractLogKey(contract)
	if err != nil {
		return nil, err
	}
	info, err := s.tronTransactionInfo(ctx, txid)
	if err != nil {
		return nil, err
	}
	out := make([]transferResult, 0)
	for _, log := range info.Logs {
		logContract, err := tronContractLogKey(log.Address)
		if err != nil || logContract != contractKey || len(log.Topics) < 3 || strings.ToLower(log.Topics[0]) != evmTransferTopic {
			continue
		}
		fromHex := tronTopicAddressHex(log.Topics[1])
		toHex := tronTopicAddressHex(log.Topics[2])
		_, fromOwned := accounts[strings.ToLower(fromHex)]
		_, toOwned := accounts[strings.ToLower(toHex)]
		category := transferCategory(fromOwned, toOwned)
		if category == "" {
			continue
		}
		value, err := hexToDecimal("0x" + strings.TrimPrefix(log.Data, "0x"))
		if err != nil {
			return nil, err
		}
		toAddress, _ := tronBase58FromHex(toHex)
		out = append(out, transferResult{
			Address:       toAddress,
			Amount:        decimalFromBaseUnits(value, int32(decimals)).String(),
			Confirmations: confirmations(latest, info.BlockNumber),
			Category:      category,
		})
	}
	return out, nil
}

type tronTxInfo struct {
	BlockNumber int64 `json:"blockNumber"`
	Logs        []struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	} `json:"log"`
}

func (s *Server) tronTransactionInfo(ctx context.Context, txid string) (tronTxInfo, error) {
	var info tronTxInfo
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/gettransactioninfobyid", map[string]any{"value": txid}, &info); err != nil {
		return info, err
	}
	return info, nil
}

func ensureTRONTransaction(tx map[string]any) error {
	if len(tx) == 0 {
		return fmt.Errorf("tron fullnode returned empty transaction")
	}
	if rawHex, ok := tx["raw_data_hex"].(string); !ok || rawHex == "" {
		return fmt.Errorf("tron fullnode returned transaction without raw_data_hex")
	}
	return nil
}

func tronTokenConfig(crypto string) (contract string, decimals int, err error) {
	normalized := strings.ReplaceAll(strings.ToUpper(crypto), "-", "_")
	for _, key := range []string{"TRON_" + normalized + "_CONTRACT", normalized + "_CONTRACT"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			contract = value
			break
		}
	}
	if contract == "" {
		switch strings.ToUpper(crypto) {
		case "USDT":
			contract = "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
		case "USDC":
			contract = "TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8"
		default:
			return "", 0, fmt.Errorf("TRON token contract is not configured for %s", crypto)
		}
	}
	decimals = int(int64Env("TRON_"+normalized+"_DECIMALS", int64Env(normalized+"_DECIMALS", 6)))
	if decimals <= 0 {
		decimals = 6
	}
	return contract, decimals, nil
}

func tronAddressHex(address string) (string, error) {
	payload, err := tronAddressBytes(address)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(payload), nil
}

func tronABIAddressParam(address string) (string, error) {
	payload, err := tronAddressBytes(address)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(leftPad(payload[1:], 32)), nil
}

func tronAddressBytes(address string) ([]byte, error) {
	value := strings.TrimSpace(address)
	hexValue := strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if len(hexValue) == 42 && strings.HasPrefix(hexValue, "41") {
		payload, err := hex.DecodeString(hexValue)
		if err != nil {
			return nil, err
		}
		if len(payload) == 21 && payload[0] == 0x41 {
			return payload, nil
		}
	}
	payload, err := base58CheckDecode(value)
	if err != nil {
		return nil, err
	}
	if len(payload) != 21 || payload[0] != 0x41 {
		return nil, fmt.Errorf("invalid TRON address: %s", address)
	}
	return payload, nil
}

func tronBase58FromHex(address string) (string, error) {
	payload, err := tronAddressBytes(address)
	if err != nil {
		return "", err
	}
	return base58Check(payload), nil
}

func tronTopicAddressHex(topic string) string {
	value := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(topic), "0x"), "0X")
	if len(value) >= 40 {
		return "41" + strings.ToLower(value[len(value)-40:])
	}
	return ""
}

func tronContractLogKey(address string) (string, error) {
	payload, err := tronAddressBytes(address)
	if err == nil {
		return strings.ToLower(hex.EncodeToString(payload[1:])), nil
	}
	value := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(address), "0x"), "0X")
	value = strings.ToLower(value)
	if len(value) == 42 && strings.HasPrefix(value, "41") {
		return value[2:], nil
	}
	if len(value) == 40 {
		return value, nil
	}
	return "", err
}

func leftPad(value []byte, size int) []byte {
	if len(value) >= size {
		return value
	}
	out := make([]byte, size)
	copy(out[size-len(value):], value)
	return out
}

func bigIntToInt64(value *big.Int) (int64, error) {
	if value == nil || value.Sign() < 0 || !value.IsInt64() {
		return 0, fmt.Errorf("integer value is outside int64 range")
	}
	return value.Int64(), nil
}

func decodeTRONMessage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	data, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil {
		return value
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return value
	}
	return text
}

const httpMethodPost = "POST"

type jsonNumber string

func (n *jsonNumber) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	value = strings.Trim(value, `"`)
	*n = jsonNumber(value)
	return nil
}

func (n jsonNumber) Decimal() (decimal.Decimal, bool) {
	value := strings.TrimSpace(string(n))
	if value == "" {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(value)
	return d, err == nil
}
