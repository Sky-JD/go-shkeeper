package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

func (h *HTTPHandler) sendUnconfirmedNotification(ctx context.Context, utx UnconfirmedTransaction, invoice Invoice) error {
	module, _ := h.crypto.Module(utx.Crypto)
	apikey := ""
	if module != nil {
		if wallet, err := h.store.WalletByCrypto(ctx, module.Name); err == nil {
			apikey = nullStringValue(wallet.APIKey)
		}
	}
	payload := map[string]any{
		"status":      "unconfirmed",
		"external_id": invoice.ExternalID,
		"crypto":      utx.Crypto,
		"addr":        utx.Addr,
		"txid":        utx.TxID,
		"amount":      utx.AmountCrypto.String(),
	}
	if err := h.postCallback(ctx, invoice.CallbackURL, apikey, payload); err != nil {
		return err
	}
	return h.store.MarkUnconfirmedCallbackConfirmed(ctx, utx.ID)
}

func (h *HTTPHandler) sendInvoiceNotification(ctx context.Context, tx Transaction, invoice Invoice) error {
	detail, err := h.store.InvoiceDetail(ctx, invoice)
	if err != nil {
		return err
	}
	rate, err := h.store.ExchangeRate(ctx, invoice.Fiat, invoice.Crypto)
	if err != nil {
		return err
	}
	transactions := make([]map[string]any, 0, len(detail.Transactions))
	for _, item := range detail.Transactions {
		itemRate := rate
		if item.Crypto != invoice.Crypto {
			if other, err := h.store.ExchangeRate(ctx, invoice.Fiat, item.Crypto); err == nil {
				itemRate = other
			}
		}
		orig := h.rates.OriginalAmount(item.AmountFiat, itemRate)
		transactions = append(transactions, map[string]any{
			"txid":                    item.TxID,
			"date":                    item.CreatedAt.Format("2006-01-02 15:04:05"),
			"amount_crypto":           item.AmountCrypto.String(),
			"amount_fiat":             item.AmountFiat.String(),
			"amount_fiat_without_fee": orig.String(),
			"fee_fiat":                item.AmountFiat.Sub(orig).String(),
			"trigger":                 item.ID == tx.ID,
			"crypto":                  item.Crypto,
		})
	}
	overpaid := invoice.BalanceFiat.Sub(invoice.AmountFiat.Mul(rateFromInt(105)).Div(rateFromInt(100)))
	if wallet, err := h.store.WalletByCrypto(ctx, invoice.Crypto); err == nil {
		overpaid = invoice.BalanceFiat.Sub(invoice.AmountFiat.Mul(wallet.ULimit).Div(rateFromInt(100)))
	}
	if overpaid.LessThan(decimal.Zero) {
		overpaid = decimal.Zero
	}
	payload := map[string]any{
		"external_id":    invoice.ExternalID,
		"crypto":         invoice.Crypto,
		"addr":           invoice.Addr,
		"fiat":           invoice.Fiat,
		"balance_fiat":   invoice.BalanceFiat.String(),
		"balance_crypto": invoice.BalanceCrypto.String(),
		"paid":           invoice.Status == InvoicePaid || invoice.Status == InvoiceOverpaid,
		"status":         invoice.Status,
		"transactions":   transactions,
		"fee_percent":    rate.Fee.String(),
		"fee_fixed":      rate.FixedFee.String(),
		"fee_policy":     rate.FeePolicy,
		"overpaid_fiat":  overpaid.Round(2).StringFixed(2),
	}
	apikey := ""
	if wallet, err := h.store.WalletByCrypto(ctx, tx.Crypto); err == nil {
		apikey = nullStringValue(wallet.APIKey)
	}
	if err := h.postCallback(ctx, invoice.CallbackURL, apikey, payload); err != nil {
		return err
	}
	return h.store.MarkTransactionCallbackConfirmed(ctx, tx.ID)
}

func (h *HTTPHandler) sendPayoutNotification(ctx context.Context, notif Notification) error {
	payout, err := h.store.PayoutByID(ctx, notif.ObjectID)
	if err != nil {
		return err
	}
	if len(payout.Transactions) == 0 || payout.Transactions[0].TxID == "" {
		return fmt.Errorf("payout has no txid yet")
	}
	rate, err := h.store.ExchangeRate(ctx, "USD", payout.Crypto)
	if err != nil {
		return err
	}
	currentRate, err := h.rates.CurrentRate(ctx, rate)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"payout_id":     payout.ID,
		"external_id":   nullStringValue(payout.ExternalID),
		"tx_hash":       payout.Transactions[0].TxID,
		"status":        PayoutSuccess,
		"amount":        payout.Amount.String(),
		"crypto":        payout.Crypto,
		"amount_fiat":   payout.Amount.Mul(currentRate).String(),
		"currency_fiat": "USD",
		"timestamp":     payout.CreatedAt.Format(time.RFC3339),
	}
	if err := h.postCallback(ctx, notif.CallbackURL, "", payload); err != nil {
		_ = h.store.IncrementNotificationRetry(ctx, notif.ID, err.Error())
		return err
	}
	return h.store.MarkNotificationCallbackConfirmed(ctx, notif.ID)
}

func (h *HTTPHandler) postCallback(ctx context.Context, callbackURL string, apiKey string, payload map[string]any) error {
	if callbackURL == "" {
		return fmt.Errorf("callback_url is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.NotificationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", h.cfg.CallbackUserAgent)
	if apiKey != "" {
		req.Header.Set("X-Shkeeper-Api-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("callback returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func rateFromInt(v int64) decimal.Decimal {
	return decimal.NewFromInt(v)
}
