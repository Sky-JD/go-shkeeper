package chainworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

type Server struct {
	cfg                 Config
	store               *Store
	logger              *slog.Logger
	client              *http.Client
	nodeMu              sync.RWMutex
	nodeURL             string
	latestBlockMu       sync.RWMutex
	latestBlockAt       time.Time
	latestBlockCachedAt time.Time
	tronSpendableMu     sync.RWMutex
	tronSpendableCache  map[string]tronSpendableCacheEntry
	tronRefreshMu       sync.Mutex
	tronRefreshing      map[string]bool
}

func NewServer(cfg Config, store *Store, logger *slog.Logger) *Server {
	return &Server{
		cfg:                cfg,
		store:              store,
		logger:             logger,
		client:             &http.Client{Timeout: cfg.RequestTimeout},
		nodeURL:            cfg.FullnodeURL,
		tronSpendableCache: map[string]tronSpendableCacheEntry{},
		tronRefreshing:     map[string]bool{},
	}
}

func (s *Server) StartBackground(ctx context.Context) {
	if s.cfg.Module == "TRON" && boolEnv("TRON_BALANCE_CACHE_ENABLED", true) {
		go s.tronSpendableRefreshLoop(ctx)
	}
	if s.isEVMModule() && s.cfg.DepositScanEnabled {
		go s.evmDepositScanLoop(ctx)
		go s.evmDepositDispatchLoop(ctx)
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "module": s.cfg.Module})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.store.db.PingContext(ctx); err != nil {
			errorJSON(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "module": s.cfg.Module})
	})
	r.Group(func(r chi.Router) {
		r.Use(s.requireBasicAuth)
		r.Get("/metrics", s.metrics)
		r.Get("/staking", s.tronStaking)
		r.Get("/staking/info", s.tronStakingInfo)
		r.Post("/staking/freeze/{amount}/{resource}", s.tronStakingFreeze)
		r.Post("/{crypto}/generate-address", s.generateAddress)
		r.Get("/{crypto}/addresses", s.addresses)
		r.Post("/{crypto}/get_all_addresses", s.addresses)
		r.Get("/{crypto}/get_all_addresses", s.addresses)
		r.Get("/{crypto}/spendable", s.spendable)
		r.Post("/{crypto}/spendable", s.spendable)
		r.Get("/{crypto}/dump", s.dumpAccounts)
		r.Post("/{crypto}/dump", s.dumpAccounts)
		r.Get("/{crypto}/activation-status", s.activationStatus)
		r.Post("/{crypto}/activation-status", s.activationStatus)
		r.Post("/{crypto}/status", s.status)
		r.Post("/{crypto}/balance", s.balance)
		r.Post("/{crypto}/calc-tx-fee/{amount}", s.calcTxFee)
		r.Get("/{crypto}/calc-tx-fee/{amount}", s.calcTxFee)
		r.Post("/{crypto}/fee-deposit-account", s.feeDepositAccount)
		r.Get("/{crypto}/fee-deposit-account", s.feeDepositAccount)
		r.Post("/{crypto}/payout/{destination}/{amount}", s.payout)
		r.Post("/{crypto}/payout/{destination}/{amount}/{fee}", s.payout)
		r.Post("/{crypto}/multipayout", s.multipayout)
		r.Post("/{crypto}/transaction/{txid}", s.transaction)
		r.Get("/{crypto}/transaction/{txid}", s.transaction)
		r.Post("/{crypto}/deposit-event", s.depositEvent)
		r.Post("/{crypto}/task/{id}", s.task)
		r.Get("/{crypto}/multiserver/status", s.tronMultiserverStatus)
		r.Post("/{crypto}/multiserver/change/{server_id}", s.tronMultiserverChange)
	})
	return r
}

