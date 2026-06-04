package deploycheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerChecksMainWorkerOrderAndAdmin(t *testing.T) {
	var mainReadyHits int64
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			atomic.AddInt64(&mainReadyHits, 1)
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/orders/order-1":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"status": "success",
				"order": map[string]any{
					"external_id": "order-1",
					"invoices":    []any{map[string]any{"status": "UNPAID"}},
					"payouts":     []any{},
				},
			})
		case "/api/v1/BTC/server":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "admin" || pass != "admin-pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{"server": "worker"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok", "module": "BTC"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready", "module": "BTC"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:         main.URL,
		WorkerURL:       worker.URL,
		APIKey:          "api-key",
		AdminUsername:   "admin",
		AdminPassword:   "admin-pass",
		Crypto:          "BTC",
		OrderExternalID: "order-1",
		ExpectStatus:    "UNPAID",
		Concurrency:     2,
		Requests:        5,
		Timeout:         time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"check=main_healthz ok",
		"check=main_readyz ok",
		"check=main_readyz_parallel requests=5 concurrency=2 ok",
		"check=order_lookup ok",
		"check=admin_auth ok",
		"check=worker_healthz ok",
		"check=worker_readyz ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if got := atomic.LoadInt64(&mainReadyHits); got < 6 {
		t.Fatalf("parallel readiness did not run enough requests: %d", got)
	}
}

