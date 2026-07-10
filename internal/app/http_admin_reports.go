package app

import (
	"encoding/csv"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
)

func (h *HTTPHandler) sourceRate(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	fiat := strings.ToUpper(chi.URLParam(r, "fiat"))
	if fiat == "" {
		fiat = "USD"
	}
	rate, err := h.store.ExchangeRate(r.Context(), fiat, module.Name)
	if err != nil {
		errorJSON(w, http.StatusNotFound, err)
		return
	}
	if source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source"))); source != "" {
		switch source {
		case "manual", "dynamic", "binance", "coinbase", "kraken", "kucoin":
			rate.Source = source
		default:
			errorJSON(w, http.StatusBadRequest, errUnknownRateSource(source))
			return
		}
	}
	currentRate, err := h.rates.CurrentRate(r.Context(), rate)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{module.Name: currentRate.String()})
}

func (h *HTTPHandler) ratesPage(w http.ResponseWriter, r *http.Request) {
	fiat := strings.ToUpper(chi.URLParam(r, "fiat"))
	if fiat == "" {
		fiat = "USD"
	}
	rates, err := h.store.ListExchangeRates(r.Context(), fiat)
	if err != nil {
		http.Redirect(w, r, "/wallets?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	var b strings.Builder
	b.WriteString(`<div class="top"><h1>汇率</h1><nav class="nav"><a href="/wallets">钱包</a><a href="/transactions">交易</a><a href="/payouts">出款</a><a href="/settings">设置</a></nav></div><section class="panel">`)
	b.WriteString(flash(r))
	b.WriteString(`<form method="post" action="/rates/` + html.EscapeString(fiat) + `"><table><thead><tr><th>币种</th><th>来源</th><th>手动汇率</th><th>百分比手续费</th><th>固定手续费</th><th>手续费策略</th></tr></thead><tbody>`)
	for _, rate := range rates {
		prefix := "rates__" + html.EscapeString(rate.Crypto) + "__"
		b.WriteString(`<tr><td>` + html.EscapeString(rate.Crypto) + `</td>`)
		b.WriteString(`<td><select name="` + prefix + `source">`)
		for _, source := range []string{"manual", "dynamic", "binance", "coinbase", "kraken", "kucoin"} {
			selected := ""
			if strings.EqualFold(rate.Source, source) {
				selected = ` selected`
			}
			b.WriteString(`<option value="` + source + `"` + selected + `>` + source + `</option>`)
		}
		b.WriteString(`</select></td>`)
		b.WriteString(`<td><input name="` + prefix + `rate" value="` + html.EscapeString(rate.Rate.String()) + `"></td>`)
		b.WriteString(`<td><input name="` + prefix + `fee" value="` + html.EscapeString(rate.Fee.String()) + `"></td>`)
		b.WriteString(`<td><input name="` + prefix + `fixed_fee" value="` + html.EscapeString(rate.FixedFee.String()) + `"></td>`)
		b.WriteString(`<td><select name="` + prefix + `fee_policy">`)
		for _, policy := range []string{"PERCENT_FEE", "FIXED_FEE", "NO_FEE", "PERCENT_OR_MINIMAL_FIXED_FEE"} {
			selected := ""
			if strings.EqualFold(rate.FeePolicy, policy) {
				selected = ` selected`
			}
			b.WriteString(`<option value="` + policy + `"` + selected + `>` + policy + `</option>`)
		}
		b.WriteString(`</select></td></tr>`)
	}
	b.WriteString(`</tbody></table><p><button type="submit">保存汇率</button></p></form></section>`)
	page(w, "汇率", b.String())
}

func (h *HTTPHandler) ratesPost(w http.ResponseWriter, r *http.Request) {
	fiat := strings.ToUpper(chi.URLParam(r, "fiat"))
	if fiat == "" {
		fiat = "USD"
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/rates/"+fiat+"?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	grouped := map[string]map[string]string{}
	for key, values := range r.Form {
		if !strings.HasPrefix(key, "rates__") || len(values) == 0 {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(key, "rates__"), "__", 2)
		if len(parts) != 2 {
			continue
		}
		if grouped[parts[0]] == nil {
			grouped[parts[0]] = map[string]string{}
		}
		grouped[parts[0]][parts[1]] = values[0]
	}
	for crypto, fields := range grouped {
		rate := ExchangeRate{
			Crypto:    strings.ToUpper(crypto),
			Fiat:      fiat,
			Source:    strings.ToLower(strings.TrimSpace(fields["source"])),
			Rate:      decimalFromStringFallback(fields["rate"], decimal.Zero),
			Fee:       decimalFromStringFallback(fields["fee"], decimal.Zero),
			FixedFee:  decimalFromStringFallback(fields["fixed_fee"], decimal.Zero),
			FeePolicy: strings.TrimSpace(fields["fee_policy"]),
		}
		if rate.Source == "" {
			rate.Source = "dynamic"
		}
		if rate.FeePolicy == "" {
			rate.FeePolicy = "PERCENT_FEE"
		}
		if err := h.store.UpdateExchangeRateSettings(r.Context(), rate, rate.Source == "manual"); err != nil {
			http.Redirect(w, r, "/rates/"+fiat+"?err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
	}
	http.Redirect(w, r, "/rates/"+fiat+"?msg="+url.QueryEscape("汇率已保存"), http.StatusFound)
}

func (h *HTTPHandler) transactionsPage(w http.ResponseWriter, r *http.Request) {
	body := `<div class="top"><h1>交易</h1><nav class="nav"><a href="/wallets">钱包</a><a href="/rates">汇率</a><a href="/payouts">出款</a><a href="/settings">设置</a></nav></div><section class="panel">` +
		transactionTableHTML(r, h) + `</section>`
	page(w, "交易", body)
}

func (h *HTTPHandler) transactionsPart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(transactionTableHTML(r, h)))
}

func transactionTableHTML(r *http.Request, h *HTTPHandler) string {
	crypto := r.URL.Query().Get("crypto")
	addr := r.URL.Query().Get("addr")
	txs, err := h.store.ListTransactions(r.Context(), crypto, addr)
	if err != nil {
		return `<p class="err">` + html.EscapeString(err.Error()) + `</p>`
	}
	var b strings.Builder
	b.WriteString(`<table><thead><tr><th>ID</th><th>时间</th><th>币种</th><th>地址</th><th>金额</th><th>TXID</th><th>确认</th></tr></thead><tbody>`)
	for _, tx := range txs {
		if query := r.URL.Query().Get("txid"); query != "" && !strings.Contains(tx.TxID, query) {
			continue
		}
		b.WriteString(`<tr><td>` + html.EscapeString(decimal.NewFromInt(tx.ID).String()) + `</td><td>` + html.EscapeString(tx.CreatedAt.Format("2006-01-02 15:04:05")) + `</td><td>` + html.EscapeString(tx.Crypto) + `</td><td>` + html.EscapeString(tx.Addr) + `</td><td>` + html.EscapeString(tx.AmountCrypto.String()) + `</td><td>` + html.EscapeString(tx.TxID) + `</td><td>` + html.EscapeString(boolText(!tx.NeedMoreConfirmations)) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func (h *HTTPHandler) payoutsPage(w http.ResponseWriter, r *http.Request) {
	body := `<div class="top"><h1>出款</h1><nav class="nav"><a href="/wallets">钱包</a><a href="/rates">汇率</a><a href="/transactions">交易</a><a href="/settings">设置</a></nav></div><section class="panel">` +
		payoutTableHTML(r, h) + `</section>`
	page(w, "出款", body)
}

func (h *HTTPHandler) payoutsPart(w http.ResponseWriter, r *http.Request) {
	payouts, err := h.store.ListPayouts(r.Context(), r.URL.Query().Get("crypto"), r.URL.Query().Get("status"), r.URL.Query().Get("dest_addr"), r.URL.Query().Get("txid"), intQuery(r, "limit", 50))
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err)
		return
	}
	if strings.EqualFold(r.URL.Query().Get("download"), "csv") {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="payouts.csv"`)
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"Date", "Destination", "Amount", "Crypto", "Tx ID"})
		for _, payout := range payouts {
			_ = writer.Write([]string{payout.CreatedAt.Format("2006-01-02 15:04:05"), payout.DestAddr, payout.Amount.String(), payout.Crypto, strings.Join(payoutTxIDs(payout), " ")})
		}
		writer.Flush()
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(payoutRowsHTML(payouts)))
}

func payoutTableHTML(r *http.Request, h *HTTPHandler) string {
	payouts, err := h.store.ListPayouts(r.Context(), r.URL.Query().Get("crypto"), r.URL.Query().Get("status"), r.URL.Query().Get("dest_addr"), r.URL.Query().Get("txid"), intQuery(r, "limit", 50))
	if err != nil {
		return `<p class="err">` + html.EscapeString(err.Error()) + `</p>`
	}
	return payoutRowsHTML(payouts)
}

func payoutRowsHTML(payouts []Payout) string {
	var b strings.Builder
	b.WriteString(`<table><thead><tr><th>ID</th><th>时间</th><th>币种</th><th>金额</th><th>目标地址</th><th>状态</th><th>TXID</th></tr></thead><tbody>`)
	for _, payout := range payouts {
		b.WriteString(`<tr><td>` + html.EscapeString(decimal.NewFromInt(payout.ID).String()) + `</td><td>` + html.EscapeString(payout.CreatedAt.Format("2006-01-02 15:04:05")) + `</td><td>` + html.EscapeString(payout.Crypto) + `</td><td>` + html.EscapeString(payout.Amount.String()) + `</td><td>` + html.EscapeString(payout.DestAddr) + `</td><td>` + html.EscapeString(payout.Status) + `</td><td>` + html.EscapeString(strings.Join(payoutTxIDs(payout), " ")) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func payoutTxIDs(payout Payout) []string {
	out := make([]string, 0, len(payout.Transactions))
	for _, tx := range payout.Transactions {
		if strings.EqualFold(tx.Kind, "gas_topup") || strings.TrimSpace(tx.TxID) == "" {
			continue
		}
		out = append(out, tx.TxID)
	}
	return out
}

func decimalFromStringFallback(value string, fallback decimal.Decimal) decimal.Decimal {
	out, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return out
}

func boolText(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func errUnknownRateSource(source string) error {
	return &rateSourceError{source: source}
}

type rateSourceError struct {
	source string
}

func (e *rateSourceError) Error() string {
	return "Unknown rate source: " + e.source
}
