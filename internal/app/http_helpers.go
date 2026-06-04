package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

type HTTPHandler struct {
	cfg    Config
	store  *Store
	crypto *CryptoRegistry
	rates  *RateService
	auth   *AuthManager
	logger *slog.Logger
}

func NewHTTPHandler(cfg Config, store *Store, crypto *CryptoRegistry, rates *RateService, auth *AuthManager, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{cfg: cfg, store: store, crypto: crypto, rates: rates, auth: auth, logger: logger}
}

func (h *HTTPHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(h.auth.WithUser)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/wallets", http.StatusFound) })
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	r.Get("/readyz", h.readyz)
	r.Get("/metrics", h.metrics)
	r.Get("/login", h.loginGet)
	r.Post("/login", h.loginPost)
	r.Get("/set-password", h.setPasswordGet)
	r.Post("/set-password", h.setPasswordPost)
	r.Get("/2fa/verify", h.twoFactorVerifyGet)
	r.Post("/2fa/verify", h.twoFactorVerifyPost)
	r.Get("/logout", h.logout)

	r.Group(func(r chi.Router) {
		r.Use(h.auth.RequireLogin)
		r.Get("/wallets", h.wallets)
		r.Get("/wallet/{crypto}", h.walletManagePage)
		r.Get("/payout/{crypto}", h.payoutPage)
		r.Get("/{crypto}/get-rate", h.sourceRate)
		r.Get("/{crypto}/get-rate/{fiat}", h.sourceRate)
		r.Get("/rates", h.ratesPage)
		r.Get("/rates/{fiat}", h.ratesPage)
		r.Post("/rates", h.ratesPost)
		r.Post("/rates/{fiat}", h.ratesPost)
		r.Get("/transactions", h.transactionsPage)
		r.Get("/parts/transactions", h.transactionsPart)
		r.Get("/payouts", h.payoutsPage)
		r.Get("/parts/payouts", h.payoutsPart)
		r.Get("/settings", h.settingsGet)
		r.Post("/settings/account", h.settingsAccountPost)
		r.Post("/settings/locale", h.settingsLocalePost)
		r.Get("/unlock", h.unlockGet)
		r.Post("/unlock", h.unlockPost)
		r.Get("/parts/tron-multiserver", h.tronMultiserverPart)
		r.Post("/parts/tron-multiserver", h.tronMultiserverPart)
		r.Get("/configure/tron", h.tronConfigurePage)
		r.Post("/configure/tron", h.tronConfigurePage)
		r.Get("/parts/tron-staking-stake", h.tronStakingStakeGet)
		r.Post("/parts/tron-staking-stake", h.tronStakingStakePost)
		r.Get("/2fa/setup", h.twoFactorSetupGet)
		r.Post("/2fa/setup", h.twoFactorSetupPost)
		r.Get("/2fa/disable", h.twoFactorDisableGet)
		r.Post("/2fa/disable", h.twoFactorDisablePost)
		r.Get("/2fa/regenerate-backup", h.twoFactorRegenerateBackupGet)
		r.Post("/2fa/regenerate-backup", h.twoFactorRegenerateBackupPost)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/crypto", h.apiListCrypto)
		r.Route("/", func(r chi.Router) {
			r.With(h.auth.RequireAPIKey).Get("/crypto/balances", h.apiBalances)
			r.With(h.auth.RequireAPIKey).Get("/transactions", h.apiTransactions)
			r.With(h.auth.RequireAPIKey).Get("/transactions/{crypto}/{addr}", h.apiTransactions)
			r.With(h.auth.RequireAPIKey).Get("/invoices", h.apiInvoices)
			r.With(h.auth.RequireAPIKey).Get("/invoices/{external_id}", h.apiInvoices)
			r.With(h.auth.RequireAPIKey).Get("/orders", h.apiOrders)
			r.With(h.auth.RequireAPIKey).Get("/orders/{external_id}", h.apiOrder)
			r.With(h.auth.RequireAPIKey).Get("/tx-info/{txid}/{external_id}", h.apiTxInfo)
			r.With(h.auth.RequireAPIKey).Post("/test-callback-receiver", h.apiTestCallbackReceiver)
			r.With(h.auth.RequireAPIKey).Post("/{crypto}/payment_request", h.apiPaymentRequest)
			r.With(h.auth.RequireAPIKey).Post("/{crypto}/quote", h.apiQuote)
			r.With(h.auth.RequireAPIKey).Post("/{crypto}/verified-transaction", h.apiVerifiedTransaction)
			r.With(h.auth.RequireAPIKey).Get("/{crypto}/balance", h.apiBalance)
			r.With(h.auth.RequireAPIKey).Get("/{crypto}/status", h.apiStatus)
			r.With(h.auth.RequireAPIKey).Get("/{crypto}/addresses", h.apiAddresses)
			r.With(h.auth.RequireAPIKey).Get("/{crypto}/fee-deposit-address", h.apiFeeDepositAddress)
			r.With(h.auth.RequireAPIKey).Get("/{crypto}/payout/status", h.apiPayoutStatus)
			r.With(h.auth.RequireAPIKey).Get("/{crypto}/payouts", h.apiPayouts)
			r.With(h.auth.RequireAPIKey).Post("/decryption-key", h.apiDecryptionKey)

			r.With(h.auth.RequireAdminOrBasic).Get("/{crypto}/generate-address", h.apiGenerateAddress)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/transaction", h.apiAddTransaction)
			r.Get("/{crypto}/decrypt", h.apiDecryptStatus)
			r.With(h.auth.RequireAdminOrBasic).Get("/{crypto}/server", h.apiServerDetails)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/server/key", h.apiServerKey)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/server/host", h.apiServerHost)
			r.With(h.auth.RequireAdminOrBasic).Get("/{crypto}/backup", h.apiBackup)
			r.With(h.auth.RequireAdminOrBasic).Get("/{crypto}/payment-gateway", h.apiPaymentGatewayGet)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/payment-gateway", h.apiPaymentGatewaySet)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/payment-gateway/token", h.apiPaymentGatewayToken)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/payout_destinations", h.apiPayoutDestinations)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/autopayout", h.apiAutopayout)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/exchange-rate", h.apiExchangeRate)
			r.With(h.auth.RequireAdminOrBasic).Get("/{crypto}/estimate-tx-fee/{amount}", h.apiEstimateTxFee)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/payout", h.apiPayout)
			r.With(h.auth.RequireAdminOrBasic).Post("/{crypto}/multipayout", h.apiMultiPayout)
			r.With(h.auth.RequireAdminOrBasic).Get("/{crypto}/task/{id}", h.apiTask)
			r.With(h.auth.RequireAdminOrBasic).Patch("/admin/account", h.apiAdminAccount)

			r.Post("/walletnotify/{crypto}/{txid}", h.apiWalletNotify)
			r.Post("/payoutnotify/{crypto}", h.apiPayoutNotify)
		})
	})

	return r
}