func TestRunnerChecksOrderLookupInParallel(t *testing.T) {
	var orderHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/orders/order-1":
			atomic.AddInt64(&orderHits, 1)
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"status": "success",
				"order": map[string]any{
					"external_id": "order-1",
					"invoices":    []any{map[string]any{"status": "UNPAID"}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:          server.URL,
		APIKey:           "api-key",
		OrderExternalID:  "order-1",
		ExpectStatus:     "UNPAID",
		OrderRequests:    8,
		OrderConcurrency: 4,
		OrderMaxLatency:  time.Second,
		Timeout:          time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := atomic.LoadInt64(&orderHits); got != 9 {
		t.Fatalf("unexpected order hit count: %d", got)
	}
	if !strings.Contains(out.String(), "check=order_lookup_parallel requests=8 concurrency=4") {
		t.Fatalf("missing parallel order check output:\n%s", out.String())
	}
}

func TestRunnerChecksOrderListInParallel(t *testing.T) {
	var orderListHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/orders":
			atomic.AddInt64(&orderListHits, 1)
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("limit") != "20" || r.URL.Query().Get("status") != "UNPAID" || r.URL.Query().Get("crypto") != "BTC" {
				t.Errorf("unexpected order list query: %s", r.URL.RawQuery)
			}
			writeTestJSON(w, map[string]any{
				"status": "success",
				"orders": []any{
					map[string]any{
						"external_id": "order-list-1",
						"invoices":    []any{map[string]any{"status": "UNPAID", "crypto": "BTC"}},
						"payouts":     []any{},
					},
					map[string]any{
						"external_id": "order-list-2",
						"invoices":    []any{map[string]any{"status": "UNPAID", "crypto": "BTC"}},
						"payouts":     []any{},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:              server.URL,
		APIKey:               "api-key",
		OrderListCheck:       true,
		OrderListStatus:      "UNPAID",
		OrderListCrypto:      "BTC",
		OrderListLimit:       20,
		OrderListMinResults:  2,
		OrderListRequests:    6,
		OrderListConcurrency: 3,
		OrderListMaxLatency:  time.Second,
		Timeout:              time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := atomic.LoadInt64(&orderListHits); got != 7 {
		t.Fatalf("unexpected order list hit count: %d", got)
	}
	text := out.String()
	for _, want := range []string{
		"check=order_list rows=2 limit=20",
		"check=order_list_parallel requests=6 concurrency=3",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

func TestOrderLookupFailsWhenLatencyExceedsThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/orders/slow-order":
			time.Sleep(25 * time.Millisecond)
			writeTestJSON(w, map[string]any{"order": map[string]any{"external_id": "slow-order"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{
		MainURL:         server.URL,
		APIKey:          "api-key",
		OrderExternalID: "slow-order",
		OrderMaxLatency: time.Millisecond,
		Timeout:         time.Second,
	}
	err := NewRunner(cfg, nil).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeded max") {
		t.Fatalf("expected latency error, got %v", err)
	}
}

func TestLoadConfigParsesNamedWorkerURLs(t *testing.T) {
	clearDeployCheckEnv(t)
	t.Setenv("DEPLOY_CHECK_WORKER_URLS", "btc=http://btc-worker:6000, tron = http://tron-worker:6000, http://xrp-worker:6000")
	cfg := LoadConfigFromEnv()
	if len(cfg.WorkerURLs) != 3 {
		t.Fatalf("unexpected worker URL count: %+v", cfg.WorkerURLs)
	}
	want := []NamedURL{
		{Name: "btc", URL: "http://btc-worker:6000"},
		{Name: "tron", URL: "http://tron-worker:6000"},
		{Name: "worker3", URL: "http://xrp-worker:6000"},
	}
	for i := range want {
		if cfg.WorkerURLs[i] != want[i] {
			t.Fatalf("worker url %d=%+v want %+v", i, cfg.WorkerURLs[i], want[i])
		}
	}
}

func TestLoadConfigLoadsPlanFileAndLetsEnvOverride(t *testing.T) {
	clearDeployCheckEnv(t)
	planPath := writePlanFile(t, map[string]any{
		"main_url":                  "http://plan-main:5000",
		"worker_urls":               map[string]string{"btc": "http://btc-worker:6000", "tron": "http://tron-worker:6000"},
		"api_key":                   "plan-key",
		"admin_username":            "plan-admin",
		"admin_password":            "plan-pass",
		"crypto":                    "btc",
		"cryptos":                   []string{"btc", "ltc", "btc"},
		"coverage_cryptos":          []string{"btc", "trx", "trx"},
		"order_external_id":         "order-from-plan",
		"expect_status":             "unpaid",
		"order_requests":            3,
		"order_concurrency":         2,
		"order_max_latency_ms":      500,
		"order_list_check":          true,
		"order_list_status":         "unpaid",
		"order_list_crypto":         "btc",
		"order_list_limit":          25,
		"order_list_min_results":    2,
		"order_list_requests":       5,
		"order_list_concurrency":    3,
		"order_list_max_latency_ms": 700,
		"main_status_check":         true,
		"mutating":                  true,
		"require_payment_coverage":  true,
		"require_payout_coverage":   true,
		"payment_checks": []map[string]any{{
			"name":        "payment-btc",
			"crypto":      "btc",
			"fiat":        "usd",
			"amount":      "10",
			"external_id": "payment-from-plan",
		}},
		"payout_checks": []map[string]any{{
			"name":        "small-btc",
			"crypto":      "btc",
			"destination": "bc1plan",
			"amount":      "0.0001",
			"external_id": "payout-from-plan",
		}},
		"worker_address_checks": []map[string]any{{
			"name":     "bnb-address",
			"worker":   "bnb",
			"url":      "http://bnb-worker:6000",
			"crypto":   "bnb",
			"username": "worker-user",
			"password": "worker-pass",
		}},
		"report_file":     "plan-report.json",
		"timeout_seconds": 7,
		"source":          "mariadb:wallet.enabled",
		"generated_at":    "2026-06-04T08:00:00Z",
		"notes":           []string{"generated by final-plan"},
		"worker_crypto_map": map[string]string{
			"bnb": "BNB",
		},
		"placeholders": map[string]string{
			"payout_amount": "set final amount",
		},
	})
	t.Setenv("DEPLOY_CHECK_PLAN_FILE", planPath)
	t.Setenv("DEPLOY_CHECK_API_KEY", "env-key")
	t.Setenv("DEPLOY_CHECK_ORDER_CONCURRENCY", "4")
	t.Setenv("DEPLOY_CHECK_MUTATING", "false")
	t.Setenv("DEPLOY_CHECK_REQUIRE_PAYMENT_COVERAGE", "false")
	t.Setenv("DEPLOY_CHECK_REPORT_FILE", "env-report.json")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.MainURL != "http://plan-main:5000" {
		t.Fatalf("unexpected main URL: %s", cfg.MainURL)
	}
	if cfg.APIKey != "env-key" {
		t.Fatalf("env api key should override plan, got %s", cfg.APIKey)
	}
	if cfg.AdminUsername != "plan-admin" || cfg.AdminPassword != "plan-pass" {
		t.Fatalf("admin credentials not loaded from plan: %+v", cfg)
	}
	if cfg.Crypto != "BTC" || strings.Join(cfg.Cryptos, ",") != "BTC,LTC" {
		t.Fatalf("cryptos not normalized: crypto=%s cryptos=%v", cfg.Crypto, cfg.Cryptos)
	}
	if strings.Join(cfg.CoverageCryptos, ",") != "BTC,TRX" {
		t.Fatalf("coverage cryptos not normalized: %+v", cfg.CoverageCryptos)
	}
	if cfg.ExpectStatus != "UNPAID" || cfg.OrderExternalID != "order-from-plan" {
		t.Fatalf("order settings not loaded: %+v", cfg)
	}
	if cfg.OrderRequests != 3 || cfg.OrderConcurrency != 4 || cfg.OrderMaxLatency != 500*time.Millisecond {
		t.Fatalf("order concurrency settings wrong: requests=%d concurrency=%d latency=%s", cfg.OrderRequests, cfg.OrderConcurrency, cfg.OrderMaxLatency)
	}
	if !cfg.OrderListCheck || cfg.OrderListStatus != "UNPAID" || cfg.OrderListCrypto != "BTC" || cfg.OrderListLimit != 25 || cfg.OrderListMinResults != 2 || cfg.OrderListRequests != 5 || cfg.OrderListConcurrency != 3 || cfg.OrderListMaxLatency != 700*time.Millisecond {
		t.Fatalf("order list settings wrong: %+v", cfg)
	}
	if cfg.Timeout != 7*time.Second || !cfg.MainStatusCheck || cfg.Mutating {
		t.Fatalf("plan/env booleans or timeout wrong: timeout=%s main=%v mutating=%v", cfg.Timeout, cfg.MainStatusCheck, cfg.Mutating)
	}
	if cfg.RequirePaymentCoverage || !cfg.RequirePayoutCoverage {
		t.Fatalf("coverage booleans not loaded/overridden: payment=%v payout=%v", cfg.RequirePaymentCoverage, cfg.RequirePayoutCoverage)
	}
	if cfg.ReportFile != "env-report.json" {
		t.Fatalf("env report file should override plan, got %s", cfg.ReportFile)
	}
	if len(cfg.PaymentChecks) != 1 || cfg.PaymentChecks[0].Name != "payment-btc" || cfg.PaymentChecks[0].Crypto != "BTC" || cfg.PaymentChecks[0].Fiat != "USD" {
		t.Fatalf("payment checks not loaded and normalized: %+v", cfg.PaymentChecks)
	}
	if len(cfg.PayoutChecks) != 1 || cfg.PayoutChecks[0].Name != "small-btc" || cfg.PayoutChecks[0].Crypto != "BTC" {
		t.Fatalf("payout checks not loaded and normalized: %+v", cfg.PayoutChecks)
	}
	if len(cfg.WorkerAddressChecks) != 1 || cfg.WorkerAddressChecks[0].Name != "bnb-address" || cfg.WorkerAddressChecks[0].Crypto != "BNB" || cfg.WorkerAddressChecks[0].Username != "worker-user" {
		t.Fatalf("worker address checks not loaded and normalized: %+v", cfg.WorkerAddressChecks)
	}
	workerURLs := map[string]string{}
	for _, worker := range cfg.WorkerURLs {
		workerURLs[worker.Name] = worker.URL
	}
	if workerURLs["btc"] != "http://btc-worker:6000" || workerURLs["tron"] != "http://tron-worker:6000" {
		t.Fatalf("worker URLs not loaded from plan: %+v", cfg.WorkerURLs)
	}
}

func TestLoadConfigSupportsSecretFilesAndRuntimePlaceholders(t *testing.T) {
	dir := t.TempDir()
	writeSecret := func(name string, value string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatalf("write secret %s: %v", name, err)
		}
		return path
	}
	planPath := writePlanFile(t, map[string]any{
		"api_key":                 "replace-with-wallet-api-key",
		"admin_username":          "admin",
		"admin_password":          "replace-with-current-admin-password",
		"admin_update_username":   "admin",
		"admin_update_password":   "replace-with-updated-admin-password",
		"worker_username":         "replace-with-worker-user",
		"worker_password":         "replace-with-worker-password",
		"require_payout_coverage": true,
		"payout_checks": []map[string]any{{
			"name":                     "payout-bnb-usdt",
			"crypto":                   "BNB-USDT",
			"destination_from_payment": "payment-bnb-usdt",
			"amount":                   "replace-with-bnb-usdt-small-value-amount",
		}, {
			"name":                     "payout-trx",
			"crypto":                   "TRX",
			"destination_from_payment": "payment-trx",
			"amount":                   "replace-with-trx-small-value-amount",
		}},
		"worker_address_checks": []map[string]any{{
			"worker":   "bnb",
			"crypto":   "BNB",
			"username": "replace-with-bnb-worker-user",
			"password": "replace-with-bnb-worker-password",
		}, {
			"worker":   "tron",
			"crypto":   "TRX",
			"username": "replace-with-tron-worker-user",
			"password": "replace-with-tron-worker-password",
		}},
	})
	t.Setenv("DEPLOY_CHECK_PLAN_FILE", planPath)
	t.Setenv("DEPLOY_CHECK_API_KEY_FILE", writeSecret("api-key", "api-from-file"))
	t.Setenv("DEPLOY_CHECK_ADMIN_PASSWORD_FILE", writeSecret("admin-password", "admin-from-file"))
	t.Setenv("DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD_FILE", writeSecret("admin-update-password", "admin-update-from-file"))
	t.Setenv("DEPLOY_CHECK_WORKER_USERNAME", "shared-worker")
	t.Setenv("DEPLOY_CHECK_WORKER_PASSWORD_FILE", writeSecret("worker-password", "shared-worker-from-file"))
	t.Setenv("DEPLOY_CHECK_WORKER_USERNAME_TRON", "tron-worker")
	t.Setenv("DEPLOY_CHECK_WORKER_PASSWORD_TRON_FILE", writeSecret("tron-password", "tron-worker-from-file"))
	t.Setenv("DEPLOY_CHECK_PAYOUT_AMOUNT", "0.01")
	t.Setenv("DEPLOY_CHECK_PAYOUT_AMOUNT_TRX", "1")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.APIKey != "api-from-file" || cfg.AdminPassword != "admin-from-file" || cfg.AdminUpdatePassword != "admin-update-from-file" {
		t.Fatalf("secret file overrides not applied: %+v", cfg)
	}
	if cfg.PayoutChecks[0].Amount != "0.01" || cfg.PayoutChecks[1].Amount != "1" {
		t.Fatalf("payout amount overrides not applied: %+v", cfg.PayoutChecks)
	}
	if cfg.WorkerAddressChecks[0].Username != "shared-worker" || cfg.WorkerAddressChecks[0].Password != "shared-worker-from-file" {
		t.Fatalf("shared worker override not applied: %+v", cfg.WorkerAddressChecks[0])
	}
	if cfg.WorkerAddressChecks[1].Username != "tron-worker" || cfg.WorkerAddressChecks[1].Password != "tron-worker-from-file" {
		t.Fatalf("specific worker override not applied: %+v", cfg.WorkerAddressChecks[1])
	}
}

func TestRunnerRunsFromPlanFile(t *testing.T) {
	clearDeployCheckEnv(t)
	var orderHits int64
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/orders/order-from-plan":
			atomic.AddInt64(&orderHits, 1)
			if r.Header.Get("X-Shkeeper-Api-Key") != "plan-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"order": map[string]any{
					"external_id": "order-from-plan",
					"invoices":    []any{map[string]any{"status": "UNPAID"}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	worker := newDeployCheckWorkerServer(t, "BTC", true)
	defer worker.Close()

	planPath := writePlanFile(t, map[string]any{
		"main_url":             main.URL,
		"worker_urls":          []map[string]string{{"name": "btc", "url": worker.URL}},
		"api_key":              "plan-key",
		"order_external_id":    "order-from-plan",
		"expect_status":        "UNPAID",
		"order_requests":       3,
		"order_concurrency":    2,
		"order_max_latency_ms": 1000,
		"timeout_seconds":      1,
	})
	t.Setenv("DEPLOY_CHECK_PLAN_FILE", planPath)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	var out bytes.Buffer
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"check=order_lookup ok",
		"check=order_lookup_parallel requests=3 concurrency=2",
		"check=worker_healthz name=btc ok",
		"check=worker_readyz name=btc ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if got := atomic.LoadInt64(&orderHits); got != 4 {
		t.Fatalf("unexpected order hit count: %d", got)
	}
}

func TestRunnerWritesSuccessReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	runner := NewRunner(Config{MainURL: server.URL, Timeout: time.Second}, nil)
	err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	reportPath := filepath.Join(t.TempDir(), "deploy-check-report.json")
	if err := runner.WriteReport(reportPath, err); err != nil {
		t.Fatalf("WriteReport() error = %v", err)
	}
	report := readReportFile(t, reportPath)
	if report.Status != "ok" || report.Error != "" {
		t.Fatalf("unexpected success report status=%s error=%s", report.Status, report.Error)
	}
	if report.DurationMS < 0 || report.StartedAt.IsZero() || report.FinishedAt.IsZero() {
		t.Fatalf("report timing was not populated: %+v", report)
	}
	statuses := reportStatuses(report)
	if statuses["main_healthz"] != "ok" || statuses["main_readyz"] != "ok" || statuses["order_lookup"] != "skipped" {
		t.Fatalf("unexpected report checks: %+v", report.Checks)
	}
}

func TestRunnerWritesFailureReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			w.WriteHeader(http.StatusServiceUnavailable)
			writeTestJSON(w, map[string]any{"status": "error"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	runner := NewRunner(Config{MainURL: server.URL, Timeout: time.Second}, nil)
	err := runner.Run(context.Background())
	if err == nil {
		t.Fatalf("expected Run() error")
	}
	reportPath := filepath.Join(t.TempDir(), "deploy-check-report.json")
	if writeErr := runner.WriteReport(reportPath, err); writeErr != nil {
		t.Fatalf("WriteReport() error = %v", writeErr)
	}
	report := readReportFile(t, reportPath)
	if report.Status != "failed" || !strings.Contains(report.Error, "main_readyz") {
		t.Fatalf("unexpected failure report status=%s error=%s", report.Status, report.Error)
	}
	foundFailure := false
	for _, check := range report.Checks {
		if check.Name == "main_readyz" && check.Status == "failed" && strings.Contains(check.Error, "status=503") {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatalf("failure check was not recorded: %+v", report.Checks)
	}
}

func TestCoveragePlanFailsBeforeMutatingWhenChecksAreMissing(t *testing.T) {
	runner := NewRunner(Config{
		Crypto:                 "BTC",
		Cryptos:                []string{"BTC", "TRX"},
		RequirePaymentCoverage: true,
		PaymentChecks: []PaymentCheck{{
			Crypto: "BTC",
			Amount: "10",
		}},
	}, nil)

	err := runner.checkCoveragePlan()
	if err == nil || !strings.Contains(err.Error(), "payment_coverage") || !strings.Contains(err.Error(), "TRX") {
		t.Fatalf("expected missing TRX payment coverage error, got %v", err)
	}
}

func TestRunnerChecksCoverageBeforeMutatingProbes(t *testing.T) {
	var adminMutations int64
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/server":
			writeTestJSON(w, map[string]any{"server": "ok"})
		case "/api/v1/admin/account":
			atomic.AddInt64(&adminMutations, 1)
			writeTestJSON(w, map[string]any{"status": "success"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	var addressMutations int64
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/BNB/generate-address":
			atomic.AddInt64(&addressMutations, 1)
			writeTestJSON(w, map[string]any{"address": "0xworkeraddress"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()

	err := NewRunner(Config{
		MainURL:                main.URL,
		WorkerURLs:             []NamedURL{{Name: "bnb", URL: worker.URL}},
		Crypto:                 "BTC",
		Cryptos:                []string{"BTC", "TRX"},
		RequirePaymentCoverage: true,
		PaymentChecks:          []PaymentCheck{{Crypto: "BTC", Amount: "10"}},
		AdminUsername:          "admin",
		AdminPassword:          "admin-pass",
		AdminUpdatePassword:    "new-admin-pass",
		Mutating:               true,
		WorkerAddressChecks: []WorkerAddressCheck{{
			Name:     "bnb-address",
			Worker:   "bnb",
			Crypto:   "BNB",
			Username: "worker-user",
			Password: "worker-pass",
		}},
		Timeout: time.Second,
	}, nil).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "payment_coverage") || !strings.Contains(err.Error(), "TRX") {
		t.Fatalf("expected missing TRX payment coverage error, got %v", err)
	}
	if got := atomic.LoadInt64(&adminMutations); got != 0 {
		t.Fatalf("admin mutation ran before coverage failure: %d", got)
	}
	if got := atomic.LoadInt64(&addressMutations); got != 0 {
		t.Fatalf("worker address mutation ran before coverage failure: %d", got)
	}
}

func TestMutatingPlanRejectsPlaceholdersBeforeHTTPChecks(t *testing.T) {
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		writeTestJSON(w, map[string]any{"status": "ok"})
	}))
	defer server.Close()

	err := NewRunner(Config{
		MainURL:                server.URL,
		APIKey:                 "replace-with-wallet-api-key",
		AdminUsername:          "admin",
		AdminPassword:          "replace-with-admin-password",
		AdminUpdatePassword:    "replace-with-new-admin-password",
		Crypto:                 "BTC",
		CoverageCryptos:        []string{"BTC"},
		Mutating:               true,
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  true,
		PaymentChecks: []PaymentCheck{{
			Crypto:           "BTC",
			Amount:           "1",
			ExternalIDPrefix: "deploy-check-btc-invoice",
		}},
		PayoutChecks: []PayoutCheck{{
			Crypto:      "BTC",
			Destination: "replace-with-btc-destination",
			Amount:      "replace-with-btc-amount",
		}},
		WorkerAddressChecks: []WorkerAddressCheck{{
			Worker:   "btc",
			Crypto:   "BTC",
			Username: "replace-with-worker-user",
			Password: "replace-with-worker-password",
		}},
		Timeout: time.Second,
	}, nil).Run(context.Background())

	if err == nil || !strings.Contains(err.Error(), "plan_placeholders") || !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "payout_checks[0].destination") {
		t.Fatalf("expected placeholder guard error, got %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Fatalf("placeholder guard should run before HTTP checks, got %d hits", got)
	}
}

func TestNonMutatingPlanSkipsWriteChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			t.Fatalf("unexpected write-capable request in non-mutating run: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	runner := NewRunner(Config{
		MainURL:             server.URL,
		Crypto:              "BTC",
		Mutating:            false,
		AdminUpdatePassword: "replace-with-new-admin-password",
		PaymentChecks:       []PaymentCheck{{Crypto: "BTC", Amount: "replace-with-btc-amount"}},
		PayoutChecks:        []PayoutCheck{{Crypto: "BTC", DestinationFromPayment: "payment-btc", Amount: "replace-with-btc-amount"}},
		WorkerAddressChecks: []WorkerAddressCheck{{Worker: "btc", Crypto: "BTC", Username: "replace-with-worker-user", Password: "replace-with-worker-password"}},
		Timeout:             time.Second,
	}, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	report := runner.Report(nil)
	for _, name := range []string{"admin_account_roundtrip", "worker_address", "payment_request", "payment_order", "payout_dispatch", "payout_status", "payout_order"} {
		if !reportHasCheckStatus(report, name, "skipped") {
			t.Fatalf("expected skipped %s in report: %+v", name, report.Checks)
		}
	}
}

func reportHasCheckStatus(report Report, name string, status string) bool {
	for _, check := range report.Checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func TestCoveragePlanUsesCoverageCryptosWhenProvided(t *testing.T) {
	runner := NewRunner(Config{
		Crypto:                 "BTC",
		Cryptos:                []string{"BTC"},
		CoverageCryptos:        []string{"BTC", "TRX", "BNB-USDT"},
		RequirePaymentCoverage: true,
		PaymentChecks: []PaymentCheck{
			{Crypto: "BTC", Amount: "10"},
			{Crypto: "TRX", Amount: "10"},
		},
	}, nil)

	err := runner.checkCoveragePlan()
	if err == nil || !strings.Contains(err.Error(), "BNB-USDT") {
		t.Fatalf("expected missing BNB-USDT coverage error, got %v", err)
	}
	report := runner.Report(err)
	if strings.Join(report.CoverageCryptos, ",") != "BTC,TRX,BNB-USDT" {
		t.Fatalf("report did not preserve coverage cryptos: %+v", report.CoverageCryptos)
	}
}

func TestCoveragePlanRecordsCompletePaymentAndPayoutCoverage(t *testing.T) {
	runner := NewRunner(Config{
		Crypto:                 "BTC",
		Cryptos:                []string{"BTC", "TRX"},
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  true,
		PaymentChecks: []PaymentCheck{
			{Crypto: "BTC", Amount: "10"},
			{Crypto: "TRX", Amount: "10"},
		},
		PayoutChecks: []PayoutCheck{
			{Crypto: "BTC", Destination: "bc1dest", Amount: "0.0001"},
			{Crypto: "TRX", Destination: "Tdest", Amount: "1"},
		},
	}, nil)

	if err := runner.checkCoveragePlan(); err != nil {
		t.Fatalf("checkCoveragePlan() error = %v", err)
	}
	statuses := reportStatuses(runner.Report(nil))
	if statuses["payment_coverage"] != "ok" || statuses["payout_coverage"] != "ok" {
		t.Fatalf("coverage ok checks not recorded: %+v", runner.Report(nil).Checks)
	}
}

func TestExamplePlanFileIsValidAndReadOnlyByDefault(t *testing.T) {
	clearDeployCheckEnv(t)
	cfg := defaultConfig()
	if err := applyPlanFile(&cfg, "../../deploy/deploy-check.plan.example.json"); err != nil {
		t.Fatalf("example plan should decode: %v", err)
	}
	normalizeConfig(&cfg)
	if cfg.Mutating {
		t.Fatalf("example plan must not enable mutating checks by default")
	}
	if cfg.RequirePaymentCoverage || cfg.RequirePayoutCoverage {
		t.Fatalf("example plan must not require mutating coverage by default")
	}
	if len(cfg.PayoutChecks) == 0 {
		t.Fatalf("example plan should document payout_checks")
	}
	if len(cfg.CoverageCryptos) == 0 {
		t.Fatalf("example plan should document coverage_cryptos")
	}
	if len(cfg.PaymentChecks) == 0 {
		t.Fatalf("example plan should document payment_checks")
	}
	if cfg.PaymentChecks[0].Crypto != "BTC" || cfg.PaymentChecks[0].Amount == "" {
		t.Fatalf("example payment check is incomplete: %+v", cfg.PaymentChecks[0])
	}
	if cfg.PayoutChecks[0].Crypto != "BTC" || cfg.PayoutChecks[0].Destination == "" || cfg.PayoutChecks[0].Amount == "" {
		t.Fatalf("example payout check is incomplete: %+v", cfg.PayoutChecks[0])
	}
}

func TestExamplePlanCoverageCryptosMatchHKModularDefaults(t *testing.T) {
	clearDeployCheckEnv(t)
	cfg := defaultConfig()
	if err := applyPlanFile(&cfg, "../../deploy/deploy-check.plan.example.json"); err != nil {
		t.Fatalf("example plan should decode: %v", err)
	}
	normalizeConfig(&cfg)
	want := defaultHKComposeCryptos(t)
	got := map[string]struct{}{}
	for _, crypto := range cfg.CoverageCryptos {
		got[crypto] = struct{}{}
	}
	for crypto := range want {
		if _, ok := got[crypto]; !ok {
			t.Fatalf("deploy-check example coverage_cryptos is missing hk default crypto %s", crypto)
		}
	}
	for crypto := range got {
		if _, ok := want[crypto]; !ok {
			t.Fatalf("deploy-check example coverage_cryptos includes %s not present in hk defaults", crypto)
		}
	}
}

func TestFinalCutoverPlanTemplateRequiresFullPaymentPayoutAndWorkerCoverage(t *testing.T) {
	clearDeployCheckEnv(t)
	cfg := defaultConfig()
	if err := applyPlanFile(&cfg, "../../deploy/final-cutover.plan.template.json"); err != nil {
		t.Fatalf("final cutover plan template should decode: %v", err)
	}
	normalizeConfig(&cfg)
	if !cfg.Mutating || !cfg.RequirePaymentCoverage || !cfg.RequirePayoutCoverage || !cfg.MainStatusCheck || !cfg.OrderStatusMatrixCheck {
		t.Fatalf("final cutover plan must enable mutating coverage gates, main status, and order matrix: mutating=%v payment=%v payout=%v main=%v matrix=%v", cfg.Mutating, cfg.RequirePaymentCoverage, cfg.RequirePayoutCoverage, cfg.MainStatusCheck, cfg.OrderStatusMatrixCheck)
	}
	wantCryptos := defaultHKComposeCryptos(t)
	assertCryptoSetMatches(t, "cryptos", cfg.Cryptos, wantCryptos)
	assertCryptoSetMatches(t, "coverage_cryptos", cfg.CoverageCryptos, wantCryptos)

	paymentCryptos := paymentCheckCryptos(cfg.PaymentChecks, cfg.Crypto)
	payoutCryptos := payoutCheckCryptos(cfg.PayoutChecks, cfg.Crypto)
	for crypto := range wantCryptos {
		if _, ok := paymentCryptos[crypto]; !ok {
			t.Fatalf("final cutover plan payment_checks missing %s", crypto)
		}
		if _, ok := payoutCryptos[crypto]; !ok {
			t.Fatalf("final cutover plan payout_checks missing %s", crypto)
		}
	}
	if len(paymentCryptos) != len(wantCryptos) || len(payoutCryptos) != len(wantCryptos) {
		t.Fatalf("final cutover plan has unexpected payment/payout crypto counts: payment=%d payout=%d want=%d", len(paymentCryptos), len(payoutCryptos), len(wantCryptos))
	}

	addressWorkers := map[string]struct{}{}
	for _, check := range cfg.WorkerAddressChecks {
		if check.Worker == "" {
			t.Fatalf("worker address check is missing worker name: %+v", check)
		}
		if check.Username == "" || check.Password == "" {
			t.Fatalf("worker address check is missing credentials: %+v", check)
		}
		addressWorkers[check.Worker] = struct{}{}
	}
	for _, worker := range cfg.WorkerURLs {
		if _, ok := addressWorkers[worker.Name]; !ok {
			t.Fatalf("final cutover plan worker_address_checks missing worker %s", worker.Name)
		}
	}
	if len(addressWorkers) != len(cfg.WorkerURLs) {
		t.Fatalf("final cutover plan worker address count mismatch: got=%d want=%d", len(addressWorkers), len(cfg.WorkerURLs))
	}

	err := NewRunner(cfg, nil).checkMutatingPlaceholders()
	if err == nil || !strings.Contains(err.Error(), "plan_placeholders") {
		t.Fatalf("final cutover template should be blocked until placeholders are replaced, got %v", err)
	}
}

func TestPaymentChecksRequireMutatingFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{
		MainURL: server.URL,
		APIKey:  "api-key",
		Crypto:  "BTC",
		PaymentChecks: []PaymentCheck{{
			Crypto:     "BTC",
			Fiat:       "USD",
			Amount:     "10",
			ExternalID: "payment-check",
		}},
		Timeout: time.Second,
	}
	runner := NewRunner(cfg, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	report := runner.Report(nil)
	for _, name := range []string{"payment_request", "payment_order"} {
		if !reportHasCheckStatus(report, name, "skipped") {
			t.Fatalf("expected skipped %s in report: %+v", name, report.Checks)
		}
	}
}

func TestRunnerCreatesPaymentChecksAndVerifiesOrder(t *testing.T) {
	var paymentHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/payment_request":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode payment body: %v", err)
			}
			if body["external_id"] != "payment-check" || body["fiat"] != "USD" || body["amount"] != "10" || body["callback_url"] != "https://merchant.test/callback" {
				t.Errorf("unexpected payment body: %v", body)
			}
			atomic.AddInt64(&paymentHits, 1)
			writeTestJSON(w, map[string]any{"status": "success", "wallet": "bc1generated", "amount": "0.001"})
		case "/api/v1/orders/payment-check":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"order": map[string]any{
					"external_id": "payment-check",
					"invoices": []any{map[string]any{
						"external_id": "payment-check",
						"crypto":      "BTC",
						"addr":        "bc1generated",
						"status":      "UNPAID",
						"addresses":   []any{map[string]any{"crypto": "BTC", "addr": "bc1generated"}},
					}},
					"payouts": []any{},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:  server.URL,
		APIKey:   "api-key",
		Crypto:   "BTC",
		Mutating: true,
		PaymentChecks: []PaymentCheck{{
			Name:         "small-btc-invoice",
			Crypto:       "BTC",
			Fiat:         "USD",
			Amount:       "10",
			ExternalID:   "payment-check",
			CallbackURL:  "https://merchant.test/callback",
			ExpectStatus: "UNPAID",
		}},
		Timeout: time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if atomic.LoadInt64(&paymentHits) != 1 {
		t.Fatalf("expected one payment request")
	}
	for _, want := range []string{
		"check=payment_request name=small-btc-invoice crypto=BTC external_id=payment-check",
		"check=payment_order name=small-btc-invoice crypto=BTC external_id=payment-check ok",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output:\n%s", want, out.String())
		}
	}
}

func TestPayoutChecksRequireMutatingFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/server":
			writeTestJSON(w, map[string]any{"server": "worker"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{
		MainURL:       server.URL,
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		Crypto:        "BTC",
		PayoutChecks: []PayoutCheck{{
			Crypto:      "BTC",
			Destination: "bc1dest",
			Amount:      "0.0001",
			ExternalID:  "payout-check",
		}},
		Timeout: time.Second,
	}
	runner := NewRunner(cfg, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	report := runner.Report(nil)
	for _, name := range []string{"payout_dispatch", "payout_status", "payout_order"} {
		if !reportHasCheckStatus(report, name, "skipped") {
			t.Fatalf("expected skipped %s in report: %+v", name, report.Checks)
		}
	}
}

func TestRunnerDispatchesPayoutChecksAndVerifiesOrder(t *testing.T) {
	var payoutHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/server":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "admin" || pass != "admin-pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{"server": "worker"})
		case "/api/v1/BTC/payout":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "admin" || pass != "admin-pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode payout body: %v", err)
			}
			if body["destination"] != "bc1dest" || body["amount"] != "0.0001" || body["external_id"] != "payout-check" || body["fee"] != "2" {
				t.Errorf("unexpected payout body: %v", body)
			}
			atomic.AddInt64(&payoutHits, 1)
			writeTestJSON(w, map[string]any{"external_id": "payout-check", "task_id": "task-1", "result": []string{"tx-1"}})
		case "/api/v1/BTC/payout/status":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("external_id") != "payout-check" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeTestJSON(w, map[string]any{
				"external_id": "payout-check",
				"crypto":      "BTC",
				"status":      "IN_PROGRESS",
				"amount":      "0.0001",
			})
		case "/api/v1/orders/payout-check":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"order": map[string]any{
					"external_id": "payout-check",
					"invoices":    []any{},
					"payouts": []any{map[string]any{
						"external_id": "payout-check",
						"crypto":      "BTC",
						"status":      "IN_PROGRESS",
						"txids":       []any{"tx-1"},
					}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:       server.URL,
		APIKey:        "api-key",
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		Crypto:        "BTC",
		Mutating:      true,
		PayoutChecks: []PayoutCheck{{
			Name:         "small-btc",
			Crypto:       "BTC",
			Destination:  "bc1dest",
			Amount:       "0.0001",
			Fee:          "2",
			ExternalID:   "payout-check",
			ExpectStatus: "IN_PROGRESS",
		}},
		Timeout: time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if atomic.LoadInt64(&payoutHits) != 1 {
		t.Fatalf("expected one payout dispatch")
	}
	for _, want := range []string{
		"check=payout_dispatch name=small-btc crypto=BTC external_id=payout-check ok",
		"check=payout_status name=small-btc crypto=BTC external_id=payout-check ok",
		"check=payout_order name=small-btc external_id=payout-check ok",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output:\n%s", want, out.String())
		}
	}
	report := NewRunner(cfg, nil).Report(nil)
	if len(report.Checks) != 0 {
		t.Fatalf("new runner should not have report checks: %+v", report.Checks)
	}
	runner := NewRunner(cfg, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	foundDispatchTxID := false
	foundOrderTxID := false
	for _, check := range runner.Report(nil).Checks {
		if check.Name == "payout_dispatch" && check.Details["task_id"] == "task-1" && check.Details["txids"] == "tx-1" {
			foundDispatchTxID = true
		}
		if check.Name == "payout_order" && check.Details["crypto"] == "BTC" && check.Details["txids"] == "tx-1" {
			foundOrderTxID = true
		}
	}
	if !foundDispatchTxID || !foundOrderTxID {
		t.Fatalf("payout task/txid details were not recorded: %+v", runner.Report(nil).Checks)
	}
}

func TestPayoutCheckCanUsePaymentGeneratedDestination(t *testing.T) {
	var payoutDestination string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/server":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "admin" || pass != "admin-pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{"server": "worker"})
		case "/api/v1/BTC/payment_request":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"status":      "success",
				"external_id": "payment-check",
				"wallet":      "bc1generated",
			})
		case "/api/v1/orders/payment-check":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{
				"order": map[string]any{
					"external_id": "payment-check",
					"invoices": []any{map[string]any{
						"crypto": "BTC",
						"status": "UNPAID",
						"addr":   "bc1generated",
					}},
				},
			})
		case "/api/v1/BTC/payout":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "admin" || pass != "admin-pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode payout body: %v", err)
			}
			payoutDestination = fmt.Sprint(body["destination"])
			if payoutDestination != "bc1generated" || body["amount"] != "0.0001" {
				t.Errorf("unexpected payout body: %v", body)
			}
			writeTestJSON(w, map[string]any{"external_id": "payout-check", "task_id": "task-1"})
		case "/api/v1/BTC/payout/status":
			writeTestJSON(w, map[string]any{"external_id": "payout-check", "crypto": "BTC", "status": "IN_PROGRESS"})
		case "/api/v1/orders/payout-check":
			writeTestJSON(w, map[string]any{
				"order": map[string]any{
					"external_id": "payout-check",
					"payouts":     []any{map[string]any{"crypto": "BTC", "status": "IN_PROGRESS"}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{
		MainURL:       server.URL,
		APIKey:        "api-key",
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		Crypto:        "BTC",
		Mutating:      true,
		PaymentChecks: []PaymentCheck{{
			Name:       "payment-btc",
			Crypto:     "BTC",
			Amount:     "1",
			ExternalID: "payment-check",
		}},
		PayoutChecks: []PayoutCheck{{
			Name:                   "payout-btc",
			Crypto:                 "BTC",
			DestinationFromPayment: "payment-btc",
			Amount:                 "0.0001",
			ExternalID:             "payout-check",
			ExpectStatus:           "IN_PROGRESS",
		}},
		Timeout: time.Second,
	}
	if err := NewRunner(cfg, nil).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if payoutDestination != "bc1generated" {
		t.Fatalf("payout did not use generated payment destination: %s", payoutDestination)
	}
}

func TestRunnerChecksMultipleWorkerReadiness(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	workerA := newDeployCheckWorkerServer(t, "BTC", true)
	defer workerA.Close()
	workerB := newDeployCheckWorkerServer(t, "TRON", true)
	defer workerB.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:    main.URL,
		WorkerURLs: []NamedURL{{Name: "btc", URL: workerA.URL}, {Name: "tron", URL: workerB.URL}},
		Timeout:    time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"check=worker_healthz name=btc ok",
		"check=worker_readyz name=btc ok",
		"check=worker_healthz name=tron ok",
		"check=worker_readyz name=tron ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}

func TestRunnerFailsWhenAnyWorkerIsNotReady(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	readyWorker := newDeployCheckWorkerServer(t, "BTC", true)
	defer readyWorker.Close()
	failingWorker := newDeployCheckWorkerServer(t, "TRON", false)
	defer failingWorker.Close()

	cfg := Config{
		MainURL:    main.URL,
		WorkerURLs: []NamedURL{{Name: "btc", URL: readyWorker.URL}, {Name: "tron", URL: failingWorker.URL}},
		Timeout:    time.Second,
	}
	err := NewRunner(cfg, nil).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "worker_readyz name=tron") {
		t.Fatalf("expected failing worker readiness error, got %v", err)
	}
}

func TestWorkerAddressChecksRequireMutatingFlag(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	worker := newDeployCheckWorkerServer(t, "BNB", true)
	defer worker.Close()

	cfg := Config{
		MainURL: main.URL,
		WorkerURLs: []NamedURL{{
			Name: "bnb",
			URL:  worker.URL,
		}},
		WorkerAddressChecks: []WorkerAddressCheck{{
			Name:     "bnb-address",
			Worker:   "bnb",
			Crypto:   "BNB",
			Username: "worker-user",
			Password: "worker-pass",
		}},
		Timeout: time.Second,
	}
	runner := NewRunner(cfg, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	report := runner.Report(nil)
	if !reportHasCheckStatus(report, "worker_address", "skipped") {
		t.Fatalf("expected skipped worker_address in report: %+v", report.Checks)
	}
}

func TestRunnerGeneratesWorkerAddressChecks(t *testing.T) {
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer main.Close()

	var addressHits int64
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok", "module": "BNB"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready", "module": "BNB"})
		case "/BNB/generate-address":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok || user != "worker-user" || pass != "worker-pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			atomic.AddInt64(&addressHits, 1)
			writeTestJSON(w, map[string]any{"address": "0xworkeraddress"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL: main.URL,
		WorkerURLs: []NamedURL{{
			Name: "bnb",
			URL:  worker.URL,
		}},
		Mutating: true,
		WorkerAddressChecks: []WorkerAddressCheck{{
			Name:     "bnb-address",
			Worker:   "bnb",
			Crypto:   "BNB",
			Username: "worker-user",
			Password: "worker-pass",
		}},
		Timeout: time.Second,
	}
	runner := NewRunner(cfg, &out)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := atomic.LoadInt64(&addressHits); got != 1 {
		t.Fatalf("unexpected worker address hit count: %d", got)
	}
	if !strings.Contains(out.String(), "check=worker_address name=bnb-address crypto=BNB address=0xworkeraddress ok") {
		t.Fatalf("missing worker address output:\n%s", out.String())
	}
	found := false
	for _, check := range runner.Report(nil).Checks {
		if check.Name == "worker_address" && check.Details["worker"] == "bnb" && check.Details["address"] == "0xworkeraddress" {
			found = true
		}
	}
	if !found {
		t.Fatalf("worker address details missing from report: %+v", runner.Report(nil).Checks)
	}
}

func TestOrderStatusMatrixCoversDiscoveredStatusCryptoPairs(t *testing.T) {
	filterHits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/orders":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			status := r.URL.Query().Get("status")
			crypto := r.URL.Query().Get("crypto")
			if status != "" || crypto != "" {
				key := status + "/" + crypto
				filterHits[key]++
				switch key {
				case "PAID/USDT":
					writeTestJSON(w, map[string]any{"status": "success", "orders": []any{
						map[string]any{"external_id": "paid-1", "invoices": []any{map[string]any{"status": "PAID", "crypto": "USDT"}}},
					}})
				case "UNPAID/BNB-USDT":
					writeTestJSON(w, map[string]any{"status": "success", "orders": []any{
						map[string]any{"external_id": "unpaid-1", "invoices": []any{map[string]any{"status": "UNPAID", "crypto": "BNB-USDT"}}},
					}})
				default:
					writeTestJSON(w, map[string]any{"status": "success", "orders": []any{}})
				}
				return
			}
			writeTestJSON(w, map[string]any{
				"status": "success",
				"orders": []any{
					map[string]any{"external_id": "paid-1", "invoices": []any{map[string]any{"status": "PAID", "crypto": "USDT", "txs": []any{map[string]any{"status": "CONFIRMED", "crypto": "USDT"}}}}},
					map[string]any{"external_id": "unpaid-1", "invoices": []any{map[string]any{"status": "UNPAID", "crypto": "BNB-USDT"}}},
				},
				"next_cursor": "",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:                server.URL,
		APIKey:                 "api-key",
		OrderStatusMatrixCheck: true,
		OrderStatusMatrixWant:  []string{"PAID", "UNPAID"},
		Timeout:                time.Second,
	}
	runner := NewRunner(cfg, &out)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filterHits["PAID/USDT"] != 1 || filterHits["UNPAID/BNB-USDT"] != 1 {
		t.Fatalf("unexpected filter hits: %+v", filterHits)
	}
	if !strings.Contains(out.String(), "check=order_status_matrix") || !strings.Contains(out.String(), `statuses="PAID,UNPAID"`) {
		t.Fatalf("missing matrix output:\n%s", out.String())
	}
	found := false
	for _, check := range runner.Report(nil).Checks {
		if check.Name == "order_status_matrix" {
			found = true
			if check.Details["pairs"] != "2" || check.Details["orders"] != "2" {
				t.Fatalf("unexpected matrix details: %+v", check.Details)
			}
		}
	}
	if !found {
		t.Fatalf("order_status_matrix report check missing: %+v", runner.Report(nil).Checks)
	}
}

func TestOrderStatusMatrixRunsAfterMutatingPaymentChecks(t *testing.T) {
	var paymentCreated atomic.Bool
	externalID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/TRX/payment_request":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			externalID = anyString(body["external_id"])
			paymentCreated.Store(true)
			writeTestJSON(w, map[string]any{"status": "success", "wallet": "Tgenerated"})
		case "/api/v1/orders":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !paymentCreated.Load() {
				w.WriteHeader(http.StatusConflict)
				writeTestJSON(w, map[string]any{"error": "matrix ran before payment probe"})
				return
			}
			order := map[string]any{
				"external_id": externalID,
				"invoices": []any{
					map[string]any{"status": "UNPAID", "crypto": "TRX", "addresses": []any{map[string]any{"address": "Tgenerated"}}},
				},
			}
			status := r.URL.Query().Get("status")
			crypto := r.URL.Query().Get("crypto")
			if status != "" || crypto != "" {
				if status == "UNPAID" && crypto == "TRX" {
					writeTestJSON(w, map[string]any{"status": "success", "orders": []any{order}})
					return
				}
				writeTestJSON(w, map[string]any{"status": "success", "orders": []any{}})
				return
			}
			writeTestJSON(w, map[string]any{"status": "success", "orders": []any{order}, "next_cursor": ""})
		default:
			if strings.HasPrefix(r.URL.Path, "/api/v1/orders/") {
				if !paymentCreated.Load() {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				writeTestJSON(w, map[string]any{
					"status": "success",
					"order": map[string]any{
						"external_id": externalID,
						"invoices": []any{
							map[string]any{"status": "UNPAID", "crypto": "TRX", "addresses": []any{map[string]any{"address": "Tgenerated"}}},
						},
					},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(Config{
		MainURL:                server.URL,
		APIKey:                 "api-key",
		Mutating:               true,
		OrderStatusMatrixCheck: true,
		OrderStatusMatrixWant:  []string{"UNPAID"},
		PaymentChecks: []PaymentCheck{{
			Name:             "trx-payment",
			Crypto:           "TRX",
			Fiat:             "USD",
			Amount:           "1",
			ExternalIDPrefix: "matrix-after-payment",
			ExpectStatus:     "UNPAID",
		}},
		Timeout: time.Second,
	}, &out)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `check=order_status_matrix`) || !strings.Contains(out.String(), `cryptos="TRX"`) {
		t.Fatalf("matrix did not include generated payment crypto:\n%s", out.String())
	}
}

func TestOrderCheckRequiresAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			writeTestJSON(w, map[string]any{"status": "ready"})
		}
	}))
	defer server.Close()

	cfg := Config{MainURL: server.URL, OrderExternalID: "order-1", Timeout: time.Second}
	err := NewRunner(cfg, nil).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API_KEY is required") {
		t.Fatalf("expected API key error, got %v", err)
	}
}

func TestMainStatusCheckCoversMultipleCryptos(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/status":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{"name": "BTC", "server": "Synced", "balance_error": ""})
		case "/api/v1/LTC/status":
			if r.Header.Get("X-Shkeeper-Api-Key") != "api-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{"name": "LTC", "server_status": "Sync In Progress (1 blocks behind)"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:         server.URL,
		APIKey:          "api-key",
		Crypto:          "BTC",
		Cryptos:         []string{"BTC", "LTC"},
		MainStatusCheck: true,
		Timeout:         time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "check=main_status crypto=BTC") || !strings.Contains(text, "check=main_status crypto=LTC") {
		t.Fatalf("missing main status checks in output:\n%s", text)
	}
}

func TestMainStatusCheckFailsOnOfflineServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/status":
			writeTestJSON(w, map[string]any{"name": "BTC", "server": "Offline"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{MainURL: server.URL, APIKey: "api-key", Crypto: "BTC", MainStatusCheck: true, Timeout: time.Second}
	err := NewRunner(cfg, nil).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "server is Offline") {
		t.Fatalf("expected offline error, got %v", err)
	}
}

func TestAdminRoundTripRequiresMutatingFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		default:
			writeTestJSON(w, map[string]any{"status": "success"})
		}
	}))
	defer server.Close()

	cfg := Config{
		MainURL:             server.URL,
		AdminUsername:       "admin",
		AdminPassword:       "old-pass",
		AdminUpdatePassword: "new-pass",
		Timeout:             time.Second,
	}
	runner := NewRunner(cfg, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	report := runner.Report(nil)
	if !reportHasCheckStatus(report, "admin_account_roundtrip", "skipped") {
		t.Fatalf("expected skipped admin_account_roundtrip in report: %+v", report.Checks)
	}
}

