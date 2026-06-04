package chainworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/ripemd160"
)

const (
	xrpAccountVersion byte = 0
	xrpDropsDecimals       = 6
)

var (
	xrpMainnetXAddressPrefix = []byte{0x05, 0x44}
	xrpTestnetXAddressPrefix = []byte{0x04, 0x93}
)

const xrpBase58Alphabet = "rpshnaf39wBUDNEGHJKLM4PQRST7VWXYZ2bcdeCg65jkm8oFqi1tuvAxyz"

type xrpDestination struct {
	ClassicAddress string
	Tag            *uint32
	Testnet        bool
}

func (s *Server) xrpStatus(ctx context.Context) (map[string]any, error) {
	var result struct {
		Ledger struct {
			CloseTime   int64 `json:"close_time"`
			LedgerIndex any   `json:"ledger_index"`
		} `json:"ledger"`
		LedgerIndex any `json:"ledger_index"`
	}
	if err := s.xrpRPC(ctx, "ledger", map[string]any{"ledger_index": "validated", "transactions": false, "expand": false}, &result); err != nil {
		return nil, err
	}
	ledgerIndex := result.Ledger.LedgerIndex
	if ledgerIndex == nil {
		ledgerIndex = result.LedgerIndex
	}
	return map[string]any{
		"last_block_timestamp": result.Ledger.CloseTime,
		"ledger_index":         ledgerIndex,
	}, nil
}

func (s *Server) xrpBalance(ctx context.Context) (decimal.Decimal, error) {
	account, err := s.xrpFundingAccount()
	if err != nil {
		return decimal.Zero, err
	}
	var result struct {
		AccountData struct {
			Balance json.Number `json:"Balance"`
		} `json:"account_data"`
	}
	if err := s.xrpRPC(ctx, "account_info", map[string]any{"account": account, "ledger_index": "validated"}, &result); err != nil {
		return decimal.Zero, err
	}
	return xrpDropsToDecimal(result.AccountData.Balance), nil
}

func (s *Server) newXRPAddress(ctx context.Context) (address string, privateKeyHex string, err error) {
	if account, err := s.xrpFundingAccount(); err == nil {
		tag, err := randomUint32()
		if err != nil {
			return "", "", err
		}
		address, err := xrpClassicToXAddress(account, &tag, xrpUseTestnet())
		return address, "", err
	}
	priv, err := secp.GeneratePrivateKey()
	if err != nil {
		return "", "", err
	}
	accountID := xrpAccountIDFromPublicKey(priv.PubKey().SerializeCompressed())
	address = xrpClassicAddress(accountID)
	return address, hex.EncodeToString(priv.Serialize()), nil
}

