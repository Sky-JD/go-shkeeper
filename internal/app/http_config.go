package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

func (h *HTTPHandler) apiPaymentGatewayGet(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	wallet, err := h.store.WalletByCrypto(r.Context(), module.Name)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "success",
		"enabled": wallet.Enabled,
		"token":   nullStringValue(wallet.APIKey),
	})
}

func (h *HTTPHandler) apiPaymentGatewaySet(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	value, ok := req["enabled"]
	if !ok {
		errorJSON(w, http.StatusBadRequest, errors.New("enabled is required"))
		return
	}
	if err := h.store.SetWalletEnabled(r.Context(), module.Name, boolFromAny(value, false)); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) apiPaymentGatewayToken(w http.ResponseWriter, r *http.Request) {
	if _, err := h.moduleFromRoute(r); err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	token := strings.TrimSpace(anyString(req["token"]))
	if token == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("token is required"))
		return
	}
	if err := h.store.SetAllWalletAPIKeys(r.Context(), token); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) apiPayoutDestinations(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(anyString(req["action"])))
	switch action {
	case "add":
		addr := strings.TrimSpace(anyString(req["daddress"]))
		if addr == "" {
			errorJSON(w, http.StatusBadRequest, errors.New("daddress is required"))
			return
		}
		if err := h.store.UpsertPayoutDestination(r.Context(), module.Name, addr, anyString(req["comment"])); err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
	case "delete":
		addr := strings.TrimSpace(anyString(req["daddress"]))
		if addr == "" {
			errorJSON(w, http.StatusBadRequest, errors.New("daddress is required"))
			return
		}
		if err := h.store.DeletePayoutDestination(r.Context(), module.Name, addr); err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
	case "list":
		destinations, err := h.store.ListPayoutDestinations(r.Context(), module.Name)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]map[string]any, 0, len(destinations))
		for _, item := range destinations {
			out = append(out, map[string]any{"addr": item.Addr, "comment": item.Comment})
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "payout_destinations": out})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "message": "Unknown action"})
	}
}

func (h *HTTPHandler) apiAutopayout(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	wallet, err := h.store.WalletByCrypto(r.Context(), module.Name)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	policy, ok := normalizeOption(req["policy"], "manual", "scheduled", "limit")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "message": "Unknown payout policy: " + anyString(req["policy"])})
		return
	}
	reservePolicy, ok := normalizeOption(req["prespolicyOption"], "disable", "amount", "percent")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "message": "Unknown payout reserve policy: " + anyString(req["prespolicyOption"])})
		return
	}
	presAmount, err := reserveAmount(reservePolicy, req["prespolicyValue"])
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	llimit, err := decimalField(req, "partiallPaid", wallet.LLimit)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	ulimit, err := decimalField(req, "addedFee", wallet.ULimit)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	settings := WalletAutopayout{
		PDest:         wallet.PDest,
		PFee:          wallet.PFee,
		Payout:        boolFromAny(req["policyStatus"], true),
		PPolicy:       policy,
		PCond:         sqlNullText(anyString(req["policyValue"])),
		LLimit:        llimit,
		ULimit:        ulimit,
		Recalc:        intField(req, "recalc", wallet.Recalc),
		Confirmations: intField(req, "confirationNum", wallet.Confirmations),
		PresPolicy:    reservePolicy,
		PresAmount:    presAmount,
	}
	if value, ok := req["add"]; ok && strings.TrimSpace(anyString(value)) != "" {
		settings.PDest = sqlNullText(anyString(value))
	}
	if value, ok := req["fee"]; ok && strings.TrimSpace(anyString(value)) != "" {
		settings.PFee = sqlNullText(anyString(value))
	}
	if settings.Confirmations < 0 {
		errorJSON(w, http.StatusBadRequest, errors.New("confirationNum must be non-negative"))
		return
	}
	if err := h.store.UpdateWalletAutopayout(r.Context(), module.Name, settings); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) apiExchangeRate(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	fiat := strings.ToUpper(strings.TrimSpace(anyString(req["fiat"])))
	source := strings.ToLower(strings.TrimSpace(anyString(req["source"])))
	if fiat == "" || source == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("fiat and source are required"))
		return
	}
	fee, ok := decimalFromAny(req["fee"])
	if !ok {
		errorJSON(w, http.StatusBadRequest, errors.New("fee is required"))
		return
	}
	rate := decimal.Zero
	updateRate := source == "manual"
	if updateRate {
		var ok bool
		rate, ok = decimalFromAny(req["rate"])
		if !ok {
			errorJSON(w, http.StatusBadRequest, errors.New("rate is required for manual exchange-rate source"))
			return
		}
	}
	settings := ExchangeRate{
		Crypto: module.Name,
		Fiat:   fiat,
		Source: source,
		Rate:   rate,
		Fee:    fee,
	}
	if err := h.store.UpdateExchangeRateSettings(r.Context(), settings, updateRate); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) apiEstimateTxFee(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(chi.URLParam(r, "amount")))
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	payload, err := h.crypto.EstimateTxFee(r.Context(), module, amount, r.URL.Query().Get("address"))
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	if _, ok := payload["status"]; !ok {
		payload["status"] = "success"
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *HTTPHandler) apiPayouts(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	rawAmount := strings.TrimSpace(r.URL.Query().Get("amount"))
	if rawAmount == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "message": "No amount provided."})
		return
	}
	amount, err := decimal.NewFromString(rawAmount)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	payouts, err := h.store.PayoutsByCryptoAmount(r.Context(), module.Name, amount.String())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	if len(payouts) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "message": "No payouts for " + amount.String() + " " + module.Name + " found."})
		return
	}
	out := make([]map[string]any, 0, len(payouts))
	for _, payout := range payouts {
		txids := make([]string, 0, len(payout.Transactions))
		for _, tx := range payout.Transactions {
			txids = append(txids, tx.TxID)
		}
		out = append(out, map[string]any{
			"id":          payout.ID,
			"amount":      payout.Amount.String(),
			"crypto":      payout.Crypto,
			"destination": payout.DestAddr,
			"external_id": nullStringValue(payout.ExternalID),
			"task_id":     nullStringValue(payout.TaskID),
			"status":      payout.Status,
			"txids":       txids,
			"created_at":  payout.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "payouts": out})
}

func normalizeOption(value any, allowed ...string) (string, bool) {
	text := strings.ToLower(strings.TrimSpace(anyString(value)))
	for _, item := range allowed {
		if text == item || strings.ToUpper(text) == strings.ToUpper(item) {
			return item, true
		}
	}
	return text, false
}

func reserveAmount(policy string, value any) (sql.NullString, error) {
	if policy == "disable" {
		return sql.NullString{}, nil
	}
	text := strings.TrimSpace(anyString(value))
	if text == "" || strings.EqualFold(text, "none") {
		return sql.NullString{}, nil
	}
	amount, err := decimal.NewFromString(text)
	if err != nil {
		return sql.NullString{}, err
	}
	return sqlNullText(amount.String()), nil
}

func decimalField(req map[string]any, key string, fallback decimal.Decimal) (decimal.Decimal, error) {
	if value, ok := req[key]; ok {
		amount, ok := decimalFromAny(value)
		if !ok {
			return decimal.Zero, errors.New(key + " must be a decimal")
		}
		return amount, nil
	}
	return fallback, nil
}

func intField(req map[string]any, key string, fallback int) int {
	if value, ok := req[key]; ok {
		return intFromAny(value)
	}
	return fallback
}

func sqlNullText(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}
