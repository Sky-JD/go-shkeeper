package goalaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsBlockedWhenFinalEvidenceIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	runtimePath := writeJSON(t, dir, "runtime.json", runtimeAuditReport{
		Status:            "ok",
		ForbiddenCommands: []string{"python", "python3", "pip", "pip3", "sqlite3"},
		RequiredFiles: []string{
			"/app/shkeeper",
			"/app/chain-worker",
			"/app/admin-account",
			"/app/deploy-check",
			"/app/release-audit",
			"/app/runtime-audit",
			"/app/goal-audit",
		},
	})
	preflightPath := writeJSON(t, dir, "preflight.json", preflightReport{
		Status:             "pass",
		DatabaseConfigured: true,
		OrderIndexRows:     32,
		OrderQueryPlans: []orderQueryPlan{
			{Name: "order_index_sort", Key: "ix_order_index_sort"},
			{Name: "invoice_status", Key: "ix_invoice_order_status_updated"},
		},
		EnabledWalletCount: 5,
		EnabledCryptos:     []string{"BNB", "BNB-USDT", "TRX", "USDC", "USDT"},
	})
	readinessPath := writeJSON(t, dir, "readiness.json", readinessReport{
		Status:             "blocked",
		CoverageCryptos:    []string{"BNB", "BNB-USDT", "TRX", "USDC", "USDT"},
		Workers:            []string{"bnb", "tron"},
		PaymentCheckCount:  5,
		PayoutCheckCount:   5,
		WorkerAddressCount: 2,
		OrderStatusMatrix:  true,
		PlaceholderGroups: map[string][]string{
			"admin_credentials":  {"admin_password", "admin_update_password"},
			"worker_credentials": {"worker_address_checks[0].username"},
			"payout_amounts":     {"payout_checks[0].amount"},
		},
	})
	inventoryPath := writeText(t, dir, "inventory.jsonl", `{"Names":"shkeeper","Image":"ghcr.io/sky-jd/shkeeper.io-zh:zh-v2.5.7"}`+"\n")

	report, err := Run(Config{
		RuntimeAuditFile:       runtimePath,
		PreflightFile:          preflightPath,
		ReadinessFile:          readinessPath,
		ContainerInventoryFile: inventoryPath,
		ForbiddenContainers:    []string{"shkeeper"},
		ExpectedGoImage:        "go-shkeeper",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Status != StatusBlocked {
		t.Fatalf("expected blocked report, got %+v", report)
	}
	for _, name := range []string{"go_runtime_no_python_sqlite", "mariadb_only_storage", "complete_order_query", "admin_account_password_change", "production_go_cutover"} {
		if requirementByName(report, name).Name == "" {
			t.Fatalf("missing requirement %s in %+v", name, report.Requirements)
		}
	}
	if requirementByName(report, "go_runtime_no_python_sqlite").Status != StatusPass {
		t.Fatalf("runtime evidence should pass: %+v", requirementByName(report, "go_runtime_no_python_sqlite"))
	}
	if requirementByName(report, "admin_account_password_change").Status != StatusBlocked {
		t.Fatalf("admin requirement should be blocked: %+v", requirementByName(report, "admin_account_password_change"))
	}
	if requirementByName(report, "production_go_cutover").Status != StatusBlocked {
		t.Fatalf("production cutover should be blocked: %+v", requirementByName(report, "production_go_cutover"))
	}
}

func TestRunPassesWithStrictFinalEvidence(t *testing.T) {
	dir := t.TempDir()
	runtimePath := writeJSON(t, dir, "runtime.json", runtimeAuditReport{
		Status:            "ok",
		ForbiddenCommands: []string{"python", "python3", "pip", "pip3", "sqlite3"},
		RequiredFiles: []string{
			"/app/shkeeper",
			"/app/chain-worker",
			"/app/admin-account",
			"/app/deploy-check",
			"/app/release-audit",
			"/app/runtime-audit",
			"/app/goal-audit",
		},
	})
	preflightPath := writeJSON(t, dir, "preflight.json", preflightReport{
		Status:             "pass",
		DatabaseConfigured: true,
		OrderIndexRows:     32,
		OrderQueryPlans: []orderQueryPlan{
			{Name: "order_index_sort", Key: "ix_order_index_sort"},
			{Name: "invoice_status", Key: "ix_invoice_order_status_updated"},
			{Name: "payout_status", Key: "ix_payout_order_status_updated"},
		},
		EnabledWalletCount: 2,
		EnabledCryptos:     []string{"BNB-USDT", "TRX"},
	})
	readinessPath := writeJSON(t, dir, "readiness.json", readinessReport{
		Status:              "ready",
		CoverageCryptos:     []string{"BNB-USDT", "TRX"},
		Workers:             []string{"bnb", "tron"},
		PaymentCheckCount:   2,
		PayoutCheckCount:    2,
		WorkerAddressCount:  2,
		OrderStatusMatrix:   true,
		ReadyForDeployCheck: true,
	})
	deployPath := writeJSON(t, dir, "deploy.json", deployReport{
		Status:          "ok",
		CoverageCryptos: []string{"BNB-USDT", "TRX"},
		Mutating:        true,
		Checks: []reportCheck{
			{Name: "order_status_matrix", Status: "ok"},
			{Name: "admin_account_roundtrip", Status: "ok"},
			{Name: "worker_address", Status: "ok"},
			{Name: "order_lookup_parallel", Status: "ok"},
			{Name: "order_list_parallel", Status: "ok"},
		},
	})
	releasePath := writeJSON(t, dir, "release.json", releaseAuditReport{
		Status:          "pass",
		CoverageCryptos: []string{"BNB-USDT", "TRX"},
		Gates:           strictReleaseGates(),
	})
	postPath := writeJSON(t, dir, "post.json", postCutoverReport{
		Status:             "pass",
		ObservedContainers: map[string]string{"go-shkeeper": "go-shkeeper:final"},
	})
	inventoryPath := writeText(t, dir, "inventory.jsonl", `{"Names":"go-shkeeper","Image":"go-shkeeper:final"}`+"\n")
	statsPath := writeText(t, dir, "stats.jsonl", `{"Name":"go-shkeeper","MemUsage":"82.5MiB / 7.7GiB"}`+"\n")

	report, err := Run(Config{
		RuntimeAuditFile:       runtimePath,
		PreflightFile:          preflightPath,
		ReadinessFile:          readinessPath,
		DeployReportFiles:      []string{deployPath},
		ReleaseAuditFile:       releasePath,
		PostCutoverFile:        postPath,
		ContainerInventoryFile: inventoryPath,
		ContainerStatsFile:     statsPath,
		ForbiddenContainers:    []string{"shkeeper", "bnb-shkeeper"},
		ExpectedGoImage:        "go-shkeeper",
		MaxContainerMemoryMB:   128,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Status != StatusPass {
		t.Fatalf("expected pass report, got %+v", report)
	}
	for _, req := range report.Requirements {
		if req.Status != StatusPass {
			t.Fatalf("requirement should pass: %+v", req)
		}
	}
}

func TestRunFailsWhenReleaseAuditGatesWereRelaxed(t *testing.T) {
	dir := t.TempDir()
	releasePath := writeJSON(t, dir, "release.json", releaseAuditReport{
		Status: "pass",
		Gates:  map[string]bool{"order_status_matrix": true, "payment_order": true},
	})

	report, err := Run(Config{
		RuntimeAuditFile:     writeJSON(t, dir, "runtime.json", runtimeAuditReport{Status: "ok", RequiredFiles: []string{"/app/shkeeper", "/app/chain-worker", "/app/admin-account", "/app/deploy-check", "/app/release-audit", "/app/runtime-audit", "/app/goal-audit"}}),
		PreflightFile:        writeJSON(t, dir, "preflight.json", preflightReport{Status: "pass", DatabaseConfigured: true, OrderIndexRows: 1, OrderQueryPlans: []orderQueryPlan{{Name: "order_index_sort", Key: "ix_order_index_sort"}}}),
		ReadinessFile:        writeJSON(t, dir, "readiness.json", readinessReport{OrderStatusMatrix: true}),
		ReleaseAuditFile:     releasePath,
		RequireStrictRelease: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	release := requirementByName(report, "strict_release_audit")
	if release.Status != StatusFail || !strings.Contains(strings.Join(release.Missing, "\n"), "release audit gate not strict: payout_txid") {
		t.Fatalf("expected relaxed release gate failure, got %+v", release)
	}
}

func TestRunBlocksWhenContainerMemoryEvidenceExceedsLimit(t *testing.T) {
	dir := t.TempDir()
	preflightPath := writeJSON(t, dir, "preflight.json", preflightReport{
		Status:             "pass",
		DatabaseConfigured: true,
		OrderIndexRows:     1,
		OrderQueryPlans:    []orderQueryPlan{{Name: "order_index_sort", Key: "ix_order_index_sort"}},
	})
	deployPath := writeJSON(t, dir, "deploy.json", deployReport{
		Status: "ok",
		Checks: []reportCheck{
			{Name: "order_lookup_parallel", Status: "ok"},
			{Name: "order_list_parallel", Status: "ok"},
		},
	})
	statsPath := writeText(t, dir, "stats.jsonl", `{"Name":"go-shkeeper","MemUsage":"900MiB / 7.7GiB"}`+"\n")

	report, err := Run(Config{
		RuntimeAuditFile:     writeJSON(t, dir, "runtime.json", runtimeAuditReport{Status: "ok", RequiredFiles: []string{"/app/shkeeper", "/app/chain-worker", "/app/admin-account", "/app/deploy-check", "/app/release-audit", "/app/runtime-audit", "/app/goal-audit"}}),
		PreflightFile:        preflightPath,
		ReadinessFile:        writeJSON(t, dir, "readiness.json", readinessReport{OrderStatusMatrix: true}),
		DeployReportFiles:    []string{deployPath},
		ContainerStatsFile:   statsPath,
		MaxContainerMemoryMB: 512,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	perf := requirementByName(report, "high_concurrency_fast_api")
	if perf.Status != StatusBlocked || !strings.Contains(strings.Join(perf.Missing, "\n"), "exceeds limit 512MiB") {
		t.Fatalf("expected memory limit blocker, got %+v", perf)
	}
}

func strictReleaseGates() map[string]bool {
	out := map[string]bool{}
	for _, gate := range requiredReleaseGates() {
		out[gate] = true
	}
	return out
}

func writeJSON(t *testing.T, dir string, name string, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	data = append(data, '\n')
	return writeBytes(t, dir, name, data)
}

func writeText(t *testing.T, dir string, name string, value string) string {
	t.Helper()
	return writeBytes(t, dir, name, []byte(value))
}

func writeBytes(t *testing.T, dir string, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func requirementByName(report Report, name string) Requirement {
	for _, req := range report.Requirements {
		if req.Name == name {
			return req
		}
	}
	return Requirement{}
}
