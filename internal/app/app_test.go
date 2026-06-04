package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func testStore(t *testing.T) (*Store, Config) {
	t.Helper()
	databaseURL := os.Getenv("GO_SHKEEPER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GO_SHKEEPER_TEST_DATABASE_URL=mariadb://user:<password>@host:3306/testdb to run MariaDB integration tests")
	}
	driver, dsn := parseDatabaseURL(databaseURL)
	cfg := Config{
		DatabaseURL:           databaseURL,
		DatabaseDriver:        driver,
		DatabaseDSN:           dsn,
		DatabaseConfigError:   databaseURLConfigError(databaseURL),
		SuggestedWalletAPIKey: "test-api-key",
		Fiats:                 []string{"USD"},
		BalanceWorkers:        2,
		SecretKey:             []byte("test-secret"),
		RequestTimeout:        200 * time.Millisecond,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	store, err := OpenStore(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cleanupTestTables(t, store)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("remigrate after cleanup: %v", err)
	}
	return store, cfg
}

func cleanupTestTables(t *testing.T, store *Store) {
	t.Helper()
	tables := []string{
		"order_index",
		"bitcoin_lightning_invoice", "notification", "payout_destination", "payout_tx",
		"payout", "unconfirmed_transaction", "transaction", "invoice_address",
		"invoice", "exchange_rate", "wallet", "setting", "user",
	}
	for _, table := range tables {
		if _, err := store.DB().Exec("DELETE FROM " + store.table(table)); err != nil {
			t.Fatalf("cleanup %s: %v", table, err)
		}
	}
}

func TestOpenStoreAppliesDatabasePoolSettings(t *testing.T) {
	databaseURL := os.Getenv("GO_SHKEEPER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GO_SHKEEPER_TEST_DATABASE_URL=mariadb://user:<password>@host:3306/testdb to run MariaDB integration tests")
	}
	driver, dsn := parseDatabaseURL(databaseURL)
	cfg := Config{
		DatabaseURL:         databaseURL,
		DatabaseDriver:      driver,
		DatabaseDSN:         dsn,
		DatabaseConfigError: databaseURLConfigError(databaseURL),
		DBMaxOpenConns:      7,
		DBMaxIdleConns:      3,
		DBConnMaxIdleTime:   11 * time.Second,
		DBConnMaxLifetime:   22 * time.Second,
	}
	store, err := OpenStore(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if got := store.DB().Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("max open connections=%d, want 7", got)
	}
}

func TestOrderQueryReturnsUnpaidInvoice(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	invoice := Invoice{
		Crypto:        "BTC",
		Addr:          "btc-address-1",
		ExternalID:    "order-100",
		Fiat:          "USD",
		CallbackURL:   "https://example.test/callback",
		BalanceFiat:   decimal.Zero,
		BalanceCrypto: decimal.Zero,
		AmountFiat:    decimal.NewFromInt(25),
		AmountCrypto:  decimal.NewFromFloat(0.0004),
		ExchangeRate:  decimal.NewFromInt(62500),
		Status:        InvoiceUnpaid,
	}
	if err := store.CreateInvoice(ctx, &invoice); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if err := store.AddInvoiceAddress(ctx, invoice.ID, "BTC", invoice.Addr); err != nil {
		t.Fatalf("add invoice address: %v", err)
	}

	order, err := store.GetOrder(ctx, "order-100")
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if len(order.Invoices) != 1 {
		t.Fatalf("expected 1 invoice, got %d", len(order.Invoices))
	}
	if order.Invoices[0].Invoice.Status != InvoiceUnpaid {
		t.Fatalf("expected unpaid invoice, got %s", order.Invoices[0].Invoice.Status)
	}
	if len(order.Invoices[0].Transactions) != 0 {
		t.Fatalf("expected no successful transactions")
	}
}