func (s *Server) generateAddress(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	if s.isBitcoinLikeModule() {
		var address string
		var err error
		if crypto == "FIRO-SPARK" {
			address, err = s.newFiroSparkAddress(r.Context())
		} else {
			address, err = s.newBitcoinAddress(r.Context())
		}
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		account := Account{Module: s.cfg.Module, Crypto: crypto, Address: address, PrivateKeyHex: ""}
		if err := s.store.AddAccount(r.Context(), &account); err != nil && !isDuplicateStoreError(err) {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"address": address})
		return
	}
	if s.cfg.Module == "BTC-LIGHTNING" {
		paymentRequest, err := s.newLightningInvoice(r.Context(), lightningAmountFromRequest(r))
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"payment_request": paymentRequest, "address": paymentRequest})
		return
	}
	if s.cfg.Module == "XMR" {
		address, err := s.newMoneroAddress(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		account := Account{Module: s.cfg.Module, Crypto: crypto, Address: address, PrivateKeyHex: ""}
		if err := s.store.AddAccount(r.Context(), &account); err != nil && !isDuplicateStoreError(err) {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"address": address})
		return
	}
	if s.cfg.Module == "XRP" {
		address, privateKeyHex, err := s.newXRPAddress(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		encryptedPrivateKey := ""
		if privateKeyHex != "" {
			encryptedPrivateKey, err = encryptSecret(s.cfg.AccountPassword, privateKeyHex)
			if err != nil {
				errorJSON(w, http.StatusInternalServerError, err)
				return
			}
		}
		account := Account{Module: s.cfg.Module, Crypto: crypto, Address: address, PrivateKeyHex: encryptedPrivateKey}
		if err := s.store.AddAccount(r.Context(), &account); err != nil && !isDuplicateStoreError(err) {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"address": address})
		return
	}
	address, privateKeyHex, err := s.newAccount()
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	encryptedPrivateKey, err := encryptSecret(s.cfg.AccountPassword, privateKeyHex)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	account := Account{
		Module:        s.cfg.Module,
		Crypto:        crypto,
		Address:       address,
		PrivateKeyHex: encryptedPrivateKey,
	}
	if err := s.store.AddAccount(r.Context(), &account); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{"address": address}
	if s.cfg.Module == "TRON" {
		resp["base58check_address"] = address
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) addresses(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	if s.isBitcoinLikeModule() {
		addresses, err := s.bitcoinAddresses(r.Context(), crypto)
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, addresses)
		return
	}
	if s.cfg.Module == "XMR" {
		addresses, err := s.moneroAddresses(r.Context(), crypto)
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, addresses)
		return
	}
	if s.cfg.Module == "BTC-LIGHTNING" {
		addresses, err := s.lightningAddresses(r.Context())
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, addresses)
		return
	}
	if s.cfg.Module == "XRP" {
		addresses, err := s.xrpAddresses(r.Context(), crypto)
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, addresses)
		return
	}
	accounts, err := s.store.ListAccounts(r.Context(), s.cfg.Module, crypto)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	addresses := make([]string, 0, len(accounts))
	for _, account := range accounts {
		addresses = append(addresses, account.Address)
	}
	if s.cfg.Module == "TRON" {
		writeJSON(w, http.StatusOK, map[string]any{"accounts": addresses})
		return
	}
	writeJSON(w, http.StatusOK, addresses)
}

