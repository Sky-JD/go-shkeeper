package chainworker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP go_chain_worker_build_info Go chain worker build info.\n# TYPE go_chain_worker_build_info gauge\ngo_chain_worker_build_info{module=\"%s\"} 1\n", metricLabel(s.cfg.Module))
	_, _ = fmt.Fprintf(w, "# HELP go_chain_worker_unix_time Current unix time.\n# TYPE go_chain_worker_unix_time gauge\ngo_chain_worker_unix_time %d\n", time.Now().Unix())
}

func (s *Server) tronMultiserverStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireTRON(w) {
		return
	}
	active := s.fullnodeURL()
	urls := s.fullnodeURLs()
	statuses := make([]map[string]any, 0, len(urls))
	for i, endpoint := range urls {
		statuses = append(statuses, s.tronServerStatus(r.Context(), strconv.Itoa(i), endpoint, endpoint == active))
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "active": active, "statuses": statuses})
}

func (s *Server) tronMultiserverChange(w http.ResponseWriter, r *http.Request) {
	if !s.requireTRON(w) {
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "server_id"))
	urls := s.fullnodeURLs()
	for i, endpoint := range urls {
		if id == strconv.Itoa(i) || id == endpoint || id == serverName(endpoint) {
			s.setFullnodeURL(endpoint)
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "active": endpoint})
			return
		}
	}
	errorJSON(w, http.StatusNotFound, fmt.Errorf("unknown TRON server id: %s", id))
}

func (s *Server) tronStakingInfo(w http.ResponseWriter, r *http.Request) {
	if !s.requireTRON(w) {
		return
	}
	feeAccount, feeBalance, err := s.tronFeeDepositAccount(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	stakingAccount, err := s.tronStakingAccount(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	stakingBalance, _ := s.tronBalance(r.Context(), "TRX", stakingAccount.Address)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"config": map[string]any{
			"energy_delegation_mode":                                      boolEnv("ENERGY_DELEGATION_MODE", true),
			"energy_delegation_mode_allow_burn_trx_for_bandwith":          boolEnv("ENERGY_DELEGATION_MODE_ALLOW_BURN_TRX_FOR_BANDWITH", false),
			"energy_delegation_mode_allow_burn_trx_on_payout":             boolEnv("ENERGY_DELEGATION_MODE_ALLOW_BURN_TRX_ON_PAYOUT", false),
			"energy_delegation_mode_allow_additional_energy_delegation":   boolEnv("ENERGY_DELEGATION_MODE_ALLOW_ADDITIONAL_ENERGY_DELEGATION", false),
			"energy_delegation_mode_energy_delegation_factor":             env("ENERGY_DELEGATION_MODE_ENERGY_DELEGATION_FACTOR", "1.0"),
			"energy_delegation_mode_separate_balance_and_energy_accounts": boolEnv("ENERGY_DELEGATION_MODE_SEPARATE_BALANCE_AND_ENERGY_ACCOUNTS", false),
		},
		"fee_deposit_account": map[string]any{
			"address":   feeAccount.Address,
			"balance":   feeBalance.String(),
			"is_active": s.tronAccountIsActive(r.Context(), feeAccount.Address),
		},
		"energy_delegator_account": map[string]any{
			"address":   stakingAccount.Address,
			"balance":   stakingBalance.String(),
			"is_active": s.tronAccountIsActive(r.Context(), stakingAccount.Address),
		},
	})
}

func (s *Server) tronStaking(w http.ResponseWriter, r *http.Request) {
	if !s.requireTRON(w) {
		return
	}
	account, err := s.tronStakingAccount(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	accountHex, err := tronAddressHex(account.Address)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	accountInfo := map[string]any{}
	if err := s.httpJSON(r.Context(), httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getaccount", map[string]any{
		"address": accountHex,
		"visible": false,
	}, &accountInfo); err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	if len(accountInfo) == 0 {
		accountInfo["address"] = account.Address
		accountInfo["balance"] = 0
	}
	accountInfo["address"] = account.Address

	accountResource := map[string]any{}
	_ = s.httpJSON(r.Context(), httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getaccountresource", map[string]any{
		"address": accountHex,
		"visible": false,
	}, &accountResource)

	delegated := map[string]any{}
	_ = s.httpJSON(r.Context(), httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getdelegatedresourcev2", map[string]any{
		"fromAddress": accountHex,
		"toAddress":   accountHex,
		"visible":     false,
	}, &delegated)
	delegatedResources, _ := delegated["delegatedResource"].([]any)
	if delegatedResources == nil {
		delegatedResources = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_info":        accountInfo,
		"delegated_resources": delegatedResources,
		"account_resource":    accountResource,
	})
}