func (s *Server) xrpAddresses(ctx context.Context, crypto string) ([]string, error) {
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
	if account, err := s.xrpFundingAccount(); err == nil {
		seen[account] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for address := range seen {
		out = append(out, address)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) xrpTransfersByTx(ctx context.Context, txid string) ([]transferResult, error) {
	var result struct {
		TransactionType string          `json:"TransactionType"`
		Account         string          `json:"Account"`
		Destination     string          `json:"Destination"`
		DestinationTag  *uint32         `json:"DestinationTag"`
		Amount          json.RawMessage `json:"Amount"`
		LedgerIndex     any             `json:"ledger_index"`
		Validated       bool            `json:"validated"`
		Meta            struct {
			TransactionResult string `json:"TransactionResult"`
		} `json:"meta"`
		MetaData struct {
			TransactionResult string `json:"TransactionResult"`
		} `json:"metaData"`
		TxJSON *struct {
			TransactionType string          `json:"TransactionType"`
			Account         string          `json:"Account"`
			Destination     string          `json:"Destination"`
			DestinationTag  *uint32         `json:"DestinationTag"`
			Amount          json.RawMessage `json:"Amount"`
			LedgerIndex     any             `json:"ledger_index"`
		} `json:"tx_json"`
	}
	if err := s.xrpRPC(ctx, "tx", map[string]any{"transaction": txid, "binary": false}, &result); err != nil {
		return nil, err
	}
	if result.TxJSON != nil {
		result.TransactionType = firstNonEmpty(result.TransactionType, result.TxJSON.TransactionType)
		result.Account = firstNonEmpty(result.Account, result.TxJSON.Account)
		result.Destination = firstNonEmpty(result.Destination, result.TxJSON.Destination)
		if result.DestinationTag == nil {
			result.DestinationTag = result.TxJSON.DestinationTag
		}
		if len(result.Amount) == 0 {
			result.Amount = result.TxJSON.Amount
		}
		if result.LedgerIndex == nil {
			result.LedgerIndex = result.TxJSON.LedgerIndex
		}
	}
	if result.TransactionType != "Payment" {
		return nil, fmt.Errorf("xrp transaction is not a payment: %s", txid)
	}
	txStatus := firstNonEmpty(result.Meta.TransactionResult, result.MetaData.TransactionResult)
	if txStatus != "" && txStatus != "tesSUCCESS" {
		return nil, fmt.Errorf("xrp transaction is not successful: %s", txStatus)
	}
	amount, err := xrpAmountFromRaw(result.Amount)
	if err != nil {
		return nil, err
	}
	toAddress := result.Destination
	if result.DestinationTag != nil {
		if xaddr, err := xrpClassicToXAddress(result.Destination, result.DestinationTag, xrpUseTestnet()); err == nil {
			toAddress = xaddr
		}
	}
	toOwned, fromOwned, err := s.xrpOwnedDirections(ctx, result.Account, result.Destination, result.DestinationTag)
	if err != nil {
		return nil, err
	}
	category := transferCategory(fromOwned, toOwned)
	if category == "" {
		return nil, fmt.Errorf("xrp transaction does not match known worker accounts: %s", txid)
	}
	conf := int64(0)
	if result.Validated {
		if latest, err := s.xrpLatestLedgerIndex(ctx); err == nil {
			conf = confirmations(latest, int64FromAny(result.LedgerIndex))
		} else {
			conf = 1
		}
	}
	return []transferResult{{
		Address:       toAddress,
		Amount:        amount.String(),
		Confirmations: conf,
		Category:      category,
	}}, nil
}

func (s *Server) broadcastXRPPayout(ctx context.Context, destination string, amount decimal.Decimal, fee string) (broadcastResult, error) {
	account, err := s.xrpFundingAccount()
	if err != nil {
		return broadcastResult{}, err
	}
	secret := firstNonEmpty(getenv("XRP_ACCOUNT_SECRET", ""), getenv("XRP_SECRET", ""))
	if secret == "" {
		return broadcastResult{}, errors.New("XRP_ACCOUNT_SECRET is not configured")
	}
	dest, err := xrpParseDestination(destination)
	if err != nil {
		return broadcastResult{}, err
	}
	tx := map[string]any{
		"TransactionType": "Payment",
		"Account":         account,
		"Destination":     dest.ClassicAddress,
		"Amount":          amountToBaseUnits(amount, xrpDropsDecimals).String(),
	}
	if dest.Tag != nil {
		tx["DestinationTag"] = *dest.Tag
	}
	if feeDrops := strings.TrimSpace(fee); feeDrops != "" && feeDrops != "0" {
		feeAmount, err := decimal.NewFromString(feeDrops)
		if err != nil {
			return broadcastResult{}, fmt.Errorf("invalid XRP fee: %w", err)
		}
		tx["Fee"] = amountToBaseUnits(feeAmount, xrpDropsDecimals).String()
	}
	var result struct {
		EngineResult string `json:"engine_result"`
		TxJSON       struct {
			Hash      string `json:"hash"`
			HashUpper string `json:"Hash"`
		} `json:"tx_json"`
		Hash string `json:"hash"`
	}
	if err := s.xrpRPC(ctx, "submit", map[string]any{"secret": secret, "tx_json": tx}, &result); err != nil {
		return broadcastResult{}, err
	}
	if result.EngineResult != "" && result.EngineResult != "tesSUCCESS" {
		return broadcastResult{}, fmt.Errorf("xrp submit result: %s", result.EngineResult)
	}
	txid := firstNonEmpty(result.TxJSON.Hash, result.TxJSON.HashUpper, result.Hash)
	if txid == "" {
		return broadcastResult{}, errors.New("xrp submit returned no transaction hash")
	}
	return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
}

func (s *Server) xrpOwnedDirections(ctx context.Context, from string, to string, tag *uint32) (toOwned bool, fromOwned bool, err error) {
	accounts, err := s.xrpAccountSet(ctx, "XRP")
	if err != nil {
		return false, false, err
	}
	toOwned = accounts[strings.ToLower(to)]
	if tag != nil {
		if xaddr, err := xrpClassicToXAddress(to, tag, xrpUseTestnet()); err == nil {
			toOwned = toOwned || accounts[strings.ToLower(xaddr)]
		}
	}
	fromOwned = accounts[strings.ToLower(from)]
	return toOwned, fromOwned, nil
}

func (s *Server) xrpAccountSet(ctx context.Context, crypto string) (map[string]bool, error) {
	accounts := map[string]bool{}
	if s.store != nil {
		rows, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Address == "" {
				continue
			}
			accounts[strings.ToLower(row.Address)] = true
			if parsed, err := xrpParseDestination(row.Address); err == nil {
				accounts[strings.ToLower(parsed.ClassicAddress)] = true
			}
		}
	}
	if account, err := s.xrpFundingAccount(); err == nil {
		accounts[strings.ToLower(account)] = true
	}
	return accounts, nil
}

func (s *Server) xrpLatestLedgerIndex(ctx context.Context) (int64, error) {
	var result struct {
		Ledger struct {
			LedgerIndex any `json:"ledger_index"`
		} `json:"ledger"`
		LedgerIndex any `json:"ledger_index"`
	}
	if err := s.xrpRPC(ctx, "ledger", map[string]any{"ledger_index": "validated", "transactions": false, "expand": false}, &result); err != nil {
		return 0, err
	}
	if result.Ledger.LedgerIndex != nil {
		return int64FromAny(result.Ledger.LedgerIndex), nil
	}
	return int64FromAny(result.LedgerIndex), nil
}

func (s *Server) xrpFundingAccount() (string, error) {
	value := firstNonEmpty(getenv("XRP_ACCOUNT_ADDRESS", ""), getenv("XRP_CLASSIC_ADDRESS", ""), getenv("XRP_ADDRESS", ""))
	if value == "" {
		return "", errors.New("XRP_ACCOUNT_ADDRESS is not configured")
	}
	dest, err := xrpParseDestination(value)
	if err != nil {
		return "", err
	}
	return dest.ClassicAddress, nil
}

func (s *Server) xrpRPC(ctx context.Context, method string, params map[string]any, result any) error {
	endpoint := normalizeHTTPURL(s.cfg.FullnodeURL)
	body, err := json.Marshal(map[string]any{"method": method, "params": []any{params}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("xrp rpc %s returned %d: %s", method, resp.StatusCode, string(data))
	}
	var payload struct {
		Result       json.RawMessage `json:"result"`
		Status       string          `json:"status"`
		Error        string          `json:"error"`
		ErrorMessage string          `json:"error_message"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return err
	}
	if payload.Error != "" {
		return fmt.Errorf("xrp rpc %s error: %s %s", method, payload.Error, payload.ErrorMessage)
	}
	if payload.Status == "error" {
		return fmt.Errorf("xrp rpc %s returned error status", method)
	}
	if result == nil {
		return nil
	}
	dec = json.NewDecoder(bytes.NewReader(payload.Result))
	dec.UseNumber()
	return dec.Decode(result)
}

func xrpParseDestination(value string) (xrpDestination, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return xrpDestination{}, errors.New("empty xrp address")
	}
	if strings.Contains(value, ":") {
		parts := strings.SplitN(value, ":", 2)
		tag, err := parseUint32(strings.TrimSpace(parts[1]))
		if err != nil {
			return xrpDestination{}, err
		}
		return xrpDestination{ClassicAddress: strings.TrimSpace(parts[0]), Tag: &tag}, nil
	}
	if strings.HasPrefix(value, "X") || strings.HasPrefix(value, "T") {
		return xrpXAddressToClassic(value)
	}
	return xrpDestination{ClassicAddress: value}, nil
}

func xrpClassicToXAddress(classic string, tag *uint32, testnet bool) (string, error) {
	accountID, err := xrpClassicAccountID(classic)
	if err != nil {
		return "", err
	}
	payload := make([]byte, 0, 29)
	payload = append(payload, accountID...)
	if tag == nil {
		payload = append(payload, make([]byte, 9)...)
	} else {
		payload = append(payload, 1)
		tagBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(tagBytes, *tag)
		payload = append(payload, tagBytes...)
		payload = append(payload, 0, 0, 0, 0)
	}
	prefix := xrpMainnetXAddressPrefix
	if testnet {
		prefix = xrpTestnetXAddressPrefix
	}
	return xrpBase58CheckEncode(append(prefix, payload...)), nil
}

func xrpXAddressToClassic(value string) (xrpDestination, error) {
	decoded, err := xrpBase58CheckDecode(value)
	if err != nil {
		return xrpDestination{}, err
	}
	testnet := false
	switch {
	case bytes.HasPrefix(decoded, xrpMainnetXAddressPrefix):
		decoded = decoded[len(xrpMainnetXAddressPrefix):]
	case bytes.HasPrefix(decoded, xrpTestnetXAddressPrefix):
		decoded = decoded[len(xrpTestnetXAddressPrefix):]
		testnet = true
	default:
		return xrpDestination{}, errors.New("invalid x-address prefix")
	}
	if len(decoded) != 29 {
		return xrpDestination{}, fmt.Errorf("invalid x-address payload length: %d", len(decoded))
	}
	accountID := decoded[:20]
	flag := decoded[20]
	reserved := decoded[25:]
	if !bytes.Equal(reserved, []byte{0, 0, 0, 0}) {
		return xrpDestination{}, errors.New("invalid x-address reserved bytes")
	}
	var tag *uint32
	switch flag {
	case 0:
		if !bytes.Equal(decoded[21:25], []byte{0, 0, 0, 0}) {
			return xrpDestination{}, errors.New("x-address has tag bytes without tag flag")
		}
	case 1:
		value := binary.LittleEndian.Uint32(decoded[21:25])
		tag = &value
	default:
		return xrpDestination{}, fmt.Errorf("invalid x-address tag flag: %d", flag)
	}
	return xrpDestination{ClassicAddress: xrpClassicAddress(accountID), Tag: tag, Testnet: testnet}, nil
}

func xrpClassicAddress(accountID []byte) string {
	return xrpBase58CheckEncode(append([]byte{xrpAccountVersion}, accountID...))
}

func xrpClassicAccountID(address string) ([]byte, error) {
	decoded, err := xrpBase58CheckDecode(address)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 21 || decoded[0] != xrpAccountVersion {
		return nil, errors.New("invalid classic xrp address")
	}
	return decoded[1:], nil
}

func xrpAccountIDFromPublicKey(publicKey []byte) []byte {
	sha := sha256.Sum256(publicKey)
	h := ripemd160.New()
	_, _ = h.Write(sha[:])
	return h.Sum(nil)
}

func xrpBase58CheckEncode(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return xrpBase58Encode(append(append([]byte{}, payload...), second[:4]...))
}

func xrpBase58CheckDecode(value string) ([]byte, error) {
	decoded, err := xrpBase58Decode(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 5 {
		return nil, errors.New("xrp base58check value is too short")
	}
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(checksum, second[:4]) {
		return nil, errors.New("xrp base58check checksum mismatch")
	}
	return payload, nil
}

func xrpBase58Encode(data []byte) string {
	x := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, xrpBase58Alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		out = append(out, xrpBase58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func xrpBase58Decode(value string) ([]byte, error) {
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range value {
		index := int64(-1)
		for i, b := range xrpBase58Alphabet {
			if r == b {
				index = int64(i)
				break
			}
		}
		if index < 0 {
			return nil, errors.New("invalid xrp base58 character")
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(index))
	}
	out := x.Bytes()
	leading := 0
	for _, r := range value {
		if r != rune(xrpBase58Alphabet[0]) {
			break
		}
		leading++
	}
	if leading > 0 {
		out = append(make([]byte, leading), out...)
	}
	return out, nil
}

func xrpAmountFromRaw(raw json.RawMessage) (decimal.Decimal, error) {
	var drops json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&drops); err == nil {
		return xrpDropsToDecimal(drops), nil
	}
	var dropsString string
	if err := json.Unmarshal(raw, &dropsString); err == nil {
		return xrpDropsToDecimal(dropsString), nil
	}
	var tokenAmount map[string]any
	if err := json.Unmarshal(raw, &tokenAmount); err == nil {
		return decimal.Zero, errors.New("issued-currency XRP transaction amounts are not supported")
	}
	return decimal.Zero, errors.New("invalid XRP amount")
}

func xrpDropsToDecimal(value any) decimal.Decimal {
	amount, ok := decimalFromAny(value)
	if !ok {
		return decimal.Zero
	}
	return decimalFromBaseUnits(amount, xrpDropsDecimals)
}

func randomUint32() (uint32, error) {
	id, err := randomID()
	if err != nil {
		return 0, err
	}
	raw, err := hex.DecodeString(id[:8])
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(raw), nil
}

func parseUint32(value string) (uint32, error) {
	i, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(i), nil
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case json.Number:
		i, _ := strconv.ParseInt(string(v), 10, 64)
		return i
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	default:
		return 0
	}
}

func normalizeHTTPURL(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return endpoint
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	return endpoint
}

func xrpUseTestnet() bool {
	value := strings.ToLower(strings.TrimSpace(getenv("XRP_TESTNET", "")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
