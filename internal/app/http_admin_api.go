package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/chainworker"
	"github.com/shopspring/decimal"
)

const desiredCryptosSettingName = "AdminDesiredCryptos"

func (h *HTTPHandler) apiAdminBootstrap(w http.ResponseWriter, r *http.Request) {
	user := h.adminRequestUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"user": map[string]any{
			"id":              user.ID,
			"username":        user.Username,
			"totp_enabled":    user.TOTPEnabled,
			"totp_enabled_at": nullableTime(user.TOTPEnabledAt.Valid, user.TOTPEnabledAt.Time),
		},
		"fiats": h.cfg.Fiats,
		"wallet_encryption": map[string]string{
			"persistent_status": h.walletEncryptionPersistentStatus(r),
			"runtime_status":    h.walletEncryptionRuntimeStatus(r),
		},
	})
}

func (h *HTTPHandler) adminRequestUser(r *http.Request) User {
	if user, ok := h.auth.CurrentUser(r); ok {
		return user
	}
	if user, ok := r.Context().Value(userContextKey).(User); ok {
		return user
	}
	return User{}
}

func (h *HTTPHandler) apiAdminWallets(w http.ResponseWriter, r *http.Request) {
	includeBalance := boolQuery(r, "include_balance") || boolQuery(r, "live_balance")
	rows := make([]map[string]any, 0, len(h.crypto.Modules()))
	for _, module := range h.crypto.Modules() {
		rows = append(rows, h.adminWalletJSON(r, module, false, includeBalance))
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "wallets": rows})
}

func (h *HTTPHandler) apiAdminWalletDetail(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	row := h.adminWalletJSON(r, module, true, true)
	destinations, err := h.store.ListPayoutDestinations(r.Context(), module.Name)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(destinations))
	for _, item := range destinations {
		out = append(out, map[string]any{"addr": item.Addr, "comment": item.Comment})
	}
	row["payout_destinations"] = out
	row["server"] = h.crypto.ServerDetails(r.Context(), module)
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "wallet": row})
}

func (h *HTTPHandler) adminWalletJSON(r *http.Request, module *CryptoModule, includeRates bool, includeBalance bool) map[string]any {
	ctx := r.Context()
	wallet, walletErr := h.store.WalletByCrypto(ctx, module.Name)
	status := h.crypto.Status(ctx, module)
	row := map[string]any{
		"name":           module.Name,
		"display_name":   module.DisplayName,
		"network":        module.Network,
		"adapter":        module.Adapter,
		"status":         status,
		"balance":        "",
		"balance_source": "not_loaded",
		"balance_error":  "",
	}
	if includeBalance {
		balance, source, balanceErr := h.crypto.Balance(ctx, module)
		row["balance"] = balance.String()
		row["balance_source"] = source
		row["balance_error"] = balanceErr
	}
	if walletErr != nil {
		row["wallet_error"] = walletErr.Error()
		return row
	}
	row["enabled"] = wallet.Enabled
	row["api_key"] = nullStringValue(wallet.APIKey)
	row["autopayout_enabled"] = wallet.Payout
	row["autopayout_destination"] = nullStringValue(wallet.PDest)
	row["autopayout_fee"] = nullStringValue(wallet.PFee)
	row["autopayout_policy"] = strings.ToLower(wallet.PPolicy)
	row["autopayout_condition"] = nullStringValue(wallet.PCond)
	row["reserve_policy"] = strings.ToLower(wallet.PresPolicy)
	row["reserve_amount"] = nullStringValue(wallet.PresAmount)
	row["partial_paid_percent"] = wallet.LLimit.String()
	row["overpaid_percent"] = wallet.ULimit.String()
	row["recalculate_after"] = wallet.Recalc
	row["confirmations"] = wallet.Confirmations
	row["last_payout_attempt"] = nullableTime(wallet.LastAttempt.Valid, wallet.LastAttempt.Time)
	if includeRates {
		if module.Network == "TRX" {
			row["activation"] = h.crypto.ActivationStatus(ctx, module)
		}
		rates := make([]map[string]any, 0, len(h.cfg.Fiats))
		for _, fiat := range h.cfg.Fiats {
			rate, err := h.store.ExchangeRate(ctx, fiat, module.Name)
			if err != nil {
				rates = append(rates, map[string]any{"fiat": fiat, "error": err.Error()})
				continue
			}
			rates = append(rates, h.adminRateJSON(ctxRequest{r}, rate))
		}
		row["rates"] = rates
	}
	return row
}

