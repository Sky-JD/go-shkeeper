package chainworker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const (
	bnbNativeGasLimit = uint64(21_000)
	bnbTokenGasLimit  = uint64(100_000)
)

type broadcastResult struct {
	Dest          string     `json:"dest"`
	TxIDs         []string   `json:"txids"`
	Status        string     `json:"status"`
	Sources       []string   `json:"sources,omitempty"`
	Amounts       []string   `json:"amounts,omitempty"`
	GasTopupTxIDs []string   `json:"gas_topup_txids,omitempty"`
	Details       []txDetail `json:"details,omitempty"`
	ActualFee     string     `json:"actual_fee,omitempty"`
	FeeAsset      string     `json:"fee_asset,omitempty"`
	FeeTxIDs      []string   `json:"fee_txids,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type txDetail struct {
	Kind        string `json:"kind,omitempty"`
	TxID        string `json:"txid,omitempty"`
	Status      string `json:"status,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	Amount      string `json:"amount,omitempty"`
	Crypto      string `json:"crypto,omitempty"`
	Error       string `json:"error,omitempty"`
}

type evmSpendableAccount struct {
	Account       Account
	Balance       decimal.Decimal
	NativeBalance decimal.Decimal
}

type evmSpendableCacheEntry struct {
	Candidates  []evmSpendableAccount
	CachedAt    time.Time
	Payload     map[string]any
	RefreshedAt time.Time
	LastError   string
	LastErrorAt time.Time
}

type evmSpendableScanResult struct {
	Account evmSpendableAccount
	Checked bool
	OK      bool
}

type evmPayoutPart struct {
	Account Account
	Amount  decimal.Decimal
}

type evmGasFundingAccount struct {
	Account   Account
	Available decimal.Decimal
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
	accounts, err := s.accountsForCrypto(ctx, crypto)
	if err != nil {
		return broadcastResult{}, err
	}
	gasPrice, err := s.gasPrice(ctx)
	if err != nil {
		return broadcastResult{}, err
	}
	candidates, err := s.evmSpendableAccounts(ctx, crypto, accounts)
	if err != nil {
		return broadcastResult{}, err
	}
	gasLimit := s.evmPayoutGasLimit(ctx, crypto, destination, amount, candidates)
	fee := evmNativeFee(gasPrice, gasLimit)
	gasTopupDetails, err := s.ensureEVMPayoutGas(ctx, crypto, candidates, amount, fee, gasPrice)
	if err != nil {
		result := broadcastResult{
			Dest:          destination,
			Status:        "FAIL",
			GasTopupTxIDs: txIDsFromDetails(gasTopupDetails),
			Details:       gasTopupDetails,
			Error:         err.Error(),
		}
		return result, err
	}
	if len(gasTopupDetails) > 0 {
		candidates, err = s.evmSpendableAccounts(ctx, crypto, accounts)
		if err != nil {
			result := broadcastResult{
				Dest:          destination,
				Status:        "FAIL",
				GasTopupTxIDs: txIDsFromDetails(gasTopupDetails),
				Details:       gasTopupDetails,
				Error:         err.Error(),
			}
			return result, err
		}
	}
	parts := selectEVMPayoutParts(candidates, amount, fee, s.isNative(crypto), !s.isNative(crypto))
	if len(parts) == 0 {
		report := evmSpendableSummary(candidates, fee, s.isNative(crypto))
		return broadcastResult{}, fmt.Errorf("no %s account has enough balance and gas for payout; requested=%s spendable=%s max_single_account=%s funded_account_count=%d account_count=%d required_native_per_tx=%s",
			crypto, amount.String(), report.Total.String(), report.Max.String(), report.FundedAccountCount, len(accounts), fee.String())
	}
	txids := make([]string, 0, len(parts))
	sources := make([]string, 0, len(parts))
	amounts := make([]string, 0, len(parts))
	details := append([]txDetail{}, gasTopupDetails...)
	for _, part := range parts {
		partGasLimit := gasLimit
		if !s.isNative(crypto) {
			partGasLimit = s.evmTransferGasLimit(ctx, crypto, part.Account.Address, destination, part.Amount, gasLimit)
		}
		txid, err := s.signAndBroadcastBNB(ctx, part.Account, crypto, destination, part.Amount, partGasLimit, gasPrice)
		if err != nil {
			details = append(details, txDetail{
				Kind:        "payout",
				Status:      "FAIL",
				Source:      part.Account.Address,
				Destination: destination,
				Amount:      part.Amount.String(),
				Crypto:      crypto,
				Error:       err.Error(),
			})
			result := broadcastResult{
				Dest:          destination,
				TxIDs:         txids,
				Status:        partialStatusForPayoutTxIDs(txids),
				Sources:       sources,
				Amounts:       amounts,
				GasTopupTxIDs: txIDsFromDetails(gasTopupDetails),
				Details:       details,
				Error:         err.Error(),
			}
			return result, err
		}
		txids = append(txids, txid)
		sources = append(sources, part.Account.Address)
		amounts = append(amounts, part.Amount.String())
		details = append(details, txDetail{
			Kind:        "payout",
			TxID:        txid,
			Status:      "IN_PROGRESS",
			Source:      part.Account.Address,
			Destination: destination,
			Amount:      part.Amount.String(),
			Crypto:      crypto,
		})
	}
	result := broadcastResult{Dest: destination, TxIDs: txids, Status: "success", Sources: sources, Amounts: amounts, GasTopupTxIDs: txIDsFromDetails(gasTopupDetails), Details: details}
	feeTxIDs := append([]string{}, result.GasTopupTxIDs...)
	feeTxIDs = append(feeTxIDs, txids...)
	if actualFee, ok := s.evmActualFeeForTxs(ctx, feeTxIDs, gasPrice); ok {
		result.ActualFee = actualFee.String()
		result.FeeAsset = s.nativeCrypto()
		result.FeeTxIDs = uniqueEVMTxIDs(feeTxIDs)
	}
	return result, nil
}