func (s *Server) tronStakingFreeze(w http.ResponseWriter, r *http.Request) {
	if !s.requireTRON(w) {
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(chi.URLParam(r, "amount")))
	if err != nil || !amount.GreaterThan(decimal.Zero) {
		errorJSON(w, http.StatusBadRequest, errors.New("positive amount is required"))
		return
	}
	resource := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "resource")))
	if resource != "ENERGY" && resource != "BANDWIDTH" {
		errorJSON(w, http.StatusBadRequest, errors.New("resource must be ENERGY or BANDWIDTH"))
		return
	}
	account, err := s.tronStakingAccount(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	privateKey, err := s.tronAccountPrivateKey(account)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	tx, err := s.createTRONFreezeV2(r.Context(), account.Address, amount, resource)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	signed, err := signTRONTransaction(tx, privateKey)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	txid, err := s.broadcastTRON(r.Context(), signed)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "txid": txid, "resource": resource, "amount": amount.String()})
}

func (s *Server) createTRONFreezeV2(ctx context.Context, owner string, amount decimal.Decimal, resource string) (map[string]any, error) {
	ownerHex, err := tronAddressHex(owner)
	if err != nil {
		return nil, err
	}
	amountSun, err := bigIntToInt64(amountToBaseUnits(amount, tronNativeDecimals))
	if err != nil {
		return nil, err
	}
	var tx map[string]any
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/freezebalancev2", map[string]any{
		"owner_address":  ownerHex,
		"frozen_balance": amountSun,
		"resource":       resource,
		"visible":        false,
	}, &tx); err != nil {
		return nil, err
	}
	if err := ensureTRONTransaction(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

func (s *Server) tronFeeDepositAccount(ctx context.Context) (Account, decimal.Decimal, error) {
	account, err := s.tronFeeAccount(ctx)
	if err != nil {
		return Account{}, decimal.Zero, err
	}
	balance, err := s.tronBalance(ctx, "TRX", account.Address)
	if err != nil {
		return account, decimal.Zero, err
	}
	return account, balance, nil
}

func (s *Server) tronFeeAccount(ctx context.Context) (Account, error) {
	if address := strings.TrimSpace(os.Getenv("TRON_FEE_DEPOSIT_ADDRESS")); address != "" {
		return Account{Module: "TRON", Crypto: "TRX", Address: address, PrivateKeyHex: strings.TrimSpace(os.Getenv("TRON_FEE_DEPOSIT_PRIVATE_KEY_HEX"))}, nil
	}
	accounts, err := s.store.ListAccounts(ctx, "TRON", "TRX")
	if err != nil {
		return Account{}, err
	}
	if len(accounts) > 0 {
		return accounts[0], nil
	}
	address, privateKeyHex, err := newTRONAccount()
	if err != nil {
		return Account{}, err
	}
	encryptedPrivateKey, err := encryptSecret(s.cfg.AccountPassword, privateKeyHex)
	if err != nil {
		return Account{}, err
	}
	account := Account{Module: "TRON", Crypto: "TRX", Address: address, PrivateKeyHex: encryptedPrivateKey}
	if err := s.store.AddAccount(ctx, &account); err != nil {
		if !isDuplicateStoreError(err) {
			return Account{}, err
		}
		accounts, err = s.store.ListAccounts(ctx, "TRON", "TRX")
		if err != nil {
			return Account{}, err
		}
		if len(accounts) > 0 {
			return accounts[0], nil
		}
	}
	return account, nil
}

func (s *Server) tronStakingAccount(ctx context.Context) (Account, error) {
	if address := firstNonEmpty(os.Getenv("TRON_STAKING_ADDRESS"), os.Getenv("TRON_ENERGY_DELEGATOR_ADDRESS")); address != "" {
		privateKey := firstNonEmpty(os.Getenv("TRON_STAKING_PRIVATE_KEY_HEX"), os.Getenv("TRON_ENERGY_DELEGATOR_PRIVATE_KEY_HEX"))
		return Account{Module: "TRON", Crypto: "TRX", Address: address, PrivateKeyHex: privateKey}, nil
	}
	return s.tronFeeAccount(ctx)
}

func (s *Server) tronAccountPrivateKey(account Account) (string, error) {
	if privateKey := firstNonEmpty(os.Getenv("TRON_STAKING_PRIVATE_KEY_HEX"), os.Getenv("TRON_ENERGY_DELEGATOR_PRIVATE_KEY_HEX"), os.Getenv("TRON_FEE_DEPOSIT_PRIVATE_KEY_HEX")); privateKey != "" {
		return privateKey, nil
	}
	if strings.TrimSpace(account.PrivateKeyHex) == "" {
		return "", errors.New("TRON private key is not configured for staking account")
	}
	if !strings.HasPrefix(account.PrivateKeyHex, "v1:") {
		return strings.TrimSpace(account.PrivateKeyHex), nil
	}
	return decryptSecret(s.cfg.AccountPassword, account.PrivateKeyHex)
}

func (s *Server) tronAccountIsActive(ctx context.Context, address string) bool {
	addressHex, err := tronAddressHex(address)
	if err != nil {
		return false
	}
	accountInfo := map[string]any{}
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getaccount", map[string]any{
		"address": addressHex,
		"visible": false,
	}, &accountInfo); err != nil {
		return false
	}
	return len(accountInfo) > 0
}