func TestGetOrderDoesNotTruncateLargeExternalIDHistory(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	stmt, err := store.DB().PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(crypto, addr, external_id, fiat, callback_url, balance_fiat, balance_crypto, amount_fiat, amount_crypto, exchange_rate, status)
		VALUES (?, ?, ?, 'USD', 'https://example.test/large-history', 0, 0, 1, 1, 1, ?)`, store.table("invoice")))
	if err != nil {
		t.Fatalf("prepare invoice insert: %v", err)
	}
	defer stmt.Close()
	for i := 0; i < 501; i++ {
		status := InvoiceUnpaid
		if i%2 == 0 {
			status = InvoicePaid
		}
		if _, err := stmt.ExecContext(ctx, "BTC", fmt.Sprintf("btc-large-history-%03d", i), "order-large-history", status); err != nil {
			t.Fatalf("insert invoice %d: %v", i, err)
		}
	}

	order, err := store.GetOrder(ctx, "order-large-history")
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if len(order.Invoices) != 501 {
		t.Fatalf("expected all 501 invoices, got %d", len(order.Invoices))
	}
}

func TestListOrdersReturnsFullInvoiceAndPayoutDetails(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	if err := store.EnsureWallet(ctx, "BNB-USDT", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	invoice := Invoice{
		Crypto:        "BNB-USDT",
		Addr:          "0xinvoice",
		ExternalID:    "order-full",
		Fiat:          "USD",
		CallbackURL:   "https://example.test/callback",
		BalanceFiat:   decimal.NewFromInt(10),
		BalanceCrypto: decimal.NewFromInt(10),
		AmountFiat:    decimal.NewFromInt(10),
		AmountCrypto:  decimal.NewFromInt(10),
		ExchangeRate:  decimal.NewFromInt(1),
		Status:        InvoicePaid,
	}
	if err := store.CreateInvoice(ctx, &invoice); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if err := store.AddInvoiceAddress(ctx, invoice.ID, "BNB-USDT", "0xinvoice"); err != nil {
		t.Fatalf("add invoice address: %v", err)
	}
	tx := Transaction{InvoiceID: invoice.ID, TxID: "0xconfirmed", Crypto: "BNB-USDT", AmountCrypto: decimal.NewFromInt(10), AmountFiat: decimal.NewFromInt(10)}
	if err := store.AddTransaction(ctx, &tx); err != nil {
		t.Fatalf("add transaction: %v", err)
	}
	utx := UnconfirmedTransaction{InvoiceID: invoice.ID, Addr: "0xinvoice", TxID: "0xunconfirmed", Crypto: "BNB-USDT", AmountCrypto: decimal.NewFromInt(1)}
	if err := store.AddUnconfirmed(ctx, &utx); err != nil {
		t.Fatalf("add unconfirmed: %v", err)
	}
	payout := Payout{
		Amount:     decimal.NewFromInt(3),
		Crypto:     "BNB-USDT",
		DestAddr:   "0xpayout",
		ExternalID: sqlNullString("order-full"),
		TaskID:     sqlNullString("task-full"),
		Status:     PayoutInProgress,
	}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if err := store.AddPayoutTx(ctx, payout.ID, "0xpayouttx"); err != nil {
		t.Fatalf("add payout tx: %v", err)
	}

	orders, err := store.ListOrders(ctx, OrderFilter{ExternalID: "order-full", Limit: 10})
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != 1 || len(orders[0].Invoices) != 1 {
		t.Fatalf("unexpected orders: %+v", orders)
	}
	detail := orders[0].Invoices[0]
	if len(detail.Addresses) != 1 || len(detail.Transactions) != 1 || len(detail.UnconfirmedTXs) != 1 {
		t.Fatalf("missing invoice details: %+v", detail)
	}
	if len(orders[0].Payouts) != 1 || len(orders[0].Payouts[0].Transactions) != 1 {
		t.Fatalf("missing payout details: %+v", orders[0].Payouts)
	}
}

func TestListOrdersIncludesOutgoingAndPayoutOnlyRecords(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	outgoing := Invoice{
		Crypto:        "USDT",
		Addr:          "outgoing-address",
		ExternalID:    "order-outgoing",
		Fiat:          "USD",
		CallbackURL:   "https://example.test/outgoing",
		BalanceFiat:   decimal.Zero,
		BalanceCrypto: decimal.Zero,
		AmountFiat:    decimal.NewFromInt(5),
		AmountCrypto:  decimal.NewFromInt(5),
		ExchangeRate:  decimal.NewFromInt(1),
		Status:        InvoiceOutgoing,
	}
	if err := store.CreateInvoice(ctx, &outgoing); err != nil {
		t.Fatalf("create outgoing invoice: %v", err)
	}
	if err := store.AddInvoiceAddress(ctx, outgoing.ID, "USDT", outgoing.Addr); err != nil {
		t.Fatalf("add outgoing address: %v", err)
	}
	payoutOnly := Payout{
		Amount:     decimal.NewFromInt(7),
		Crypto:     "USDT",
		DestAddr:   "payout-only-destination",
		ExternalID: sqlNullString("order-payout-only"),
		TaskID:     sqlNullString("payout-only-task"),
		Status:     PayoutInProgress,
	}
	if err := store.CreatePayout(ctx, &payoutOnly); err != nil {
		t.Fatalf("create payout-only order: %v", err)
	}

	orders, err := store.ListOrders(ctx, OrderFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	byExternal := map[string]OrderRecord{}
	for _, order := range orders {
		byExternal[order.ExternalID] = order
	}
	if got := byExternal["order-outgoing"]; len(got.Invoices) != 1 || got.Invoices[0].Invoice.Status != InvoiceOutgoing {
		t.Fatalf("outgoing invoice was not returned as a complete order: %+v", got)
	}
	if got := byExternal["order-payout-only"]; len(got.Invoices) != 0 || len(got.Payouts) != 1 {
		t.Fatalf("payout-only order was not returned: %+v", got)
	}

	filtered, err := store.ListOrders(ctx, OrderFilter{ExternalID: "order-payout-only", Limit: 10})
	if err != nil {
		t.Fatalf("list payout-only order by external id: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ExternalID != "order-payout-only" || len(filtered[0].Payouts) != 1 {
		t.Fatalf("unexpected payout-only external-id result: %+v", filtered)
	}
}

func TestListOrdersReturnsCompleteDetailsAfterStatusFilterSelectsOrder(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	invoice := Invoice{
		Crypto:        "BTC",
		Addr:          "btc-filtered-detail-address",
		ExternalID:    "order-filter-complete",
		Fiat:          "USD",
		CallbackURL:   "https://example.test/filter-complete",
		BalanceFiat:   decimal.Zero,
		BalanceCrypto: decimal.Zero,
		AmountFiat:    decimal.NewFromInt(12),
		AmountCrypto:  decimal.NewFromFloat(0.0012),
		ExchangeRate:  decimal.NewFromInt(10000),
		Status:        InvoiceUnpaid,
	}
	if err := store.CreateInvoice(ctx, &invoice); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if err := store.AddInvoiceAddress(ctx, invoice.ID, "BTC", invoice.Addr); err != nil {
		t.Fatalf("add invoice address: %v", err)
	}
	payout := Payout{
		Amount:     decimal.NewFromInt(4),
		Crypto:     "BTC",
		DestAddr:   "btc-filtered-detail-payout",
		ExternalID: sqlNullString("order-filter-complete"),
		TaskID:     sqlNullString("task-filter-complete"),
		Status:     PayoutFail,
	}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout: %v", err)
	}

	orders, err := store.ListOrders(ctx, OrderFilter{Status: PayoutFail, Limit: 10})
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	var found OrderRecord
	for _, order := range orders {
		if order.ExternalID == "order-filter-complete" {
			found = order
			break
		}
	}
	if found.ExternalID == "" {
		t.Fatalf("status filter did not select payout-backed order: %+v", orders)
	}
	if len(found.Payouts) != 1 || found.Payouts[0].Status != PayoutFail {
		t.Fatalf("expected matching payout detail, got %+v", found.Payouts)
	}
	if len(found.Invoices) != 1 || found.Invoices[0].Invoice.Status != InvoiceUnpaid {
		t.Fatalf("filtered order lost non-matching invoice detail: %+v", found.Invoices)
	}
	if len(found.Invoices[0].Addresses) != 1 {
		t.Fatalf("filtered order lost invoice address detail: %+v", found.Invoices[0])
	}
}

func TestOrderAPIStatusMatrixReturnsEveryInvoiceAndPayoutState(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := context.Background()

	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	handler := newTestHTTPHandler(t, store, cfg)

	invoiceExternalByStatus := map[string]string{}
	for idx, status := range []string{
		InvoiceUnpaid,
		InvoicePartial,
		InvoicePaid,
		InvoiceOverpaid,
		InvoiceCancelled,
		InvoiceRefunded,
		InvoiceOutgoing,
	} {
		externalID := "matrix-invoice-" + strings.ToLower(strings.ReplaceAll(status, "_", "-"))
		invoice := Invoice{
			Crypto:        "BTC",
			Addr:          externalID + "-addr",
			ExternalID:    externalID,
			Fiat:          "USD",
			CallbackURL:   "https://example.test/status-matrix",
			BalanceFiat:   decimal.NewFromInt(int64(idx)),
			BalanceCrypto: decimal.NewFromInt(int64(idx)),
			AmountFiat:    decimal.NewFromInt(10),
			AmountCrypto:  decimal.NewFromInt(1),
			ExchangeRate:  decimal.NewFromInt(10),
			Status:        status,
		}
		if err := store.CreateInvoice(ctx, &invoice); err != nil {
			t.Fatalf("create invoice %s: %v", status, err)
		}
		if err := store.AddInvoiceAddress(ctx, invoice.ID, "BTC", invoice.Addr); err != nil {
			t.Fatalf("add invoice address %s: %v", status, err)
		}
		invoiceExternalByStatus[status] = externalID
	}

	payoutExternalByStatus := map[string]string{}
	for idx, status := range []string{PayoutInProgress, PayoutSuccess, PayoutFail} {
		externalID := "matrix-payout-" + strings.ToLower(strings.ReplaceAll(status, "_", "-"))
		payout := Payout{
			Amount:     decimal.NewFromInt(int64(idx + 1)),
			Crypto:     "BTC",
			DestAddr:   externalID + "-destination",
			ExternalID: sqlNullString(externalID),
			TaskID:     sqlNullString(externalID + "-task"),
			Status:     status,
		}
		if err := store.CreatePayout(ctx, &payout); err != nil {
			t.Fatalf("create payout %s: %v", status, err)
		}
		payoutExternalByStatus[status] = externalID
	}

	all := requestOrdersPage(t, handler, "/api/v1/orders?limit=20")
	allByExternalID := responseOrdersByExternalID(t, all)
	for status, externalID := range invoiceExternalByStatus {
		order, ok := allByExternalID[externalID]
		if !ok {
			t.Fatalf("list endpoint missed invoice status %s external_id=%s body=%v", status, externalID, all)
		}
		if got := orderStatuses(t, order, "invoices"); strings.Join(got, ",") != status {
			t.Fatalf("list endpoint invoice status for %s = %v, want %s", externalID, got, status)
		}
	}
	for status, externalID := range payoutExternalByStatus {
		order, ok := allByExternalID[externalID]
		if !ok {
			t.Fatalf("list endpoint missed payout status %s external_id=%s body=%v", status, externalID, all)
		}
		if got := orderStatuses(t, order, "payouts"); strings.Join(got, ",") != status {
			t.Fatalf("list endpoint payout status for %s = %v, want %s", externalID, got, status)
		}
	}

	for status, externalID := range invoiceExternalByStatus {
		detail := requestOrderDetail(t, handler, externalID)
		order := responseOrder(t, detail)
		if got := orderStatuses(t, order, "invoices"); strings.Join(got, ",") != status {
			t.Fatalf("detail endpoint invoice status for %s = %v, want %s", externalID, got, status)
		}
		filtered := requestOrdersPage(t, handler, "/api/v1/orders?limit=20&status="+status)
		filteredByExternalID := responseOrdersByExternalID(t, filtered)
		if _, ok := filteredByExternalID[externalID]; !ok {
			t.Fatalf("status filter %s missed invoice external_id=%s body=%v", status, externalID, filtered)
		}
	}
	for status, externalID := range payoutExternalByStatus {
		detail := requestOrderDetail(t, handler, externalID)
		order := responseOrder(t, detail)
		if got := orderStatuses(t, order, "payouts"); strings.Join(got, ",") != status {
			t.Fatalf("detail endpoint payout status for %s = %v, want %s", externalID, got, status)
		}
		filtered := requestOrdersPage(t, handler, "/api/v1/orders?limit=20&status="+status)
		filteredByExternalID := responseOrdersByExternalID(t, filtered)
		if _, ok := filteredByExternalID[externalID]; !ok {
			t.Fatalf("status filter %s missed payout external_id=%s body=%v", status, externalID, filtered)
		}
	}
}

func TestOrderAPICursorPagination(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	base := time.Date(2026, 6, 4, 1, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		externalID string
		sortAt     time.Time
	}{
		{externalID: "cursor-order-a", sortAt: base.Add(3 * time.Second)},
		{externalID: "cursor-order-c", sortAt: base.Add(2 * time.Second)},
	} {
		invoice := Invoice{
			Crypto:        "BTC",
			Addr:          row.externalID + "-addr",
			ExternalID:    row.externalID,
			Fiat:          "USD",
			CallbackURL:   "https://example.test/cursor",
			BalanceFiat:   decimal.Zero,
			BalanceCrypto: decimal.Zero,
			AmountFiat:    decimal.NewFromInt(1),
			AmountCrypto:  decimal.NewFromInt(1),
			ExchangeRate:  decimal.NewFromInt(1),
			Status:        InvoiceUnpaid,
		}
		if err := store.CreateInvoice(ctx, &invoice); err != nil {
			t.Fatalf("create invoice %s: %v", row.externalID, err)
		}
		if _, err := store.DB().Exec("UPDATE "+store.table("invoice")+" SET updated_at = ? WHERE id = ?", row.sortAt, invoice.ID); err != nil {
			t.Fatalf("set invoice sort time: %v", err)
		}
	}
	payout := Payout{
		Amount:     decimal.NewFromInt(1),
		Crypto:     "BTC",
		DestAddr:   "cursor-payout-destination",
		ExternalID: sqlNullString("cursor-order-b"),
		TaskID:     sqlNullString("cursor-task"),
		Status:     PayoutInProgress,
	}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout cursor order: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE "+store.table("payout")+" SET updated_at = ? WHERE id = ?", base.Add(2*time.Second), payout.ID); err != nil {
		t.Fatalf("set payout sort time: %v", err)
	}
	if err := store.RebuildOrderIndex(ctx); err != nil {
		t.Fatalf("rebuild order index after sort time edits: %v", err)
	}

	handler := newTestHTTPHandler(t, store, cfg)
	first := requestOrdersPage(t, handler, "/api/v1/orders?limit=2")
	firstIDs := responseOrderIDs(t, first)
	if strings.Join(firstIDs, ",") != "cursor-order-a,cursor-order-c" {
		t.Fatalf("unexpected first page order: %v body=%v", firstIDs, first)
	}
	cursor, _ := first["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("expected next cursor in first page: %v", first)
	}

	second := requestOrdersPage(t, handler, "/api/v1/orders?limit=2&cursor="+cursor)
	secondIDs := responseOrderIDs(t, second)
	if strings.Join(secondIDs, ",") != "cursor-order-b" {
		t.Fatalf("unexpected second page order: %v body=%v", secondIDs, second)
	}
	if next, _ := second["next_cursor"].(string); next != "" {
		t.Fatalf("second page should not have next cursor: %q", next)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders?cursor=not-a-valid-cursor", nil)
	req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestOrderAPIHandlesConcurrentListQueries(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	stmt, err := store.DB().PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(crypto, addr, external_id, fiat, callback_url, balance_fiat, balance_crypto, amount_fiat, amount_crypto, exchange_rate, status)
		VALUES ('BTC', ?, ?, 'USD', 'https://example.test/concurrent', 0, 0, 1, 1, 1, 'UNPAID')`, store.table("invoice")))
	if err != nil {
		t.Fatalf("prepare concurrent invoices: %v", err)
	}
	defer stmt.Close()
	for i := 0; i < 80; i++ {
		if _, err := stmt.ExecContext(ctx, fmt.Sprintf("btc-concurrent-%03d", i), fmt.Sprintf("concurrent-order-%03d", i)); err != nil {
			t.Fatalf("insert concurrent invoice %d: %v", i, err)
		}
	}
	if err := store.RebuildOrderIndex(ctx); err != nil {
		t.Fatalf("rebuild order index after raw concurrent inserts: %v", err)
	}

	routes := newTestHTTPHandler(t, store, cfg).Routes()
	start := make(chan struct{})
	errCh := make(chan string, 24)
	var wg sync.WaitGroup
	for worker := 0; worker < 24; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 3; i++ {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/orders?limit=20&status=UNPAID&crypto=BTC", nil)
				req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
				res := httptest.NewRecorder()
				routes.ServeHTTP(res, req)
				if res.Code != http.StatusOK {
					errCh <- fmt.Sprintf("worker %d request %d status=%d body=%s", worker, i, res.Code, res.Body.String())
					return
				}
				var body map[string]any
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					errCh <- fmt.Sprintf("worker %d request %d decode: %v", worker, i, err)
					return
				}
				raw, ok := body["orders"].([]any)
				if !ok || len(raw) == 0 || len(raw) > 20 {
					errCh <- fmt.Sprintf("worker %d request %d unexpected order count: %v", worker, i, body)
					return
				}
				if cursor, _ := body["next_cursor"].(string); cursor == "" {
					errCh <- fmt.Sprintf("worker %d request %d missing next cursor: %v", worker, i, body)
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestOrderQueryIndexesAreMigrated(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()

	rows, err := store.DB().Query(`
		SELECT INDEX_NAME, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN ('invoice', 'payout', 'order_index')
		  AND INDEX_NAME IN (
			'ix_invoice_order_external_updated',
			'ix_invoice_order_status_updated',
			'ix_invoice_order_crypto_updated',
			'ix_invoice_list_status_id',
			'ix_invoice_list_crypto_id',
			'ix_invoice_list_status_crypto_id',
			'ix_payout_order_external_updated',
			'ix_payout_order_status_updated',
			'ix_payout_order_crypto_updated',
			'ix_order_index_sort'
		  )
		GROUP BY INDEX_NAME`)
	if err != nil {
		t.Fatalf("query index metadata: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var name, columns string
		if err := rows.Scan(&name, &columns); err != nil {
			t.Fatalf("scan index metadata: %v", err)
		}
		got[name] = columns
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read index metadata: %v", err)
	}
	want := map[string]string{
		"ix_invoice_order_external_updated": "external_id,updated_at,id",
		"ix_invoice_order_status_updated":   "status,updated_at,external_id",
		"ix_invoice_order_crypto_updated":   "crypto,updated_at,external_id",
		"ix_invoice_list_status_id":         "status,id",
		"ix_invoice_list_crypto_id":         "crypto,id",
		"ix_invoice_list_status_crypto_id":  "status,crypto,id",
		"ix_payout_order_external_updated":  "external_id,updated_at,id",
		"ix_payout_order_status_updated":    "status,updated_at,external_id",
		"ix_payout_order_crypto_updated":    "crypto,updated_at,external_id",
		"ix_order_index_sort":               "sort_at,external_id",
	}
	for name, columns := range want {
		if got[name] != columns {
			t.Fatalf("index %s columns=%q, want %q; all=%v", name, got[name], columns, got)
		}
	}
}

func TestOrderDetailIndexesAreMigrated(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()

	rows, err := store.DB().Query(`
		SELECT TABLE_NAME, INDEX_NAME, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME IN ('transaction', 'unconfirmed_transaction')
		  AND INDEX_NAME IN (
			'ix_transaction_invoice_id',
			'ix_unconfirmed_transaction_invoice_id'
		  )
		GROUP BY TABLE_NAME, INDEX_NAME`)
	if err != nil {
		t.Fatalf("query detail index metadata: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var table, name, columns string
		if err := rows.Scan(&table, &name, &columns); err != nil {
			t.Fatalf("scan detail index metadata: %v", err)
		}
		got[table+"."+name] = columns
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read detail index metadata: %v", err)
	}
	want := map[string]string{
		"transaction.ix_transaction_invoice_id":                         "invoice_id,id",
		"unconfirmed_transaction.ix_unconfirmed_transaction_invoice_id": "invoice_id,id",
	}
	for name, columns := range want {
		if got[name] != columns {
			t.Fatalf("index %s columns=%q, want %q; all=%v", name, got[name], columns, got)
		}
	}
}

func TestOrderHotQueriesCanUseMariaDBIndexes(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT external_id, sort_at FROM %s FORCE INDEX (ix_order_index_sort) WHERE sort_at < ? ORDER BY sort_at DESC, external_id DESC LIMIT 20", store.table("order_index")),
		"ix_order_index_sort", time.Now())
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT external_id, updated_at FROM %s FORCE INDEX (ix_invoice_order_status_updated) WHERE COALESCE(external_id, '') <> '' AND status = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20", store.table("invoice")),
		"ix_invoice_order_status_updated", InvoiceUnpaid)
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT external_id, updated_at FROM %s FORCE INDEX (ix_invoice_order_crypto_updated) WHERE COALESCE(external_id, '') <> '' AND crypto = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20", store.table("invoice")),
		"ix_invoice_order_crypto_updated", "BTC")
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT id, crypto, status FROM %s FORCE INDEX (ix_invoice_list_status_id) WHERE status = ? ORDER BY id DESC LIMIT 20", store.table("invoice")),
		"ix_invoice_list_status_id", InvoiceUnpaid)
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT id, crypto, status FROM %s FORCE INDEX (ix_invoice_list_crypto_id) WHERE crypto = ? ORDER BY id DESC LIMIT 20", store.table("invoice")),
		"ix_invoice_list_crypto_id", "BTC")
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT id, crypto, status FROM %s FORCE INDEX (ix_invoice_list_status_crypto_id) WHERE status = ? AND crypto = ? ORDER BY id DESC LIMIT 20", store.table("invoice")),
		"ix_invoice_list_status_crypto_id", InvoiceUnpaid, "BTC")
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT external_id, updated_at FROM %s FORCE INDEX (ix_payout_order_status_updated) WHERE COALESCE(external_id, '') <> '' AND status = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20", store.table("payout")),
		"ix_payout_order_status_updated", PayoutInProgress)
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT external_id, updated_at FROM %s FORCE INDEX (ix_payout_order_crypto_updated) WHERE COALESCE(external_id, '') <> '' AND crypto = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20", store.table("payout")),
		"ix_payout_order_crypto_updated", "BTC")
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT id, invoice_id FROM %s FORCE INDEX (ix_transaction_invoice_id) WHERE invoice_id IN (?, ?) ORDER BY invoice_id, id", store.table("transaction")),
		"ix_transaction_invoice_id", 1, 2)
	expectExplainUsesIndex(t, ctx, store,
		fmt.Sprintf("SELECT id, invoice_id FROM %s FORCE INDEX (ix_unconfirmed_transaction_invoice_id) WHERE invoice_id IN (?, ?) ORDER BY invoice_id, id", store.table("unconfirmed_transaction")),
		"ix_unconfirmed_transaction_invoice_id", 1, 2)
}