func evmNativeFee(gasPrice *big.Int, gasLimit uint64) decimal.Decimal {
	return decimal.NewFromBigInt(new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(gasLimit)), 0).Div(decimal.New(1, 18))
}

func (s *Server) evmActualFeeForTxs(ctx context.Context, txids []string, fallbackGasPrice *big.Int) (decimal.Decimal, bool) {
	pending := uniqueEVMTxIDs(txids)
	if len(pending) == 0 {
		return decimal.Zero, false
	}
	waitSeconds := int64Env("EVM_PAYOUT_RECEIPT_WAIT_SECONDS", 0)
	if waitSeconds <= 0 {
		return decimal.Zero, false
	}
	total := decimal.Zero
	for {
		next := pending[:0]
		for _, txid := range pending {
			fee, err := s.evmReceiptFee(ctx, txid, fallbackGasPrice)
			if err != nil {
				next = append(next, txid)
				continue
			}
			total = total.Add(fee)
		}
		if len(next) == 0 {
			return total, true
		}
		pending = next
		break
	}
	deadline := time.After(time.Duration(waitSeconds) * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return decimal.Zero, false
		case <-deadline:
			return decimal.Zero, false
		case <-ticker.C:
			next := pending[:0]
			for _, txid := range pending {
				fee, err := s.evmReceiptFee(ctx, txid, fallbackGasPrice)
				if err != nil {
					next = append(next, txid)
					continue
				}
				total = total.Add(fee)
			}
			if len(next) == 0 {
				return total, true
			}
			pending = next
		}
	}
}

func (s *Server) evmReceiptFee(ctx context.Context, txid string, fallbackGasPrice *big.Int) (decimal.Decimal, error) {
	var receipt struct {
		GasUsed           string `json:"gasUsed"`
		EffectiveGasPrice string `json:"effectiveGasPrice"`
	}
	if err := s.rpc(ctx, "eth_getTransactionReceipt", []any{txid}, &receipt); err != nil {
		return decimal.Zero, err
	}
	if strings.TrimSpace(receipt.GasUsed) == "" {
		return decimal.Zero, errors.New("transaction receipt is not ready")
	}
	gasUsed, err := hexToDecimal(receipt.GasUsed)
	if err != nil {
		return decimal.Zero, err
	}
	gasPrice := fallbackGasPrice
	if strings.TrimSpace(receipt.EffectiveGasPrice) != "" {
		value, err := hexToDecimal(receipt.EffectiveGasPrice)
		if err != nil {
			return decimal.Zero, err
		}
		gasPrice = value.BigInt()
	} else if txGasPrice, err := s.evmTransactionGasPrice(ctx, txid); err == nil {
		gasPrice = txGasPrice
	}
	if gasPrice == nil || gasPrice.Sign() <= 0 {
		return decimal.Zero, errors.New("transaction gas price is unavailable")
	}
	feeWei := new(big.Int).Mul(gasUsed.BigInt(), gasPrice)
	return decimal.NewFromBigInt(feeWei, 0).Div(decimal.New(1, 18)), nil
}

func (s *Server) evmTransactionGasPrice(ctx context.Context, txid string) (*big.Int, error) {
	var tx struct {
		GasPrice string `json:"gasPrice"`
	}
	if err := s.rpc(ctx, "eth_getTransactionByHash", []any{txid}, &tx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tx.GasPrice) == "" {
		return nil, errors.New("transaction gas price is unavailable")
	}
	value, err := hexToDecimal(tx.GasPrice)
	if err != nil {
		return nil, err
	}
	return value.BigInt(), nil
}

func uniqueEVMTxIDs(txids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(txids))
	for _, txid := range txids {
		txid = strings.TrimSpace(txid)
		if txid == "" {
			continue
		}
		key := strings.ToLower(txid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, txid)
	}
	return out
}

func txIDsFromDetails(details []txDetail) []string {
	out := make([]string, 0, len(details))
	for _, detail := range details {
		if strings.TrimSpace(detail.TxID) != "" {
			out = append(out, detail.TxID)
		}
	}
	return uniqueEVMTxIDs(out)
}

func partialStatusForPayoutTxIDs(txids []string) string {
	if len(uniqueEVMTxIDs(txids)) > 0 {
		return "PARTIAL"
	}
	return "FAIL"
}

func evmTokenGasLimitFallback() uint64 {
	value := int64Env("EVM_TOKEN_GAS_LIMIT", int64(bnbTokenGasLimit))
	if value < int64(bnbNativeGasLimit) {
		return bnbTokenGasLimit
	}
	return uint64(value)
}

func evmEstimatedGasLimit(value uint64) uint64 {
	bufferPercent := int64Env("EVM_ESTIMATE_GAS_BUFFER_PERCENT", 0)
	if bufferPercent <= 0 {
		return value
	}
	if bufferPercent > 100 {
		bufferPercent = 100
	}
	return uint64((uint64(100+bufferPercent)*value + 99) / 100)
}

func (s *Server) evmPayoutGasLimit(ctx context.Context, crypto string, destination string, amount decimal.Decimal, candidates []evmSpendableAccount) uint64 {
	if s.isNative(crypto) {
		return bnbNativeGasLimit
	}
	source, estimateAmount, ok := evmEstimateSource(candidates, amount)
	if !ok {
		return evmTokenGasLimitFallback()
	}
	return s.evmTransferGasLimit(ctx, crypto, source.Account.Address, destination, estimateAmount, evmTokenGasLimitFallback())
}

func (s *Server) evmTransferGasLimit(ctx context.Context, crypto string, source string, destination string, amount decimal.Decimal, fallback uint64) uint64 {
	limit, err := s.estimateEVMTransferGasLimit(ctx, crypto, source, destination, amount)
	if err != nil {
		return fallback
	}
	return limit
}

