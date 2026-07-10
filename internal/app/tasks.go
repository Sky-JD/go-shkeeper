package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type Scheduler struct {
	cfg     Config
	store   *Store
	crypto  *CryptoRegistry
	rates   *RateService
	logger  *slog.Logger
	stop    context.CancelFunc
	wg      sync.WaitGroup
	handler *HTTPHandler
}

const (
	schedulerLeaseName          = "go-shkeeper-background"
	schedulerLeaseTTL           = 20 * time.Second
	schedulerLeaseRenewInterval = 5 * time.Second
)

func NewScheduler(cfg Config, store *Store, crypto *CryptoRegistry, rates *RateService, logger *slog.Logger) *Scheduler {
	auth := NewAuthManager(cfg, store, logger)
	handler := NewHTTPHandler(cfg, store, crypto, rates, auth, logger)
	return &Scheduler{cfg: cfg, store: store, crypto: crypto, rates: rates, logger: logger, handler: handler}
}

func (s *Scheduler) Start(parent context.Context) {
	if !s.cfg.SchedulerEnabled {
		s.logger.Info("scheduler disabled")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.stop = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runLeaderLoop(ctx)
	}()
}

func (s *Scheduler) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.wg.Wait()
}

func (s *Scheduler) runLeaderLoop(ctx context.Context) {
	hostname, _ := os.Hostname()
	owner := fmt.Sprintf("%s-%d-%s", hostname, os.Getpid(), randomToken(8))
	ticker := time.NewTicker(schedulerLeaseRenewInterval)
	defer ticker.Stop()

	var jobsCancel context.CancelFunc
	var jobsWG *sync.WaitGroup
	stopJobs := func() {
		if jobsCancel == nil {
			return
		}
		jobsCancel()
		jobsWG.Wait()
		jobsCancel = nil
		jobsWG = nil
		s.logger.Warn("scheduler leadership lost; background jobs stopped", "owner", owner)
	}
	updateLeadership := func() {
		leader, err := s.store.TryAcquireSchedulerLease(ctx, schedulerLeaseName, owner, schedulerLeaseTTL)
		if err != nil {
			s.logger.Warn("scheduler lease unavailable", "owner", owner, "error", err)
			stopJobs()
			return
		}
		if !leader {
			stopJobs()
			return
		}
		if jobsCancel == nil {
			jobsCancel, jobsWG = s.startScheduledJobs(ctx)
			s.logger.Info("scheduler leadership acquired; background jobs started", "owner", owner)
		}
	}

	updateLeadership()
	for {
		select {
		case <-ctx.Done():
			stopJobs()
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.store.ReleaseSchedulerLease(releaseCtx, schedulerLeaseName, owner)
			cancel()
			return
		case <-ticker.C:
			updateLeadership()
		}
	}
}

func (s *Scheduler) startScheduledJobs(parent context.Context) (context.CancelFunc, *sync.WaitGroup) {
	ctx, cancel := context.WithCancel(parent)
	wg := &sync.WaitGroup{}
	s.every(ctx, wg, 30*time.Second, s.updateConfirmations)
	s.every(ctx, wg, 30*time.Second, s.sendCallbacks)
	s.every(ctx, wg, 60*time.Second, s.processAutopayouts)
	s.every(ctx, wg, 45*time.Second, s.pollPayouts)
	s.every(ctx, wg, 45*time.Second, s.sendPayoutCallbacks)
	return cancel, wg
}

func (s *Scheduler) every(ctx context.Context, wg *sync.WaitGroup, interval time.Duration, fn func(context.Context)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		fn(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fn(ctx)
			}
		}
	}()
}

func (s *Scheduler) updateConfirmations(ctx context.Context) {
	txs, err := s.store.PendingConfirmationTransactions(ctx)
	if err != nil {
		s.logger.Warn("load pending confirmation transactions", "error", err)
		return
	}
	for _, tx := range txs {
		module, ok := s.crypto.Module(tx.Crypto)
		if !ok {
			continue
		}
		confirmations, err := s.crypto.Confirmations(ctx, module, tx.TxID)
		if err != nil {
			continue
		}
		invoice, err := s.store.InvoiceByID(ctx, tx.InvoiceID)
		if err != nil {
			continue
		}
		wallet, err := s.store.WalletByCrypto(ctx, invoice.Crypto)
		if err != nil {
			continue
		}
		if confirmations >= wallet.Confirmations {
			_ = s.store.MarkTransactionConfirmationsDone(ctx, tx.ID)
		}
	}
}