func TestAdminAccountPasswordCanChange(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := context.Background()
	auth := NewAuthManager(cfg, store, slog.New(slog.NewTextHandler(os.Stdout, nil)))

	initial, err := auth.HashPassword("first-password")
	if err != nil {
		t.Fatalf("hash initial password: %v", err)
	}
	if err := store.SetInitialPassword(ctx, initial); err != nil {
		t.Fatalf("set initial password: %v", err)
	}
	user, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if !auth.VerifyPassword(user, "first-password") {
		t.Fatalf("initial password did not verify")
	}

	next, err := auth.HashPassword("second-password")
	if err != nil {
		t.Fatalf("hash next password: %v", err)
	}
	if err := store.UpdateAdminAccount(ctx, user.ID, "root-admin", next); err != nil {
		t.Fatalf("update account: %v", err)
	}
	updated, err := store.UserByUsername(ctx, "root-admin")
	if err != nil {
		t.Fatalf("load updated admin: %v", err)
	}
	if auth.VerifyPassword(updated, "first-password") {
		t.Fatalf("old password still verifies")
	}
	if !auth.VerifyPassword(updated, "second-password") {
		t.Fatalf("new password did not verify")
	}
}

func TestAdminAccountPatchEndpointChangesUsernameAndPassword(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	auth := NewAuthManager(cfg, store, logger)
	initial, err := auth.HashPassword("first-password")
	if err != nil {
		t.Fatalf("hash initial password: %v", err)
	}
	if err := store.SetInitialPassword(ctx, initial); err != nil {
		t.Fatalf("set initial password: %v", err)
	}
	handler := NewHTTPHandler(cfg, store, NewCryptoRegistry(cfg, store, logger), NewRateService(cfg, store, logger), auth, logger)

	body, _ := json.Marshal(map[string]string{
		"username":         "api-admin",
		"current_password": "first-password",
		"new_password":     "second-password",
		"confirm_password": "second-password",
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/account", bytes.NewReader(body))
	req.SetBasicAuth("admin", "first-password")
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", res.Code, res.Body.String())
	}
	updated, err := store.UserByUsername(ctx, "api-admin")
	if err != nil {
		t.Fatalf("load updated admin: %v", err)
	}
	if auth.VerifyPassword(updated, "first-password") {
		t.Fatalf("old password still verifies after API update")
	}
	if !auth.VerifyPassword(updated, "second-password") {
		t.Fatalf("new password does not verify after API update")
	}
}

func sqlNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func requestOrdersPage(t *testing.T, handler *HTTPHandler, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("orders page status=%d body=%s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode orders response: %v", err)
	}
	return body
}