func (s *Server) estimateEVMTransferGasLimit(ctx context.Context, crypto string, source string, destination string, amount decimal.Decimal) (uint64, error) {
	if s.isNative(crypto) {
		return bnbNativeGasLimit, nil
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(destination) == "" || !amount.GreaterThan(decimal.Zero) {
		return 0, errors.New("source, destination and positive amount are required for EVM gas estimation")
	}
	contract, decimals, err := tokenConfig(crypto)
	if err != nil {
		return 0, err
	}
	if _, err := evmAddressBytes(source); err != nil {
		return 0, err
	}
	if _, err := evmAddressBytes(contract); err != nil {
		return 0, err
	}
	destinationBytes, err := evmAddressBytes(destination)
	if err != nil {
		return 0, err
	}
	data := evmCallData("transfer(address,uint256)", destinationBytes, amountToBaseUnits(amount, decimals).Bytes())
	var hexGas string
	if err := s.rpc(ctx, "eth_estimateGas", []any{map[string]any{
		"from":  source,
		"to":    contract,
		"value": "0x0",
		"data":  "0x" + hex.EncodeToString(data),
	}}, &hexGas); err != nil {
		return 0, err
	}
	gas, err := hexToInt(hexGas)
	if err != nil {
		return 0, err
	}
	if gas <= 0 {
		return 0, fmt.Errorf("eth_estimateGas returned non-positive gas: %s", hexGas)
	}
	return evmEstimatedGasLimit(uint64(gas)), nil
}

func evmEstimateSource(candidates []evmSpendableAccount, amount decimal.Decimal) (evmSpendableAccount, decimal.Decimal, bool) {
	eligible := make([]evmSpendableAccount, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Balance.GreaterThan(decimal.Zero) {
			eligible = append(eligible, candidate)
		}
	}
	if len(eligible) == 0 {
		return evmSpendableAccount{}, decimal.Zero, false
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].Balance.GreaterThan(eligible[j].Balance)
	})
	estimateAmount := amount
	if !estimateAmount.GreaterThan(decimal.Zero) || estimateAmount.GreaterThan(eligible[0].Balance) {
		estimateAmount = eligible[0].Balance
	}
	return eligible[0], estimateAmount, true
}

func (s *Server) evmSpendableAccounts(ctx context.Context, crypto string, accounts []Account) ([]evmSpendableAccount, error) {
	if len(accounts) == 0 {
		return []evmSpendableAccount{}, nil
	}
	results := make([]evmSpendableScanResult, len(accounts))
	limit := make(chan struct{}, evmBalanceScanConcurrency(len(accounts)))
	var wg sync.WaitGroup
	for i, account := range accounts {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		case limit <- struct{}{}:
		}
		wg.Add(1)
		go func(index int, item Account) {
			defer wg.Done()
			defer func() { <-limit }()
			results[index] = s.evmSpendableAccount(ctx, crypto, item)
		}(i, account)
	}
	wg.Wait()

	out := make([]evmSpendableAccount, 0, len(accounts))
	var checked int
	for _, result := range results {
		if result.Checked {
			checked++
		}
		if result.OK {
			out = append(out, result.Account)
		}
	}
	if len(out) == 0 && len(accounts) > 0 && checked == 0 {
		return nil, fmt.Errorf("unable to read %s balances from any account", crypto)
	}
	return out, nil
}

func (s *Server) evmSpendableAccount(ctx context.Context, crypto string, account Account) evmSpendableScanResult {
	balance, err := s.bnbBalance(ctx, crypto, account.Address)
	if err != nil {
		return evmSpendableScanResult{}
	}
	nativeBalance := balance
	if !s.isNative(crypto) {
		nativeBalance, err = s.nativeBalance(ctx, account.Address)
		if err != nil {
			return evmSpendableScanResult{}
		}
	}
	return evmSpendableScanResult{
		Account: evmSpendableAccount{Account: account, Balance: balance, NativeBalance: nativeBalance},
		Checked: true,
		OK:      true,
	}
}

func evmBalanceScanConcurrency(count int) int {
	if count <= 1 {
		return 1
	}
	concurrency := intEnv("EVM_BALANCE_SCAN_CONCURRENCY", 8)
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > count {
		concurrency = count
	}
	return concurrency
}

func (s *Server) evmTotalBalance(ctx context.Context, crypto string, accounts []Account) decimal.Decimal {
	if len(accounts) == 0 {
		return decimal.Zero
	}
	results := make([]decimal.Decimal, len(accounts))
	limit := make(chan struct{}, evmBalanceScanConcurrency(len(accounts)))
	var wg sync.WaitGroup
	for i, account := range accounts {
		select {
		case <-ctx.Done():
			wg.Wait()
			return decimal.Zero
		case limit <- struct{}{}:
		}
		wg.Add(1)
		go func(index int, item Account) {
			defer wg.Done()
			defer func() { <-limit }()
			value, err := s.bnbBalance(ctx, crypto, item.Address)
			if err == nil {
				results[index] = value
			}
		}(i, account)
	}
	wg.Wait()

	total := decimal.Zero
	for _, value := range results {
		total = total.Add(value)
	}
	return total
}

func (s *Server) evmSpendableAccountsForQuote(ctx context.Context, crypto string, accounts []Account, forceRefresh bool) ([]evmSpendableAccount, error) {
	ttl := evmSpendableCacheTTL()
	key := evmSpendableCacheKey(crypto, accounts)
	if !forceRefresh && ttl > 0 {
		entry, ok := s.loadEVMSpendableCacheByKey(key)
		if ok && time.Since(entry.CachedAt) <= ttl {
			return copyEVMSpendableAccounts(entry.Candidates), nil
		}
	}
	candidates, err := s.evmSpendableAccounts(ctx, crypto, accounts)
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		s.storeEVMSpendableCache(crypto, accounts, candidates, nil, nil)
	}
	return candidates, nil
}

func evmSpendableCacheTTL() time.Duration {
	seconds := intEnv("EVM_SPENDABLE_CACHE_SECONDS", 60)
	if seconds < 0 {
		seconds = 0
	}
	return time.Duration(seconds) * time.Second
}

