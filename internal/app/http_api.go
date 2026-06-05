package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

func (h *HTTPHandler) apiListCrypto(w http.ResponseWriter, r *http.Request) {
	data, err := h.crypto.AvailableCryptos(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "success",
		"crypto":      data["filtered"],
		"crypto_list": data["crypto_list"],
	})
}

func (h *HTTPHandler) apiBalances(w http.ResponseWriter, r *http.Request) {
	data, err := h.crypto.AvailableCryptos(r.Context())
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	names, _ := data["filtered"].([]string)
	if includes := r.URL.Query().Get("includes"); includes != "" {
		allowed := map[string]bool{}
		for _, name := range names {
			allowed[name] = true
		}
		names = nil
		for _, part := range strings.Split(includes, ",") {
			name := strings.ToUpper(strings.TrimSpace(part))
			if allowed[name] {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		errorJSON(w, http.StatusBadRequest, errors.New("No valid cryptos requested"))
		return
	}

	type row struct {
		idx int
		val map[string]any
	}
	jobs := make(chan row)
	results := make(chan row, len(names))
	workers := h.cfg.BalanceWorkers
	if workers <= 0 {
		workers = 4
	}
	if workers > len(names) {
		workers = len(names)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				module, ok := h.crypto.Module(names[job.idx])
				if !ok {
					continue
				}
				serverStatus := h.crypto.Status(r.Context(), module)
				balance, source, balanceErr := h.crypto.Balance(r.Context(), module)
				rate, err := h.store.ExchangeRate(r.Context(), "USD", module.Name)
				currentRate := decimal.Zero
				if err == nil {
					currentRate, err = h.rates.CurrentRate(r.Context(), rate)
				}
				val := map[string]any{
					"name":           module.Name,
					"display_name":   module.DisplayName,
					"amount_crypto":  balance.String(),
					"rate":           currentRate.String(),
					"fiat":           "USD",
					"amount_fiat":    balance.Mul(currentRate).String(),
					"server_status":  serverStatus,
					"balance_source": source,
					"balance_error":  balanceErr,
				}
				if err != nil {
					val["rate_error"] = err.Error()
				}
				results <- row{idx: job.idx, val: val}
			}
		}()
	}
	for i := range names {
		jobs <- row{idx: i}
	}
	close(jobs)
	wg.Wait()
	close(results)
	ordered := make([]map[string]any, len(names))
	for res := range results {
		ordered[res.idx] = res.val
	}
	out := make([]map[string]any, 0, len(ordered))
	for _, item := range ordered {
		if item != nil {
			out = append(out, item)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) apiBalance(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	serverStatus := h.crypto.Status(r.Context(), module)
	balance, source, balanceErr := h.crypto.Balance(r.Context(), module)
	rate, rateErr := h.store.ExchangeRate(r.Context(), "USD", module.Name)
	currentRate := decimal.Zero
	if rateErr == nil {
		currentRate, rateErr = h.rates.CurrentRate(r.Context(), rate)
	}
	resp := map[string]any{
		"name":           module.Name,
		"display_name":   module.DisplayName,
		"amount_crypto":  balance.String(),
		"rate":           currentRate.String(),
		"fiat":           "USD",
		"amount_fiat":    balance.Mul(currentRate).String(),
		"server_status":  serverStatus,
		"balance_source": source,
		"balance_error":  balanceErr,
	}
	if rateErr != nil {
		resp["rate_error"] = rateErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *HTTPHandler) apiAddresses(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	addresses, err := h.crypto.AllAddresses(r.Context(), module)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "addresses": addresses})
}

func (h *HTTPHandler) apiQuote(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	var req struct {
		Fiat   string `json:"fiat"`
		Amount string `json:"amount"`
	}
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	if req.Fiat == "" || req.Amount == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("'fiat' and 'amount' are required fields"))
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	cryptoAmount, exchangeRate, err := h.rates.Convert(r.Context(), amount, strings.ToUpper(req.Fiat), module)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "success",
		"fiat":          strings.ToUpper(req.Fiat),
		"amount_fiat":   amount.String(),
		"crypto":        module.Name,
		"amount_crypto": cryptoAmount.String(),
		"exchange_rate": exchangeRate.String(),
	})
}

func (h *HTTPHandler) apiPaymentRequest(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	wallet, err := h.store.WalletByCrypto(r.Context(), module.Name)
	if err != nil || !wallet.Enabled {
		errorJSON(w, http.StatusServiceUnavailable, fmt.Errorf("%s payment gateway is unavailable", module.Name))
		return
	}
	if h.cfg.DisableCryptoWhenLags && h.crypto.Status(r.Context(), module) != "Synced" {
		errorJSON(w, http.StatusServiceUnavailable, fmt.Errorf("%s payment gateway is unavailable because of lagging", module.Name))
		return
	}
	var req struct {
		ExternalID  string `json:"external_id"`
		Fiat        string `json:"fiat"`
		Amount      string `json:"amount"`
		CallbackURL string `json:"callback_url"`
	}
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	if req.ExternalID == "" || req.Fiat == "" || req.Amount == "" || req.CallbackURL == "" {
		errorJSON(w, http.StatusBadRequest, errors.New("external_id, fiat, amount and callback_url are required"))
		return
	}
	amountFiat, err := decimal.NewFromString(req.Amount)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	amountCrypto, exchangeRate, err := h.rates.Convert(r.Context(), amountFiat, strings.ToUpper(req.Fiat), module)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	invoice, err := h.upsertInvoice(r, module, req.ExternalID, strings.ToUpper(req.Fiat), req.CallbackURL, amountFiat, amountCrypto, exchangeRate)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "success",
		"id":                invoice.ID,
		"exchange_rate":     invoice.ExchangeRate.String(),
		"amount":            invoice.AmountCrypto.String(),
		"wallet":            invoice.Addr,
		"recalculate_after": wallet.Recalc,
		"display_name":      module.DisplayName,
	})
}

