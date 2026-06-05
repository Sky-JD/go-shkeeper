package app

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func (h *HTTPHandler) apiStatus(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	serverStatus := h.crypto.Status(r.Context(), module)
	balance, source, balanceErr := h.crypto.Balance(r.Context(), module)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":           module.Name,
		"amount":         balance.String(),
		"server":         serverStatus,
		"balance_source": source,
		"balance_error":  balanceErr,
	})
}

func (h *HTTPHandler) apiFeeDepositAddress(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	address, err := h.crypto.FeeDepositAddress(r.Context(), module)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "crypto": module.Name, "fee_deposit_address": address})
}

func (h *HTTPHandler) apiTask(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	id := strings.TrimSpace(chiParam(r, "id"))
	if id == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("task id is required"))
		return
	}
	task, err := h.crypto.Task(r.Context(), module, id)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *HTTPHandler) apiDecryptionKey(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" && strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "json") {
		var req struct {
			Key string `json:"key"`
		}
		if err := readJSON(r, &req); err != nil {
			errorJSON(w, http.StatusBadRequest, err)
			return
		}
		key = strings.TrimSpace(req.Key)
	}
	if key == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("Decryption key is required"))
		return
	}
	hash, err := h.store.Setting(r.Context(), "WalletEncryptionPasswordHash")
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(hash) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "Decryption is not needed"})
		return
	}
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(key)) != nil {
		_ = h.store.UpsertSetting(r.Context(), "WalletEncryptionRuntimeStatus", "2")
		errorJSON(w, http.StatusBadRequest, errors.New("Invalid decryption key"))
		return
	}
	if err := h.store.UpsertSetting(r.Context(), "WalletEncryptionRuntimeStatus", "3"); err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) metrics(w http.ResponseWriter, r *http.Request) {
	if !h.acceptsMetricsAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
		errorJSON(w, http.StatusUnauthorized, errors.New("metrics auth required"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	now := time.Now().Unix()
	_, _ = fmt.Fprintf(w, "# HELP go_shkeeper_build_info Go SHKeeper build info.\n# TYPE go_shkeeper_build_info gauge\ngo_shkeeper_build_info 1\n")
	_, _ = fmt.Fprintf(w, "# HELP go_shkeeper_unix_time Current unix time.\n# TYPE go_shkeeper_unix_time gauge\ngo_shkeeper_unix_time %d\n", now)
	_, _ = fmt.Fprintf(w, "# HELP go_shkeeper_wallets_total Wallet rows by enabled state.\n# TYPE go_shkeeper_wallets_total gauge\n")
	for _, row := range h.walletMetricRows(r) {
		_, _ = fmt.Fprintf(w, "go_shkeeper_wallets_total{enabled=\"%s\"} %d\n", row.enabled, row.count)
	}
	_, _ = fmt.Fprintf(w, "# HELP go_shkeeper_orders_total Invoice rows by status.\n# TYPE go_shkeeper_orders_total gauge\n")
	for _, row := range h.orderMetricRows(r) {
		_, _ = fmt.Fprintf(w, "go_shkeeper_orders_total{status=\"%s\"} %d\n", metricLabel(row.status), row.count)
	}
}

func (h *HTTPHandler) acceptsMetricsAuth(r *http.Request) bool {
	wantUser := env("METRICS_USERNAME", "shkeeper")
	wantPass := env("METRICS_PASSWORD", "shkeeper")
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(wantPass)) == 1
}

type walletMetricRow struct {
	enabled string
	count   int64
}

func (h *HTTPHandler) walletMetricRows(r *http.Request) []walletMetricRow {
	rows, err := h.store.DB().QueryContext(r.Context(), fmt.Sprintf("SELECT COALESCE(enabled, 0), COUNT(*) FROM %s GROUP BY COALESCE(enabled, 0)", h.store.table("wallet")))
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]walletMetricRow, 0)
	for rows.Next() {
		var enabled int
		var count int64
		if err := rows.Scan(&enabled, &count); err == nil {
			out = append(out, walletMetricRow{enabled: strconv.Itoa(enabled), count: count})
		}
	}
	return out
}

type orderMetricRow struct {
	status string
	count  int64
}

func (h *HTTPHandler) orderMetricRows(r *http.Request) []orderMetricRow {
	rows, err := h.store.DB().QueryContext(r.Context(), fmt.Sprintf("SELECT COALESCE(status, 'UNPAID'), COUNT(*) FROM %s GROUP BY COALESCE(status, 'UNPAID')", h.store.table("invoice")))
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]orderMetricRow, 0)
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err == nil {
			out = append(out, orderMetricRow{status: status, count: count})
		}
	}
	return out
}

func metricLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", "")
	return value
}

func chiParam(r *http.Request, name string) string {
	return strings.TrimSpace(chi.URLParam(r, name))
}
