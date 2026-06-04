package releaseaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/cutoveraudit"
	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
	"github.com/Sky-JD/go-shkeeper/internal/finalplan"
	"github.com/Sky-JD/go-shkeeper/internal/postcutover"
)

func TestRunPassesWithStrictFinalArtifacts(t *testing.T) {
	dir := t.TempDir()
	planPath := writeJSON(t, filepath.Join(dir, "plan.json"), finalplan.Plan{
		CoverageCryptos:        []string{"BTC", "TRX"},
		Cryptos:                []string{"BTC", "TRX"},
		Mutating:               true,
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  true,
		OrderStatusMatrixCheck: true,
		WorkerURLs:             map[string]string{"btc": "http://go-btc-worker:6000", "tron": "http://go-tron-worker:6000"},
		PaymentChecks: []deploycheck.PaymentCheck{
			{Name: "payment-btc", Crypto: "BTC", Amount: "1"},
			{Name: "payment-trx", Crypto: "TRX", Amount: "1"},
		},
		PayoutChecks: []deploycheck.PayoutCheck{
			{Name: "payout-btc", Crypto: "BTC", Amount: "0.001", DestinationFromPayment: "payment-btc"},
			{Name: "payout-trx", Crypto: "TRX", Amount: "1", DestinationFromPayment: "payment-trx"},
		},
		WorkerAddressChecks: []deploycheck.WorkerAddressCheck{
			{Worker: "btc", Crypto: "BTC", Username: "worker", Password: "pass"},
			{Worker: "tron", Crypto: "TRX", Username: "worker", Password: "pass"},
		},
	})
	deployPath := writeJSON(t, filepath.Join(dir, "deploy.json"), deploycheck.Report{
		Status:          "ok",
		Mutating:        true,
		CoverageCryptos: []string{"BTC", "TRX"},
		Checks:          []deploycheck.ReportCheck{{Name: "payment_order", Status: "ok"}},
	})
	auditPath := writeJSON(t, filepath.Join(dir, "audit.json"), cutoveraudit.Audit{
		Status:          "pass",
		RequiredCryptos: []string{"BTC", "TRX"},
		Gates: cutoveraudit.Gates{
			ImportSync:        true,
			Preflight:         true,
			OrderStatusMatrix: true,
			PaymentOrder:      true,
			PayoutOrder:       true,
			PayoutTxID:        true,
			WorkerReady:       true,
			WorkerAddress:     true,
		},
		ImportSync:  true,
		Preflight:   true,
		OrderMatrix: true,
		Coverage: map[string]cutoveraudit.Coverage{
			"BTC": {OrderStatusMatrix: true, PaymentOrder: true, PayoutOrder: true, PayoutTxID: true},
			"TRX": {OrderStatusMatrix: true, PaymentOrder: true, PayoutOrder: true, PayoutTxID: true},
		},
	})
	postPath := writeJSON(t, filepath.Join(dir, "post.json"), postcutover.Result{
		Status:             "pass",
		GeneratedAt:        time.Now(),
		CutoverAuditStatus: "pass",
		DeployReports:      map[string]string{deployPath: "ok"},
	})

	report, err := Run(Config{
		PlanFile:               planPath,
		DeployReportFiles:      []string{deployPath},
		CutoverAuditFile:       auditPath,
		PostCutoverFile:        postPath,
		RequireMutating:        true,
		RequireImportSync:      true,
		RequirePreflight:       true,
		RequireOrderMatrix:     true,
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  true,
		RequirePaymentOrder:    true,
		RequirePayoutOrder:     true,
		RequirePayoutTxID:      true,
		RequirePostCutover:     true,
		RequireWorkerReady:     true,
		RequireWorkerAddress:   true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Status != "pass" || len(report.Missing) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if strings.Join(report.CoverageCryptos, ",") != "BTC,TRX" || report.CutoverAuditStatus != "pass" || report.PostCutoverStatus != "pass" {
		t.Fatalf("evidence summary not recorded: %+v", report)
	}
}

func TestRunFailsWhenFinalPayoutTxIDProofIsMissing(t *testing.T) {
	dir := t.TempDir()
	planPath := writeJSON(t, filepath.Join(dir, "plan.json"), finalplan.Plan{
		CoverageCryptos:        []string{"BTC"},
		Cryptos:                []string{"BTC"},
		Mutating:               true,
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  true,
		OrderStatusMatrixCheck: true,
		PaymentChecks:          []deploycheck.PaymentCheck{{Crypto: "BTC", Amount: "1"}},
		PayoutChecks:           []deploycheck.PayoutCheck{{Crypto: "BTC", Amount: "0.001", Destination: "bc1dest"}},
	})
	deployPath := writeJSON(t, filepath.Join(dir, "deploy.json"), deploycheck.Report{Status: "ok", Mutating: true, CoverageCryptos: []string{"BTC"}})
	auditPath := writeJSON(t, filepath.Join(dir, "audit.json"), cutoveraudit.Audit{
		Status:          "pass",
		RequiredCryptos: []string{"BTC"},
		Gates:           cutoveraudit.Gates{ImportSync: true, Preflight: true, OrderStatusMatrix: true, PaymentOrder: true, PayoutOrder: true, WorkerReady: true},
		ImportSync:      true,
		Preflight:       true,
		OrderMatrix:     true,
		Coverage:        map[string]cutoveraudit.Coverage{"BTC": {OrderStatusMatrix: true, PaymentOrder: true, PayoutOrder: true}},
	})
	postPath := writeJSON(t, filepath.Join(dir, "post.json"), postcutover.Result{Status: "pass", GeneratedAt: time.Now()})

	report, err := Run(Config{
		PlanFile:               planPath,
		DeployReportFiles:      []string{deployPath},
		CutoverAuditFile:       auditPath,
		PostCutoverFile:        postPath,
		RequirePayoutTxID:      true,
		RequirePostCutover:     true,
		RequireImportSync:      true,
		RequirePreflight:       true,
		RequireOrderMatrix:     true,
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  true,
		RequirePaymentOrder:    true,
		RequirePayoutOrder:     true,
		RequireWorkerReady:     true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	reasons := strings.Join(report.Missing, "\n")
	if report.Status != "fail" || !strings.Contains(reasons, "payout_txid gate was not enabled") || !strings.Contains(reasons, "crypto BTC missing payout txid") {
		t.Fatalf("expected payout txid failures, got %+v", report)
	}
}

func TestRunAllowsExplicitNonProductionPayoutRelaxation(t *testing.T) {
	dir := t.TempDir()
	planPath := writeJSON(t, filepath.Join(dir, "plan.json"), finalplan.Plan{
		CoverageCryptos:        []string{"TRX"},
		Cryptos:                []string{"TRX"},
		Mutating:               true,
		RequirePaymentCoverage: true,
		OrderStatusMatrixCheck: true,
		WorkerURLs:             map[string]string{"tron": "http://go-tron-worker:6000"},
		PaymentChecks:          []deploycheck.PaymentCheck{{Name: "payment-trx", Crypto: "TRX", Amount: "1"}},
		WorkerAddressChecks:    []deploycheck.WorkerAddressCheck{{Worker: "tron", Crypto: "TRX", Username: "worker", Password: "pass"}},
	})
	deployPath := writeJSON(t, filepath.Join(dir, "deploy.json"), deploycheck.Report{Status: "ok", Mutating: true, CoverageCryptos: []string{"TRX"}})
	auditPath := writeJSON(t, filepath.Join(dir, "audit.json"), cutoveraudit.Audit{
		Status:          "pass",
		RequiredCryptos: []string{"TRX"},
		Gates:           cutoveraudit.Gates{ImportSync: true, Preflight: true, OrderStatusMatrix: true, PaymentOrder: true, WorkerReady: true, WorkerAddress: true},
		ImportSync:      true,
		Preflight:       true,
		OrderMatrix:     true,
		Coverage:        map[string]cutoveraudit.Coverage{"TRX": {OrderStatusMatrix: true, PaymentOrder: true}},
	})

	report, err := Run(Config{
		PlanFile:               planPath,
		DeployReportFiles:      []string{deployPath},
		CutoverAuditFile:       auditPath,
		RequireMutating:        true,
		RequireImportSync:      true,
		RequirePreflight:       true,
		RequireOrderMatrix:     true,
		RequirePaymentCoverage: true,
		RequirePayoutCoverage:  false,
		RequirePaymentOrder:    true,
		RequirePayoutOrder:     false,
		RequirePayoutTxID:      false,
		RequirePostCutover:     false,
		RequireWorkerReady:     true,
		RequireWorkerAddress:   true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Status != "pass" || len(report.Missing) != 0 {
		t.Fatalf("unexpected relaxed staging report: %+v", report)
	}
	if report.Gates["payout_coverage"] || report.Gates["payout_order"] || report.Gates["payout_txid"] || report.Gates["post_cutover"] {
		t.Fatalf("relaxed gates were not recorded: %+v", report.Gates)
	}
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