func boolQuery(r *http.Request, key string) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (h *HTTPHandler) apiAdminPayoutQuote(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("crypto")))
	amount := decimal.Zero
	amountText := strings.TrimSpace(r.URL.Query().Get("amount"))
	if amountText != "" {
		parsed, err := decimal.NewFromString(amountText)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err)
			return
		}
		amount = parsed
	}
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	payload := map[string]any{
		"status": "success",
		"crypto": crypto,
		"amount": amount.String(),
	}
	module, ok := h.crypto.Module(crypto)
	if crypto == "" || !ok {
		payload["status"] = "disabled"
		payload["balance"] = "0"
		payload["max_single_account"] = "0"
		payload["balance_source"] = "disabled"
		payload["balance_error"] = "该币种当前未启用，或对应 Docker 服务已被禁用"
		writeJSON(w, http.StatusOK, payload)
		return
	}
	payload["crypto"] = module.Name
	if wallet, err := h.store.WalletByCrypto(r.Context(), module.Name); err == nil && !wallet.Enabled {
		payload["status"] = "disabled"
		payload["balance"] = "0"
		payload["max_single_account"] = "0"
		payload["balance_source"] = "disabled"
		payload["balance_error"] = "该币种钱包当前未启用"
		writeJSON(w, http.StatusOK, payload)
		return
	}
	if module.Network == "TRX" {
		spendable, err := h.crypto.Spendable(r.Context(), module)
		if err != nil {
			payload["balance"] = "0"
			payload["max_single_account"] = "0"
			payload["balance_error"] = err.Error()
		} else {
			copyMapValue(payload, spendable, "balance")
			copyMapValue(payload, spendable, "max_single_account")
			copyMapValue(payload, spendable, "account")
			copyMapValue(payload, spendable, "account_count")
			copyMapValue(payload, spendable, "checked")
			copyMapValue(payload, spendable, "failed")
			copyMapValue(payload, spendable, "balance_error")
			copyMapValue(payload, spendable, "cache_ready")
			copyMapValue(payload, spendable, "cache_stale")
			copyMapValue(payload, spendable, "cache_age_seconds")
			copyMapValue(payload, spendable, "refreshed_at")
			copyMapValue(payload, spendable, "refreshing")
			copyMapValue(payload, spendable, "balance_source")
			if _, ok := payload["balance_source"]; !ok {
				payload["balance_source"] = "wallet_cache"
			}
		}
	} else {
		balance, source, balanceErr := h.crypto.Balance(r.Context(), module)
		payload["balance"] = balance.String()
		payload["max_single_account"] = balance.String()
		payload["balance_source"] = source
		payload["balance_error"] = balanceErr
	}
	if amount.GreaterThan(decimal.Zero) {
		fee, err := h.crypto.EstimateTxFee(r.Context(), module, amount, address)
		if err != nil {
			payload["fee_error"] = err.Error()
		} else {
			payload["fee"] = anyString(fee["fee"])
			payload["fee_asset"] = module.Name
			if module.Network == "TRX" {
				payload["fee_asset"] = "TRX"
			}
			copyMapValue(payload, fee, "fee_sun")
			copyMapValue(payload, fee, "fee_satoshi")
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

func copyMapValue(dst map[string]any, src map[string]any, key string) {
	if value, ok := src[key]; ok {
		dst[key] = value
	}
}

func (h *HTTPHandler) apiAdminOrders(w http.ResponseWriter, r *http.Request) {
	filter := OrderFilter{
		ExternalID: r.URL.Query().Get("external_id"),
		Status:     strings.ToUpper(r.URL.Query().Get("status")),
		Crypto:     strings.ToUpper(r.URL.Query().Get("crypto")),
		FromDate:   r.URL.Query().Get("from_date"),
		ToDate:     r.URL.Query().Get("to_date"),
		Limit:      intQuery(r, "limit", 30),
		Offset:     intQuery(r, "offset", 0),
		Cursor:     r.URL.Query().Get("cursor"),
	}
	page, err := h.store.ListOrdersPage(r.Context(), filter)
	if err != nil {
		if errors.Is(err, errInvalidOrderCursor) {
			errorJSON(w, http.StatusBadRequest, err)
			return
		}
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(page.Orders))
	for _, order := range page.Orders {
		out = append(out, orderJSON(order))
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "orders": out, "next_cursor": page.NextCursor})
}

func (h *HTTPHandler) apiAdminPayouts(w http.ResponseWriter, r *http.Request) {
	payouts, err := h.store.ListPayouts(
		r.Context(),
		r.URL.Query().Get("crypto"),
		r.URL.Query().Get("status"),
		r.URL.Query().Get("dest_addr"),
		r.URL.Query().Get("txid"),
		intQuery(r, "limit", 50),
	)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(payouts))
	for _, payout := range payouts {
		txids := payoutTxIDs(payout)
		out = append(out, map[string]any{
			"id":           payout.ID,
			"created_at":   payout.CreatedAt.Format(time.RFC3339),
			"updated_at":   payout.UpdatedAt.Format(time.RFC3339),
			"crypto":       payout.Crypto,
			"amount":       payout.Amount.String(),
			"destination":  payout.DestAddr,
			"status":       payout.Status,
			"success":      nullStringValue(payout.Success),
			"error":        nullStringValue(payout.Error),
			"task_id":      nullStringValue(payout.TaskID),
			"external_id":  nullStringValue(payout.ExternalID),
			"callback_url": nullStringValue(payout.CallbackURL),
			"txids":        txids,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "payouts": out})
}

func (h *HTTPHandler) apiAdminRates(w http.ResponseWriter, r *http.Request) {
	fiat := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("fiat")))
	if fiat == "" {
		fiat = firstFiat(h.cfg.Fiats)
	}
	wallets, err := h.store.ListWallets(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	rates := make([]ExchangeRate, 0, len(wallets))
	seen := map[string]struct{}{}
	for _, wallet := range wallets {
		crypto := strings.ToUpper(strings.TrimSpace(wallet.Crypto))
		if crypto == "" || !wallet.Enabled {
			continue
		}
		if _, ok := h.crypto.Module(crypto); !ok {
			continue
		}
		if _, ok := seen[crypto]; ok {
			continue
		}
		seen[crypto] = struct{}{}
		if err := h.store.EnsureExchangeRate(r.Context(), crypto, fiat); err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		rate, err := h.store.ExchangeRate(r.Context(), fiat, crypto)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		rates = append(rates, rate)
	}
	if len(rates) == 0 {
		existing, err := h.store.ListExchangeRates(r.Context(), fiat)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		for _, rate := range existing {
			crypto := strings.ToUpper(strings.TrimSpace(rate.Crypto))
			if _, ok := h.crypto.Module(crypto); !ok {
				continue
			}
			rates = append(rates, rate)
		}
	}
	sort.Slice(rates, func(i, j int) bool { return rates[i].Crypto < rates[j].Crypto })
	out := make([]map[string]any, 0, len(rates))
	for _, rate := range rates {
		out = append(out, h.adminRateJSON(ctxRequest{r}, rate))
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "fiat": fiat, "rates": out})
}

