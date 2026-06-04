package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigFromEnvNormalizesTablesAndFlags(t *testing.T) {
	t.Setenv("CUTOVER_PREFLIGHT_OUTPUT_FILE", "/tmp/preflight.json")
	t.Setenv("CUTOVER_PREFLIGHT_REQUIRED_TABLES", "wallet, invoice, wallet, ORDER_INDEX")
	t.Setenv("CUTOVER_PREFLIGHT_REQUIRE_ORDER_INDEXES", "false")
	t.Setenv("CUTOVER_PREFLIGHT_REQUIRE_ENABLED_WALLETS", "false")

	cfg := LoadConfigFromEnv()
	if cfg.OutputFile != "/tmp/preflight.json" {
		t.Fatalf("unexpected output file: %s", cfg.OutputFile)
	}
	tables := strings.Join(uniqueLowerNonEmpty(cfg.RequiredTables), ",")
	if tables != "invoice,order_index,wallet" {
		t.Fatalf("unexpected required tables: %s", tables)
	}
	if cfg.RequireEnabledWallets {
		t.Fatalf("enabled wallets gate should be disabled")
	}
	if cfg.RequireOrderIndexes {
		t.Fatalf("order index gate should be disabled")
	}
}

func TestWriteReportAndStatusError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preflight.json")
	report := Report{
		Status:         "blocked",
		RequiredTables: []string{"wallet"},
		MissingTables:  []string{"wallet"},
		RequiredIndexes: []string{
			"order_index.ix_order_index_sort",
		},
		OrderIndexRows: 32,
		Blockers:       []string{"missing required MariaDB tables: wallet"},
	}
	if err := WriteReport(path, report); err != nil {
		t.Fatalf("write report: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(data), `"status": "blocked"`) || !strings.Contains(string(data), `"wallet"`) {
		t.Fatalf("unexpected report JSON: %s", string(data))
	}
	if !strings.Contains(string(data), `"order_index_rows": 32`) || !strings.Contains(string(data), `"order_index.ix_order_index_sort"`) {
		t.Fatalf("report is missing order index evidence: %s", string(data))
	}
	if err := ErrorForStatus(report); err == nil {
		t.Fatalf("blocked report should return an error")
	}
	if err := ErrorForStatus(Report{Status: "pass"}); err != nil {
		t.Fatalf("pass report should not return an error: %v", err)
	}
}

func TestRequiredOrderIndexesIncludesHotOrderPaths(t *testing.T) {
	indexes := strings.Join(requiredOrderIndexes(), ",")
	for _, want := range []string{
		"order_index.ix_order_index_sort",
		"invoice.ix_invoice_order_status_updated",
		"invoice.ix_invoice_list_status_crypto_id",
		"payout.ix_payout_order_status_updated",
	} {
		if !strings.Contains(indexes, want) {
			t.Fatalf("required order indexes missing %s: %s", want, indexes)
		}
	}
}

func TestRunReturnsBlockedReportForBadDatabaseConfig(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "sqlite:////tmp/shkeeper.sqlite")
	report, err := Run(t.Context(), Config{RequiredTables: []string{"wallet"}, RequireEnabledWallets: true}, nil)
	if err != nil {
		t.Fatalf("run preflight: %v", err)
	}
	if report.Status != "blocked" || len(report.Blockers) == 0 {
		t.Fatalf("expected blocked report: %+v", report)
	}
	if !strings.Contains(strings.ToLower(strings.Join(report.Blockers, ",")), "sqlite") {
		t.Fatalf("expected sqlite blocker: %+v", report.Blockers)
	}
	if err := ErrorForStatus(report); err == nil {
		t.Fatalf("expected status error, got %v", err)
	}
}
