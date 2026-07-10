package app

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

func (h *HTTPHandler) apiVerifiedTransaction(w http.ResponseWriter, r *http.Request) {
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
	txid := strings.TrimSpace(anyString(firstAny(req, "txid", "transaction_hash")))
	addr := strings.TrimSpace(anyString(firstAny(req, "addr", "address", "to_address")))
	amount, ok := decimalFromAny(firstAny(req, "amount", "amount_crypto"))
	if txid == "" || addr == "" || !ok {
		errorJSON(w, http.StatusBadRequest, errors.New("txid, addr and amount are required"))
		return
	}
	if !amount.GreaterThan(decimal.Zero) {
		errorJSON(w, http.StatusBadRequest, errors.New("amount must be positive"))
		return
	}
	externalID := strings.TrimSpace(anyString(firstAny(req, "external_id")))
	confirmations := intFromAny(firstAny(req, "confirmations"))
	if confirmations == 0 {
		wallet, _ := h.store.WalletByCrypto(r.Context(), module.Name)
		confirmations = wallet.Confirmations
	}
	sendCallback := boolFromAny(firstAny(req, "send_callback"), true)

	tx, invoice, duplicate, err := h.recordConfirmedTransaction(r, module, txid, addr, amount, confirmations, externalID)
	if err != nil {
		errorJSON(w, statusForRecordErr(err), err)
		return
	}
	if !tx.NeedMoreConfirmations {
		if sendCallback {
			_ = h.sendInvoiceNotification(r.Context(), tx, invoice)
		} else {
			_ = h.store.MarkTransactionCallbackConfirmed(r.Context(), tx.ID)
		}
	}
	detail, _ := h.store.InvoiceDetail(r.Context(), invoice)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "success",
		"id":        tx.ID,
		"duplicate": duplicate,
		"invoice":   invoiceJSON(detail.Invoice, detail.Transactions, detail.UnconfirmedTXs),
	})
}

func (h *HTTPHandler) recordConfirmedTransaction(r *http.Request, module *CryptoModule, txid, addr string, amount decimal.Decimal, confirmations int, externalID string) (Transaction, Invoice, bool, error) {
	ctx := r.Context()
	invoice, err := h.store.InvoiceByCryptoAddress(ctx, module.Name, addr)
	if err != nil {
		return Transaction{}, Invoice{}, false, fmt.Errorf("%s is not related to any invoice", addr)
	}
	if externalID != "" && invoice.ExternalID != externalID {
		return Transaction{}, Invoice{}, false, errors.New("external_id does not match invoice address")
	}
	wallet, err := h.store.WalletByCrypto(ctx, invoice.Crypto)
	if err != nil {
		return Transaction{}, Invoice{}, false, err
	}
	amountFiat := amount.Mul(invoice.ExchangeRate)
	if invoice.Crypto != module.Name {
		rate, err := h.store.ExchangeRate(ctx, invoice.Fiat, module.Name)
		if err != nil {
			return Transaction{}, Invoice{}, false, err
		}
		currentRate, err := h.rates.CurrentRate(ctx, rate)
		if err != nil {
			return Transaction{}, Invoice{}, false, err
		}
		amountFiat = amount.Mul(currentRate)
	}
	tx := Transaction{
		InvoiceID:             invoice.ID,
		TxID:                  txid,
		Crypto:                module.Name,
		AmountCrypto:          amount,
		AmountFiat:            amountFiat,
		NeedMoreConfirmations: confirmations < wallet.Confirmations,
		CallbackConfirmed:     false,
		Addr:                  addr,
	}
	invoice, duplicate, err := h.store.ApplyConfirmedTransaction(ctx, &tx, wallet.LLimit, wallet.ULimit)
	if err != nil {
		return Transaction{}, Invoice{}, false, err
	}
	return tx, invoice, duplicate, nil
}

func invoiceStatus(balanceFiat, amountFiat, llimit, ulimit decimal.Decimal) string {
	if amountFiat.IsZero() {
		return InvoicePaid
	}
	lower := amountFiat.Mul(llimit).Div(decimal.NewFromInt(100))
	upper := amountFiat.Mul(ulimit).Div(decimal.NewFromInt(100))
	if balanceFiat.LessThan(lower) {
		return InvoicePartial
	}
	if balanceFiat.LessThan(upper) {
		return InvoicePaid
	}
	return InvoiceOverpaid
}

