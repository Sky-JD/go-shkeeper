package chainworker

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	bnbNativeGasLimit = uint64(21_000)
	bnbTokenGasLimit  = uint64(100_000)
)

type broadcastResult struct {
	Dest   string   `json:"dest"`
	TxIDs  []string `json:"txids"`
	Status string   `json:"status"`
}

func (s *Server) bnbBalance(ctx context.Context, crypto string, address string) (decimal.Decimal, error) {
	if s.isNative(crypto) {
		return s.nativeBalance(ctx, address)
	}
	contract, decimals, err := tokenConfig(crypto)
	if err != nil {
		return decimal.Zero, err
	}
	addressBytes, err := evmAddressBytes(address)
	if err != nil {
		return decimal.Zero, err
	}
	data := evmCallData("balanceOf(address)", addressBytes)
	var hexBalance string
	if err := s.ethCall(ctx, contract, "0x"+hex.EncodeToString(data), &hexBalance); err != nil {
		return decimal.Zero, err
	}
	value, err := hexToDecimal(hexBalance)
	if err != nil {
		return decimal.Zero, err
	}
	return value.Div(decimal.New(1, int32(decimals))), nil
}

func (s *Server) broadcastBNBPayout(ctx context.Context, crypto string, destination string, amount decimal.Decimal) (broadcastResult, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return broadcastResult{}, err
	}
	if len(accounts) == 0 && !s.isNative(crypto) {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
		if err != nil {
			return broadcastResult{}, err
		}
	}
	gasPrice, err := s.gasPrice(ctx)
	if err != nil {
		return broadcastResult{}, err
	}
	for _, account := range accounts {
		balance, err := s.bnbBalance(ctx, crypto, account.Address)
		if err != nil || balance.LessThan(amount) {
			continue
		}
		gasLimit := bnbNativeGasLimit
		if !s.isNative(crypto) {
			gasLimit = bnbTokenGasLimit
		}
		fee := decimal.NewFromBigInt(new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(gasLimit)), 0).Div(decimal.New(1, 18))
		nativeBalance, err := s.nativeBalance(ctx, account.Address)
		if err != nil {
			continue
		}
		requiredNative := fee
		if s.isNative(crypto) {
			requiredNative = requiredNative.Add(amount)
		}
		if nativeBalance.LessThan(requiredNative) {
			continue
		}
		txid, err := s.signAndBroadcastBNB(ctx, account, crypto, destination, amount, gasLimit, gasPrice)
		if err != nil {
			return broadcastResult{}, err
		}
		return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
	}
	return broadcastResult{}, fmt.Errorf("no %s account has enough balance and gas for payout", crypto)
}

func (s *Server) signAndBroadcastBNB(ctx context.Context, account Account, crypto string, destination string, amount decimal.Decimal, gasLimit uint64, gasPrice *big.Int) (string, error) {
	privateKeyHex, err := decryptSecret(s.cfg.AccountPassword, account.PrivateKeyHex)
	if err != nil {
		return "", err
	}
	nonce, err := s.nonce(ctx, account.Address)
	if err != nil {
		return "", err
	}
	var (
		to    []byte
		value *big.Int
		data  []byte
	)
	if s.isNative(crypto) {
		to, err = evmAddressBytes(destination)
		if err != nil {
			return "", err
		}
		value = amountToBaseUnits(amount, 18)
	} else {
		contract, decimals, err := tokenConfig(crypto)
		if err != nil {
			return "", err
		}
		to, err = evmAddressBytes(contract)
		if err != nil {
			return "", err
		}
		value = big.NewInt(0)
		destinationBytes, err := evmAddressBytes(destination)
		if err != nil {
			return "", err
		}
		data = evmCallData("transfer(address,uint256)", destinationBytes, amountToBaseUnits(amount, decimals).Bytes())
	}
	raw, txHash, err := signLegacyEVMTransaction(privateKeyHex, nonce, gasPrice, gasLimit, to, value, data, big.NewInt(s.cfg.EVMChainID))
	if err != nil {
		return "", err
	}
	var txid string
	if err := s.rpc(ctx, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(raw)}, &txid); err != nil {
		return "", err
	}
	if txid == "" {
		txid = "0x" + hex.EncodeToString(txHash)
	}
	return txid, nil
}

func (s *Server) nonce(ctx context.Context, address string) (uint64, error) {
	var hexNonce string
	if err := s.rpc(ctx, "eth_getTransactionCount", []any{address, "pending"}, &hexNonce); err != nil {
		return 0, err
	}
	value, err := hexToInt(hexNonce)
	return uint64(value), err
}

func (s *Server) gasPrice(ctx context.Context) (*big.Int, error) {
	var hexGasPrice string
	if err := s.rpc(ctx, "eth_gasPrice", []any{}, &hexGasPrice); err != nil {
		return nil, err
	}
	value, err := hexToDecimal(hexGasPrice)
	if err != nil {
		return nil, err
	}
	return value.BigInt(), nil
}

func (s *Server) ethCall(ctx context.Context, to string, data string, result any) error {
	return s.rpc(ctx, "eth_call", []any{map[string]any{"to": to, "data": data}, "latest"}, result)
}