func TestAdminRoundTripUpdatesAndReverts(t *testing.T) {
	var patchCount int64
	currentUser := "admin"
	currentPass := "old-pass"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok"})
		case "/readyz":
			writeTestJSON(w, map[string]any{"status": "ready"})
		case "/api/v1/BTC/server":
			user, pass, ok := r.BasicAuth()
			if !ok || user != currentUser || pass != currentPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeTestJSON(w, map[string]any{"server": "worker"})
		case "/api/v1/admin/account":
			if r.Method != http.MethodPatch {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			count := atomic.AddInt64(&patchCount, 1)
			user, pass, ok := r.BasicAuth()
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if count == 1 {
				if !ok || user != "admin" || pass != "old-pass" || body["new_password"] != "new-pass" || body["username"] != "admin2" {
					t.Errorf("unexpected forward patch auth/body: user=%s pass=%s body=%v", user, pass, body)
				}
				currentUser = "admin2"
				currentPass = "new-pass"
			} else if count == 2 {
				if !ok || user != "admin2" || pass != "new-pass" || body["new_password"] != "old-pass" || body["username"] != "admin" {
					t.Errorf("unexpected revert patch auth/body: user=%s pass=%s body=%v", user, pass, body)
				}
				currentUser = "admin"
				currentPass = "old-pass"
			}
			writeTestJSON(w, map[string]any{"status": "success"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cfg := Config{
		MainURL:             server.URL,
		AdminUsername:       "admin",
		AdminPassword:       "old-pass",
		AdminUpdateUsername: "admin2",
		AdminUpdatePassword: "new-pass",
		Crypto:              "BTC",
		Mutating:            true,
		Timeout:             time.Second,
	}
	if err := NewRunner(cfg, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if patchCount != 2 {
		t.Fatalf("expected 2 patch requests, got %d", patchCount)
	}
	for _, want := range []string{
		"check=admin_account_updated_auth ok",
		"check=admin_account_reverted_auth ok",
		"check=admin_account_roundtrip ok",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output:\n%s", want, out.String())
		}
	}
}

func writeTestJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func newDeployCheckWorkerServer(t *testing.T, module string, ready bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeTestJSON(w, map[string]any{"status": "ok", "module": module})
		case "/readyz":
			if !ready {
				w.WriteHeader(http.StatusServiceUnavailable)
				writeTestJSON(w, map[string]any{"status": "error", "module": module})
				return
			}
			writeTestJSON(w, map[string]any{"status": "ready", "module": module})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func readReportFile(t *testing.T, path string) Report {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, string(data))
	}
	return report
}

func reportStatuses(report Report) map[string]string {
	out := map[string]string{}
	for _, check := range report.Checks {
		out[check.Name] = check.Status
	}
	return out
}

func writePlanFile(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	path := filepath.Join(t.TempDir(), "deploy-check.plan.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func clearDeployCheckEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DEPLOY_CHECK_PLAN_FILE",
		"DEPLOY_CHECK_MAIN_URL",
		"MAIN_URL",
		"DEPLOY_CHECK_WORKER_URL",
		"WORKER_URL",
		"DEPLOY_CHECK_WORKER_URLS",
		"WORKER_URLS",
		"DEPLOY_CHECK_API_KEY",
		"API_KEY",
		"DEPLOY_CHECK_ADMIN_USERNAME",
		"ADMIN_USERNAME",
		"DEPLOY_CHECK_ADMIN_PASSWORD",
		"ADMIN_PASSWORD",
		"DEPLOY_CHECK_ADMIN_UPDATE_USERNAME",
		"ADMIN_UPDATE_USERNAME",
		"DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD",
		"ADMIN_UPDATE_PASSWORD",
		"DEPLOY_CHECK_WORKER_USERNAME",
		"WORKER_USERNAME",
		"DEPLOY_CHECK_WORKER_PASSWORD",
		"WORKER_PASSWORD",
		"DEPLOY_CHECK_CRYPTO",
		"CRYPTO",
		"DEPLOY_CHECK_CRYPTOS",
		"CRYPTOS",
		"DEPLOY_CHECK_COVERAGE_CRYPTOS",
		"COVERAGE_CRYPTOS",
		"DEPLOY_CHECK_ORDER_EXTERNAL_ID",
		"ORDER_EXTERNAL_ID",
		"DEPLOY_CHECK_EXPECT_STATUS",
		"EXPECT_STATUS",
		"DEPLOY_CHECK_ORDER_REQUESTS",
		"DEPLOY_CHECK_ORDER_CONCURRENCY",
		"DEPLOY_CHECK_ORDER_MAX_LATENCY_MS",
		"DEPLOY_CHECK_ORDER_LIST",
		"DEPLOY_CHECK_ORDER_LIST_STATUS",
		"ORDER_LIST_STATUS",
		"DEPLOY_CHECK_ORDER_LIST_CRYPTO",
		"ORDER_LIST_CRYPTO",
		"DEPLOY_CHECK_ORDER_LIST_LIMIT",
		"DEPLOY_CHECK_ORDER_LIST_MIN_RESULTS",
		"DEPLOY_CHECK_ORDER_LIST_REQUESTS",
		"DEPLOY_CHECK_ORDER_LIST_CONCURRENCY",
		"DEPLOY_CHECK_ORDER_LIST_MAX_LATENCY_MS",
		"DEPLOY_CHECK_ORDER_STATUS_MATRIX",
		"DEPLOY_CHECK_ORDER_STATUS_MATRIX_LIMIT",
		"DEPLOY_CHECK_ORDER_STATUS_MATRIX_PAGES",
		"DEPLOY_CHECK_ORDER_STATUS_MATRIX_MIN",
		"DEPLOY_CHECK_ORDER_STATUS_MATRIX_EXPECT_STATUSES",
		"DEPLOY_CHECK_EXPECT_SERVER_STATUS",
		"EXPECT_SERVER_STATUS",
		"DEPLOY_CHECK_MAIN_STATUS",
		"DEPLOY_CHECK_WORKER_TASK_ID",
		"WORKER_TASK_ID",
		"DEPLOY_CHECK_WORKER_STATUS",
		"DEPLOY_CHECK_MUTATING",
		"DEPLOY_CHECK_REQUIRE_PAYMENT_COVERAGE",
		"DEPLOY_CHECK_REQUIRE_PAYOUT_COVERAGE",
		"DEPLOY_CHECK_REPORT_FILE",
		"DEPLOY_CHECK_TIMEOUT_SECONDS",
		"DEPLOY_CHECK_CONCURRENCY",
		"DEPLOY_CHECK_REQUESTS",
	} {
		t.Setenv(key, "")
	}
}

func assertCryptoSetMatches(t *testing.T, label string, got []string, want map[string]struct{}) {
	t.Helper()
	seen := map[string]struct{}{}
	for _, crypto := range got {
		crypto = strings.ToUpper(strings.TrimSpace(crypto))
		if crypto != "" {
			seen[crypto] = struct{}{}
		}
	}
	for crypto := range want {
		if _, ok := seen[crypto]; !ok {
			t.Fatalf("%s is missing hk default crypto %s", label, crypto)
		}
	}
	for crypto := range seen {
		if _, ok := want[crypto]; !ok {
			t.Fatalf("%s includes %s not present in hk defaults", label, crypto)
		}
	}
}

func defaultHKComposeCryptos(t *testing.T) map[string]struct{} {
	t.Helper()
	body, err := os.ReadFile("../../deploy/hk-16-16.modular.example.yml")
	if err != nil {
		t.Fatalf("read hk modular compose: %v", err)
	}
	pattern := regexp.MustCompile(`SHKEEPER_CRYPTOS:\s*"\$\{SHKEEPER_CRYPTOS:-([^"}]+)\}"`)
	match := pattern.FindStringSubmatch(string(body))
	if match == nil {
		t.Fatalf("hk modular compose does not include default SHKEEPER_CRYPTOS")
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(match[1], ",") {
		crypto := strings.ToUpper(strings.TrimSpace(part))
		if crypto != "" {
			out[crypto] = struct{}{}
		}
	}
	return out
}