func evmSpendableRefreshInterval() time.Duration {
	seconds := intEnv("EVM_SPENDABLE_REFRESH_INTERVAL_SECONDS", 60)
	if seconds < 5 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

func evmSpendableRefreshTimeout() time.Duration {
	seconds := intEnv("EVM_SPENDABLE_REFRESH_TIMEOUT_SECONDS", 120)
	if seconds < 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}

func evmSpendableCacheEntryStale(entry evmSpendableCacheEntry) bool {
	if entry.RefreshedAt.IsZero() {
		return true
	}
	ttl := evmSpendableCacheTTL()
	return ttl > 0 && time.Since(entry.RefreshedAt) > ttl
}

func evmSpendableCacheKey(crypto string, accounts []Account) string {
	parts := make([]string, 0, len(accounts)+1)
	parts = append(parts, strings.ToUpper(crypto))
	for _, account := range accounts {
		parts = append(parts, strings.ToLower(strings.TrimSpace(account.Address)))
	}
	return strings.Join(parts, "|")
}

func (s *Server) loadEVMSpendableCacheByKey(key string) (evmSpendableCacheEntry, bool) {
	s.evmSpendableMu.RLock()
	defer s.evmSpendableMu.RUnlock()
	entry, ok := s.evmSpendableCache[key]
	if ok {
		entry.Candidates = copyEVMSpendableAccounts(entry.Candidates)
		entry.Payload = copyMap(entry.Payload)
	}
	return entry, ok
}

func (s *Server) loadEVMSpendableCache(crypto string, accounts []Account) (evmSpendableCacheEntry, bool) {
	return s.loadEVMSpendableCacheByKey(evmSpendableCacheKey(crypto, accounts))
}

func (s *Server) storeEVMSpendableCache(crypto string, accounts []Account, candidates []evmSpendableAccount, payload map[string]any, err error) {
	if s == nil {
		return
	}
	key := evmSpendableCacheKey(crypto, accounts)
	now := time.Now()
	s.evmSpendableMu.Lock()
	defer s.evmSpendableMu.Unlock()
	if s.evmSpendableCache == nil {
		s.evmSpendableCache = map[string]evmSpendableCacheEntry{}
	}
	entry := s.evmSpendableCache[key]
	if len(candidates) > 0 || candidates != nil {
		entry.Candidates = copyEVMSpendableAccounts(candidates)
		entry.CachedAt = now
	}
	if payload != nil {
		entry.Payload = copyMap(payload)
		entry.RefreshedAt = now
	}
	if err != nil {
		entry.LastError = err.Error()
		entry.LastErrorAt = now
	} else if payload != nil {
		entry.LastError = ""
		entry.LastErrorAt = time.Time{}
	}
	s.evmSpendableCache[key] = entry
}

func copyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func copyEVMSpendableAccounts(src []evmSpendableAccount) []evmSpendableAccount {
	if len(src) == 0 {
		return []evmSpendableAccount{}
	}
	out := make([]evmSpendableAccount, len(src))
	copy(out, src)
	return out
}

func (s *Server) ensureEVMPayoutGas(ctx context.Context, crypto string, candidates []evmSpendableAccount, amount decimal.Decimal, tokenFee decimal.Decimal, gasPrice *big.Int) ([]txDetail, error) {
	if s.isNative(crypto) || len(selectEVMPayoutParts(candidates, amount, tokenFee, false, true)) > 0 {
		return nil, nil
	}
	targetNative := evmGasTopupTarget(tokenFee)
	planned := make([]evmSpendableAccount, len(candidates))
	copy(planned, candidates)
	for i := range planned {
		if planned[i].Balance.GreaterThan(decimal.Zero) && planned[i].NativeBalance.LessThan(tokenFee) {
			planned[i].NativeBalance = targetNative
		}
	}
	parts := selectEVMPayoutParts(planned, amount, tokenFee, false, true)
	if len(parts) == 0 {
		return nil, nil
	}
	actualByAddress := make(map[string]evmSpendableAccount, len(candidates))
	for _, candidate := range candidates {
		actualByAddress[strings.ToLower(candidate.Account.Address)] = candidate
	}
	funders, err := s.evmGasFundingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	nativeTransferFee := evmNativeFee(gasPrice, bnbNativeGasLimit)
	details := make([]txDetail, 0)
	fundedTargets := make(map[string]decimal.Decimal)
	nextNonceByFunder := make(map[string]uint64)
	for _, part := range parts {
		actual := actualByAddress[strings.ToLower(part.Account.Address)]
		if actual.NativeBalance.GreaterThanOrEqual(tokenFee) {
			continue
		}
		topupAmount := targetNative.Sub(actual.NativeBalance)
		if !topupAmount.GreaterThan(decimal.Zero) {
			topupAmount = tokenFee.Sub(actual.NativeBalance)
		}
		requiredFromFunder := topupAmount.Add(nativeTransferFee)
		funderIndex := selectEVMGasFundingAccount(funders, requiredFromFunder, part.Account.Address)
		if funderIndex < 0 {
			return details, fmt.Errorf("no %s gas funding account has enough balance to top up %s; need=%s transfer_fee=%s",
				s.nativeCrypto(), part.Account.Address, topupAmount.String(), nativeTransferFee.String())
		}
		funder := funders[funderIndex]
		funderKey := strings.ToLower(funder.Account.Address)
		nonce, ok := nextNonceByFunder[funderKey]
		if !ok {
			nonce, err = s.nonce(ctx, funder.Account.Address)
			if err != nil {
				return details, err
			}
		}
		txid, err := s.signAndBroadcastBNBWithNonce(ctx, funder.Account, s.nativeCrypto(), part.Account.Address, topupAmount, bnbNativeGasLimit, gasPrice, nonce)
		if err != nil {
			return details, fmt.Errorf("top up %s gas for %s: %w", s.nativeCrypto(), part.Account.Address, err)
		}
		nextNonceByFunder[funderKey] = nonce + 1
		details = append(details, txDetail{
			Kind:        "gas_topup",
			TxID:        txid,
			Status:      "IN_PROGRESS",
			Source:      funder.Account.Address,
			Destination: part.Account.Address,
			Amount:      topupAmount.String(),
			Crypto:      s.nativeCrypto(),
		})
		funders[funderIndex].Available = funders[funderIndex].Available.Sub(requiredFromFunder)
		actual.NativeBalance = actual.NativeBalance.Add(topupAmount)
		actualByAddress[strings.ToLower(part.Account.Address)] = actual
		fundedTargets[part.Account.Address] = targetNative
	}
	for address, target := range fundedTargets {
		if err := s.waitForEVMNativeBalance(ctx, address, target); err != nil {
			return details, err
		}
	}
	return details, nil
}

func evmGasTopupTarget(fee decimal.Decimal) decimal.Decimal {
	return fee
}

func (s *Server) evmGasFundingAccounts(ctx context.Context) ([]evmGasFundingAccount, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, s.nativeCrypto())
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return []evmGasFundingAccount{}, nil
	}
	results := make([]evmGasFundingAccount, len(accounts))
	ok := make([]bool, len(accounts))
	limit := make(chan struct{}, evmBalanceScanConcurrency(len(accounts)))
	var wg sync.WaitGroup
	for i, account := range accounts {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		case limit <- struct{}{}:
		}
		wg.Add(1)
		go func(index int, item Account) {
			defer wg.Done()
			defer func() { <-limit }()
			balance, err := s.nativeBalance(ctx, item.Address)
			if err == nil && balance.GreaterThan(decimal.Zero) {
				results[index] = evmGasFundingAccount{Account: item, Available: balance}
				ok[index] = true
			}
		}(i, account)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]evmGasFundingAccount, 0, len(accounts))
	for i, result := range results {
		if ok[i] {
			out = append(out, result)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Available.GreaterThan(out[j].Available)
	})
	return out, nil
}