func (h *HTTPHandler) apiAdminRatesPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fiat  string         `json:"fiat"`
		Rates []ExchangeRate `json:"rates"`
	}
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	fiat := strings.ToUpper(strings.TrimSpace(req.Fiat))
	if fiat == "" {
		fiat = firstFiat(h.cfg.Fiats)
	}
	for _, rate := range req.Rates {
		rate.Crypto = strings.ToUpper(strings.TrimSpace(rate.Crypto))
		rate.Fiat = fiat
		rate.Source = strings.ToLower(strings.TrimSpace(rate.Source))
		rate.FeePolicy = strings.TrimSpace(rate.FeePolicy)
		if rate.Crypto == "" {
			continue
		}
		if rate.Source == "" {
			rate.Source = "dynamic"
		}
		if rate.FeePolicy == "" {
			rate.FeePolicy = "PERCENT_FEE"
		}
		if err := h.store.UpdateExchangeRateSettings(r.Context(), rate, rate.Source == "manual"); err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) apiAdminCryptos(w http.ResponseWriter, r *http.Request) {
	payload, err := h.adminCryptoState(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *HTTPHandler) apiAdminCryptosPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cryptos []string `json:"cryptos"`
	}
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	selected, err := normalizeCryptoSelection(req.Cryptos)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	if len(selected) == 0 {
		errorJSON(w, http.StatusBadRequest, errors.New("at least one crypto is required"))
		return
	}
	selectedSet := cryptoStringSet(selected)
	defs := CryptoDefinitions()
	for _, def := range defs {
		enabled := selectedSet[def.Name]
		if enabled {
			if err := h.store.EnsureWallet(r.Context(), def.Name, h.cfg.SuggestedWalletAPIKey); err != nil {
				errorJSON(w, http.StatusInternalServerError, err)
				return
			}
			for _, fiat := range h.cfg.Fiats {
				if err := h.store.EnsureExchangeRate(r.Context(), def.Name, fiat); err != nil {
					errorJSON(w, http.StatusInternalServerError, err)
					return
				}
			}
		}
		if err := h.store.SetWalletEnabled(r.Context(), def.Name, enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
	}
	desired := strings.Join(selected, ",")
	if err := h.store.UpsertSetting(r.Context(), desiredCryptosSettingName, desired); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	command := adminCryptoApplyCommand(selected)
	applied, output, applyErr := runAdminCryptoApply(r.Context(), selected)
	payload, err := h.adminCryptoState(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	payload["command"] = command
	payload["short_command"] = adminCryptoShortApplyCommand(selected)
	payload["applied"] = applied
	payload["apply_output"] = output
	if applyErr != nil {
		payload["apply_error"] = applyErr.Error()
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *HTTPHandler) apiAdminWalletImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Module                string          `json:"module"`
		DefaultCrypto         string          `json:"default_crypto"`
		AccountPassword       string          `json:"account_password"`
		LegacyAccountPassword string          `json:"legacy_account_password"`
		JSON                  string          `json:"json"`
		Payload               json.RawMessage `json:"payload"`
	}
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	raw := strings.TrimSpace(req.JSON)
	if raw == "" && len(req.Payload) > 0 {
		raw = strings.TrimSpace(string(req.Payload))
	}
	raw = normalizeLegacyWalletJSONInput(raw)
	if raw == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("legacy wallet JSON is required"))
		return
	}
	module := strings.ToUpper(strings.TrimSpace(req.Module))
	defaultCrypto := strings.ToUpper(strings.TrimSpace(req.DefaultCrypto))
	accountPassword := firstNonEmpty(req.AccountPassword, accountPasswordFromEnv(module), accountPasswordFromEnv(defaultCrypto), os.Getenv("ACCOUNT_PASSWORD"))
	legacyPassword := firstNonEmpty(req.LegacyAccountPassword, os.Getenv("LEGACY_ACCOUNT_PASSWORD"))
	if legacyPassword == "" {
		legacyPassword = accountPassword
	}
	cwStore := chainworker.NewStoreFromDB(h.store.DB())
	if err := cwStore.Migrate(r.Context()); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	report, err := chainworker.ImportLegacyAccountsJSON(r.Context(), cwStore, strings.NewReader(raw), chainworker.LegacyAccountImportOptions{
		Module:                module,
		DefaultCrypto:         defaultCrypto,
		AccountPassword:       accountPassword,
		LegacyAccountPassword: legacyPassword,
	})
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	for crypto := range report.Cryptos {
		if err := h.store.EnsureWallet(r.Context(), crypto, h.cfg.SuggestedWalletAPIKey); err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		for _, fiat := range h.cfg.Fiats {
			if err := h.store.EnsureExchangeRate(r.Context(), crypto, fiat); err != nil {
				errorJSON(w, http.StatusInternalServerError, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "report": report})
}

func (h *HTTPHandler) adminCryptoState(ctx context.Context) (map[string]any, error) {
	wallets, err := h.store.ListWallets(ctx)
	if err != nil {
		return nil, err
	}
	walletByCrypto := map[string]Wallet{}
	for _, wallet := range wallets {
		walletByCrypto[strings.ToUpper(wallet.Crypto)] = wallet
	}
	configured := map[string]bool{}
	for _, module := range h.crypto.Modules() {
		configured[module.Name] = true
	}
	desiredText, err := h.store.Setting(ctx, desiredCryptosSettingName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	desiredList, _ := normalizeCryptoSelection(splitCSV(desiredText))
	desired := cryptoStringSet(desiredList)
	if len(desired) == 0 {
		for _, wallet := range wallets {
			if wallet.Enabled {
				desired[strings.ToUpper(wallet.Crypto)] = true
			}
		}
		for crypto := range configured {
			desired[crypto] = true
		}
	}
	rows := make([]map[string]any, 0)
	for _, def := range CryptoDefinitions() {
		wallet, walletExists := walletByCrypto[def.Name]
		rows = append(rows, map[string]any{
			"name":            def.Name,
			"display_name":    def.DisplayName,
			"network":         def.Network,
			"adapter":         def.Adapter,
			"default_on":      def.DefaultOn,
			"configured":      configured[def.Name],
			"selected":        desired[def.Name],
			"wallet_exists":   walletExists,
			"wallet_enabled":  walletExists && wallet.Enabled,
			"service":         def.DefaultHost,
			"host_env":        def.HostEnv,
			"port_env":        def.PortEnv,
			"requires_apply":  desired[def.Name] != configured[def.Name],
			"requires_wallet": desired[def.Name] && !walletExists,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		ni := rows[i]["name"].(string)
		nj := rows[j]["name"].(string)
		return ni < nj
	})
	selected := make([]string, 0, len(desired))
	for crypto := range desired {
		selected = append(selected, crypto)
	}
	sort.Strings(selected)
	return map[string]any{
		"status":               "success",
		"cryptos":              rows,
		"selected":             selected,
		"desired":              strings.Join(selected, ","),
		"command":              adminCryptoApplyCommand(selected),
		"short_command":        adminCryptoShortApplyCommand(selected),
		"auto_apply_enabled":   strings.TrimSpace(os.Getenv("SHKEEPER_ADMIN_CRYPTO_COMMAND")) != "",
		"command_env":          "SHKEEPER_ADMIN_CRYPTO_COMMAND",
		"manager_command_hint": "set-cryptos",
	}, nil
}

func normalizeCryptoSelection(values []string) ([]string, error) {
	valid := map[string]struct{}{}
	for _, def := range CryptoDefinitions() {
		valid[def.Name] = struct{}{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.ToUpper(strings.TrimSpace(value))
		if name == "" {
			continue
		}
		if _, ok := valid[name]; !ok {
			return nil, fmt.Errorf("unsupported crypto %s", name)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func cryptoStringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func adminCryptoApplyCommand(cryptos []string) string {
	manager := strings.TrimSpace(os.Getenv("SHKEEPER_ADMIN_CRYPTO_COMMAND"))
	if manager == "" {
		manager = "deploy/shkeeperctl.sh"
	}
	return strings.TrimSpace(manager + " set-cryptos " + strings.Join(cryptos, ","))
}

func adminCryptoShortApplyCommand(cryptos []string) string {
	return strings.TrimSpace("shkeeperctl set-cryptos " + strings.Join(cryptos, ","))
}

func runAdminCryptoApply(ctx context.Context, cryptos []string) (bool, string, error) {
	manager := strings.TrimSpace(os.Getenv("SHKEEPER_ADMIN_CRYPTO_COMMAND"))
	if manager == "" {
		return false, "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	parts := strings.Fields(manager)
	if len(parts) == 0 {
		return false, "", nil
	}
	args := append(parts[1:], "set-cryptos", strings.Join(cryptos, ","))
	cmd := exec.CommandContext(ctx, parts[0], args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return false, string(out), ctx.Err()
	}
	if err != nil {
		return false, string(out), err
	}
	return true, string(out), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeLegacyWalletJSONInput(raw string) string {
	text := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "```json") || trimmed == "```" {
			continue
		}
		filtered = append(filtered, line)
	}
	text = strings.TrimSpace(strings.Join(filtered, "\n"))
	text = strings.NewReplacer(
		"\u200b", "",
		"\u200c", "",
		"\u200d", "",
		"\u2060", "",
		"“", "\"",
		"”", "\"",
		"‘", "'",
		"’", "'",
		"，", ",",
		"：", ":",
	).Replace(text)
	startObject := strings.Index(text, "{")
	startArray := strings.Index(text, "[")
	start := minNonNegative(startObject, startArray)
	endObject := strings.LastIndex(text, "}")
	endArray := strings.LastIndex(text, "]")
	end := maxInt(endObject, endArray)
	if start >= 0 && end > start {
		text = strings.TrimSpace(text[start : end+1])
	}
	return text
}

func minNonNegative(values ...int) int {
	out := -1
	for _, value := range values {
		if value < 0 {
			continue
		}
		if out < 0 || value < out {
			out = value
		}
	}
	return out
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func accountPasswordFromEnv(value string) string {
	prefix := accountPasswordEnvPrefix(value)
	if prefix == "" {
		return ""
	}
	return os.Getenv(prefix + "_ACCOUNT_PASSWORD")
}

func accountPasswordEnvPrefix(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch {
	case value == "":
		return ""
	case value == "TRON" || value == "TRX" || value == "USDT" || value == "USDC":
		return "TRON"
	case value == "BNB" || strings.HasPrefix(value, "BNB-"):
		return "BNB"
	case value == "SOL" || strings.HasPrefix(value, "SOLANA-"):
		return "SOLANA"
	case value == "MATIC" || strings.HasPrefix(value, "POLYGON-"):
		return "POLYGON"
	case value == "AVAX" || strings.HasPrefix(value, "AVALANCHE-"):
		return "AVALANCHE"
	case value == "ARBETH" || strings.HasPrefix(value, "ARB-"):
		return "ARB"
	case value == "OPETH" || strings.HasPrefix(value, "OP-"):
		return "OP"
	case strings.HasPrefix(value, "ETH-"):
		return "ETH"
	default:
		return strings.ReplaceAll(value, "-", "_")
	}
}

type ctxRequest struct {
	r *http.Request
}

func (h *HTTPHandler) adminRateJSON(req ctxRequest, rate ExchangeRate) map[string]any {
	row := map[string]any{
		"id":         rate.ID,
		"source":     rate.Source,
		"crypto":     rate.Crypto,
		"fiat":       rate.Fiat,
		"rate":       rate.Rate.String(),
		"fee":        rate.Fee.String(),
		"fixed_fee":  rate.FixedFee.String(),
		"fee_policy": rate.FeePolicy,
	}
	current, err := h.rates.CurrentRate(req.r.Context(), rate)
	if err != nil {
		row["current_rate_error"] = err.Error()
		return row
	}
	row["current_rate"] = current.String()
	return row
}

func firstFiat(fiats []string) string {
	if len(fiats) == 0 {
		return "USD"
	}
	return strings.ToUpper(fiats[0])
}

func nullableTime(valid bool, value time.Time) any {
	if !valid {
		return nil
	}
	return value.Format(time.RFC3339)
}