func (s *Server) dumpAccounts(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	accounts, err := s.store.ListAccounts(r.Context(), s.cfg.Module, crypto)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, map[string]any{
			"id":                    account.ID,
			"module":                account.Module,
			"crypto":                account.Crypto,
			"address":               account.Address,
			"private_key_encrypted": account.PrivateKeyHex,
			"created_at":            account.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "module": s.cfg.Module, "crypto": crypto, "accounts": out})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if s.isBitcoinLikeModule() {
		status, err := s.bitcoinStatus(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if s.cfg.Module == "XMR" {
		status, err := s.moneroStatus(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if s.cfg.Module == "BTC-LIGHTNING" {
		status, err := s.lightningStatus(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if s.cfg.Module == "XRP" {
		status, err := s.xrpStatus(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if s.cfg.Module == "SOL" {
		status, err := s.solanaStatus(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	ts, err := s.latestBlockTimestamp(r.Context())
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"last_block_timestamp": ts.Unix()})
}

func (s *Server) balance(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	if s.isBitcoinLikeModule() {
		var total decimal.Decimal
		var err error
		if crypto == "FIRO-SPARK" {
			total, err = s.firoSparkBalance(r.Context())
		} else {
			total, err = s.bitcoinBalance(r.Context())
		}
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": total.String()})
		return
	}
	if s.cfg.Module == "XMR" {
		total, err := s.moneroBalance(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": total.String()})
		return
	}
	if s.cfg.Module == "BTC-LIGHTNING" {
		total, err := s.lightningBalance(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": total.String()})
		return
	}
	if s.cfg.Module == "XRP" {
		total, err := s.xrpBalance(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": total.String()})
		return
	}
	if s.cfg.Module == "SOL" {
		accounts, err := s.accountsForCrypto(r.Context(), crypto)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		total := decimal.Zero
		for _, account := range accounts {
			value, err := s.solanaBalance(r.Context(), crypto, account.Address)
			if err == nil {
				total = total.Add(value)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": total.String()})
		return
	}
	total := decimal.Zero
	if s.cfg.Module == "TRON" {
		entry, ready, err := s.cachedTRONSpendable(r.Context(), crypto, queryBool(r, "refresh") || queryBool(r, "live"))
		writeJSON(w, http.StatusOK, s.tronSpendablePayload(crypto, entry, ready, err))
		return
	}
	accounts, err := s.accountsForCrypto(r.Context(), crypto)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	if s.isEVMModule() {
		for _, account := range accounts {
			value, err := s.bnbBalance(r.Context(), crypto, account.Address)
			if err == nil {
				total = total.Add(value)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance": total.String()})
}

func (s *Server) spendable(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	if s.cfg.Module != "TRON" {
		errorJSON(w, http.StatusNotFound, errors.New("spendable report is only implemented for TRON workers"))
		return
	}
	entry, ready, err := s.cachedTRONSpendable(r.Context(), crypto, queryBool(r, "refresh") || queryBool(r, "live"))
	writeJSON(w, http.StatusOK, s.tronSpendablePayload(crypto, entry, ready, err))
}

func (s *Server) tronSpendablePayload(crypto string, entry tronSpendableCacheEntry, ready bool, err error) map[string]any {
	report := entry.Report
	payload := map[string]any{
		"status":             "success",
		"crypto":             crypto,
		"balance":            report.Total.String(),
		"max_single_account": report.Max.String(),
		"account":            report.MaxAddress,
		"account_count":      report.AccountCount,
		"checked":            report.Checked,
		"failed":             report.Failed,
		"cache_ready":        ready,
		"refreshing":         s.tronRefreshInProgress(crypto),
		"balance_source":     "wallet_cache",
	}
	if !entry.RefreshedAt.IsZero() {
		age := int64(time.Since(entry.RefreshedAt).Seconds())
		if age < 0 {
			age = 0
		}
		payload["refreshed_at"] = entry.RefreshedAt.Format(time.RFC3339)
		payload["cache_age_seconds"] = age
		payload["cache_stale"] = tronSpendableCacheEntryStale(entry)
	} else {
		payload["balance_source"] = "warming"
		payload["cache_stale"] = true
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
		payload["balance_error"] = firstNonEmpty(existing, "TRON balance cache is warming up")
	}
	return payload
}

func queryBool(r *http.Request, key string) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (s *Server) accountsForCrypto(ctx context.Context, crypto string) ([]Account, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 && !s.isNative(crypto) {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
	}
	return accounts, err
}

func (s *Server) calcTxFee(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Module == "TRON" {
		fee := decimal.NewFromInt(int64Env("TRON_TOKEN_FEE_LIMIT_SUN", 100_000_000)).Div(decimal.New(1, tronNativeDecimals))
		writeJSON(w, http.StatusOK, map[string]any{"fee": fee.String(), "fee_sun": int64Env("TRON_TOKEN_FEE_LIMIT_SUN", 100_000_000)})
		return
	}
	if !s.isBitcoinLikeModule() {
		errorJSON(w, http.StatusNotFound, errors.New("fee estimation is only implemented for Bitcoin-like/TRON workers"))
		return
	}
	fee, err := s.bitcoinEstimateTxFee(r.Context())
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, fee)
}

func (s *Server) feeDepositAccount(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Module == "TRON" {
		account, balance, err := s.tronFeeDepositAccount(r.Context())
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"account": account.Address, "balance": balance.String()})
		return
	}
	if s.cfg.Module == "BTC-LIGHTNING" {
		address, balance, err := s.lightningFeeDepositAccount(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"account": address, "balance": balance.String()})
		return
	}
	if s.cfg.Module == "XMR" {
		address, balance, err := s.moneroFeeDepositAccount(r.Context())
		if err != nil {
			errorJSON(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"account": address, "balance": balance.String()})
		return
	}
	if !s.isBitcoinLikeModule() {
		errorJSON(w, http.StatusNotFound, errors.New("fee deposit account is only implemented for Bitcoin-like/BTC-LIGHTNING/XMR/TRON workers"))
		return
	}
	address, balance, err := s.bitcoinFeeDepositAccount(r.Context())
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": address, "balance": balance.String()})
}

func (s *Server) payout(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	destination := strings.TrimSpace(chi.URLParam(r, "destination"))
	amount, err := decimal.NewFromString(strings.TrimSpace(chi.URLParam(r, "amount")))
	if destination == "" || err != nil || !amount.GreaterThan(decimal.Zero) {
		errorJSON(w, http.StatusBadRequest, errors.New("destination and positive amount are required"))
		return
	}
	taskID, err := randomID()
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	req := map[string]any{
		"destination": destination,
		"amount":      amount.String(),
		"fee":         chi.URLParam(r, "fee"),
	}
	requestJSON, _ := json.Marshal(req)
	task := Task{ID: taskID, Module: s.cfg.Module, Crypto: crypto, Kind: "payout", Status: "IN_PROGRESS", Request: requestJSON, Result: json.RawMessage(`{}`)}
	if err := s.store.AddTask(r.Context(), task); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	result, err := s.broadcastPayout(r.Context(), crypto, destination, amount, chi.URLParam(r, "fee"))
	if err != nil {
		resultJSON, _ := json.Marshal(map[string]any{"error": err.Error()})
		_ = s.store.UpdateTask(r.Context(), taskID, "FAIL", resultJSON)
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	resultJSON, _ := json.Marshal(result)
	if err := s.store.UpdateTask(r.Context(), taskID, "SUCCESS", resultJSON); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "status": "SUCCESS", "result": result})
}

func (s *Server) multipayout(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	taskID, err := randomID()
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	requests, err := parsePayoutRequests(body)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	task := Task{ID: taskID, Module: s.cfg.Module, Crypto: crypto, Kind: "multipayout", Status: "IN_PROGRESS", Request: body, Result: json.RawMessage(`{}`)}
	if err := s.store.AddTask(r.Context(), task); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	results := make([]broadcastResult, 0, len(requests))
	failures := make([]map[string]string, 0)
	for _, req := range requests {
		destination := req.Destination
		if s.cfg.Module == "XRP" && req.DestinationTag != "" {
			destination = destination + ":" + req.DestinationTag
		}
		result, err := s.broadcastPayout(r.Context(), crypto, destination, req.Amount, req.Fee)
		if err != nil {
			failures = append(failures, map[string]string{
				"dest":   destination,
				"amount": req.Amount.String(),
				"error":  err.Error(),
			})
			continue
		}
		results = append(results, result)
	}
	status := "SUCCESS"
	if len(failures) == len(requests) {
		status = "FAIL"
	} else if len(failures) > 0 {
		status = "PARTIAL"
	}
	resultPayload := map[string]any{"results": results}
	if len(failures) > 0 {
		resultPayload["errors"] = failures
	}
	resultJSON, _ := json.Marshal(resultPayload)
	if err := s.store.UpdateTask(r.Context(), taskID, status, resultJSON); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "status": status, "result": results, "errors": failures})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.Task(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var result any
	_ = json.Unmarshal(task.Result, &result)
	writeJSON(w, http.StatusOK, map[string]any{"status": task.Status, "result": result})
}

func (s *Server) transaction(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	txid := strings.TrimSpace(chi.URLParam(r, "txid"))
	transfers, err := s.transfersByTx(r.Context(), crypto, txid)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	if s.isBitcoinLikeModule() {
		writeJSON(w, http.StatusOK, transfers)
		return
	}
	if s.cfg.Module == "TRON" {
		writeJSON(w, http.StatusOK, transfers)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": transfers})
}

func (s *Server) newAccount() (string, string, error) {
	switch s.cfg.Module {
	case "TRON":
		return newTRONAccount()
	case "BNB":
		return newBNBAccount()
	case "BTC-LIGHTNING":
		return "", "", errors.New("BTC-LIGHTNING invoices are generated through LND REST")
	case "XMR":
		return "", "", errors.New("XMR addresses are generated through monero-wallet-rpc")
	case "XRP":
		return "", "", errors.New("XRP addresses are generated through the XRP worker")
	case "SOL":
		return s.newSolanaAddress()
	default:
		if s.isBitcoinLikeModule() {
			return "", "", errors.New("Bitcoin-like addresses are generated through wallet JSON-RPC")
		}
		if s.isEVMModule() {
			return newBNBAccount()
		}
		return "", "", fmt.Errorf("unsupported chain module: %s", s.cfg.Module)
	}
}

func (s *Server) requireBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || !s.acceptsAuth(r, username, password) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "authorization required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) acceptsAuth(r *http.Request, username string, password string) bool {
	if username == s.cfg.Username && password == s.cfg.Password {
		return true
	}
	crypto := strings.ToUpper(strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0])
	if crypto == "" {
		return false
	}
	envUser := strings.TrimSpace(getenv(crypto+"_USERNAME", ""))
	envPass := strings.TrimSpace(getenv(crypto+"_PASSWORD", ""))
	if envUser != "" && envPass != "" && username == envUser && password == envPass {
		return true
	}
	return false
}

func (s *Server) isNative(crypto string) bool {
	return strings.EqualFold(crypto, s.nativeCrypto())
}

func (s *Server) broadcastPayout(ctx context.Context, crypto string, destination string, amount decimal.Decimal, fee string) (broadcastResult, error) {
	if s.isEVMModule() {
		return s.broadcastBNBPayout(ctx, crypto, destination, amount)
	}
	switch s.cfg.Module {
	case "BTC", "LTC", "DOGE":
		return s.broadcastBitcoinPayout(ctx, destination, amount, fee)
	case "FIRO":
		if crypto == "FIRO-SPARK" {
			return s.broadcastFiroSparkPayout(ctx, destination, amount, fee)
		}
		return s.broadcastBitcoinPayout(ctx, destination, amount, fee)
	case "BTC-LIGHTNING":
		return s.broadcastLightningPayout(ctx, destination)
	case "XMR":
		return s.broadcastMoneroPayout(ctx, destination, amount, fee)
	case "XRP":
		return s.broadcastXRPPayout(ctx, destination, amount, fee)
	case "SOL":
		return s.broadcastSolanaPayout(ctx, crypto, destination, amount)
	case "TRON":
		return s.broadcastTRONPayout(ctx, crypto, destination, amount)
	default:
		return broadcastResult{}, fmt.Errorf("unsupported chain module: %s", s.cfg.Module)
	}
}

func (s *Server) transfersByTx(ctx context.Context, crypto string, txid string) ([]transferResult, error) {
	if txid == "" {
		return nil, errors.New("txid is required")
	}
	if s.isEVMModule() {
		return s.bnbTransfersByTx(ctx, crypto, txid)
	}
	switch s.cfg.Module {
	case "BTC", "LTC", "DOGE":
		return s.bitcoinTransfersByTx(ctx, txid)
	case "FIRO":
		if crypto == "FIRO-SPARK" {
			return s.firoSparkTransfersByTx(ctx, txid)
		}
		return s.firoTransfersByTx(ctx, txid)
	case "BTC-LIGHTNING":
		return s.lightningTransfersByTx(ctx, txid)
	case "XMR":
		return s.moneroTransfersByTx(ctx, txid)
	case "XRP":
		return s.xrpTransfersByTx(ctx, txid)
	case "SOL":
		return s.solanaTransfersByTx(ctx, crypto, txid)
	case "TRON":
		return s.tronTransfersByTx(ctx, crypto, txid)
	default:
		return nil, fmt.Errorf("unsupported chain module: %s", s.cfg.Module)
	}
}

func (s *Server) latestBlockTimestamp(ctx context.Context) (time.Time, error) {
	if s.isEVMModule() {
		var block struct {
			Timestamp string `json:"timestamp"`
		}
		if err := s.rpc(ctx, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
			return time.Time{}, err
		}
		seconds, err := hexToInt(block.Timestamp)
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(seconds, 0), nil
	}
	switch s.cfg.Module {
	case "BTC", "LTC", "DOGE", "FIRO":
		return s.bitcoinLatestBlockTimestamp(ctx)
	case "BTC-LIGHTNING":
		status, err := s.lightningStatus(ctx)
		if err != nil {
			return time.Time{}, err
		}
		seconds, ok := decimalFromAny(status["last_block_timestamp"])
		if !ok {
			return time.Time{}, errors.New("lightning status returned no timestamp")
		}
		return time.Unix(seconds.IntPart(), 0), nil
	case "XRP":
		status, err := s.xrpStatus(ctx)
		if err != nil {
			return time.Time{}, err
		}
		seconds, ok := decimalFromAny(status["last_block_timestamp"])
		if !ok {
			return time.Time{}, errors.New("xrp status returned no close time")
		}
		return time.Unix(seconds.IntPart()+946684800, 0), nil
	case "SOL":
		status, err := s.solanaStatus(ctx)
		if err != nil {
			return time.Time{}, err
		}
		seconds, ok := decimalFromAny(status["last_block_timestamp"])
		if !ok {
			return time.Time{}, errors.New("solana status returned no timestamp")
		}
		return time.Unix(seconds.IntPart(), 0), nil
	case "TRON":
		var block struct {
			BlockHeader struct {
				RawData struct {
					Timestamp int64 `json:"timestamp"`
				} `json:"raw_data"`
			} `json:"block_header"`
		}
		if err := s.httpJSON(ctx, http.MethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getnowblock", nil, &block); err != nil {
			if cached, ok := s.cachedLatestBlockTimestamp(30 * time.Second); ok {
				return cached, nil
			}
			return time.Time{}, err
		}
		if block.BlockHeader.RawData.Timestamp <= 0 {
			return time.Time{}, errors.New("tron fullnode returned no block timestamp")
		}
		ts := time.UnixMilli(block.BlockHeader.RawData.Timestamp)
		s.rememberLatestBlockTimestamp(ts)
		return ts, nil
	default:
		return time.Time{}, fmt.Errorf("unsupported chain module: %s", s.cfg.Module)
	}
}

func (s *Server) rememberLatestBlockTimestamp(ts time.Time) {
	if ts.IsZero() {
		return
	}
	s.latestBlockMu.Lock()
	s.latestBlockAt = ts
	s.latestBlockCachedAt = time.Now()
	s.latestBlockMu.Unlock()
}

func (s *Server) cachedLatestBlockTimestamp(maxAge time.Duration) (time.Time, bool) {
	s.latestBlockMu.RLock()
	ts := s.latestBlockAt
	cachedAt := s.latestBlockCachedAt
	s.latestBlockMu.RUnlock()
	if ts.IsZero() || cachedAt.IsZero() || time.Since(cachedAt) > maxAge {
		return time.Time{}, false
	}
	return ts, true
}

func (s *Server) nativeBalance(ctx context.Context, address string) (decimal.Decimal, error) {
	if !s.isEVMModule() {
		return decimal.Zero, nil
	}
	var hexBalance string
	if err := s.rpc(ctx, "eth_getBalance", []any{address, "latest"}, &hexBalance); err != nil {
		return decimal.Zero, err
	}
	wei, err := hexToDecimal(hexBalance)
	if err != nil {
		return decimal.Zero, err
	}
	return wei.Div(decimal.NewFromInt(1_000_000_000_000_000_000)), nil
}

func (s *Server) rpc(ctx context.Context, method string, params []any, result any) error {
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := s.httpJSON(ctx, http.MethodPost, s.fullnodeURL(), map[string]any{
		"jsonrpc": "2.0",
		"id":      "go-chain-worker",
		"method":  method,
		"params":  params,
	}, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc %s error: %v", method, resp.Error)
	}
	return json.Unmarshal(resp.Result, result)
}

func (s *Server) fullnodeURL() string {
	s.nodeMu.RLock()
	value := s.nodeURL
	s.nodeMu.RUnlock()
	if strings.TrimSpace(value) != "" {
		return value
	}
	return s.cfg.FullnodeURL
}

func (s *Server) setFullnodeURL(value string) {
	s.nodeMu.Lock()
	s.nodeURL = strings.TrimSpace(value)
	s.nodeMu.Unlock()
}

func (s *Server) httpJSON(ctx context.Context, method string, url string, body any, out any) error {
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
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, string(data))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func errorJSON(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"status": "error", "message": err.Error()})
}
