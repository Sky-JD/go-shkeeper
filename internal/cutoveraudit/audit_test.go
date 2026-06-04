package cutoveraudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
)

func TestRunPassesWithFullCryptoAndWorkerCoverage(t *testing.T) {
	dir := t.TempDir()
	planPath := writeJSON(t, filepath.Join(dir, "plan.json"), map[string]any{
		"cryptos":     []string{"BTC", "TRX"},
		"worker_urls": map[string]string{"btc": "http://btc-worker:6000", "tron": "http://tron-worker:6000"},
		"api_key":     "ignored-extra-field",
	})
	importPath := writeJSON(t, filepath.Join(dir, "main-import.json"), map[string]any{
		"mode":             "mariadb-upsert",
		"rows":             92,
		"order_index_rows": 32,
		"tables":           map[string]int{"invoice": 32, "wallet": 5},
	})
	preflightPath := writeJSON(t, filepath.Join(dir, "preflight.json"), map[string]any{
		"status":                  "pass",
		"database_url_configured": true,
		"enabled_wallet_count":    5,
		"order_index_rows":        32,
	})
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:  "ok",
		Cryptos: []string{"BTC", "TRX"},
		Checks: []deploycheck.ReportCheck{
			okCheck("main_status", map[string]string{"crypto": "BTC"}),
			okCheck("main_status", map[string]string{"crypto": "TRX"}),
			okCheck("order_status_matrix", map[string]string{"cryptos": "BTC,TRX", "statuses": "PAID,UNPAID"}),
			okCheck("payment_order", map[string]string{"crypto": "BTC"}),
			okCheck("payment_order", map[string]string{"crypto": "TRX"}),
			okCheck("payout_order", map[string]string{"crypto": "BTC", "txids": "btc-tx"}),
			okCheck("payout_order", map[string]string{"crypto": "TRX", "txids": "trx-tx"}),
			okCheck("worker_readyz name=btc", nil),
			okCheck("worker_readyz name=tron", nil),
			okCheck("worker_address", map[string]string{"worker": "btc", "crypto": "BTC", "address": "bc1proof"}),
			okCheck("worker_address", map[string]string{"worker": "tron", "crypto": "TRX", "address": "Tproof"}),
		},
	})

	audit, err := Run(Config{
		PlanFile:             planPath,
		ReportFiles:          []string{reportPath},
		ImportReportFiles:    []string{importPath},
		PreflightReportFiles: []string{preflightPath},
		Cryptos:              []string{"BTC", "TRX"},
		ExpectedWorkers:      []string{"btc", "tron"},
		RequireImportSync:    true,
		RequirePreflight:     true,
		RequireMainStatus:    true,
		RequireOrderMatrix:   true,
		RequirePaymentOrder:  true,
		RequirePayoutOrder:   true,
		RequirePayoutTxID:    true,
		RequireWorkerReady:   true,
		RequireWorkerAddress: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "pass" || len(audit.Missing) != 0 {
		t.Fatalf("unexpected audit result: %+v", audit)
	}
	if !audit.OrderMatrix || !audit.Coverage["BTC"].OrderStatusMatrix || !audit.Coverage["BTC"].PaymentOrder || !audit.Coverage["TRX"].PayoutOrder || !audit.Coverage["TRX"].PayoutTxID || !audit.Workers["tron"] || !audit.WorkerAddresses["tron"] {
		t.Fatalf("coverage was not recorded: %+v", audit)
	}
	if !audit.ImportSync || !audit.Preflight || !audit.Gates.ImportSync || !audit.Gates.Preflight {
		t.Fatalf("import/preflight evidence was not recorded: %+v", audit)
	}
}

func TestRunFailsWhenOrderStatusMatrixIsRequiredButMissing(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:  "ok",
		Cryptos: []string{"BTC", "TRX"},
		Checks: []deploycheck.ReportCheck{
			okCheck("payment_order", map[string]string{"crypto": "BTC"}),
			okCheck("payment_order", map[string]string{"crypto": "TRX"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:         []string{reportPath},
		Cryptos:             []string{"BTC", "TRX"},
		RequireMainStatus:   false,
		RequireOrderMatrix:  true,
		RequirePaymentOrder: true,
		RequirePayoutOrder:  false,
		RequireWorkerReady:  false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "fail" || !strings.Contains(strings.Join(audit.Missing, "\n"), "missing order_status_matrix") {
		t.Fatalf("expected missing order matrix failure, got %+v", audit)
	}
	if audit.OrderMatrix {
		t.Fatalf("order matrix should not be recorded: %+v", audit)
	}
}

func TestRunFailsWhenOrderStatusMatrixDoesNotCoverRequiredCrypto(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:          "ok",
		CoverageCryptos: []string{"BNB-USDT", "TRX"},
		Checks: []deploycheck.ReportCheck{
			okCheck("order_status_matrix", map[string]string{"cryptos": "BNB-USDT", "statuses": "UNPAID"}),
			okCheck("payment_order", map[string]string{"crypto": "BNB-USDT"}),
			okCheck("payment_order", map[string]string{"crypto": "TRX"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:         []string{reportPath},
		RequireMainStatus:   false,
		RequireOrderMatrix:  true,
		RequirePaymentOrder: true,
		RequirePayoutOrder:  false,
		RequireWorkerReady:  false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "fail" || !strings.Contains(strings.Join(audit.Missing, "\n"), "crypto TRX: missing order_status_matrix") {
		t.Fatalf("expected missing TRX matrix coverage, got %+v", audit)
	}
	if !audit.OrderMatrix || !audit.Coverage["BNB-USDT"].OrderStatusMatrix || audit.Coverage["TRX"].OrderStatusMatrix {
		t.Fatalf("unexpected order matrix coverage: %+v", audit)
	}
}

func TestRunFailsWhenStrictImportAndPreflightReportsAreMissing(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:  "ok",
		Cryptos: []string{"BTC"},
		Checks: []deploycheck.ReportCheck{
			okCheck("order_status_matrix", map[string]string{"cryptos": "BTC", "statuses": "UNPAID"}),
			okCheck("payment_order", map[string]string{"crypto": "BTC"}),
			okCheck("payout_order", map[string]string{"crypto": "BTC"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:         []string{reportPath},
		Cryptos:             []string{"BTC"},
		RequireImportSync:   true,
		RequirePreflight:    true,
		RequireMainStatus:   false,
		RequireOrderMatrix:  true,
		RequirePaymentOrder: true,
		RequirePayoutOrder:  true,
		RequireWorkerReady:  false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	reasons := strings.Join(audit.Missing, "\n")
	if audit.Status != "fail" || !strings.Contains(reasons, "missing import report") || !strings.Contains(reasons, "missing preflight report") {
		t.Fatalf("expected missing import/preflight failures, got %+v", audit)
	}
}

func TestRunFailsWhenCryptoCoverageIsMissing(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:  "ok",
		Cryptos: []string{"BTC", "TRX"},
		Checks: []deploycheck.ReportCheck{
			okCheck("main_status", map[string]string{"crypto": "BTC"}),
			okCheck("main_status", map[string]string{"crypto": "TRX"}),
			okCheck("payment_order", map[string]string{"crypto": "BTC"}),
			okCheck("payment_order", map[string]string{"crypto": "TRX"}),
			okCheck("payout_order", map[string]string{"crypto": "BTC"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:         []string{reportPath},
		Cryptos:             []string{"BTC", "TRX"},
		RequireMainStatus:   true,
		RequirePaymentOrder: true,
		RequirePayoutOrder:  true,
		RequireWorkerReady:  false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "fail" {
		t.Fatalf("expected fail audit, got %+v", audit)
	}
	if !strings.Contains(strings.Join(audit.Missing, "\n"), "crypto TRX: missing payout_order") {
		t.Fatalf("missing reason does not mention TRX payout: %+v", audit.Missing)
	}
}

func TestRunUsesCoverageCryptosFromReportWhenPlanIsAbsent(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:          "ok",
		Cryptos:         []string{"BTC"},
		CoverageCryptos: []string{"BTC", "BNB-USDT"},
		Checks: []deploycheck.ReportCheck{
			okCheck("main_status", map[string]string{"crypto": "BTC"}),
			okCheck("payment_order", map[string]string{"crypto": "BTC"}),
			okCheck("payout_order", map[string]string{"crypto": "BTC"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:         []string{reportPath},
		RequireMainStatus:   true,
		RequirePaymentOrder: true,
		RequirePayoutOrder:  true,
		RequireWorkerReady:  false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "fail" || !strings.Contains(strings.Join(audit.RequiredCryptos, ","), "BNB-USDT") {
		t.Fatalf("coverage cryptos from report were not required: %+v", audit)
	}
	if !strings.Contains(strings.Join(audit.Missing, "\n"), "crypto BNB-USDT: missing main_status") {
		t.Fatalf("missing reason does not mention BNB-USDT: %+v", audit.Missing)
	}
}

func TestRunFailsWhenPayoutTxIDIsRequiredButMissing(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:  "ok",
		Cryptos: []string{"BTC"},
		Checks: []deploycheck.ReportCheck{
			okCheck("main_status", map[string]string{"crypto": "BTC"}),
			okCheck("payment_order", map[string]string{"crypto": "BTC"}),
			okCheck("payout_order", map[string]string{"crypto": "BTC"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:         []string{reportPath},
		Cryptos:             []string{"BTC"},
		RequireMainStatus:   true,
		RequirePaymentOrder: true,
		RequirePayoutOrder:  true,
		RequirePayoutTxID:   true,
		RequireWorkerReady:  false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "fail" || !strings.Contains(strings.Join(audit.Missing, "\n"), "missing payout txid") {
		t.Fatalf("expected missing txid failure, got %+v", audit)
	}
}

func TestRunFailsWhenWorkerAddressIsRequiredButMissing(t *testing.T) {
	dir := t.TempDir()
	reportPath := writeReport(t, filepath.Join(dir, "report.json"), deploycheck.Report{
		Status:  "ok",
		Cryptos: []string{"BTC"},
		Checks: []deploycheck.ReportCheck{
			okCheck("worker_readyz name=btc", nil),
			okCheck("worker_readyz name=bnb", nil),
			okCheck("worker_address", map[string]string{"worker": "btc", "crypto": "BTC", "address": "bc1proof"}),
		},
	})

	audit, err := Run(Config{
		ReportFiles:          []string{reportPath},
		Cryptos:              []string{"BTC"},
		ExpectedWorkers:      []string{"btc", "bnb"},
		RequireMainStatus:    false,
		RequirePaymentOrder:  false,
		RequirePayoutOrder:   false,
		RequireWorkerReady:   true,
		RequireWorkerAddress: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if audit.Status != "fail" || !strings.Contains(strings.Join(audit.Missing, "\n"), "worker bnb: missing worker_address") {
		t.Fatalf("expected missing worker address failure, got %+v", audit)
	}
	if !audit.WorkerAddresses["btc"] || audit.WorkerAddresses["bnb"] {
		t.Fatalf("worker address coverage was not recorded correctly: %+v", audit.WorkerAddresses)
	}
}

func TestLoadConfigAcceptsDeployCheckExamplePlan(t *testing.T) {
	clearAuditEnv(t)
	reportPath := writeReport(t, filepath.Join(t.TempDir(), "report.json"), deploycheck.Report{Status: "ok", Cryptos: []string{"BTC"}})
	t.Setenv("CUTOVER_AUDIT_PLAN_FILE", "../../deploy/deploy-check.plan.example.json")
	t.Setenv("CUTOVER_AUDIT_REPORT_FILES", reportPath)
	t.Setenv("CUTOVER_AUDIT_REQUIRE_PAYMENT_ORDER", "false")
	t.Setenv("CUTOVER_AUDIT_REQUIRE_PAYOUT_ORDER", "false")
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if len(cfg.Cryptos) == 0 || !containsString(cfg.Cryptos, "BNB-USDT") || !containsString(cfg.Cryptos, "SOLANA-PYUSD") {
		t.Fatalf("plan coverage cryptos were not loaded: %+v", cfg.Cryptos)
	}
	if len(cfg.ExpectedWorkers) == 0 {
		t.Fatalf("worker names were not loaded from plan")
	}
}

func okCheck(name string, details map[string]string) deploycheck.ReportCheck {
	return deploycheck.ReportCheck{
		Name:      name,
		Status:    "ok",
		Details:   details,
		Timestamp: time.Now(),
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeReport(t *testing.T, path string, report deploycheck.Report) string {
	t.Helper()
	return writeJSON(t, path, report)
}

func writeJSON(t *testing.T, path string, payload any) string {
	t.Helper()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
	return path
}

func clearAuditEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CUTOVER_AUDIT_PLAN_FILE",
		"CUTOVER_AUDIT_REPORT_FILES",
		"CUTOVER_AUDIT_IMPORT_REPORT_FILES",
		"CUTOVER_AUDIT_PREFLIGHT_REPORT_FILES",
		"CUTOVER_AUDIT_OUTPUT_FILE",
		"CUTOVER_AUDIT_CRYPTOS",
		"CUTOVER_AUDIT_COVERAGE_CRYPTOS",
		"CUTOVER_AUDIT_WORKERS",
		"CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC",
		"CUTOVER_AUDIT_REQUIRE_PREFLIGHT",
		"CUTOVER_AUDIT_REQUIRE_MAIN_STATUS",
		"CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX",
		"CUTOVER_AUDIT_REQUIRE_PAYMENT_ORDER",
		"CUTOVER_AUDIT_REQUIRE_PAYOUT_ORDER",
		"CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID",
		"CUTOVER_AUDIT_REQUIRE_WORKER_READY",
		"CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS",
	} {
		t.Setenv(key, "")
	}
}