func selectEVMGasFundingAccount(funders []evmGasFundingAccount, required decimal.Decimal, targetAddress string) int {
	for i, funder := range funders {
		if strings.EqualFold(funder.Account.Address, targetAddress) {
			continue
		}
		if funder.Available.GreaterThanOrEqual(required) {
			return i
		}
	}
	return -1
}

func (s *Server) waitForEVMNativeBalance(ctx context.Context, address string, minimum decimal.Decimal) error {
	waitSeconds := int64Env("EVM_GAS_TOPUP_WAIT_SECONDS", 30)
	if waitSeconds <= 0 {
		return nil
	}
	deadline := time.After(time.Duration(waitSeconds) * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		balance, err := s.nativeBalance(ctx, address)
		if err == nil && balance.GreaterThanOrEqual(minimum) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("%s gas top-up for %s was broadcast but balance is still below %s", s.nativeCrypto(), address, minimum.String())
		case <-ticker.C:
		}
	}
}

func selectEVMPayoutParts(candidates []evmSpendableAccount, amount decimal.Decimal, fee decimal.Decimal, native bool, allowSplit bool) []evmPayoutPart {
	if !amount.GreaterThan(decimal.Zero) {
		return nil
	}
	eligible := make([]evmSpendableAccount, 0, len(candidates))
	for _, candidate := range candidates {
		spendable := evmAccountSpendable(candidate, fee, native)
		if !spendable.GreaterThan(decimal.Zero) {
			continue
		}
		if spendable.GreaterThan(candidate.Balance) {
			spendable = candidate.Balance
		}
		candidate.Balance = spendable
		eligible = append(eligible, candidate)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].Balance.GreaterThan(eligible[j].Balance)
	})
	for _, candidate := range eligible {
		if candidate.Balance.GreaterThanOrEqual(amount) {
			return []evmPayoutPart{{Account: candidate.Account, Amount: amount}}
		}
	}
	if native || !allowSplit {
		return nil
	}
	remaining := amount
	parts := make([]evmPayoutPart, 0, len(eligible))
	for _, candidate := range eligible {
		part := candidate.Balance
		if remaining.LessThan(part) {
			part = remaining
		}
		if !part.GreaterThan(decimal.Zero) {
			continue
		}
		parts = append(parts, evmPayoutPart{Account: candidate.Account, Amount: part})
		remaining = remaining.Sub(part)
		if !remaining.GreaterThan(decimal.Zero) {
			return parts
		}
	}
	return nil
}

type evmSpendableReport struct {
	Total              decimal.Decimal
	Max                decimal.Decimal
	FundedAccountCount int
	TopupAccountCount  int
	TopupAmount        decimal.Decimal
	TopupTransferFee   decimal.Decimal
}

func evmSpendableSummary(candidates []evmSpendableAccount, fee decimal.Decimal, native bool) evmSpendableReport {
	var report evmSpendableReport
	for _, candidate := range candidates {
		spendable := evmAccountSpendable(candidate, fee, native)
		if spendable.GreaterThan(candidate.Balance) {
			spendable = candidate.Balance
		}
		if spendable.GreaterThan(decimal.Zero) {
			report.FundedAccountCount++
			report.Total = report.Total.Add(spendable)
			if spendable.GreaterThan(report.Max) {
				report.Max = spendable
			}
		}
	}
	return report
}

func evmAutoTopupSpendableSummary(candidates []evmSpendableAccount, fee decimal.Decimal, nativeTransferFee decimal.Decimal, funders []evmGasFundingAccount) evmSpendableReport {
	report := evmSpendableSummary(candidates, fee, false)
	gasStarved := make([]evmSpendableAccount, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Balance.GreaterThan(decimal.Zero) && candidate.NativeBalance.LessThan(fee) {
			gasStarved = append(gasStarved, candidate)
		}
	}
	sort.SliceStable(gasStarved, func(i, j int) bool {
		return gasStarved[i].Balance.GreaterThan(gasStarved[j].Balance)
	})
	targetNative := evmGasTopupTarget(fee)
	for _, candidate := range gasStarved {
		topupAmount := targetNative.Sub(candidate.NativeBalance)
		minimumTopup := fee.Sub(candidate.NativeBalance)
		if topupAmount.LessThan(minimumTopup) {
			topupAmount = minimumTopup
		}
		if !topupAmount.GreaterThan(decimal.Zero) {
			continue
		}
		requiredFromFunder := topupAmount.Add(nativeTransferFee)
		funderIndex := selectEVMGasFundingAccount(funders, requiredFromFunder, candidate.Account.Address)
		if funderIndex < 0 {
			continue
		}
		funders[funderIndex].Available = funders[funderIndex].Available.Sub(requiredFromFunder)
		report.Total = report.Total.Add(candidate.Balance)
		if candidate.Balance.GreaterThan(report.Max) {
			report.Max = candidate.Balance
		}
		report.FundedAccountCount++
		report.TopupAccountCount++
		report.TopupAmount = report.TopupAmount.Add(topupAmount)
		report.TopupTransferFee = report.TopupTransferFee.Add(nativeTransferFee)
	}
	return report
}

