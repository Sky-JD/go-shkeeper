package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestSchedulerProcessesLimitAutopayoutWithReserveAmount(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	wallet, err := store.WalletByCrypto(ctx, "BTC")
	if err != nil {
		t.Fatalf("load wallet: %v", err)
	}
	if err := store.UpdateWalletAutopayout(ctx, "BTC", WalletAutopayout{
		PDest:         sqlNullString("bc1auto"),
		PFee:          sqlNullString("2"),
		Payout:        true,
		PPolicy:       "limit",
		PCond:         sqlNullString("5"),
		LLimit:        wallet.LLimit,
		ULimit:        wallet.ULimit,
		Recalc:        wallet.Recalc,
		Confirmations: wallet.Confirmations,
		PresPolicy:    "amount",
		PresAmount:    sqlNullString("1"),
	}); err != nil {
		t.Fatalf("update autopayout: %v", err)
	}
	payoutCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BTC/balance":
			_ = json.NewEncoder(w).Encode(map[string]any{"balance": "5.5"})
		case "/BTC/payout/bc1auto/4.5/2":
			payoutCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  "SUCCESS",
				"task_id": "auto-task",
				"result":  map[string]any{"txids": []string{"auto-tx"}},
			})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))

	scheduler := NewScheduler(cfg, store, NewCryptoRegistry(cfg, store, testLogger()), NewRateService(cfg, store, testLogger()), testLogger())
	scheduler.processAutopayouts(ctx)
	scheduler.processAutopayouts(ctx)

	if payoutCalls != 1 {
		t.Fatalf("autopayout worker calls=%d want=1 while first payout is still active", payoutCalls)
	}
	payouts, err := store.ListPayouts(ctx, "BTC", "", "bc1auto", "auto-tx", 10)
	if err != nil {
		t.Fatalf("list payouts: %v", err)
	}
	if len(payouts) != 1 || !payouts[0].Amount.Equal(decimal.RequireFromString("4.5")) || nullStringValue(payouts[0].TaskID) != "auto-task" || len(payouts[0].Transactions) != 1 {
		t.Fatalf("autopayout was not persisted correctly: %+v", payouts)
	}
	updated, err := store.WalletByCrypto(ctx, "BTC")
	if err != nil {
		t.Fatalf("reload wallet: %v", err)
	}
	if !updated.LastAttempt.Valid {
		t.Fatalf("last_payout_attempt was not updated")
	}
}

func TestSchedulerSkipsScheduledAutopayoutUntilDue(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	wallet, err := store.WalletByCrypto(ctx, "BTC")
	if err != nil {
		t.Fatalf("load wallet: %v", err)
	}
	if err := store.UpdateWalletAutopayout(ctx, "BTC", WalletAutopayout{
		PDest:         sqlNullString("bc1scheduled"),
		Payout:        true,
		PPolicy:       "scheduled",
		PCond:         sqlNullString("60"),
		LLimit:        wallet.LLimit,
		ULimit:        wallet.ULimit,
		Recalc:        wallet.Recalc,
		Confirmations: wallet.Confirmations,
		PresPolicy:    "disable",
		PresAmount:    sql.NullString{},
	}); err != nil {
		t.Fatalf("update autopayout: %v", err)
	}
	if err := store.UpdateWalletLastPayoutAttempt(ctx, "BTC", time.Now().UTC()); err != nil {
		t.Fatalf("set last attempt: %v", err)
	}
	called := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))

	scheduler := NewScheduler(cfg, store, NewCryptoRegistry(cfg, store, testLogger()), NewRateService(cfg, store, testLogger()), testLogger())
	scheduler.processAutopayouts(ctx)

	if called {
		t.Fatalf("scheduled autopayout called worker before due time")
	}
	payouts, err := store.ListPayouts(ctx, "BTC", "", "bc1scheduled", "", 10)
	if err != nil {
		t.Fatalf("list payouts: %v", err)
	}
	if len(payouts) != 0 {
		t.Fatalf("scheduled autopayout should not create payout before due: %+v", payouts)
	}
}

