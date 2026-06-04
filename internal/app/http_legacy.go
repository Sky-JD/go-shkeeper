package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

func (h *HTTPHandler) apiGenerateAddress(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	amount := decimal.Zero
	if raw := strings.TrimSpace(r.URL.Query().Get("amount")); raw != "" {
		amount, err = decimal.NewFromString(raw)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err)
			return
		}
	}
	addr, err := h.crypto.MakeAddress(r.Context(), module, amount)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "addr": addr})
}

func (h *HTTPHandler) apiAddTransaction(w http.ResponseWriter, r *http.Request) {
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
	confirmations := intFromAny(firstAny(req, "confirmations"))
	if confirmations == 0 {
		wallet, _ := h.store.WalletByCrypto(r.Context(), module.Name)
		confirmations = wallet.Confirmations
	}
	tx, invoice, duplicate, err := h.recordConfirmedTransaction(r, module, txid, addr, amount, confirmations, anyString(firstAny(req, "external_id")))
	if err != nil {
		errorJSON(w, statusForRecordErr(err), err)
		return
	}
	if !tx.NeedMoreConfirmations {
		_ = h.store.MarkTransactionCallbackConfirmed(r.Context(), tx.ID)
	}
	detail, _ := h.store.InvoiceDetail(r.Context(), invoice)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "success",
		"id":        tx.ID,
		"duplicate": duplicate,
		"invoice":   invoiceJSON(detail.Invoice, detail.Transactions, detail.UnconfirmedTXs),
	})
}

func (h *HTTPHandler) apiDecryptStatus(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "Ignoring notification for unavailable crypto"})
		return
	}
	if !h.validBackendKey(r, module) {
		errorJSON(w, http.StatusForbidden, errors.New("Wrong backend key"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"persistent_status": h.walletEncryptionPersistentStatus(r),
		"runtime_status":    h.walletEncryptionRuntimeStatus(r),
		"key":               "",
	})
}

func (h *HTTPHandler) apiServerDetails(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, h.crypto.ServerDetails(r.Context(), module))
}

func (h *HTTPHandler) apiServerKey(w http.ResponseWriter, r *http.Request) {
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
	key := serverKeyFromRequest(req)
	if key == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("key or username/password is required"))
		return
	}
	if err := h.store.UpdateWalletServerKey(r.Context(), module.Name, key); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	user, pass := parseServerAuthKey(key, module)
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "key": user + ":" + pass})
}

func (h *HTTPHandler) apiServerHost(w http.ResponseWriter, r *http.Request) {
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
	host, err := normalizeServerHost(anyString(firstAny(req, "host", "server", "url")))
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.UpdateWalletServerHost(r.Context(), module.Name, host); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "host": host})
}

func (h *HTTPHandler) apiBackup(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	data, contentType, err := h.crypto.Backup(r.Context(), module)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+module.Name+`-backup.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *HTTPHandler) apiTestCallbackReceiver(w http.ResponseWriter, r *http.Request) {
	var payload any
	if err := readJSON(r, &payload); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	h.logger.Info("test callback received", "payload", payload)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "success", "message": "callback logged"})
}

func (h *HTTPHandler) walletEncryptionPersistentStatus(r *http.Request) string {
	if raw, err := h.store.Setting(r.Context(), "WalletEncryptionPersistentStatus"); err == nil {
		return walletEncryptionPersistentStatusName(raw)
	}
	if hash, err := h.store.Setting(r.Context(), "WalletEncryptionPasswordHash"); err == nil && strings.TrimSpace(hash) != "" {
		return "enabled"
	}
	return "disabled"
}

func (h *HTTPHandler) walletEncryptionRuntimeStatus(r *http.Request) string {
	raw, err := h.store.Setting(r.Context(), "WalletEncryptionRuntimeStatus")
	if errors.Is(err, sql.ErrNoRows) {
		return "pending"
	}
	if err != nil {
		return "pending"
	}
	return walletEncryptionRuntimeStatusName(raw)
}

func walletEncryptionPersistentStatusName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "pending":
		return "pending"
	case "2", "disabled":
		return "disabled"
	case "3", "enabled":
		return "enabled"
	default:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return walletEncryptionPersistentStatusName(strconv.Itoa(parsed))
		}
		return "pending"
	}
}

func walletEncryptionRuntimeStatusName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "pending":
		return "pending"
	case "2", "fail":
		return "fail"
	case "3", "success":
		return "success"
	default:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return walletEncryptionRuntimeStatusName(strconv.Itoa(parsed))
		}
		return "pending"
	}
}

func serverKeyFromRequest(req map[string]any) string {
	key := strings.TrimSpace(anyString(firstAny(req, "key", "server_key", "serverkey")))
	if key != "" {
		return key
	}
	username := strings.TrimSpace(anyString(firstAny(req, "username", "user")))
	password := strings.TrimSpace(anyString(firstAny(req, "password", "pass")))
	if username == "" || password == "" {
		return ""
	}
	return username + ":" + password
}

func normalizeServerHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("host is required")
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid host: %s", value)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			return "", fmt.Errorf("host must not include a path: %s", value)
		}
		value = parsed.Host
	}
	if strings.ContainsAny(value, "/?#") {
		return "", fmt.Errorf("host must be host[:port], got: %s", value)
	}
	return value, nil
}
