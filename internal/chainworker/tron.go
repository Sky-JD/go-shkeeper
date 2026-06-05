package chainworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

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
		if err := s.tronHTTPJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getaccount", map[string]any{
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
	if err := s.tronHTTPJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/triggerconstantcontract", map[string]any{
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

func (s *Server) tronHTTPJSON(ctx context.Context, method string, url string, body any, out any) error {
	retries := intEnv("TRON_HTTP_MAX_RETRIES", 2)
	if retries < 0 {
		retries = 0
	}
	var err error
	for attempt := 0; attempt <= retries; attempt++ {
		err = s.httpJSON(ctx, method, url, body, out)
		if err == nil || !isTRONRateLimitError(err) || attempt == retries {
			return err
		}
		if sleepErr := sleepContext(ctx, tronRateLimitRetryDelay()); sleepErr != nil {
			return sleepErr
		}
	}
	return err
}

func isTRONRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "returned 429") || strings.Contains(text, "request rate exceeded") || strings.Contains(text, "allowed_rps")
}

func tronRateLimitRetryDelay() time.Duration {
	ms := intEnv("TRON_RATE_LIMIT_RETRY_MS", 5500)
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

func tronBalanceQueryInterval() time.Duration {
	ms := intEnv("TRON_BALANCE_QUERY_INTERVAL_MS", 400)
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type tronSpendableReport struct {
	Total        decimal.Decimal
	Max          decimal.Decimal
	MaxAddress   string
	MaxAccount   *Account
	AccountCount int
	Checked      int
	Failed       int
	FirstErr     error
}

type tronSpendableCacheEntry struct {
	Report      tronSpendableReport
	RefreshedAt time.Time
	LastError   string
	LastErrorAt time.Time
}

func (s *Server) tronSpendableRefreshLoop(ctx context.Context) {
	cryptos := tronBalanceRefreshCryptos()
	if len(cryptos) == 0 {
		if s.logger != nil {
			s.logger.Info("tron balance cache disabled because no TRON cryptos are enabled")
		}
		return
	}
	if boolEnv("TRON_BALANCE_REFRESH_ON_START", true) {
		s.refreshTRONSpendableCryptos(ctx, cryptos)
	}
	interval := tronBalanceRefreshInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshTRONSpendableCryptos(ctx, cryptos)
		}
	}
}

func (s *Server) refreshTRONSpendableCryptos(ctx context.Context, cryptos []string) {
	for _, crypto := range cryptos {
		if ctx.Err() != nil {
			return
		}
		refreshCtx, cancel := context.WithTimeout(ctx, tronBalanceRefreshTimeout())
		_, err := s.refreshTRONSpendable(refreshCtx, crypto)
		cancel()
		if err != nil && s.logger != nil {
			s.logger.Warn("tron balance cache refresh failed", "crypto", crypto, "error", err)
		}
	}
}

func (s *Server) refreshTRONSpendable(ctx context.Context, crypto string) (tronSpendableReport, error) {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	report, err := s.tronSpendable(ctx, crypto)
	s.storeTRONSpendableCache(crypto, report, err)
	return report, err
}

func (s *Server) storeTRONSpendableCache(crypto string, report tronSpendableReport, err error) {
	if s == nil {
		return
	}
	now := time.Now()
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	s.tronSpendableMu.Lock()
	defer s.tronSpendableMu.Unlock()
	entry := s.tronSpendableCache[crypto]
	if err != nil {
		entry.LastError = err.Error()
		entry.LastErrorAt = now
		if entry.RefreshedAt.IsZero() {
			entry.Report = report
		}
		s.tronSpendableCache[crypto] = entry
		return
	}
	s.tronSpendableCache[crypto] = tronSpendableCacheEntry{
		Report:      report,
		RefreshedAt: now,
	}
}

func (s *Server) loadTRONSpendableCache(crypto string) (tronSpendableCacheEntry, bool) {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	s.tronSpendableMu.RLock()
	defer s.tronSpendableMu.RUnlock()
	entry, ok := s.tronSpendableCache[crypto]
	return entry, ok
}

func (s *Server) cachedTRONSpendable(ctx context.Context, crypto string, refresh bool) (tronSpendableCacheEntry, bool, error) {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	if refresh || !boolEnv("TRON_BALANCE_CACHE_ENABLED", true) {
		_, err := s.refreshTRONSpendable(ctx, crypto)
		entry, ok := s.loadTRONSpendableCache(crypto)
		return entry, ok && !entry.RefreshedAt.IsZero(), err
	}
	entry, ok := s.loadTRONSpendableCache(crypto)
	if ok && !entry.RefreshedAt.IsZero() {
		return entry, true, nil
	}
	s.kickTRONSpendableRefresh(crypto)
	entry, ok = s.loadTRONSpendableCache(crypto)
	return entry, ok && !entry.RefreshedAt.IsZero(), nil
}

func (s *Server) kickTRONSpendableRefresh(crypto string) {
	if !boolEnv("TRON_BALANCE_CACHE_ENABLED", true) {
		return
	}
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	if crypto == "" {
		return
	}
	s.tronRefreshMu.Lock()
	if s.tronRefreshing[crypto] {
		s.tronRefreshMu.Unlock()
		return
	}
	s.tronRefreshing[crypto] = true
	s.tronRefreshMu.Unlock()
	go func() {
		defer func() {
			s.tronRefreshMu.Lock()
			delete(s.tronRefreshing, crypto)
			s.tronRefreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), tronBalanceRefreshTimeout())
		defer cancel()
		if _, err := s.refreshTRONSpendable(ctx, crypto); err != nil && s.logger != nil {
			s.logger.Warn("tron balance cache async refresh failed", "crypto", crypto, "error", err)
		}
	}()
}