func evmAccountSpendable(candidate evmSpendableAccount, fee decimal.Decimal, native bool) decimal.Decimal {
	if native {
		if candidate.NativeBalance.LessThanOrEqual(fee) {
			return decimal.Zero
		}
		return candidate.NativeBalance.Sub(fee)
	}
	if candidate.Balance.LessThanOrEqual(decimal.Zero) || candidate.NativeBalance.LessThan(fee) {
		return decimal.Zero
	}
	return candidate.Balance
}

func (s *Server) evmSpendableReport(ctx context.Context, crypto string, forceRefresh bool) (map[string]any, error) {
	accounts, err := s.accountsForCrypto(ctx, crypto)
	if err != nil {
		return nil, err
	}
	payload, _, err := s.evmSpendableReportForAccounts(ctx, crypto, accounts, forceRefresh)
	return payload, err
}

func (s *Server) evmSpendableReportForAccounts(ctx context.Context, crypto string, accounts []Account, forceRefresh bool) (map[string]any, []evmSpendableAccount, error) {
	gasPrice, err := s.gasPrice(ctx)
	if err != nil {
		return nil, nil, err
	}
	gasLimit := bnbNativeGasLimit
	if !s.isNative(crypto) {
		gasLimit = bnbTokenGasLimit
	}
	fee := evmNativeFee(gasPrice, gasLimit)
	candidates, err := s.evmSpendableAccountsForQuote(ctx, crypto, accounts, forceRefresh)
	if err != nil {
		return nil, nil, err
	}
	directReport := evmSpendableSummary(candidates, fee, s.isNative(crypto))
	report := directReport
	totalBalance := decimal.Zero
	totalNative := decimal.Zero
	for _, candidate := range candidates {
		totalBalance = totalBalance.Add(candidate.Balance)
		totalNative = totalNative.Add(candidate.NativeBalance)
	}
	nativeTransferFee := evmNativeFee(gasPrice, bnbNativeGasLimit)
	gasFundingBalance := decimal.Zero
	gasFundingCount := 0
	gasTopupEnabled := !s.isNative(crypto)
	if !s.isNative(crypto) {
		if funders, err := s.evmGasFundingAccounts(ctx); err == nil {
			gasFundingCount = len(funders)
			for _, funder := range funders {
				gasFundingBalance = gasFundingBalance.Add(funder.Available)
			}
			report = evmAutoTopupSpendableSummary(candidates, fee, nativeTransferFee, funders)
		}
	}
	payload := map[string]any{
		"status":                        "success",
		"crypto":                        crypto,
		"balance":                       totalBalance.String(),
		"spendable":                     report.Total.String(),
		"max_single_account":            report.Max.String(),
		"direct_spendable":              directReport.Total.String(),
		"direct_max_single_account":     directReport.Max.String(),
		"account_count":                 len(accounts),
		"checked":                       len(candidates),
		"funded_account_count":          report.FundedAccountCount,
		"direct_funded_account_count":   directReport.FundedAccountCount,
		"native_balance":                totalNative.String(),
		"required_native_per_tx":        fee.String(),
		"fee":                           fee.String(),
		"fee_asset":                     s.nativeCrypto(),
		"can_split_payout":              !s.isNative(crypto),
		"auto_gas_topup":                gasTopupEnabled,
		"gas_topup_target":              evmGasTopupTarget(fee).String(),
		"gas_topup_account_count":       report.TopupAccountCount,
		"gas_topup_amount":              report.TopupAmount.String(),
		"gas_topup_transfer_fee":        report.TopupTransferFee.String(),
		"gas_topup_transfer_fee_per_tx": nativeTransferFee.String(),
		"gas_funding_balance":           gasFundingBalance.String(),
		"gas_funding_account_count":     gasFundingCount,
		"balance_source":                "evm_accounts_spendable",
	}
	s.storeEVMSpendableCache(crypto, accounts, candidates, payload, nil)
	return payload, candidates, nil
}

func (s *Server) cachedEVMSpendable(ctx context.Context, crypto string, refresh bool) (evmSpendableCacheEntry, bool, error) {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	accounts, err := s.accountsForCrypto(ctx, crypto)
	if err != nil {
		return evmSpendableCacheEntry{}, false, err
	}
	if refresh || !boolEnv("EVM_SPENDABLE_CACHE_ENABLED", true) {
		err := s.refreshEVMSpendableForAccounts(ctx, crypto, accounts)
		entry, ok := s.loadEVMSpendableCache(crypto, accounts)
		return entry, ok && entry.Payload != nil && !entry.RefreshedAt.IsZero(), err
	}
	entry, ok := s.loadEVMSpendableCache(crypto, accounts)
	if ok && entry.Payload != nil && !entry.RefreshedAt.IsZero() {
		if evmSpendableCacheEntryStale(entry) {
			s.kickEVMSpendableRefresh(crypto)
		}
		return entry, true, nil
	}
	s.kickEVMSpendableRefresh(crypto)
	entry, ok = s.loadEVMSpendableCache(crypto, accounts)
	return entry, ok && entry.Payload != nil && !entry.RefreshedAt.IsZero(), nil
}