func (s *Server) fullnodeURLs() []string {
	active := s.fullnodeURL()
	urls := make([]string, 0, len(s.cfg.FullnodeURLs)+1)
	seen := map[string]struct{}{}
	for _, endpoint := range append([]string{active}, s.cfg.FullnodeURLs...) {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		urls = append(urls, endpoint)
	}
	return urls
}

func (s *Server) tronServerStatus(ctx context.Context, id string, endpoint string, active bool) map[string]any {
	row := map[string]any{
		"id":        id,
		"name":      serverName(endpoint),
		"url":       endpoint,
		"is_active": active,
	}
	var block struct {
		BlockHeader struct {
			RawData struct {
				Number    int64 `json:"number"`
				Timestamp int64 `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(endpoint, "/")+"/wallet/getnowblock", nil, &block); err != nil {
		row["status"] = "error"
		row["error"] = err.Error()
		return row
	}
	nodeInfo := map[string]any{}
	_ = s.httpJSON(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/wallet/getnodeinfo", nil, &nodeInfo)
	lag := int64(0)
	if block.BlockHeader.RawData.Timestamp > 0 {
		lag = time.Since(time.UnixMilli(block.BlockHeader.RawData.Timestamp)).Milliseconds() / 3000
		if lag < 0 {
			lag = 0
		}
	}
	if _, ok := nodeInfo["configNodeInfo"]; !ok {
		nodeInfo["configNodeInfo"] = map[string]any{"codeVersion": ""}
	}
	nodeInfo["block"] = block.BlockHeader.RawData.Number
	nodeInfo["lag"] = lag
	row["status"] = "success"
	row["node_info"] = nodeInfo
	return row
}

func (s *Server) requireTRON(w http.ResponseWriter) bool {
	if s.cfg.Module != "TRON" {
		errorJSON(w, http.StatusNotFound, errors.New("TRON endpoint is only available in TRON worker"))
		return false
	}
	return true
}

func serverName(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Host != "" {
		host, _, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr == nil {
			return host
		}
		return parsed.Host
	}
	return endpoint
}

func boolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "":
		return fallback
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return fallback
	}
}

func metricLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", "")
	return value
}