func (s *Server) tronRefreshInProgress(crypto string) bool {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	s.tronRefreshMu.Lock()
	defer s.tronRefreshMu.Unlock()
	return s.tronRefreshing[crypto]
}

func (s *Server) tronSpendableForPayout(ctx context.Context, crypto string, amount decimal.Decimal) (tronSpendableReport, error) {
	entry, ready, err := s.cachedTRONSpendable(ctx, crypto, false)
	if err != nil {
		return entry.Report, err
	}
	if ready {
		if tronSpendableCacheEntryStale(entry) {
			s.kickTRONSpendableRefresh(crypto)
		}
		return entry.Report, nil
	}
	s.kickTRONSpendableRefresh(crypto)
	return entry.Report, fmt.Errorf("TRON %s balance cache is warming up", strings.ToUpper(strings.TrimSpace(crypto)))
}

func tronBalanceRefreshInterval() time.Duration {
	seconds := intEnv("TRON_BALANCE_REFRESH_INTERVAL_SECONDS", 60)
	if seconds < 5 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

func tronBalanceRefreshTimeout() time.Duration {
	seconds := intEnv("TRON_BALANCE_REFRESH_TIMEOUT_SECONDS", 120)
	if seconds < 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}

func tronBalanceCacheMaxAge() time.Duration {
	seconds := intEnv("TRON_BALANCE_CACHE_MAX_AGE_SECONDS", 180)
	if seconds < 0 {
		seconds = 0
	}
	return time.Duration(seconds) * time.Second
}

func tronSpendableCacheEntryStale(entry tronSpendableCacheEntry) bool {
	if entry.RefreshedAt.IsZero() {
		return true
	}
	maxAge := tronBalanceCacheMaxAge()
	return maxAge > 0 && time.Since(entry.RefreshedAt) > maxAge
}

func tronBalanceRefreshCryptos() []string {
	source := firstNonEmpty(os.Getenv("TRON_BALANCE_REFRESH_CRYPTOS"), os.Getenv("SHKEEPER_CRYPTOS"))
	if strings.TrimSpace(source) == "" {
		return []string{"TRX", "USDT", "USDC"}
	}
	enabled := strings.TrimSpace(os.Getenv("SHKEEPER_CRYPTOS")) != "" && strings.TrimSpace(os.Getenv("TRON_BALANCE_REFRESH_CRYPTOS")) == ""
	out := make([]string, 0, 3)
	seen := map[string]struct{}{}
	var add func(string)
	add = func(value string) {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			return
		}
		if value == "TRON" {
			for _, item := range []string{"TRX", "USDT", "USDC"} {
				add(item)
			}
			return
		}
		if value != "TRX" && value != "USDT" && value != "USDC" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, item := range splitCSV(source) {
		add(item)
	}
	if enabled {
		return out
	}
	if len(out) == 0 {
		return []string{"TRX", "USDT", "USDC"}
	}
	return out
}

func (s *Server) tronSpendable(ctx context.Context, crypto string) (tronSpendableReport, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return tronSpendableReport{}, err
	}
	if len(accounts) == 0 && crypto != "TRX" {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
		if err != nil {
			return tronSpendableReport{}, err
		}
	}
	report := tronSpendableReport{AccountCount: len(accounts)}
	for i, account := range accounts {
		if i > 0 {
			if err := sleepContext(ctx, tronBalanceQueryInterval()); err != nil {
				return report, err
			}
		}
		balance, err := s.tronBalance(ctx, crypto, account.Address)
		if err != nil {
			report.Failed++
			if report.FirstErr == nil {
				report.FirstErr = err
			}
			if s.logger != nil {
				s.logger.Warn("tron balance lookup failed", "crypto", crypto, "address", account.Address, "error", err)
			}
			continue
		}
		report.Checked++
		report.Total = report.Total.Add(balance)
		if balance.GreaterThan(report.Max) {
			accountCopy := account
			report.Max = balance
			report.MaxAddress = account.Address
			report.MaxAccount = &accountCopy
		}
	}
	if report.Checked == 0 && report.Failed > 0 {
		return report, fmt.Errorf("TRON %s balance lookup failed for all %d accounts; first error: %v", crypto, report.Failed, report.FirstErr)
	}
	return report, nil
}

func (s *Server) broadcastTRONPayout(ctx context.Context, crypto string, destination string, amount decimal.Decimal) (broadcastResult, error) {
	report, err := s.tronSpendableForPayout(ctx, crypto, amount)
	if err != nil {
		return broadcastResult{}, err
	}
	if report.MaxAccount == nil || report.Max.LessThan(amount) {
		return broadcastResult{}, fmt.Errorf("no %s account has enough balance for payout; requested=%s total=%s max_single_account=%s account_count=%d", crypto, amount.String(), report.Total.String(), report.Max.String(), report.AccountCount)
	}
	txid, err := s.signAndBroadcastTRON(ctx, *report.MaxAccount, crypto, destination, amount)
	if err != nil {
		return broadcastResult{}, err
	}
	return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
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
			contract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
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