func (s *Scheduler) sendCallbacks(ctx context.Context) {
	utxs, err := s.store.PendingUnconfirmedCallbacks(ctx)
	if err == nil {
		for _, utx := range utxs {
			invoice, err := s.store.InvoiceByID(ctx, utx.InvoiceID)
			if err == nil {
				_ = s.handler.sendUnconfirmedNotification(ctx, utx, invoice)
			}
		}
	}
	txs, err := s.store.PendingCallbackTransactions(ctx)
	if err != nil {
		s.logger.Warn("load pending callbacks", "error", err)
		return
	}
	for _, tx := range txs {
		if tx.Invoice == nil || tx.Invoice.Status == InvoiceOutgoing {
			_ = s.store.MarkTransactionCallbackConfirmed(ctx, tx.ID)
			continue
		}
		if time.Since(tx.CreatedAt) < s.cfg.NotificationTaskDelay {
			continue
		}
		_ = s.handler.sendInvoiceNotification(ctx, tx, *tx.Invoice)
	}
}

func (s *Scheduler) processAutopayouts(ctx context.Context) {
	wallets, err := s.store.ListWallets(ctx)
	if err != nil {
		s.logger.Warn("load wallets for autopayout", "error", err)
		return
	}
	for _, wallet := range wallets {
		if !wallet.Payout {
			continue
		}
		module, ok := s.crypto.Module(wallet.Crypto)
		if !ok {
			continue
		}
		destination := strings.TrimSpace(nullStringValue(wallet.PDest))
		if destination == "" {
			continue
		}
		if !autopayoutDue(wallet, time.Now().UTC()) {
			continue
		}
		activePayout, err := s.store.HasInProgressPayout(ctx, wallet.Crypto)
		if err != nil {
			s.logger.Warn("check active payout before autopayout", "crypto", wallet.Crypto, "error", err)
			continue
		}
		if activePayout {
			continue
		}
		balance, _, balanceErr := s.crypto.Balance(ctx, module)
		if balanceErr != "" || !balance.GreaterThan(decimal.Zero) {
			continue
		}
		if !autopayoutBalanceReached(wallet, balance) {
			continue
		}
		amount, err := autopayoutAmount(wallet, balance)
		if err != nil || !amount.GreaterThan(decimal.Zero) {
			s.logger.Warn("autopayout amount unavailable", "crypto", wallet.Crypto, "balance", balance.String(), "error", err)
			continue
		}
		s.dispatchAutopayout(ctx, module, wallet, destination, amount)
	}
}

func autopayoutDue(wallet Wallet, now time.Time) bool {
	switch strings.ToLower(strings.TrimSpace(wallet.PPolicy)) {
	case "limit":
		if !wallet.LastAttempt.Valid {
			return true
		}
		return !wallet.LastAttempt.Time.Add(5 * time.Minute).After(now)
	case "scheduled":
		interval := intFromAny(nullStringValue(wallet.PCond))
		if interval <= 0 {
			return true
		}
		if !wallet.LastAttempt.Valid {
			return true
		}
		return wallet.LastAttempt.Time.Add(time.Duration(interval) * time.Minute).Before(now)
	default:
		return false
	}
}

func autopayoutBalanceReached(wallet Wallet, balance decimal.Decimal) bool {
	if strings.EqualFold(strings.TrimSpace(wallet.PPolicy), "limit") {
		limit, err := decimal.NewFromString(strings.TrimSpace(nullStringValue(wallet.PCond)))
		if err != nil || !limit.GreaterThan(decimal.Zero) {
			return false
		}
		return balance.GreaterThanOrEqual(limit)
	}
	return true
}

func autopayoutAmount(wallet Wallet, balance decimal.Decimal) (decimal.Decimal, error) {
	switch strings.ToLower(strings.TrimSpace(wallet.PresPolicy)) {
	case "", "disable":
		return balance, nil
	case "amount":
		reserve, err := decimal.NewFromString(strings.TrimSpace(nullStringValue(wallet.PresAmount)))
		if err != nil {
			return decimal.Zero, err
		}
		return balance.Sub(reserve), nil
	case "percent":
		reservePercent, err := decimal.NewFromString(strings.TrimSpace(nullStringValue(wallet.PresAmount)))
		if err != nil {
			return decimal.Zero, err
		}
		return balance.Mul(decimal.NewFromInt(100).Sub(reservePercent)).Div(decimal.NewFromInt(100)), nil
	default:
		return balance, nil
	}
}