func (s *Server) refreshEVMSpendableForAccounts(ctx context.Context, crypto string, accounts []Account) error {
	payload, _, err := s.evmSpendableReportForAccounts(ctx, crypto, accounts, true)
	if err != nil {
		s.storeEVMSpendableCache(crypto, accounts, nil, nil, err)
		return err
	}
	s.storeEVMSpendableCache(crypto, accounts, nil, payload, nil)
	return nil
}

func (s *Server) refreshEVMSpendable(ctx context.Context, crypto string) error {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	accounts, err := s.accountsForCrypto(ctx, crypto)
	if err != nil {
		return err
	}
	return s.refreshEVMSpendableForAccounts(ctx, crypto, accounts)
}

func (s *Server) evmSpendablePayload(crypto string, entry evmSpendableCacheEntry, ready bool, err error) map[string]any {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	payload := copyMap(entry.Payload)
	if payload == nil {
		payload = map[string]any{
			"status":             "success",
			"crypto":             crypto,
			"balance":            "0",
			"spendable":          "0",
			"max_single_account": "0",
			"account_count":      0,
			"checked":            0,
			"fee_asset":          s.nativeCrypto(),
			"balance_source":     "warming",
		}
	} else {
		payload["crypto"] = crypto
	}
	payload["cache_ready"] = ready
	payload["refreshing"] = s.evmRefreshInProgress(crypto)
	if !entry.RefreshedAt.IsZero() {
		age := int64(time.Since(entry.RefreshedAt).Seconds())
		if age < 0 {
			age = 0
		}
		payload["refreshed_at"] = entry.RefreshedAt.Format(time.RFC3339)
		payload["cache_age_seconds"] = age
		payload["cache_stale"] = evmSpendableCacheEntryStale(entry)
	} else {
		payload["cache_stale"] = true
		payload["balance_source"] = "warming"
	}
	if entry.LastError != "" {
		payload["balance_error"] = entry.LastError
		if !entry.LastErrorAt.IsZero() {
			payload["last_error_at"] = entry.LastErrorAt.Format(time.RFC3339)
		}
	}
	if err != nil {
		payload["balance_error"] = err.Error()
	}
	if !ready {
		existing, _ := payload["balance_error"].(string)
		payload["balance_error"] = firstNonEmpty(existing, fmt.Sprintf("EVM %s balance cache is warming up", crypto))
	}
	return payload
}

func (s *Server) evmSpendableRefreshLoop(ctx context.Context) {
	cryptos := s.evmSpendableRefreshCryptos()
	if len(cryptos) == 0 {
		if s.logger != nil {
			s.logger.Info("evm spendable cache disabled because no EVM cryptos are enabled", "module", s.cfg.Module)
		}
		return
	}
	if boolEnv("EVM_SPENDABLE_REFRESH_ON_START", true) {
		s.refreshEVMSpendableCryptos(ctx, cryptos)
	}
	ticker := time.NewTicker(evmSpendableRefreshInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshEVMSpendableCryptos(ctx, cryptos)
		}
	}
}

func (s *Server) refreshEVMSpendableCryptos(ctx context.Context, cryptos []string) {
	for _, crypto := range cryptos {
		if ctx.Err() != nil {
			return
		}
		refreshCtx, cancel := context.WithTimeout(ctx, evmSpendableRefreshTimeout())
		err := s.refreshEVMSpendable(refreshCtx, crypto)
		cancel()
		if err != nil && s.logger != nil {
			s.logger.Warn("evm spendable cache refresh failed", "module", s.cfg.Module, "crypto", crypto, "error", err)
		}
	}
}

func (s *Server) kickEVMSpendableRefresh(crypto string) {
	if !boolEnv("EVM_SPENDABLE_CACHE_ENABLED", true) {
		return
	}
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	if crypto == "" {
		return
	}
	s.evmRefreshMu.Lock()
	if s.evmRefreshing == nil {
		s.evmRefreshing = map[string]bool{}
	}
	if s.evmRefreshing[crypto] {
		s.evmRefreshMu.Unlock()
		return
	}
	s.evmRefreshing[crypto] = true
	s.evmRefreshMu.Unlock()
	go func() {
		defer func() {
			s.evmRefreshMu.Lock()
			delete(s.evmRefreshing, crypto)
			s.evmRefreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), evmSpendableRefreshTimeout())
		defer cancel()
		if err := s.refreshEVMSpendable(ctx, crypto); err != nil && s.logger != nil {
			s.logger.Warn("evm spendable cache async refresh failed", "module", s.cfg.Module, "crypto", crypto, "error", err)
		}
	}()
}

func (s *Server) evmRefreshInProgress(crypto string) bool {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	s.evmRefreshMu.Lock()
	defer s.evmRefreshMu.Unlock()
	return s.evmRefreshing[crypto]
}

func (s *Server) evmSpendableRefreshCryptos() []string {
	source := firstNonEmpty(os.Getenv("EVM_SPENDABLE_REFRESH_CRYPTOS"), os.Getenv("SHKEEPER_CRYPTOS"))
	if strings.TrimSpace(source) == "" {
		return []string{s.nativeCrypto()}
	}
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, item := range splitCSV(source) {
		crypto := strings.ToUpper(strings.TrimSpace(item))
		if crypto == "" || !s.evmDepositScannerOwnsCrypto(crypto) {
			continue
		}
		if _, ok := seen[crypto]; ok {
			continue
		}
		seen[crypto] = struct{}{}
		out = append(out, crypto)
	}
	return out
}