func (s *Server) bnbTransfersByTx(ctx context.Context, crypto string, txid string) ([]transferResult, error) {
	latest, err := s.latestBlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.accountSet(ctx, crypto)
	if err != nil {
		return nil, err
	}
	if s.isNative(crypto) {
		return s.bnbNativeTransfersByTx(ctx, txid, latest, accounts)
	}
	return s.bnbTokenTransfersByTx(ctx, crypto, txid, latest, accounts)
}

func (s *Server) bnbNativeTransfersByTx(ctx context.Context, txid string, latest int64, accounts map[string]struct{}) ([]transferResult, error) {
	var tx struct {
		From        string `json:"from"`
		To          string `json:"to"`
		Value       string `json:"value"`
		BlockNumber string `json:"blockNumber"`
	}
	if err := s.rpc(ctx, "eth_getTransactionByHash", []any{txid}, &tx); err != nil {
		return nil, err
	}
	if tx.To == "" {
		return nil, nil
	}
	value, err := hexToDecimal(tx.Value)
	if err != nil {
		return nil, err
	}
	blockNumber, _ := hexToInt(tx.BlockNumber)
	_, fromOwned := accounts[strings.ToLower(tx.From)]
	_, toOwned := accounts[strings.ToLower(tx.To)]
	category := transferCategory(fromOwned, toOwned)
	if category == "" {
		return nil, nil
	}
	return []transferResult{{
		Address:       tx.To,
		Amount:        decimalFromBaseUnits(value, 18).String(),
		Confirmations: confirmations(latest, blockNumber),
		Category:      category,
	}}, nil
}

func (s *Server) bnbTokenTransfersByTx(ctx context.Context, crypto string, txid string, latest int64, accounts map[string]struct{}) ([]transferResult, error) {
	contract, decimals, err := tokenConfig(crypto)
	if err != nil {
		return nil, err
	}
	contract = strings.ToLower(contract)
	var receipt struct {
		BlockNumber string `json:"blockNumber"`
		Logs        []struct {
			Address string   `json:"address"`
			Topics  []string `json:"topics"`
			Data    string   `json:"data"`
		} `json:"logs"`
	}
	if err := s.rpc(ctx, "eth_getTransactionReceipt", []any{txid}, &receipt); err != nil {
		return nil, err
	}
	blockNumber, _ := hexToInt(receipt.BlockNumber)
	out := make([]transferResult, 0)
	for _, log := range receipt.Logs {
		if strings.ToLower(log.Address) != contract || len(log.Topics) < 3 || strings.ToLower(log.Topics[0]) != evmTransferTopic {
			continue
		}
		from := evmTopicAddress(log.Topics[1])
		to := evmTopicAddress(log.Topics[2])
		_, fromOwned := accounts[strings.ToLower(from)]
		_, toOwned := accounts[strings.ToLower(to)]
		category := transferCategory(fromOwned, toOwned)
		if category == "" {
			continue
		}
		value, err := hexToDecimal(log.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, transferResult{
			Address:       to,
			Amount:        decimalFromBaseUnits(value, int32(decimals)).String(),
			Confirmations: confirmations(latest, blockNumber),
			Category:      category,
		})
	}
	return out, nil
}

func evmTopicAddress(topic string) string {
	value := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(topic), "0x"), "0X")
	if len(value) >= 40 {
		return "0x" + strings.ToLower(value[len(value)-40:])
	}
	return ""
}

func evmCallData(signature string, args ...[]byte) []byte {
	hash := keccak256([]byte(signature))
	out := append([]byte{}, hash[:4]...)
	for _, arg := range args {
		out = append(out, leftPad(arg, 32)...)
	}
	return out
}

func amountToBaseUnits(amount decimal.Decimal, decimals int) *big.Int {
	return amount.Mul(decimal.New(1, int32(decimals))).Truncate(0).BigInt()
}

func tokenConfig(crypto string) (contract string, decimals int, err error) {
	normalized := strings.ReplaceAll(strings.ToUpper(crypto), "-", "_")
	def, hasDefault := defaultTokenConfig(crypto)
	if value := strings.TrimSpace(os.Getenv(normalized + "_CONTRACT")); value != "" {
		contract = value
		defaultDecimals := int64(0)
		if hasDefault {
			defaultDecimals = int64(def.Decimals)
		}
		decimals = int(int64Env(normalized+"_DECIMALS", defaultDecimals))
		if decimals <= 0 {
			return "", 0, fmt.Errorf("token decimals are not configured for %s", crypto)
		}
		return contract, decimals, nil
	}
	if !hasDefault {
		return "", 0, fmt.Errorf("token contract is not configured for %s", crypto)
	}
	return def.Contract, def.Decimals, nil
}

type tokenDefinition struct {
	Contract string
	Decimals int
}

func defaultTokenConfig(crypto string) (tokenDefinition, bool) {
	switch strings.ToUpper(crypto) {
	case "BNB-USDT":
		return tokenDefinition{Contract: "0x55d398326f99059fF775485246999027B3197955", Decimals: 18}, true
	case "BNB-USDC":
		return tokenDefinition{Contract: "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d", Decimals: 18}, true
	default:
		return tokenDefinition{}, false
	}
}

func evmAddressBytes(address string) ([]byte, error) {
	value := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(address), "0x"), "0X")
	if len(value) != 40 {
		return nil, fmt.Errorf("invalid EVM address length: %s", address)
	}
	out, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(out) != 20 {
		return nil, fmt.Errorf("invalid EVM address: %s", address)
	}
	return out, nil
}