func statusForRecordErr(err error) int {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not related"):
		return http.StatusNotFound
	case strings.Contains(msg, "external_id"):
		return http.StatusConflict
	default:
		return http.StatusConflict
	}
}

func (h *HTTPHandler) apiWalletNotify(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "Ignoring notification for unavailable crypto"})
		return
	}
	if !h.validBackendKey(r, module) {
		errorJSON(w, http.StatusForbidden, errors.New("Wrong backend key"))
		return
	}
	txid := chi.URLParam(r, "txid")
	transfers, err := h.crypto.TransfersByTx(r.Context(), module, txid)
	if err != nil {
		errorJSON(w, http.StatusConflict, err)
		return
	}
	for _, transfer := range transfers {
		if transfer.Category != "" && transfer.Category != "receive" && transfer.Category != "send" {
			continue
		}
		if transfer.Category == "send" {
			if _, _, duplicate, err := h.recordOutgoingTransaction(r, module, txid, transfer.Address, transfer.Amount); err != nil {
				h.logger.Warn("outgoing wallet notification skipped", "crypto", module.Name, "txid", txid, "error", err)
			} else if !duplicate {
				h.logger.Info("outgoing wallet notification recorded", "crypto", module.Name, "txid", txid)
			}
			continue
		}
		if transfer.Confirmations == 0 {
			if h.cfg.UnconfirmedTXNotification {
				if invoice, err := h.store.InvoiceByCryptoAddress(r.Context(), module.Name, transfer.Address); err == nil {
					utx := UnconfirmedTransaction{InvoiceID: invoice.ID, Addr: transfer.Address, TxID: txid, Crypto: module.Name, AmountCrypto: transfer.Amount}
					if err := h.store.AddUnconfirmed(r.Context(), &utx); err == nil {
						_ = h.sendUnconfirmedNotification(r.Context(), utx, invoice)
					}
				}
			}
			continue
		}
		tx, invoice, duplicate, err := h.recordConfirmedTransaction(r, module, txid, transfer.Address, transfer.Amount, transfer.Confirmations, "")
		if err != nil {
			h.logger.Warn("wallet notification skipped", "crypto", module.Name, "txid", txid, "error", err)
			continue
		}
		if !duplicate && !tx.NeedMoreConfirmations {
			_ = h.sendInvoiceNotification(r.Context(), tx, invoice)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) recordOutgoingTransaction(r *http.Request, module *CryptoModule, txid, addr string, amount decimal.Decimal) (Transaction, Invoice, bool, error) {
	amountFiat := decimal.Zero
	rate, err := h.store.ExchangeRate(r.Context(), "USD", module.Name)
	if err == nil {
		currentRate, rateErr := h.rates.CurrentRate(r.Context(), rate)
		if rateErr == nil {
			amountFiat = amount.Mul(currentRate)
		}
	}
	return h.store.AddOutgoingTransaction(r.Context(), module.Name, txid, addr, amount, amountFiat)
}

func (h *HTTPHandler) apiPayout(w http.ResponseWriter, r *http.Request) {
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
	prepared, err := h.preparePayoutRequest(r, module, req)
	if err != nil {
		errorJSON(w, statusForPayoutValidationErr(err), err)
		return
	}
	if err := h.requirePayoutWalletReady(r.Context(), module); err != nil {
		errorJSON(w, statusForPayoutReadinessErr(err), err)
		return
	}
	payout := Payout{
		Amount:      prepared.Amount,
		Crypto:      module.Name,
		DestAddr:    prepared.Destination,
		CallbackURL: sql.NullString{String: prepared.CallbackURL, Valid: prepared.CallbackURL != ""},
		ExternalID:  sql.NullString{String: prepared.ExternalID, Valid: prepared.ExternalID != ""},
		Status:      PayoutInProgress,
	}
	if err := h.store.CreatePayout(r.Context(), &payout); err != nil {
		status, responseErr := payoutCreateError(prepared.ExternalID, err)
		errorJSON(w, status, responseErr)
		return
	}
	res, err := h.crypto.Payout(r.Context(), module, prepared.Destination, prepared.Amount, prepared.Fee)
	if err != nil {
		_ = h.store.MarkPayoutFail(r.Context(), payout.ID, err.Error())
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	result := res["result"]
	if result == nil {
		result = res
	}
	details := payoutTxDetailsFromAny(result, module.Name, prepared.Destination)
	if len(details) == 0 {
		details = payoutTxDetailsFromTxIDs(txIDsFromAny(result), module.Name, prepared.Destination)
	}
	if err := h.store.SetPayoutTaskAndTxDetails(r.Context(), payout.ID, anyString(res["task_id"]), details); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	if fee, asset, ok := payoutFeeFromResult(res); ok {
		_ = h.store.SetPayoutFee(r.Context(), payout.ID, fee, asset)
	}
	switch strings.ToUpper(strings.TrimSpace(anyString(res["status"]))) {
	case PayoutFail, "FAILED", "FAILURE", "ERROR":
		message := payoutResultErrorText(res)
		_ = h.store.MarkPayoutFail(r.Context(), payout.ID, message)
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "error", "message": message, "payout_status": PayoutFail, "result": result, "task_id": anyString(res["task_id"])})
		return
	case PayoutPartial:
		_ = h.store.MarkPayoutPartial(r.Context(), payout.ID, payoutResultErrorText(res))
	}
	if prepared.ExternalID != "" {
		res["external_id"] = prepared.ExternalID
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *HTTPHandler) apiMultiPayout(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req []map[string]any
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, errors.New("Expected an array of payouts"))
		return
	}
	prepared, err := h.prepareMultiPayoutRequests(r, module, req)
	if err != nil {
		errorJSON(w, statusForPayoutValidationErr(err), err)
		return
	}
	if err := h.requirePayoutWalletReady(r.Context(), module); err != nil {
		errorJSON(w, statusForPayoutReadinessErr(err), err)
		return
	}
	created := make([]Payout, 0, len(prepared))
	externalIDs := make([]string, 0, len(prepared))
	requestPayload := make([]map[string]any, 0, len(prepared))
	cancelCreated := func(message string) {
		for _, payout := range created {
			_ = h.store.MarkPayoutFail(r.Context(), payout.ID, message)
		}
	}
	for _, item := range prepared {
		payout := Payout{
			Amount:      item.Amount,
			Crypto:      module.Name,
			DestAddr:    item.Destination,
			CallbackURL: sql.NullString{String: item.CallbackURL, Valid: item.CallbackURL != ""},
			ExternalID:  sql.NullString{String: item.ExternalID, Valid: item.ExternalID != ""},
			Status:      PayoutInProgress,
		}
		if err := h.store.CreatePayout(r.Context(), &payout); err != nil {
			status, responseErr := payoutCreateError(item.ExternalID, err)
			cancelCreated("payout dispatch canceled: " + responseErr.Error())
			errorJSON(w, status, responseErr)
			return
		}
		created = append(created, payout)
		if item.ExternalID != "" {
			externalIDs = append(externalIDs, item.ExternalID)
		}
		requestPayload = append(requestPayload, item.Raw)
	}
	res, err := h.crypto.MultiPayout(r.Context(), module, requestPayload)
	if err != nil {
		cancelCreated(err.Error())
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	taskID := anyString(res["task_id"])
	txidQueues := payoutTxIDsByDestination(res["result"])
	if len(txidQueues) == 0 {
		txidQueues = payoutTxIDsByDestination(res)
	}
	for i, item := range prepared {
		if err := h.store.SetPayoutTaskAndTxIDs(r.Context(), created[i].ID, taskID, popPayoutTxIDs(txidQueues, item.Destination)); err != nil {
			errorJSON(w, http.StatusInternalServerError, err)
			return
		}
	}
	res["external_ids"] = externalIDs
	writeJSON(w, http.StatusOK, res)
}