func (h *HTTPHandler) upsertInvoice(r *http.Request, module *CryptoModule, externalID, fiat, callbackURL string, amountFiat, amountCrypto, exchangeRate decimal.Decimal) (Invoice, error) {
	ctx := r.Context()
	invoice, err := h.store.FindInvoiceByExternalCallbackFiat(ctx, externalID, callbackURL, fiat)
	if errors.Is(err, sql.ErrNoRows) {
		addr, err := h.crypto.MakeAddress(ctx, module, amountCrypto)
		if err != nil {
			return Invoice{}, err
		}
		invoice = Invoice{
			Crypto:        module.Name,
			Addr:          addr,
			ExternalID:    externalID,
			Fiat:          fiat,
			CallbackURL:   callbackURL,
			BalanceFiat:   decimal.Zero,
			BalanceCrypto: decimal.Zero,
			AmountFiat:    amountFiat,
			AmountCrypto:  amountCrypto,
			ExchangeRate:  exchangeRate,
			Status:        InvoiceUnpaid,
		}
		if err := h.store.CreateInvoice(ctx, &invoice); err != nil {
			return Invoice{}, err
		}
		if err := h.store.AddInvoiceAddress(ctx, invoice.ID, module.Name, addr); err != nil {
			return Invoice{}, err
		}
		return invoice, nil
	}
	if err != nil {
		return Invoice{}, err
	}

	invoice.Fiat = fiat
	invoice.AmountFiat = amountFiat
	invoice.AmountCrypto = amountCrypto
	invoice.ExchangeRate = exchangeRate
	cryptoChanged := invoice.Crypto != module.Name
	if cryptoChanged || module.Name == "BTC-LIGHTNING" {
		invoice.Crypto = module.Name
		addrRow, err := h.store.FindInvoiceAddress(ctx, invoice.ID, module.Name)
		if err == nil && module.Name != "BTC-LIGHTNING" {
			invoice.Addr = addrRow.Addr
		} else {
			addr, err := h.crypto.MakeAddress(ctx, module, amountCrypto)
			if err != nil {
				return Invoice{}, err
			}
			invoice.Addr = addr
			if err := h.store.AddInvoiceAddress(ctx, invoice.ID, module.Name, addr); err != nil {
				return Invoice{}, err
			}
		}
	}
	if err := h.store.UpdateInvoicePayment(ctx, invoice); err != nil {
		return Invoice{}, err
	}
	return invoice, nil
}

func (h *HTTPHandler) apiInvoices(w http.ResponseWriter, r *http.Request) {
	externalID := chi.URLParam(r, "external_id")
	filter := OrderFilter{
		Status:   strings.ToUpper(r.URL.Query().Get("status")),
		Crypto:   strings.ToUpper(r.URL.Query().Get("crypto")),
		FromDate: r.URL.Query().Get("from_date"),
		ToDate:   r.URL.Query().Get("to_date"),
		Limit:    intQuery(r, "limit", 50),
		Offset:   intQuery(r, "offset", 0),
	}
	details, err := h.store.ListInvoices(r.Context(), externalID, filter)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(details))
	for _, detail := range details {
		out = append(out, invoiceJSON(detail.Invoice, detail.Transactions, detail.UnconfirmedTXs))
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "invoices": out})
}

func (h *HTTPHandler) apiOrder(w http.ResponseWriter, r *http.Request) {
	externalID := chi.URLParam(r, "external_id")
	record, err := h.store.GetOrder(r.Context(), externalID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "order": orderJSON(record)})
}

func (h *HTTPHandler) apiOrders(w http.ResponseWriter, r *http.Request) {
	filter := OrderFilter{
		ExternalID: r.URL.Query().Get("external_id"),
		Status:     strings.ToUpper(r.URL.Query().Get("status")),
		Crypto:     strings.ToUpper(r.URL.Query().Get("crypto")),
		FromDate:   r.URL.Query().Get("from_date"),
		ToDate:     r.URL.Query().Get("to_date"),
		Limit:      intQuery(r, "limit", 50),
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

func (h *HTTPHandler) apiTransactions(w http.ResponseWriter, r *http.Request) {
	crypto := chi.URLParam(r, "crypto")
	addr := chi.URLParam(r, "addr")
	txs, err := h.store.ListTransactions(r.Context(), crypto, addr)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	utxs, err := h.store.ListUnconfirmedTransactions(r.Context(), crypto, addr)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(txs)+len(utxs))
	for _, tx := range txs {
		out = append(out, transactionJSON(tx))
	}
	for _, tx := range utxs {
		out = append(out, map[string]any{"amount": tx.AmountCrypto.String(), "crypto": tx.Crypto, "addr": tx.Addr, "txid": tx.TxID, "status": "UNCONFIRMED"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "transactions": out})
}

func (h *HTTPHandler) apiTxInfo(w http.ResponseWriter, r *http.Request) {
	info, err := h.store.TxInfo(r.Context(), chi.URLParam(r, "txid"), chi.URLParam(r, "external_id"))
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "info": info})
}

func (h *HTTPHandler) moduleFromRoute(r *http.Request) (*CryptoModule, error) {
	name := strings.ToUpper(chi.URLParam(r, "crypto"))
	module, ok := h.crypto.Module(name)
	if !ok {
		return nil, fmt.Errorf("%s payment gateway is unavailable", name)
	}
	return module, nil
}
