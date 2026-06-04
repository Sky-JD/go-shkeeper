package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestImportLegacyJSONPreservesOrdersAndNormalizesLegacyValues(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	dump := `{
	  "tables": {
	    "user": [
	      {"id": 70, "username": "legacy-admin", "passhash": {"__bytes_utf8": "$2b$12$legacyhash"}, "api_key": null, "totp_secret": null, "totp_enabled": 0, "backup_codes": null, "totp_enabled_at": null}
	    ],
	    "wallet": [
	      {"id": 71, "crypto": "BNB-USDT", "serverkey": "worker:secret", "pdest": "dest", "pfee": "1", "payout": 0, "ppolicy": "MANUAL", "pcond": "none", "last_payout_attempt": "0001-01-01 00:00:00.000000", "enabled": 1, "apikey": "legacy-api-key", "llimit": 95, "ulimit": 105, "recalc": 0, "confirmations": 1, "bkey": null, "prespolicy": "DISABLE", "presamount": null}
	    ],
	    "exchange_rate": [
	      {"id": 72, "source": "manual", "crypto": "BNB-USDT", "fiat": "USD", "rate": 1, "fee": 0, "fixed_fee": 0, "fee_policy": "NO_FEE"}
	    ],
	    "invoice": [
	      {"id": 73, "crypto": "BNB-USDT", "addr": "0xlegacyinvoice", "external_id": "legacy-order", "fiat": "USD", "callback_url": "https://example.test/callback", "balance_fiat": 0, "balance_crypto": 0, "amount_fiat": 1, "amount_crypto": 1.02, "exchange_rate": 1, "status": "UNPAID", "created_at": "2026-06-02 15:46:56", "updated_at": "2026-06-02 15:46:56"}
	    ],
	    "invoice_address": [
	      {"id": 74, "invoice_id": 73, "crypto": "BNB-USDT", "addr": "0xlegacyinvoice", "created_at": "2026-06-02 15:46:56"}
	    ],
	    "transaction": [
	      {"id": 75, "invoice_id": 73, "txid": "0xlegacytx", "crypto": "BNB-USDT", "amount_crypto": 1.02, "amount_fiat": 1, "need_more_confirmations": 0, "callback_confirmed": 1, "created_at": "2026-06-02 15:50:00", "updated_at": "2026-06-02 15:50:00"}
	    ],
	    "payout": [
	      {"id": 76, "created_at": "2026-06-02 16:00:00", "updated_at": "2026-06-02 16:00:00", "amount": 1, "crypto": "BNB-USDT", "dest_addr": "0xpayout", "success": null, "error": null, "callback_url": null, "task_id": "legacy-task", "external_id": "legacy-order", "status": "IN_PROGRESS"}
	    ],
	    "payout_tx": [
	      {"id": 77, "payout_id": 76, "created_at": "2026-06-02 16:00:01", "updated_at": "2026-06-02 16:00:01", "txid": "0xpayouttx", "status": "IN_PROGRESS"}
	    ],
	    "payout_destination": [
	      {"id": 78, "crypto": "BNB-USDT", "addr": "0xpayout", "comment": "legacy destination"}
	    ],
	    "setting": [
	      {"name": "WalletEncryptionPersistentStatus", "value": "2"}
	    ]
	  }
	}`

	report, err := ImportLegacyJSON(ctx, store, strings.NewReader(dump))
	if err != nil {
		t.Fatalf("import legacy json: %v", err)
	}
	if report.Mode != "upsert" || report.OrderIndexRows != 1 {
		t.Fatalf("unexpected import mode/order index count: %+v", report)
	}
	if report.Tables["invoice"] != 1 || report.Tables["transaction"] != 1 || report.Tables["payout"] != 1 {
		t.Fatalf("unexpected import report: %+v", report)
	}
	user, err := store.UserByUsername(ctx, "legacy-admin")
	if err != nil {
		t.Fatalf("load legacy admin: %v", err)
	}
	if user.Passhash.String != "$2b$12$legacyhash" {
		t.Fatalf("legacy byte passhash was not decoded: %+v", user.Passhash)
	}
	wallet, err := store.WalletByCrypto(ctx, "BNB-USDT")
	if err != nil {
		t.Fatalf("load legacy wallet: %v", err)
	}
	if wallet.LastAttempt.Valid {
		t.Fatalf("legacy minimum datetime should import as NULL, got %+v", wallet.LastAttempt)
	}
	order, err := store.GetOrder(ctx, "legacy-order")
	if err != nil {
		t.Fatalf("load imported order: %v", err)
	}
	if len(order.Invoices) != 1 || len(order.Invoices[0].Transactions) != 1 || len(order.Payouts) != 1 || len(order.Payouts[0].Transactions) != 1 {
		t.Fatalf("imported order is incomplete: %+v", order)
	}
	if order.Invoices[0].Invoice.Status != InvoiceUnpaid || order.Payouts[0].Status != PayoutInProgress {
		t.Fatalf("imported statuses changed: %+v", order)
	}

	updatedDump := strings.Replace(dump, `"status": "UNPAID"`, `"status": "PAID"`, 1)
	report, err = ImportLegacyJSON(ctx, store, strings.NewReader(updatedDump))
	if err != nil {
		t.Fatalf("reimport legacy json: %v", err)
	}
	if report.Mode != "upsert" || report.OrderIndexRows != 1 {
		t.Fatalf("reimport should keep one order index row: %+v", report)
	}
	order, err = store.GetOrder(ctx, "legacy-order")
	if err != nil {
		t.Fatalf("load reimported order: %v", err)
	}
	if order.Invoices[0].Invoice.Status != InvoicePaid {
		t.Fatalf("reimport should update existing invoice status, got %+v", order.Invoices[0].Invoice)
	}
	var orderIndexRows int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM `order_index` WHERE external_id = ?", "legacy-order").Scan(&orderIndexRows); err != nil {
		t.Fatalf("count order index: %v", err)
	}
	if orderIndexRows != 1 {
		t.Fatalf("reimport duplicated order index rows: %d", orderIndexRows)
	}
}

func TestNormalizeLegacyValueHandlesMariaDBDriverValues(t *testing.T) {
	timeColumns := map[string]struct{}{"created_at": {}}
	ts := time.Date(2026, 6, 4, 12, 30, 0, 0, time.UTC)
	if got := normalizeLegacyValue("passhash", []byte("$2b$12$hash"), nil); got != "$2b$12$hash" {
		t.Fatalf("[]byte value was not converted to string: %#v", got)
	}
	if got := normalizeLegacyValue("created_at", ts, timeColumns); got != ts {
		t.Fatalf("time.Time value was not preserved: %#v", got)
	}
	if got := normalizeLegacyValue("created_at", time.Time{}, timeColumns); got != nil {
		t.Fatalf("zero time should import as NULL, got %#v", got)
	}
}