type preparedPayoutRequest struct {
	Destination string
	Amount      decimal.Decimal
	Fee         string
	ExternalID  string
	CallbackURL string
	Raw         map[string]any
}

var errPayoutWalletUnavailable = errors.New("payout wallet unavailable")

func (h *HTTPHandler) requirePayoutWalletReady(ctx context.Context, module *CryptoModule) error {
	wallet, err := h.store.WalletByCrypto(ctx, module.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s wallet is not configured", errPayoutWalletUnavailable, module.Name)
	}
	if err != nil {
		return err
	}
	if !wallet.Enabled {
		return fmt.Errorf("%w: %s wallet is disabled", errPayoutWalletUnavailable, module.Name)
	}
	return nil
}

func (h *HTTPHandler) preparePayoutRequest(r *http.Request, module *CryptoModule, req map[string]any) (preparedPayoutRequest, error) {
	destination := strings.TrimSpace(anyString(firstAny(req, "destination", "dest")))
	amount, ok := decimalFromAny(firstAny(req, "amount"))
	if destination == "" || !ok {
		return preparedPayoutRequest{}, errors.New("destination and amount are required")
	}
	if !amount.GreaterThan(decimal.Zero) {
		return preparedPayoutRequest{}, errors.New("amount must be positive")
	}
	callbackURL := strings.TrimSpace(anyString(firstAny(req, "callback_url")))
	if err := validateCallbackURL(callbackURL); err != nil {
		return preparedPayoutRequest{}, err
	}
	externalID := strings.TrimSpace(anyString(firstAny(req, "external_id")))
	if externalID != "" {
		if _, err := h.store.PayoutByExternalID(r.Context(), module.Name, externalID); err == nil {
			return preparedPayoutRequest{}, fmt.Errorf("Payout with this external_id already exists: %s", externalID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return preparedPayoutRequest{}, err
		}
	}
	raw := make(map[string]any, len(req))
	for key, value := range req {
		raw[key] = value
	}
	raw["destination"] = destination
	raw["amount"] = amount.String()
	if callbackURL != "" {
		raw["callback_url"] = callbackURL
	}
	if externalID != "" {
		raw["external_id"] = externalID
	}
	return preparedPayoutRequest{
		Destination: destination,
		Amount:      amount,
		Fee:         strings.TrimSpace(anyString(firstAny(req, "fee"))),
		ExternalID:  externalID,
		CallbackURL: callbackURL,
		Raw:         raw,
	}, nil
}

func (h *HTTPHandler) prepareMultiPayoutRequests(r *http.Request, module *CryptoModule, req []map[string]any) ([]preparedPayoutRequest, error) {
	if len(req) == 0 {
		return nil, errors.New("Expected a non-empty array of payouts")
	}
	out := make([]preparedPayoutRequest, 0, len(req))
	seenExternalIDs := map[string]struct{}{}
	for _, item := range req {
		prepared, err := h.preparePayoutRequest(r, module, item)
		if err != nil {
			return nil, err
		}
		if prepared.ExternalID != "" {
			if _, ok := seenExternalIDs[prepared.ExternalID]; ok {
				return nil, fmt.Errorf("Duplicate external_id in payout list: %s", prepared.ExternalID)
			}
			seenExternalIDs[prepared.ExternalID] = struct{}{}
		}
		out = append(out, prepared)
	}
	return out, nil
}

func validateCallbackURL(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("Invalid callback_url: %s", value)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("Invalid callback_url scheme: %s", value)
	}
	if parsed.User != nil {
		return fmt.Errorf("Invalid callback_url userinfo: %s", value)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("Invalid callback_url private host: %s", value)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
			return fmt.Errorf("Invalid callback_url private address: %s", value)
		}
	}
	return nil
}