func (h *HTTPHandler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.DB().PingContext(ctx); err != nil {
		errorJSON(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func readJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	return dec.Decode(out)
}

func errorJSON(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"status": "error", "message": err.Error()})
}

func intQuery(r *http.Request, name string, fallback int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return v
}

func boolFromAny(v any, fallback bool) bool {
	switch x := v.(type) {
	case nil:
		return fallback
	case bool:
		return x
	case string:
		x = strings.ToLower(strings.TrimSpace(x))
		return x == "1" || x == "true" || x == "yes" || x == "on"
	default:
		return fallback
	}
}

func decimalString(v decimal.Decimal) string {
	return v.String()
}

func page(w http.ResponseWriter, title string, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Arial,sans-serif;background:#f7f8fa;color:#1d1d1f}
.shell{max-width:1080px;margin:0 auto;padding:32px 20px}
.top{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px}
.nav a{margin-left:16px;color:#2563eb;text-decoration:none}
.panel{background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:22px;box-shadow:0 1px 2px rgba(0,0,0,.04)}
label{display:block;font-size:13px;color:#4b5563;margin:14px 0 6px}
input,select{width:100%%;box-sizing:border-box;border:1px solid #d1d5db;border-radius:6px;padding:10px 12px;font-size:14px;background:#fff}
button{border:0;border-radius:6px;background:#111827;color:#fff;padding:10px 16px;font-size:14px;cursor:pointer}
button.secondary{background:#2563eb}
table{width:100%%;border-collapse:collapse}
th,td{text-align:left;border-bottom:1px solid #e5e7eb;padding:10px;font-size:14px}
.muted{color:#6b7280}.err{color:#b91c1c}.ok{color:#047857}
</style>
</head>
<body><main class="shell">%s</main></body></html>`, html.EscapeString(title), body)
}

func flash(r *http.Request) string {
	if msg := r.URL.Query().Get("msg"); msg != "" {
		return `<p class="ok">` + html.EscapeString(msg) + `</p>`
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		return `<p class="err">` + html.EscapeString(msg) + `</p>`
	}
	return ""
}

func invoiceJSON(i Invoice, txs []Transaction, utxs []UnconfirmedTransaction) map[string]any {
	txList := make([]map[string]any, 0, len(txs)+len(utxs))
	for _, tx := range txs {
		txList = append(txList, transactionJSON(tx))
	}
	for _, tx := range utxs {
		txList = append(txList, map[string]any{
			"amount": tx.AmountCrypto.String(),
			"crypto": tx.Crypto,
			"addr":   tx.Addr,
			"txid":   tx.TxID,
			"status": "UNCONFIRMED",
		})
	}
	return map[string]any{
		"id":             i.ID,
		"txs":            txList,
		"external_id":    i.ExternalID,
		"crypto":         i.Crypto,
		"addr":           i.Addr,
		"balance_fiat":   i.BalanceFiat.String(),
		"balance_crypto": i.BalanceCrypto.String(),
		"fiat":           i.Fiat,
		"amount_fiat":    i.AmountFiat.String(),
		"amount_crypto":  i.AmountCrypto.String(),
		"exchange_rate":  i.ExchangeRate.String(),
		"status":         i.Status,
		"callback_url":   i.CallbackURL,
		"created_at":     i.CreatedAt.Format(time.RFC3339),
		"updated_at":     i.UpdatedAt.Format(time.RFC3339),
	}
}

func transactionJSON(tx Transaction) map[string]any {
	return map[string]any{
		"id":                      tx.ID,
		"amount":                  tx.AmountCrypto.String(),
		"amount_crypto":           tx.AmountCrypto.String(),
		"amount_fiat":             tx.AmountFiat.String(),
		"crypto":                  tx.Crypto,
		"addr":                    tx.Addr,
		"txid":                    tx.TxID,
		"status":                  "CONFIRMED",
		"need_more_confirmations": tx.NeedMoreConfirmations,
		"callback_confirmed":      tx.CallbackConfirmed,
		"created_at":              tx.CreatedAt.Format(time.RFC3339),
	}
}

func orderJSON(record OrderRecord) map[string]any {
	invoices := make([]map[string]any, 0, len(record.Invoices))
	for _, detail := range record.Invoices {
		addresses := make([]map[string]any, 0, len(detail.Addresses))
		for _, a := range detail.Addresses {
			addresses = append(addresses, map[string]any{"crypto": a.Crypto, "addr": a.Addr, "created_at": a.CreatedAt.Format(time.RFC3339)})
		}
		row := invoiceJSON(detail.Invoice, detail.Transactions, detail.UnconfirmedTXs)
		row["addresses"] = addresses
		invoices = append(invoices, row)
	}
	payouts := make([]map[string]any, 0, len(record.Payouts))
	for _, payout := range record.Payouts {
		txids := make([]string, 0, len(payout.Transactions))
		for _, tx := range payout.Transactions {
			txids = append(txids, tx.TxID)
		}
		payouts = append(payouts, map[string]any{
			"id":           payout.ID,
			"external_id":  nullStringValue(payout.ExternalID),
			"crypto":       payout.Crypto,
			"amount":       payout.Amount.String(),
			"destination":  payout.DestAddr,
			"status":       payout.Status,
			"task_id":      nullStringValue(payout.TaskID),
			"txids":        txids,
			"callback_url": nullStringValue(payout.CallbackURL),
			"created_at":   payout.CreatedAt.Format(time.RFC3339),
			"updated_at":   payout.UpdatedAt.Format(time.RFC3339),
		})
	}
	return map[string]any{"external_id": record.ExternalID, "invoices": invoices, "payouts": payouts}
}
