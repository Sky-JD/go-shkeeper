package chainworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

type evmDepositLog struct {
	Address          string   `json:"address"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	LogIndex         string   `json:"logIndex"`
	TransactionIndex string   `json:"transactionIndex"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
}

type evmActiveAddressIndex struct {
	ByTopic  map[string]DepositInvoiceAddress
	Earliest time.Time
}

func (s *Server) depositEvent(w http.ResponseWriter, r *http.Request) {
	if !s.isEVMModule() {
		errorJSON(w, http.StatusNotFound, fmt.Errorf("deposit event ingestion is only implemented for EVM workers"))
		return
	}
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	if !s.evmDepositScannerOwnsCrypto(crypto) || s.isNative(crypto) {
		errorJSON(w, http.StatusNotFound, fmt.Errorf("unsupported deposit crypto: %s", crypto))
		return
	}
	contract, _, err := tokenConfig(crypto)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		TxID          string `json:"txid"`
		TransactionID string `json:"transaction_hash"`
		Address       string `json:"address"`
		To            string `json:"to"`
		Contract      string `json:"contract"`
		LogIndex      int64  `json:"log_index"`
		BlockNumber   int64  `json:"block_number"`
		Confirmations int64  `json:"confirmations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	txid := strings.ToLower(firstNonEmpty(req.TxID, req.TransactionID))
	address := strings.ToLower(firstNonEmpty(req.Address, req.To))
	if txid == "" || address == "" || req.BlockNumber <= 0 || req.LogIndex < 0 {
		errorJSON(w, http.StatusBadRequest, fmt.Errorf("txid, address, non-negative log_index and positive block_number are required"))
		return
	}
	if req.Contract != "" && !strings.EqualFold(req.Contract, contract) {
		errorJSON(w, http.StatusBadRequest, fmt.Errorf("contract does not match %s", crypto))
		return
	}
	if evmTransferToTopic(address) == "" {
		errorJSON(w, http.StatusBadRequest, fmt.Errorf("invalid EVM address: %s", address))
		return
	}
	confirmationCount := req.Confirmations
	if confirmationCount <= 0 {
		if latest, err := s.latestBlockNumber(r.Context()); err == nil {
			confirmationCount = confirmations(latest, req.BlockNumber)
		}
	}
	inserted, err := s.store.InsertDepositEvent(r.Context(), DepositEvent{
		Module:        s.cfg.Module,
		Crypto:        crypto,
		Contract:      strings.ToLower(contract),
		Address:       address,
		TxID:          txid,
		LogIndex:      req.LogIndex,
		BlockNumber:   req.BlockNumber,
		Confirmations: confirmationCount,
	})
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "queued": inserted})
}

func (s *Server) evmDepositScanLoop(ctx context.Context) {
	interval := s.cfg.DepositScanInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	s.logger.Info("evm deposit indexer enabled",
		"module", s.cfg.Module,
		"interval", interval.String(),
		"max_invoice_age", s.cfg.DepositScanMaxInvoiceAge.String(),
		"active_address_limit", s.cfg.DepositScanBatchSize,
		"block_step", s.cfg.DepositScanBlockStep,
		"min_confirmations", s.depositScanMinConfirmations(),
	)
	s.scanEVMDeposits(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("evm deposit indexer stopped", "module", s.cfg.Module)
			return
		case <-ticker.C:
			s.scanEVMDeposits(ctx)
		}
	}
}

func (s *Server) evmDepositDispatchLoop(ctx context.Context) {
	interval := s.cfg.DepositDispatchInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	s.logger.Info("evm deposit dispatcher enabled",
		"module", s.cfg.Module,
		"interval", interval.String(),
		"batch_size", s.cfg.DepositDispatchBatchSize,
		"concurrency", s.depositDispatchConcurrency(),
	)
	s.dispatchEVMDepositEvents(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("evm deposit dispatcher stopped", "module", s.cfg.Module)
			return
		case <-ticker.C:
			s.dispatchEVMDepositEvents(ctx)
		}
	}
}

func (s *Server) scanEVMDeposits(ctx context.Context) {
	indexes, err := s.activeEVMDepositIndexes(ctx)
	if err != nil {
		s.logger.Warn("evm deposit indexer failed to load active invoice addresses", "module", s.cfg.Module, "error", err)
		return
	}
	if len(indexes) == 0 {
		return
	}
	latest, err := s.latestBlockNumber(ctx)
	if err != nil {
		s.logger.Warn("evm deposit indexer failed to load latest block", "module", s.cfg.Module, "error", err)
		return
	}
	toBlock := latest - s.depositScanMinConfirmations() + 1
	if toBlock < 1 {
		return
	}
	latestAt, err := s.latestBlockTimestamp(ctx)
	if err != nil {
		s.logger.Warn("evm deposit indexer failed to load latest block timestamp; using local time", "module", s.cfg.Module, "error", err)
		latestAt = time.Now()
	}
	for crypto, index := range indexes {
		if err := s.scanEVMDepositCrypto(ctx, crypto, index, latestAt, latest, toBlock); err != nil {
			s.logger.Warn("evm deposit indexer crypto pass failed", "module", s.cfg.Module, "crypto", crypto, "error", err)
		}
	}
}

func (s *Server) activeEVMDepositIndexes(ctx context.Context) (map[string]evmActiveAddressIndex, error) {
	candidates, err := s.store.ActiveDepositInvoiceAddresses(ctx, s.cfg.DepositScanMaxInvoiceAge, s.cfg.DepositScanBatchSize)
	if err != nil {
		return nil, err
	}
	indexes := make(map[string]evmActiveAddressIndex)
	for _, candidate := range candidates {
		crypto := strings.ToUpper(strings.TrimSpace(candidate.Crypto))
		address := strings.ToLower(strings.TrimSpace(candidate.Address))
		if crypto == "" || address == "" || !s.evmDepositScannerOwnsCrypto(crypto) || s.isNative(crypto) {
			continue
		}
		if _, _, err := tokenConfig(crypto); err != nil {
			s.logger.Debug("evm deposit indexer skipping token without contract config", "module", s.cfg.Module, "crypto", crypto, "error", err)
			continue
		}
		topic := evmTransferToTopic(address)
		if topic == "" {
			s.logger.Debug("evm deposit indexer skipping invalid address", "module", s.cfg.Module, "crypto", crypto, "address", candidate.Address)
			continue
		}
		candidate.Crypto = crypto
		candidate.Address = address
		index := indexes[crypto]
		if index.ByTopic == nil {
			index.ByTopic = make(map[string]DepositInvoiceAddress)
		}
		if _, exists := index.ByTopic[topic]; !exists {
			index.ByTopic[topic] = candidate
		}
		if index.Earliest.IsZero() || candidate.CreatedAt.Before(index.Earliest) {
			index.Earliest = candidate.CreatedAt
		}
		indexes[crypto] = index
	}
	return indexes, nil
}

func (s *Server) scanEVMDepositCrypto(ctx context.Context, crypto string, index evmActiveAddressIndex, latestAt time.Time, latest int64, toBlock int64) error {
	if len(index.ByTopic) == 0 {
		return nil
	}
	contract, _, err := tokenConfig(crypto)
	if err != nil {
		return err
	}
	contract = strings.ToLower(contract)
	cursorBlock, hasCursor, err := s.store.DepositScanCursor(ctx, s.cfg.Module, crypto, contract)
	if err != nil {
		return err
	}
	fromBlock := cursorBlock + 1
	if !hasCursor || fromBlock < 1 {
		fromBlock, _ = s.evmDepositScanBlockRange(latest, latestAt, index.Earliest)
	}
	if fromBlock > toBlock {
		return nil
	}
	step := s.cfg.DepositScanBlockStep
	if step <= 0 {
		step = 2000
	}
	for blockStart := fromBlock; blockStart <= toBlock; blockStart += step {
		blockEnd := blockStart + step - 1
		if blockEnd > toBlock {
			blockEnd = toBlock
		}
		logs, err := s.evmTransferLogs(ctx, contract, blockStart, blockEnd)
		if err != nil {
			return err
		}
		inserted := 0
		for _, log := range logs {
			ok, err := s.indexEVMDepositLog(ctx, crypto, contract, log, latest, index)
			if err != nil {
				return err
			}
			if ok {
				inserted++
			}
		}
		if err := s.store.UpsertDepositScanCursor(ctx, s.cfg.Module, crypto, contract, blockEnd); err != nil {
			return err
		}
		if inserted > 0 {
			s.logger.Info("evm deposit indexer queued events", "module", s.cfg.Module, "crypto", crypto, "from_block", blockStart, "to_block", blockEnd, "events", inserted)
		}
	}
	return nil
}

func (s *Server) indexEVMDepositLog(ctx context.Context, crypto string, contract string, log evmDepositLog, latest int64, index evmActiveAddressIndex) (bool, error) {
	if strings.ToLower(log.Address) != contract || len(log.Topics) < 3 || strings.ToLower(log.Topics[0]) != evmTransferTopic {
		return false, nil
	}
	topic := strings.ToLower(log.Topics[2])
	item, ok := index.ByTopic[topic]
	if !ok {
		return false, nil
	}
	blockNumber, err := hexToInt(log.BlockNumber)
	if err != nil {
		return false, fmt.Errorf("invalid block number for tx %s: %w", log.TransactionHash, err)
	}
	logIndex, err := hexToInt(log.LogIndex)
	if err != nil {
		return false, fmt.Errorf("invalid log index for tx %s: %w", log.TransactionHash, err)
	}
	conf := confirmations(latest, blockNumber)
	if conf < s.depositScanMinConfirmations() {
		return false, nil
	}
	txid := strings.ToLower(strings.TrimSpace(log.TransactionHash))
	if txid == "" {
		return false, nil
	}
	inserted, err := s.store.InsertDepositEvent(ctx, DepositEvent{
		Module:        s.cfg.Module,
		Crypto:        crypto,
		Contract:      contract,
		Address:       item.Address,
		TxID:          txid,
		LogIndex:      logIndex,
		BlockNumber:   blockNumber,
		Confirmations: conf,
	})
	if err != nil {
		return false, err
	}
	if inserted {
		s.logger.Info("evm deposit indexer matched active address", "module", s.cfg.Module, "crypto", crypto, "txid", txid, "log_index", logIndex, "address", item.Address, "invoice_id", item.InvoiceID, "block", blockNumber)
	}
	return inserted, nil
}

func (s *Server) dispatchEVMDepositEvents(ctx context.Context) {
	claimToken := fmt.Sprintf("%s-%d", s.cfg.Module, time.Now().UnixNano())
	events, err := s.store.ClaimPendingDepositEvents(ctx, s.cfg.Module, s.cfg.DepositDispatchBatchSize, s.depositEventMaxAttempts(), claimToken, time.Minute)
	if err != nil {
		s.logger.Warn("evm deposit dispatcher failed to load events", "module", s.cfg.Module, "error", err)
		return
	}
	if len(events) == 0 {
		return
	}
	concurrency := s.depositDispatchConcurrency()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, event := range events {
		event := event
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.dispatchEVMDepositEvent(ctx, event)
		}()
	}
	wg.Wait()
}

func (s *Server) dispatchEVMDepositEvent(ctx context.Context, event DepositEvent) {
	if err := s.notifyShkeeperWallet(ctx, event.Crypto, event.TxID); err != nil {
		retryAfter := depositEventRetryDelay(event.Attempts)
		if markErr := s.store.MarkDepositEventFailed(ctx, event.ID, err.Error(), retryAfter, s.depositEventMaxAttempts()); markErr != nil {
			s.logger.Warn("evm deposit dispatcher failed to mark failed event", "module", s.cfg.Module, "event", describeDepositEvent(event), "error", markErr)
		}
		s.logger.Warn("evm deposit dispatcher failed to notify shkeeper", "module", s.cfg.Module, "event", describeDepositEvent(event), "retry_after", retryAfter.String(), "error", err)
		return
	}
	if err := s.store.MarkDepositEventDelivered(ctx, event.ID); err != nil {
		s.logger.Warn("evm deposit dispatcher failed to mark delivered event", "module", s.cfg.Module, "event", describeDepositEvent(event), "error", err)
		return
	}
	s.logger.Info("evm deposit dispatcher delivered event", "module", s.cfg.Module, "event", describeDepositEvent(event))
}

func (s *Server) evmTransferLogs(ctx context.Context, contract string, fromBlock int64, toBlock int64) ([]evmDepositLog, error) {
	filter := map[string]any{
		"fromBlock": fmt.Sprintf("0x%x", fromBlock),
		"toBlock":   fmt.Sprintf("0x%x", toBlock),
		"address":   strings.ToLower(contract),
		"topics":    []any{evmTransferTopic},
	}
	var logs []evmDepositLog
	if err := s.rpc(ctx, "eth_getLogs", []any{filter}, &logs); err != nil {
		return nil, err
	}
	return logs, nil
}

func (s *Server) notifyShkeeperWallet(ctx context.Context, crypto string, txid string) error {
	endpoint := strings.TrimRight(s.cfg.ShkeeperAPIBaseURL, "/") + "/walletnotify/" + url.PathEscape(crypto) + "/" + url.PathEscape(txid)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Shkeeper-Backend-Key", s.cfg.BackendKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, string(data))
	}
	return nil
}

func (s *Server) evmDepositScanBlockRange(latest int64, latestAt time.Time, earliest time.Time) (int64, int64) {
	toBlock := latest - s.depositScanMinConfirmations() + 1
	if toBlock < 1 {
		return 1, 0
	}
	if earliest.IsZero() {
		earliest = latestAt
	}
	if margin := s.cfg.DepositScanStartMargin; margin > 0 {
		earliest = earliest.Add(-margin)
	}
	avg := s.cfg.EVMAverageBlockSeconds
	if avg <= 0 {
		avg = defaultEVMAverageBlockSeconds(s.cfg.Module)
	}
	span := latestAt.Sub(earliest)
	if span < 0 {
		span = 0
	}
	seconds := int64(span.Seconds())
	blocksBack := (seconds + avg - 1) / avg
	blocksBack += 64
	fromBlock := latest - blocksBack
	if fromBlock < 1 {
		fromBlock = 1
	}
	return fromBlock, toBlock
}

func (s *Server) depositScanMinConfirmations() int64 {
	if s.cfg.DepositScanMinConfirmations <= 0 {
		return 1
	}
	return s.cfg.DepositScanMinConfirmations
}

func (s *Server) depositDispatchConcurrency() int {
	if s.cfg.DepositDispatchConcurrency <= 0 {
		return 1
	}
	if s.cfg.DepositDispatchConcurrency > 64 {
		return 64
	}
	return s.cfg.DepositDispatchConcurrency
}

func (s *Server) depositEventMaxAttempts() int {
	if s.cfg.DepositEventMaxAttempts <= 0 {
		return 20
	}
	return s.cfg.DepositEventMaxAttempts
}

func (s *Server) evmDepositScannerOwnsCrypto(crypto string) bool {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	switch strings.ToUpper(s.cfg.Module) {
	case "BNB":
		return crypto == "BNB" || strings.HasPrefix(crypto, "BNB-")
	case "ETH":
		return crypto == "ETH" || strings.HasPrefix(crypto, "ETH-")
	case "MATIC":
		return crypto == "MATIC" || strings.HasPrefix(crypto, "MATIC-") || strings.HasPrefix(crypto, "POLYGON-")
	case "AVAX":
		return crypto == "AVAX" || strings.HasPrefix(crypto, "AVAX-") || strings.HasPrefix(crypto, "AVALANCHE-")
	case "ARBETH":
		return crypto == "ARBETH" || strings.HasPrefix(crypto, "ARBETH-") || strings.HasPrefix(crypto, "ARB-")
	case "OPETH":
		return crypto == "OPETH" || strings.HasPrefix(crypto, "OPETH-") || strings.HasPrefix(crypto, "OP-")
	default:
		return false
	}
}

func evmTransferToTopic(address string) string {
	value := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(address), "0x"), "0X")
	if len(value) != 40 {
		return ""
	}
	return "0x" + strings.Repeat("0", 24) + strings.ToLower(value)
}

func evmTopicChunks(values []string, size int) [][]string {
	if size <= 0 {
		size = 100
	}
	var chunks [][]string
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}