func statusForPayoutValidationErr(err error) int {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "external_id") || strings.Contains(msg, "duplicate") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func statusForPayoutReadinessErr(err error) int {
	if errors.Is(err, errPayoutWalletUnavailable) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func payoutCreateError(externalID string, err error) (int, error) {
	if externalID != "" && (isDuplicateSchemaError(err) || strings.Contains(strings.ToLower(err.Error()), "unique")) {
		return http.StatusConflict, fmt.Errorf("Payout with this external_id already exists: %s", externalID)
	}
	return http.StatusInternalServerError, err
}

func (h *HTTPHandler) apiPayoutStatus(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	externalID := r.URL.Query().Get("external_id")
	if externalID == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("external_id is required"))
		return
	}
	payout, err := h.store.PayoutByExternalID(r.Context(), module.Name, externalID)
	if err != nil {
		errorJSON(w, http.StatusNotFound, errors.New("Payout not found"))
		return
	}
	var txid any
	if len(payout.Transactions) > 0 {
		txid = payout.Transactions[0].TxID
	}
	fee, feeAsset, _ := h.store.PayoutFee(r.Context(), payout.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          payout.ID,
		"external_id": externalID,
		"crypto":      payout.Crypto,
		"status":      payout.Status,
		"amount":      payout.Amount.String(),
		"destination": payout.DestAddr,
		"txid":        txid,
		"fee":         payoutFeeString(fee, feeAsset),
		"fee_asset":   feeAsset,
	})
}