func requestOrderDetail(t *testing.T, handler *HTTPHandler, externalID string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+externalID, nil)
	req.Header.Set("X-Shkeeper-Api-Key", "test-api-key")
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("order detail status=%d body=%s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode order detail response: %v", err)
	}
	return body
}

func responseOrderIDs(t *testing.T, response map[string]any) []string {
	t.Helper()
	raw, ok := response["orders"].([]any)
	if !ok {
		t.Fatalf("orders response has no orders array: %v", response)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected order row: %v", item)
		}
		id, _ := row["external_id"].(string)
		out = append(out, id)
	}
	return out
}

func responseOrdersByExternalID(t *testing.T, response map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := response["orders"].([]any)
	if !ok {
		t.Fatalf("orders response has no orders array: %v", response)
	}
	out := make(map[string]map[string]any, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected order row: %v", item)
		}
		externalID, _ := row["external_id"].(string)
		out[externalID] = row
	}
	return out
}

func responseOrder(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	order, ok := response["order"].(map[string]any)
	if !ok {
		t.Fatalf("order detail response has no order object: %v", response)
	}
	return order
}

func orderStatuses(t *testing.T, order map[string]any, key string) []string {
	t.Helper()
	raw, ok := order[key].([]any)
	if !ok {
		t.Fatalf("order has no %s array: %v", key, order)
	}
	statuses := make([]string, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected %s row: %v", key, item)
		}
		status, _ := row["status"].(string)
		statuses = append(statuses, status)
	}
	return statuses
}

func expectExplainUsesIndex(t *testing.T, ctx context.Context, store *Store, query string, wantIndex string, args ...any) {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain query with %s: %v", wantIndex, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("read explain columns: %v", err)
	}
	keyColumn := -1
	for i, column := range columns {
		if strings.EqualFold(column, "key") {
			keyColumn = i
			break
		}
	}
	if keyColumn < 0 {
		t.Fatalf("EXPLAIN output has no key column: %v", columns)
	}
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan explain row: %v", err)
		}
		if values[keyColumn].Valid && values[keyColumn].String == wantIndex {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read explain rows: %v", err)
	}
	t.Fatalf("EXPLAIN did not use index %s for query: %s", wantIndex, query)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