func (s *Scheduler) dispatchAutopayout(ctx context.Context, module *CryptoModule, wallet Wallet, destination string, amount decimal.Decimal) {
	now := time.Now().UTC()
	if err := s.store.UpdateWalletLastPayoutAttempt(ctx, wallet.Crypto, now); err != nil {
		s.logger.Warn("update autopayout attempt", "crypto", wallet.Crypto, "error", err)
		return
	}
	payout := Payout{
		Amount:   amount,
		Crypto:   wallet.Crypto,
		DestAddr: destination,
		Status:   PayoutInProgress,
	}
	if err := s.store.CreatePayout(ctx, &payout); err != nil {
		s.logger.Warn("create autopayout row", "crypto", wallet.Crypto, "error", err)
		return
	}
	res, err := s.crypto.Payout(ctx, module, destination, amount, nullStringValue(wallet.PFee))
	if err != nil {
		_ = s.store.MarkPayoutFail(ctx, payout.ID, err.Error())
		return
	}
	result := res["result"]
	if result == nil {
		result = res
	}
	details := payoutTxDetailsFromAny(result, module.Name, destination)
	if len(details) == 0 {
		details = payoutTxDetailsFromTxIDs(txIDsFromAny(result), module.Name, destination)
	}
	if err := s.store.SetPayoutTaskAndTxDetails(ctx, payout.ID, anyString(res["task_id"]), details); err != nil {
		s.logger.Warn("attach autopayout result", "crypto", wallet.Crypto, "payout_id", payout.ID, "error", err)
	}
	switch strings.ToUpper(strings.TrimSpace(anyString(res["status"]))) {
	case PayoutFail, "FAILED", "FAILURE", "ERROR":
		_ = s.store.MarkPayoutFail(ctx, payout.ID, payoutResultErrorText(res))
	case PayoutPartial:
		_ = s.store.MarkPayoutPartial(ctx, payout.ID, payoutResultErrorText(res))
	}
}

func (s *Scheduler) pollPayouts(ctx context.Context) {
	payouts, err := s.store.PendingPayouts(ctx)
	if err != nil {
		s.logger.Warn("load pending payouts", "error", err)
		return
	}
	for _, payout := range payouts {
		module, ok := s.crypto.Module(payout.Crypto)
		if !ok {
			continue
		}
		var active bool
		payout, active = s.refreshPayoutFromTask(ctx, module, payout)
		if !active {
			continue
		}
		if txid := s.confirmedPayoutTxID(ctx, module, payout); txid != "" {
			if err := s.store.CompletePayoutSuccess(ctx, payout, txid, s.cfg.EnablePayoutCallback); err != nil {
				s.logger.Warn("complete payout success", "payout_id", payout.ID, "error", err)
			}
		}
	}
}

func (s *Scheduler) refreshPayoutFromTask(ctx context.Context, module *CryptoModule, payout Payout) (Payout, bool) {
	taskID := nullStringValue(payout.TaskID)
	if taskID == "" {
		return payout, true
	}
	task, err := s.crypto.Task(ctx, module, taskID)
	if err != nil {
		return payout, true
	}
	status := strings.ToUpper(strings.TrimSpace(anyString(task["status"])))
	switch status {
	case "FAIL", "FAILED", "FAILURE", "ERROR":
		if err := s.store.MarkPayoutFail(ctx, payout.ID, payoutTaskFailureMessage(task, payout.DestAddr)); err != nil {
			s.logger.Warn("mark payout task failure", "payout_id", payout.ID, "task_id", taskID, "error", err)
		}
		return payout, false
	case "SUCCESS", "PARTIAL":
		details := payoutTaskTxDetails(task, payout.Crypto, payout.DestAddr)
		txids := payoutTxIDsFromDetails(details)
		if len(details) > 0 {
			if err := s.store.SetPayoutTaskAndTxDetails(ctx, payout.ID, taskID, details); err != nil {
				s.logger.Warn("attach payout task txids", "payout_id", payout.ID, "task_id", taskID, "error", err)
			} else if loaded, err := s.store.PayoutByID(ctx, payout.ID); err == nil {
				payout = loaded
			}
		}
		if status == "PARTIAL" {
			if len(txids) > 0 {
				if err := s.store.MarkPayoutPartial(ctx, payout.ID, payoutTaskFailureMessage(task, payout.DestAddr)); err != nil {
					s.logger.Warn("mark partial payout", "payout_id", payout.ID, "task_id", taskID, "error", err)
				}
				return payout, false
			}
			if message := payoutTaskFailureForDestination(task, payout.DestAddr); message != "" {
				if err := s.store.MarkPayoutFail(ctx, payout.ID, message); err != nil {
					s.logger.Warn("mark partial payout failure", "payout_id", payout.ID, "task_id", taskID, "error", err)
				}
				return payout, false
			}
		}
	}
	return payout, true
}