func (h *HTTPHandler) apiPayoutNotify(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "Ignoring notification for unavailable crypto"})
		return
	}
	if !h.validBackendKey(r, module) {
		errorJSON(w, http.StatusForbidden, errors.New("Wrong backend key"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) validBackendKey(r *http.Request, module *CryptoModule) bool {
	key := r.Header.Get("X-Shkeeper-Backend-Key")
	if key == "" {
		key = r.Header.Get("X-SHKEEPER-BACKEND-KEY")
	}
	if key == "" {
		return false
	}
	specific := os.Getenv("SHKEEPER_" + strings.ReplaceAll(module.Name, "-", "_") + "_BACKEND_KEY")
	if specific == "" {
		specific = os.Getenv("SHKEEPER_BACKEND_KEY")
	}
	if specific == "" {
		specific = os.Getenv("SHKEEPER_BTC_BACKEND_KEY")
	}
	if specific == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(key), []byte(specific)) == 1
}

func anyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func txIDsFromAny(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			switch row := item.(type) {
			case string:
				out = append(out, row)
			case map[string]any:
				for _, txid := range txIDsFromAny(row["txids"]) {
					out = append(out, txid)
				}
			}
		}
		return out
	case map[string]any:
		if txids := txIDsFromAny(x["txids"]); len(txids) > 0 {
			return txids
		}
		if txid := strings.TrimSpace(anyString(x["txid"])); txid != "" {
			return []string{txid}
		}
		if result, ok := x["result"]; ok {
			return txIDsFromAny(result)
		}
		if results, ok := x["results"]; ok {
			return txIDsFromAny(results)
		}
	default:
		return nil
	}
	return nil
}

func payoutTxDetailsFromAny(v any, defaultCrypto string, defaultDestination string) []PayoutTx {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]PayoutTx, 0)
		for _, item := range x {
			out = append(out, payoutTxDetailsFromAny(item, defaultCrypto, defaultDestination)...)
		}
		return out
	case map[string]any:
		if details, ok := x["details"]; ok {
			parsed := payoutTxDetailList(details, defaultCrypto, defaultDestination)
			if len(parsed) > 0 {
				return parsed
			}
		}
		if result, ok := x["result"]; ok {
			parsed := payoutTxDetailsFromAny(result, defaultCrypto, defaultDestination)
			if len(parsed) > 0 {
				return parsed
			}
		}
		if results, ok := x["results"]; ok {
			return payoutTxDetailsFromAny(results, defaultCrypto, defaultDestination)
		}
		destination := strings.TrimSpace(anyString(firstAny(x, "dest", "destination", "dest_addr")))
		if destination == "" {
			destination = defaultDestination
		}
		crypto := strings.TrimSpace(anyString(firstAny(x, "crypto")))
		if crypto == "" {
			crypto = defaultCrypto
		}
		txids := txIDsFromAny(x["txids"])
		if len(txids) == 0 {
			txids = txIDsFromAny(x["txid"])
		}
		sources := stringSliceFromAny(x["sources"])
		amounts := anySliceFromAny(x["amounts"])
		out := make([]PayoutTx, 0, len(txids))
		for i, txid := range txids {
			detail := PayoutTx{TxID: txid, Status: PayoutInProgress, Kind: "payout", DestAddr: destination, Crypto: crypto}
			if i < len(sources) {
				detail.SourceAddr = sources[i]
			}
			if i < len(amounts) {
				if amount, ok := decimalFromAny(amounts[i]); ok {
					detail.Amount = amount
				}
			}
			out = append(out, detail)
		}
		return out
	case string:
		return payoutTxDetailsFromTxIDs(txIDsFromAny(x), defaultCrypto, defaultDestination)
	default:
		return nil
	}
}