func (s *Server) evmEstimateTxFee(ctx context.Context, crypto string, amount decimal.Decimal, destination string, forceRefresh bool) (map[string]any, error) {
	gasPrice, err := s.gasPrice(ctx)
	if err != nil {
		return nil, err
	}
	gasLimit := bnbNativeGasLimit
	estimated := s.isNative(crypto)
	source := ""
	estimateAmount := amount
	var candidates []evmSpendableAccount
	if !s.isNative(crypto) {
		gasLimit = evmTokenGasLimitFallback()
		if accounts, err := s.accountsForCrypto(ctx, crypto); err == nil {
			if scanned, err := s.evmSpendableAccountsForQuote(ctx, crypto, accounts, forceRefresh); err == nil {
				candidates = scanned
				if candidate, candidateAmount, ok := evmEstimateSource(candidates, amount); ok {
					source = candidate.Account.Address
					estimateAmount = candidateAmount
					if limit, err := s.estimateEVMTransferGasLimit(ctx, crypto, source, destination, candidateAmount); err == nil {
						gasLimit = limit
						estimated = true
					}
				}
			}
		}
	}
	fee := evmNativeFee(gasPrice, gasLimit)
	nativeTransferFee := evmNativeFee(gasPrice, bnbNativeGasLimit)
	payload := map[string]any{
		"status":                 "success",
		"crypto":                 crypto,
		"amount":                 amount.String(),
		"address":                destination,
		"fee":                    fee.String(),
		"fee_asset":              s.nativeCrypto(),
		"required_native_per_tx": fee.String(),
		"gas_price_wei":          gasPrice.String(),
		"gas_limit":              gasLimit,
		"estimated_gas":          estimated,
		"gas_topup_target":       evmGasTopupTarget(fee).String(),
	}
	if source != "" {
		payload["estimate_source"] = source
		payload["estimate_amount"] = estimateAmount.String()
	}
	if !s.isNative(crypto) {
		payload["gas_topup_transfer_fee_per_tx"] = nativeTransferFee.String()
		if amount.GreaterThan(decimal.Zero) {
			if topup := s.evmPayoutTopupEstimate(ctx, candidates, amount, fee, nativeTransferFee); topup != nil {
				for key, value := range topup {
					payload[key] = value
				}
			}
		}
	}
	return payload, nil
}

func (s *Server) evmPayoutTopupEstimate(ctx context.Context, candidates []evmSpendableAccount, amount decimal.Decimal, fee decimal.Decimal, nativeTransferFee decimal.Decimal) map[string]any {
	if len(candidates) == 0 {
		return nil
	}
	planned := make([]evmSpendableAccount, len(candidates))
	copy(planned, candidates)
	targetNative := evmGasTopupTarget(fee)
	for i := range planned {
		if planned[i].Balance.GreaterThan(decimal.Zero) && planned[i].NativeBalance.LessThan(fee) {
			planned[i].NativeBalance = targetNative
		}
	}
	parts := selectEVMPayoutParts(planned, amount, fee, false, true)
	if len(parts) == 0 {
		return nil
	}
	actualByAddress := make(map[string]evmSpendableAccount, len(candidates))
	for _, candidate := range candidates {
		actualByAddress[strings.ToLower(candidate.Account.Address)] = candidate
	}
	funders, err := s.evmGasFundingAccounts(ctx)
	if err != nil {
		return nil
	}
	topupAccountCount := 0
	topupAmount := decimal.Zero
	topupTransferFee := decimal.Zero
	for _, part := range parts {
		actual := actualByAddress[strings.ToLower(part.Account.Address)]
		if actual.NativeBalance.GreaterThanOrEqual(fee) {
			continue
		}
		amountNeeded := targetNative.Sub(actual.NativeBalance)
		if !amountNeeded.GreaterThan(decimal.Zero) {
			amountNeeded = fee.Sub(actual.NativeBalance)
		}
		requiredFromFunder := amountNeeded.Add(nativeTransferFee)
		funderIndex := selectEVMGasFundingAccount(funders, requiredFromFunder, part.Account.Address)
		if funderIndex < 0 {
			continue
		}
		funders[funderIndex].Available = funders[funderIndex].Available.Sub(requiredFromFunder)
		topupAccountCount++
		topupAmount = topupAmount.Add(amountNeeded)
		topupTransferFee = topupTransferFee.Add(nativeTransferFee)
	}
	return map[string]any{
		"gas_topup_account_count": topupAccountCount,
		"gas_topup_amount":        topupAmount.String(),
		"gas_topup_transfer_fee":  topupTransferFee.String(),
	}
}

func (s *Server) evmFeeDepositAccount(ctx context.Context) (Account, decimal.Decimal, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, s.nativeCrypto())
	if err != nil {
		return Account{}, decimal.Zero, err
	}
	if len(accounts) == 0 {
		address, privateKeyHex, err := newBNBAccount()
		if err != nil {
			return Account{}, decimal.Zero, err
		}
		encryptedPrivateKey, err := encryptSecret(s.cfg.AccountPassword, privateKeyHex)
		if err != nil {
			return Account{}, decimal.Zero, err
		}
		account := Account{Module: s.cfg.Module, Crypto: s.nativeCrypto(), Address: address, PrivateKeyHex: encryptedPrivateKey}
		if err := s.store.AddAccount(ctx, &account); err != nil && !isDuplicateStoreError(err) {
			return Account{}, decimal.Zero, err
		}
		return account, decimal.Zero, nil
	}
	best := accounts[0]
	bestBalance, _ := s.nativeBalance(ctx, best.Address)
	for _, account := range accounts[1:] {
		balance, err := s.nativeBalance(ctx, account.Address)
		if err == nil && balance.GreaterThan(bestBalance) {
			best = account
			bestBalance = balance
		}
	}
	return best, bestBalance, nil
}

func (s *Server) signAndBroadcastBNB(ctx context.Context, account Account, crypto string, destination string, amount decimal.Decimal, gasLimit uint64, gasPrice *big.Int) (string, error) {
	nonce, err := s.nonce(ctx, account.Address)
	if err != nil {
		return "", err
	}
	return s.signAndBroadcastBNBWithNonce(ctx, account, crypto, destination, amount, gasLimit, gasPrice, nonce)
}

func (s *Server) signAndBroadcastBNBWithNonce(ctx context.Context, account Account, crypto string, destination string, amount decimal.Decimal, gasLimit uint64, gasPrice *big.Int, nonce uint64) (string, error) {
	privateKeyHex, err := decryptSecret(s.cfg.AccountPassword, account.PrivateKeyHex)
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