func (s *Scheduler) confirmedPayoutTxID(ctx context.Context, module *CryptoModule, payout Payout) string {
	first := ""
	pending := 0
	for _, tx := range payout.Transactions {
		if tx.TxID == "" {
			continue
		}
		if strings.EqualFold(tx.Kind, "gas_topup") || strings.EqualFold(tx.Status, PayoutFail) {
			continue
		}
		if first == "" {
			first = tx.TxID
		}
		confirmations, err := s.crypto.Confirmations(ctx, module, tx.TxID)
		if err != nil || confirmations <= s.cfg.MinConfirmationBlockForPayout {
			pending++
		}
	}
	if first != "" && pending == 0 {
		return first
	}
	return ""
}

func payoutTaskTxIDs(task map[string]any, destination string) []string {
	return payoutTxIDsFromDetails(payoutTaskTxDetails(task, "", destination))
}

func payoutTaskTxDetails(task map[string]any, crypto string, destination string) []PayoutTx {
	result := task["result"]
	details := payoutTxDetailsFromAny(result, crypto, destination)
	if len(details) > 0 {
		return details
	}
	queues := payoutTxIDsByDestination(result)
	if len(queues) > 0 {
		return payoutTxDetailsFromTxIDs(popPayoutTxIDs(queues, destination), crypto, destination)
	}
	if resultMap, ok := result.(map[string]any); ok {
		if _, ok := resultMap["results"]; ok {
			return nil
		}
	}
	return payoutTxDetailsFromTxIDs(txIDsFromAny(result), crypto, destination)
}

func payoutTxIDsFromDetails(details []PayoutTx) []string {
	out := make([]string, 0, len(details))
	for _, detail := range details {
		if strings.EqualFold(detail.Kind, "gas_topup") {
			continue
		}
		if strings.TrimSpace(detail.TxID) != "" {
			out = append(out, detail.TxID)
		}
	}
	return uniqueNonEmptyStrings(out)
}

func payoutTaskFailureMessage(task map[string]any, destination string) string {
	if message := payoutTaskFailureForDestination(task, destination); message != "" {
		return message
	}
	status := strings.TrimSpace(anyString(task["status"]))
	if status == "" {
		status = "FAIL"
	}
	return "payout task " + status + ": " + shortAny(task["result"])
}

func payoutTaskFailureForDestination(task map[string]any, destination string) string {
	if message := payoutTaskFailureValue(task["errors"], destination); message != "" {
		return message
	}
	return payoutTaskFailureValue(task["result"], destination)
}

func payoutTaskFailureValue(value any, destination string) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []any:
		for _, item := range v {
			if message := payoutTaskFailureValue(item, destination); message != "" {
				return message
			}
		}
	case map[string]any:
		if nested, ok := v["errors"]; ok {
			if message := payoutTaskFailureValue(nested, destination); message != "" {
				return message
			}
		}
		dest := strings.TrimSpace(anyString(firstAny(v, "dest", "destination", "address", "to_address")))
		if dest != "" && destination != "" && !strings.EqualFold(dest, destination) {
			return ""
		}
		if message := strings.TrimSpace(anyString(firstAny(v, "error", "message", "reason"))); message != "" {
			if dest != "" {
				return fmt.Sprintf("%s: %s", dest, message)
			}
			return message
		}
	case string:
		return strings.TrimSpace(v)
	}
	return ""
}

func shortAny(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	if len(data) > 512 {
		data = append(data[:512], '.', '.', '.')
	}
	return string(data)
}

func (s *Scheduler) sendPayoutCallbacks(ctx context.Context) {
	if !s.cfg.EnablePayoutCallback {
		return
	}
	notifs, err := s.store.PendingNotifications(ctx, s.cfg.NotificationRetries)
	if err != nil {
		s.logger.Warn("load payout notifications", "error", err)
		return
	}
	for _, notif := range notifs {
		delay := time.Duration((notif.Retries+1)*(notif.Retries+1)) * time.Second
		if time.Since(notif.CreatedAt) < delay {
			continue
		}
		_ = s.handler.sendPayoutNotification(ctx, notif)
	}
}