func payoutTxDetailList(v any, defaultCrypto string, defaultDestination string) []PayoutTx {
	items := anySliceFromAny(v)
	out := make([]PayoutTx, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		crypto := strings.TrimSpace(anyString(firstAny(row, "crypto")))
		if crypto == "" {
			crypto = defaultCrypto
		}
		destination := strings.TrimSpace(anyString(firstAny(row, "destination", "dest", "dest_addr")))
		if destination == "" {
			destination = defaultDestination
		}
		status := strings.TrimSpace(anyString(firstAny(row, "status")))
		if status == "" {
			status = PayoutInProgress
		}
		kind := strings.TrimSpace(anyString(firstAny(row, "kind", "type")))
		if kind == "" {
			kind = "payout"
		}
		var amount decimal.Decimal
		if parsed, ok := decimalFromAny(firstAny(row, "amount")); ok {
			amount = parsed
		}
		detail := PayoutTx{
			TxID:       strings.TrimSpace(anyString(firstAny(row, "txid", "tx_id"))),
			Status:     status,
			Kind:       kind,
			SourceAddr: strings.TrimSpace(anyString(firstAny(row, "source", "source_addr", "from"))),
			DestAddr:   destination,
			Amount:     amount,
			Crypto:     crypto,
			Error:      strings.TrimSpace(anyString(firstAny(row, "error", "message"))),
		}
		if detail.TxID == "" && detail.Error == "" {
			continue
		}
		out = append(out, detail)
	}
	return out
}

func payoutTxDetailsFromTxIDs(txids []string, crypto string, destination string) []PayoutTx {
	out := make([]PayoutTx, 0, len(txids))
	for _, txid := range uniqueNonEmptyStrings(txids) {
		out = append(out, PayoutTx{TxID: txid, Status: PayoutInProgress, Kind: "payout", Crypto: crypto, DestAddr: destination})
	}
	return out
}

func stringSliceFromAny(v any) []string {
	items := anySliceFromAny(v)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, strings.TrimSpace(anyString(item)))
	}
	return out
}

func anySliceFromAny(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	case []string:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, item)
		}
		return out
	default:
		return []any{x}
	}
}

func payoutResultErrorText(res map[string]any) string {
	for _, candidate := range []any{res["message"], res["error"]} {
		if text := strings.TrimSpace(anyString(candidate)); text != "" {
			return text
		}
	}
	if result, ok := res["result"].(map[string]any); ok {
		if text := strings.TrimSpace(anyString(firstAny(result, "error", "message"))); text != "" {
			return text
		}
	}
	return "payout failed"
}

func payoutFeeFromResult(res map[string]any) (decimal.Decimal, string, bool) {
	return payoutFeeFromAny(res)
}

func payoutFeeFromAny(v any) (decimal.Decimal, string, bool) {
	switch x := v.(type) {
	case map[string]any:
		fee, ok := decimalFromAny(firstAny(x, "actual_fee", "network_fee"))
		if ok && fee.GreaterThanOrEqual(decimal.Zero) {
			asset := strings.TrimSpace(anyString(firstAny(x, "fee_asset", "network_fee_asset")))
			if asset != "" {
				return fee, asset, true
			}
		}
		if result, ok := x["result"]; ok {
			if fee, asset, ok := payoutFeeFromAny(result); ok {
				return fee, asset, true
			}
		}
	case []any:
		total := decimal.Zero
		asset := ""
		for _, item := range x {
			itemFee, itemAsset, ok := payoutFeeFromAny(item)
			if !ok {
				continue
			}
			if asset == "" {
				asset = itemAsset
			}
			if strings.EqualFold(asset, itemAsset) {
				total = total.Add(itemFee)
			}
		}
		if asset != "" {
			return total, asset, true
		}
	}
	return decimal.Zero, "", false
}

func payoutTxIDsByDestination(v any) map[string][][]string {
	queues := map[string][][]string{}
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			dest := anyString(firstAny(row, "dest", "destination"))
			txids := txIDsFromAny(row["txids"])
			if dest != "" && len(txids) > 0 {
				queues[dest] = append(queues[dest], txids)
			}
		}
	case map[string]any:
		if results, ok := x["results"]; ok {
			return payoutTxIDsByDestination(results)
		}
		dest := anyString(firstAny(x, "dest", "destination"))
		txids := txIDsFromAny(x["txids"])
		if dest != "" && len(txids) > 0 {
			queues[dest] = append(queues[dest], txids)
		}
	}
	return queues
}

func popPayoutTxIDs(queues map[string][][]string, destination string) []string {
	key := destination
	rows := queues[key]
	if len(rows) == 0 {
		for candidate, candidateRows := range queues {
			if strings.EqualFold(candidate, destination) {
				key = candidate
				rows = candidateRows
				break
			}
		}
	}
	if len(rows) == 0 {
		return nil
	}
	txids := rows[0]
	if len(rows) == 1 {
		delete(queues, key)
	} else {
		queues[key] = rows[1:]
	}
	return txids
}
