package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestApplyConfirmedTransactionSerializesConcurrentCredits(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	invoice := Invoice{
		Crypto:        "BNB-USDT",
		Addr:          "0x0000000000000000000000000000000000000001",
		ExternalID:    "atomic-concurrent-order",
		Fiat:          "USD",
		CallbackURL:   "https://merchant.test/callback",
		BalanceFiat:   decimal.Zero,
		BalanceCrypto: decimal.Zero,
		AmountFiat:    decimal.NewFromInt(100),
		AmountCrypto:  decimal.NewFromInt(100),
		ExchangeRate:  decimal.NewFromInt(1),
		Status:        InvoiceUnpaid,
	}
	if err := store.CreateInvoiceWithAddress(ctx, &invoice, invoice.Crypto, invoice.Addr, invoiceRequestKey(invoice.ExternalID, invoice.CallbackURL, invoice.Fiat)); err != nil {
		t.Fatalf("create invoice with address: %v", err)
	}

	const credits = 24
	start := make(chan struct{})
	errCh := make(chan error, credits)
	var wg sync.WaitGroup
	for i := 0; i < credits; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			transaction := Transaction{
				InvoiceID:             invoice.ID,
				TxID:                  fmt.Sprintf("atomic-tx-%02d", index),
				Crypto:                invoice.Crypto,
				AmountCrypto:          decimal.NewFromInt(1),
				AmountFiat:            decimal.NewFromInt(1),
				NeedMoreConfirmations: false,
			}
			if _, duplicate, err := store.ApplyConfirmedTransaction(ctx, &transaction, decimal.NewFromInt(95), decimal.NewFromInt(105)); err != nil {
				errCh <- err
			} else if duplicate {
				errCh <- fmt.Errorf("unexpected duplicate for %s", transaction.TxID)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent credit failed: %v", err)
	}

	loaded, err := store.InvoiceByID(ctx, invoice.ID)
	if err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if !loaded.BalanceFiat.Equal(decimal.NewFromInt(credits)) || !loaded.BalanceCrypto.Equal(decimal.NewFromInt(credits)) {
		t.Fatalf("concurrent credits lost: fiat=%s crypto=%s", loaded.BalanceFiat, loaded.BalanceCrypto)
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM `transaction` WHERE invoice_id = ?", invoice.ID).Scan(&count); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if count != credits {
		t.Fatalf("transaction count=%d want=%d", count, credits)
	}
}

func TestApplyConfirmedTransactionIsIdempotentUnderConcurrency(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	invoice := Invoice{
		Crypto:       "BNB-USDT",
		Addr:         "0x0000000000000000000000000000000000000002",
		ExternalID:   "atomic-duplicate-order",
		Fiat:         "USD",
		CallbackURL:  "https://merchant.test/callback",
		AmountFiat:   decimal.NewFromInt(1),
		AmountCrypto: decimal.NewFromInt(1),
		ExchangeRate: decimal.NewFromInt(1),
		Status:       InvoiceUnpaid,
	}
	if err := store.CreateInvoiceWithAddress(ctx, &invoice, invoice.Crypto, invoice.Addr, invoiceRequestKey(invoice.ExternalID, invoice.CallbackURL, invoice.Fiat)); err != nil {
		t.Fatalf("create invoice: %v", err)
	}

	start := make(chan struct{})
	results := make(chan bool, 2)
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			transaction := Transaction{InvoiceID: invoice.ID, TxID: "same-tx", Crypto: invoice.Crypto, AmountCrypto: decimal.NewFromInt(1), AmountFiat: decimal.NewFromInt(1)}
			_, duplicate, err := store.ApplyConfirmedTransaction(ctx, &transaction, decimal.NewFromInt(95), decimal.NewFromInt(105))
			if err != nil {
				errCh <- err
				return
			}
			results <- duplicate
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errCh)
	for err := range errCh {
		t.Fatalf("duplicate credit failed: %v", err)
	}
	duplicates := 0
	for duplicate := range results {
		if duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate results=%d want=1", duplicates)
	}
	loaded, err := store.InvoiceByID(ctx, invoice.ID)
	if err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if !loaded.BalanceFiat.Equal(decimal.NewFromInt(1)) || !loaded.BalanceCrypto.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("duplicate credit changed balance more than once: %+v", loaded)
	}
}

func TestCreateInvoiceWithAddressEnforcesRequestIdempotency(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()
	key := invoiceRequestKey("same-request", "https://merchant.test/callback", "USD")

	start := make(chan struct{})
	var wg sync.WaitGroup
	successes := make(chan int64, 8)
	errorsCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			invoice := Invoice{
				Crypto:       "BNB-USDT",
				Addr:         "0x0000000000000000000000000000000000000003",
				ExternalID:   "same-request",
				Fiat:         "USD",
				CallbackURL:  "https://merchant.test/callback",
				AmountFiat:   decimal.NewFromInt(1),
				AmountCrypto: decimal.NewFromInt(1),
				ExchangeRate: decimal.NewFromInt(1),
				Status:       InvoiceUnpaid,
			}
			err := store.CreateInvoiceWithAddress(ctx, &invoice, invoice.Crypto, invoice.Addr, key)
			if err == nil {
				successes <- invoice.ID
				return
			}
			if !isDuplicateSchemaError(err) {
				errorsCh <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(successes)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("create invoice: %v", err)
	}
	created := 0
	for range successes {
		created++
	}
	if created != 1 {
		t.Fatalf("successful creates=%d want=1", created)
	}
	var invoices, addresses int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM invoice WHERE idempotency_key = ?", key).Scan(&invoices); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM invoice_address ia JOIN invoice i ON i.id = ia.invoice_id WHERE i.idempotency_key = ?", key).Scan(&addresses); err != nil {
		t.Fatalf("count invoice addresses: %v", err)
	}
	if invoices != 1 || addresses != 1 {
		t.Fatalf("idempotent create rows invoices=%d addresses=%d", invoices, addresses)
	}
}

func TestSchedulerLeaseAllowsOnlyOneOwner(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	ctx := context.Background()

	first, err := store.TryAcquireSchedulerLease(ctx, "test-lease", "owner-a", 100*time.Millisecond)
	if err != nil || !first {
		t.Fatalf("first lease acquire=%v err=%v", first, err)
	}
	second, err := store.TryAcquireSchedulerLease(ctx, "test-lease", "owner-b", time.Second)
	if err != nil {
		t.Fatalf("second lease attempt: %v", err)
	}
	if second {
		t.Fatalf("second owner acquired an active lease")
	}
	time.Sleep(150 * time.Millisecond)
	second, err = store.TryAcquireSchedulerLease(ctx, "test-lease", "owner-b", time.Second)
	if err != nil || !second {
		t.Fatalf("expired lease takeover=%v err=%v", second, err)
	}
}

func TestDuplicateEntryIsNotTreatedAsExistingSchemaObject(t *testing.T) {
	if isDuplicateSchemaObjectError(errors.New("Error 1062: Duplicate entry 'x' for key 'uq_invoice_idempotency_key'")) {
		t.Fatalf("duplicate data must fail a unique-index migration")
	}
	if !isDuplicateSchemaObjectError(errors.New("Error 1061: Duplicate key name 'ix_invoice_external_id'")) {
		t.Fatalf("duplicate key name should be treated as an existing schema object")
	}
}