func TestSchedulerPollsPayoutTaskResultAndCreatesNotification(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	cfg.EnablePayoutCallback = true
	cfg.MinConfirmationBlockForPayout = 1
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	if err := store.EnsureExchangeRate(ctx, "BTC", "USD"); err != nil {
		t.Fatalf("ensure rate: %v", err)
	}
	payout := Payout{
		Amount:      decimal.RequireFromString("1.5"),
		Crypto:      "BTC",
		DestAddr:    "bc1dest",
		CallbackURL: sqlNullString("https://merchant.test/payout-callback"),
		ExternalID:  sqlNullString("task-payout-success"),
		TaskID:      sqlNullString("task-refresh"),
		Status:      PayoutInProgress,
	}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout: %v", err)
	}

	var sawTask, sawTransaction bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BTC/task/task-refresh":
			sawTask = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "SUCCESS",
				"result": map[string]any{
					"results": []map[string]any{
						{"dest": "BC1DEST", "txids": []string{"tx-task"}},
					},
				},
			})
		case "/BTC/transaction/tx-task":
			sawTransaction = true
			_ = json.NewEncoder(w).Encode([][]any{{"bc1dest", "1.5", 2, "send"}})
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))

	scheduler := NewScheduler(cfg, store, NewCryptoRegistry(cfg, store, testLogger()), NewRateService(cfg, store, testLogger()), testLogger())
	scheduler.pollPayouts(ctx)

	if !sawTask || !sawTransaction {
		t.Fatalf("scheduler did not call expected worker endpoints: task=%v transaction=%v", sawTask, sawTransaction)
	}
	loaded, err := store.PayoutByID(ctx, payout.ID)
	if err != nil {
		t.Fatalf("load payout: %v", err)
	}
	if loaded.Status != PayoutSuccess || nullStringValue(loaded.Success) != "Yes" {
		t.Fatalf("payout was not completed: %+v", loaded)
	}
	if len(loaded.Transactions) != 1 || loaded.Transactions[0].TxID != "tx-task" {
		t.Fatalf("task txid was not attached: %+v", loaded.Transactions)
	}
	notifs, err := store.PendingNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifs) != 1 || notifs[0].Type != "Payout" || notifs[0].ObjectID != payout.ID || nullStringValue(notifs[0].TxID) != "tx-task" {
		t.Fatalf("payout notification was not created correctly: %+v", notifs)
	}
}

func TestSchedulerMarksFailedPayoutTask(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	cfg.CryptoAllowList = []string{"BTC"}
	ctx := t.Context()
	if err := store.EnsureWallet(ctx, "BTC", "test-api-key"); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	payout := Payout{
		Amount:     decimal.RequireFromString("2.5"),
		Crypto:     "BTC",
		DestAddr:   "bc1fail",
		ExternalID: sqlNullString("task-payout-fail"),
		TaskID:     sqlNullString("task-fail"),
		Status:     PayoutInProgress,
	}
	if err := store.CreatePayout(ctx, &payout); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/BTC/task/task-fail" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "FAIL",
			"result": map[string]any{"error": "insufficient funds"},
		})
	}))
	defer backend.Close()
	t.Setenv("BTC_API_SERVER_HOST", strings.TrimPrefix(backend.URL, "http://"))

	scheduler := NewScheduler(cfg, store, NewCryptoRegistry(cfg, store, testLogger()), NewRateService(cfg, store, testLogger()), testLogger())
	scheduler.pollPayouts(ctx)

	loaded, err := store.PayoutByID(ctx, payout.ID)
	if err != nil {
		t.Fatalf("load payout: %v", err)
	}
	if loaded.Status != PayoutFail || nullStringValue(loaded.Success) != "No" || !strings.Contains(nullStringValue(loaded.Error), "insufficient funds") {
		t.Fatalf("payout failure was not persisted: %+v", loaded)
	}
	if len(loaded.Transactions) != 0 {
		t.Fatalf("failed task should not attach txids: %+v", loaded.Transactions)
	}
}
