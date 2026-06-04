package postcutover

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/cutoveraudit"
	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
)

func TestVerifyPassesWithReportsAuditReadyEndpointsAndInventory(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), passingAudit())
	inventoryPath := filepath.Join(dir, "containers.jsonl")
	if err := os.WriteFile(inventoryPath, []byte("{\"Names\":\"shkeeper\",\"Image\":\"ghcr.io/sky-jd/go-shkeeper:prod\"}\n{\"Names\":\"btc-worker\",\"Image\":\"ghcr.io/sky-jd/go-shkeeper:prod\"}\n"), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	result, err := Verify(context.Background(), Config{
		MainURL:                  main.URL,
		WorkerURLs:               []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles:        []string{reportPath},
		CutoverAuditFile:         auditPath,
		ContainerInventoryFile:   inventoryPath,
		ExpectedContainers:       []ContainerExpectation{{Name: "shkeeper", Image: "go-shkeeper"}, {Name: "btc-worker", Image: "go-shkeeper"}},
		ForbiddenContainers:      []string{"bnb-shkeeper", "tron_tasks"},
		ExpectedContainerImages:  []string{"go-shkeeper"},
		ForbiddenContainerImages: []string{"vsyshost/bnb-shkeeper"},
		Timeout:                  time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "pass" || len(result.Missing) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.Workers["btc"] || result.CutoverAuditStatus != "pass" || result.DeployReports[reportPath] != "ok" {
		t.Fatalf("evidence was not recorded: %+v", result)
	}
	if result.ObservedContainers["shkeeper"] != "ghcr.io/sky-jd/go-shkeeper:prod" {
		t.Fatalf("container inventory was not recorded: %+v", result.ObservedContainers)
	}
}

func TestVerifyFailsWhenOldImageIsStillRunning(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), passingAudit())
	inventoryPath := filepath.Join(dir, "containers.jsonl")
	if err := os.WriteFile(inventoryPath, []byte("{\"Names\":\"bnb-shkeeper\",\"Image\":\"vsyshost/bnb-shkeeper:1.1.9\"}\n"), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	result, err := Verify(context.Background(), Config{
		MainURL:                  main.URL,
		WorkerURLs:               []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles:        []string{reportPath},
		CutoverAuditFile:         auditPath,
		ContainerInventoryFile:   inventoryPath,
		ForbiddenContainerImages: []string{"vsyshost/bnb-shkeeper"},
		Timeout:                  time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "fail" {
		t.Fatalf("expected fail result, got %+v", result)
	}
	if !strings.Contains(strings.Join(result.Missing, "\n"), "forbidden image still running") {
		t.Fatalf("missing reason does not mention forbidden image: %+v", result.Missing)
	}
}

func TestVerifyFailsWhenExpectedContainerRunsWrongImage(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), passingAudit())
	inventoryPath := filepath.Join(dir, "containers.jsonl")
	if err := os.WriteFile(inventoryPath, []byte("{\"Names\":\"shkeeper\",\"Image\":\"ghcr.io/sky-jd/shkeeper.io-zh:zh-v2.5.7\"}\n{\"Names\":\"btc-worker\",\"Image\":\"ghcr.io/sky-jd/go-shkeeper:prod\"}\n"), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	result, err := Verify(context.Background(), Config{
		MainURL:                main.URL,
		WorkerURLs:             []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles:      []string{reportPath},
		CutoverAuditFile:       auditPath,
		ContainerInventoryFile: inventoryPath,
		ExpectedContainers:     []ContainerExpectation{{Name: "shkeeper", Image: "go-shkeeper"}, {Name: "btc-worker", Image: "go-shkeeper"}},
		ExpectedContainerImages: []string{
			"go-shkeeper",
		},
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	reasons := strings.Join(result.Missing, "\n")
	if result.Status != "fail" || !strings.Contains(reasons, "expected container image mismatch: shkeeper") {
		t.Fatalf("expected shkeeper image mismatch, got %+v", result)
	}
}

func TestVerifyFailsWhenForbiddenContainerNameIsStillRunning(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), passingAudit())
	inventoryPath := filepath.Join(dir, "containers.jsonl")
	if err := os.WriteFile(inventoryPath, []byte("{\"Names\":\"shkeeper\",\"Image\":\"ghcr.io/sky-jd/go-shkeeper:prod\"}\n{\"Names\":\"bnb-shkeeper\",\"Image\":\"custom/migrated-sidecar:latest\"}\n"), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	result, err := Verify(context.Background(), Config{
		MainURL:                main.URL,
		WorkerURLs:             []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles:      []string{reportPath},
		CutoverAuditFile:       auditPath,
		ContainerInventoryFile: inventoryPath,
		ExpectedContainers:     []ContainerExpectation{{Name: "shkeeper", Image: "go-shkeeper"}},
		ForbiddenContainers:    []string{"bnb-shkeeper"},
		Timeout:                time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "fail" || !strings.Contains(strings.Join(result.Missing, "\n"), "forbidden container still running: bnb-shkeeper") {
		t.Fatalf("expected forbidden container failure, got %+v", result)
	}
}

func TestLoadConfigFromEnvParsesContainerChecks(t *testing.T) {
	t.Setenv("POST_CUTOVER_EXPECTED_CONTAINERS", "/shkeeper=go-shkeeper,bnb-worker=go-shkeeper")
	t.Setenv("POST_CUTOVER_FORBIDDEN_CONTAINERS", "/bnb-shkeeper,tron_tasks")
	t.Setenv("POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID", "true")

	cfg := LoadConfigFromEnv()
	if len(cfg.ExpectedContainers) != 2 {
		t.Fatalf("unexpected expected containers: %+v", cfg.ExpectedContainers)
	}
	if cfg.ExpectedContainers[0] != (ContainerExpectation{Name: "shkeeper", Image: "go-shkeeper"}) {
		t.Fatalf("first expected container was not normalized: %+v", cfg.ExpectedContainers[0])
	}
	if strings.Join(cfg.ForbiddenContainers, ",") != "bnb-shkeeper,tron_tasks" {
		t.Fatalf("unexpected forbidden containers: %+v", cfg.ForbiddenContainers)
	}
	if !cfg.RequireAuditOrderMatrix || !cfg.RequireAuditPayoutTxID {
		t.Fatalf("unexpected audit gate defaults/options: %+v", cfg)
	}
}

func TestVerifyFailsOnFailedCutoverAudit(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), cutoveraudit.Audit{Status: "fail", Missing: []string{"crypto BTC: missing payout_order"}})

	result, err := Verify(context.Background(), Config{
		MainURL:           main.URL,
		WorkerURLs:        []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles: []string{reportPath},
		CutoverAuditFile:  auditPath,
		Timeout:           time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "fail" || !strings.Contains(strings.Join(result.Missing, "\n"), "missing payout_order") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyRequiresLiveMainAndWorkerURLs(t *testing.T) {
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), passingAudit())

	result, err := Verify(context.Background(), Config{
		DeployReportFiles: []string{reportPath},
		CutoverAuditFile:  auditPath,
		Timeout:           time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	reasons := strings.Join(result.Missing, "\n")
	if result.Status != "fail" || !strings.Contains(reasons, "POST_CUTOVER_MAIN_URL") || !strings.Contains(reasons, "POST_CUTOVER_WORKER_URLS") {
		t.Fatalf("expected missing live URL failures, got %+v", result)
	}
}

func TestVerifyFailsWhenCutoverAuditDidNotProveOrderMatrix(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), cutoveraudit.Audit{
		Status: "pass",
		Gates:  cutoveraudit.Gates{PaymentOrder: true},
	})

	result, err := Verify(context.Background(), Config{
		MainURL:                 main.URL,
		WorkerURLs:              []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles:       []string{reportPath},
		CutoverAuditFile:        auditPath,
		RequireAuditOrderMatrix: true,
		Timeout:                 time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "fail" || !strings.Contains(strings.Join(result.Missing, "\n"), "order_status_matrix gate was not proven") {
		t.Fatalf("expected missing order matrix audit failure, got %+v", result)
	}
}

func TestVerifyCanRequirePayoutTxIDGate(t *testing.T) {
	main := readyServer(t)
	defer main.Close()
	worker := readyServer(t)
	defer worker.Close()
	dir := t.TempDir()
	reportPath := writePostJSON(t, filepath.Join(dir, "deploy-report.json"), deploycheck.Report{Status: "ok"})
	auditPath := writePostJSON(t, filepath.Join(dir, "cutover-audit.json"), cutoveraudit.Audit{
		Status:      "pass",
		Gates:       cutoveraudit.Gates{OrderStatusMatrix: true, PayoutTxID: true},
		OrderMatrix: true,
		Coverage:    map[string]cutoveraudit.Coverage{"BTC": {PayoutTxID: false}},
	})

	result, err := Verify(context.Background(), Config{
		MainURL:                 main.URL,
		WorkerURLs:              []NamedURL{{Name: "btc", URL: worker.URL}},
		DeployReportFiles:       []string{reportPath},
		CutoverAuditFile:        auditPath,
		RequireAuditOrderMatrix: true,
		RequireAuditPayoutTxID:  true,
		Timeout:                 time.Second,
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "fail" || !strings.Contains(strings.Join(result.Missing, "\n"), "crypto BTC missing payout txid") {
		t.Fatalf("expected missing payout txid audit failure, got %+v", result)
	}
}

func readyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writePostHTTPJSON(t, w, map[string]any{"status": "ok"})
		case "/readyz":
			writePostHTTPJSON(t, w, map[string]any{"status": "ready"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func passingAudit() cutoveraudit.Audit {
	return cutoveraudit.Audit{
		Status:      "pass",
		Gates:       cutoveraudit.Gates{OrderStatusMatrix: true, PaymentOrder: true, WorkerReady: true},
		OrderMatrix: true,
		Coverage:    map[string]cutoveraudit.Coverage{"BTC": {OrderStatusMatrix: true, PaymentOrder: true}},
	}
}

func writePostHTTPJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

func writePostJSON(t *testing.T, path string, payload any) string {
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
