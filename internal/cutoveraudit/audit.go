package cutoveraudit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
)

type Config struct {
	PlanFile             string
	ReportFiles          []string
	ImportReportFiles    []string
	PreflightReportFiles []string
	OutputFile           string
	Cryptos              []string
	CoverageCryptos      []string
	ExpectedWorkers      []string
	RequireImportSync    bool
	RequirePreflight     bool
	RequireMainStatus    bool
	RequireOrderMatrix   bool
	RequirePaymentOrder  bool
	RequirePayoutOrder   bool
	RequirePayoutTxID    bool
	RequireWorkerReady   bool
	RequireWorkerAddress bool
}

type Audit struct {
	Status           string              `json:"status"`
	GeneratedAt      time.Time           `json:"generated_at"`
	PlanFile         string              `json:"plan_file,omitempty"`
	ReportFiles      []string            `json:"report_files"`
	ImportReports    []string            `json:"import_reports,omitempty"`
	PreflightReports []string            `json:"preflight_reports,omitempty"`
	RequiredCryptos  []string            `json:"required_cryptos"`
	ExpectedWorkers  []string            `json:"expected_workers,omitempty"`
	Gates            Gates               `json:"gates"`
	ImportSync       bool                `json:"import_sync"`
	Preflight        bool                `json:"preflight"`
	OrderMatrix      bool                `json:"order_status_matrix"`
	Coverage         map[string]Coverage `json:"coverage"`
	Workers          map[string]bool     `json:"workers,omitempty"`
	WorkerAddresses  map[string]bool     `json:"worker_addresses,omitempty"`
	ReportStatuses   map[string]string   `json:"report_statuses"`
	Missing          []string            `json:"missing,omitempty"`
}

type Gates struct {
	ImportSync        bool `json:"import_sync"`
	Preflight         bool `json:"preflight"`
	MainStatus        bool `json:"main_status"`
	OrderStatusMatrix bool `json:"order_status_matrix"`
	PaymentOrder      bool `json:"payment_order"`
	PayoutOrder       bool `json:"payout_order"`
	PayoutTxID        bool `json:"payout_txid"`
	WorkerReady       bool `json:"worker_ready"`
	WorkerAddress     bool `json:"worker_address"`
}

type Coverage struct {
	MainStatus        bool `json:"main_status"`
	OrderStatusMatrix bool `json:"order_status_matrix"`
	PaymentOrder      bool `json:"payment_order"`
	PayoutOrder       bool `json:"payout_order"`
	PayoutTxID        bool `json:"payout_txid"`
}

type planFile struct {
	Crypto          string          `json:"crypto"`
	Cryptos         []string        `json:"cryptos"`
	CoverageCryptos []string        `json:"coverage_cryptos"`
	WorkerURL       string          `json:"worker_url"`
	WorkerURLs      json.RawMessage `json:"worker_urls"`
}

type planNamedURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type importReport struct {
	Mode           string         `json:"mode"`
	Rows           int            `json:"rows"`
	OrderIndexRows int            `json:"order_index_rows"`
	Tables         map[string]int `json:"tables"`
}

type preflightReport struct {
	Status                string   `json:"status"`
	DatabaseURLConfigured bool     `json:"database_url_configured"`
	MissingTables         []string `json:"missing_tables"`
	MissingIndexes        []string `json:"missing_indexes"`
	Blockers              []string `json:"blockers"`
	EnabledWalletCount    int      `json:"enabled_wallet_count"`
	OrderIndexRows        int      `json:"order_index_rows"`
}

func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		PlanFile:             strings.TrimSpace(os.Getenv("CUTOVER_AUDIT_PLAN_FILE")),
		ReportFiles:          splitCSV(os.Getenv("CUTOVER_AUDIT_REPORT_FILES")),
		ImportReportFiles:    splitCSV(os.Getenv("CUTOVER_AUDIT_IMPORT_REPORT_FILES")),
		PreflightReportFiles: splitCSV(os.Getenv("CUTOVER_AUDIT_PREFLIGHT_REPORT_FILES")),
		OutputFile:           strings.TrimSpace(os.Getenv("CUTOVER_AUDIT_OUTPUT_FILE")),
		Cryptos:              splitCSVUpper(os.Getenv("CUTOVER_AUDIT_CRYPTOS")),
		CoverageCryptos:      splitCSVUpper(os.Getenv("CUTOVER_AUDIT_COVERAGE_CRYPTOS")),
		ExpectedWorkers:      splitCSV(os.Getenv("CUTOVER_AUDIT_WORKERS")),
		RequireImportSync:    boolEnv("CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC", false),
		RequirePreflight:     boolEnv("CUTOVER_AUDIT_REQUIRE_PREFLIGHT", false),
		RequireMainStatus:    boolEnv("CUTOVER_AUDIT_REQUIRE_MAIN_STATUS", true),
		RequireOrderMatrix:   boolEnv("CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX", true),
		RequirePaymentOrder:  boolEnv("CUTOVER_AUDIT_REQUIRE_PAYMENT_ORDER", true),
		RequirePayoutOrder:   boolEnv("CUTOVER_AUDIT_REQUIRE_PAYOUT_ORDER", true),
		RequirePayoutTxID:    boolEnv("CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID", false),
		RequireWorkerReady:   boolEnv("CUTOVER_AUDIT_REQUIRE_WORKER_READY", true),
		RequireWorkerAddress: boolEnv("CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS", false),
	}
	if cfg.PlanFile != "" {
		if err := applyPlanFile(&cfg, cfg.PlanFile); err != nil {
			return cfg, err
		}
	}
	if len(cfg.ReportFiles) == 0 {
		return cfg, errors.New("CUTOVER_AUDIT_REPORT_FILES is required")
	}
	cfg.Cryptos = normalizeUpper(cfg.Cryptos)
	cfg.CoverageCryptos = normalizeUpper(cfg.CoverageCryptos)
	if len(cfg.CoverageCryptos) > 0 {
		cfg.Cryptos = append([]string(nil), cfg.CoverageCryptos...)
	}
	cfg.ExpectedWorkers = normalizeLower(cfg.ExpectedWorkers)
	return cfg, nil
}

func applyPlanFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read cutover audit plan %s: %w", path, err)
	}
	var plan planFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&plan); err != nil {
		return fmt.Errorf("decode cutover audit plan %s: %w", path, err)
	}
	if len(cfg.Cryptos) == 0 {
		if len(plan.CoverageCryptos) > 0 {
			cfg.Cryptos = normalizeUpper(plan.CoverageCryptos)
			cfg.CoverageCryptos = normalizeUpper(plan.CoverageCryptos)
		} else if len(plan.Cryptos) > 0 {
			cfg.Cryptos = normalizeUpper(plan.Cryptos)
		} else if strings.TrimSpace(plan.Crypto) != "" {
			cfg.Cryptos = normalizeUpper([]string{plan.Crypto})
		}
	}
	if len(cfg.ExpectedWorkers) == 0 {
		cfg.ExpectedWorkers = planWorkerNames(plan)
	}
	return nil
}

func Run(cfg Config) (Audit, error) {
	reports := make([]deploycheck.Report, 0, len(cfg.ReportFiles))
	reportStatuses := map[string]string{}
	for _, path := range cfg.ReportFiles {
		report, err := readReport(path)
		if err != nil {
			return Audit{}, err
		}
		reports = append(reports, report)
		reportStatuses[path] = report.Status
		if len(cfg.Cryptos) == 0 {
			if len(report.CoverageCryptos) > 0 {
				cfg.Cryptos = normalizeUpper(report.CoverageCryptos)
			} else if len(report.Cryptos) > 0 {
				cfg.Cryptos = normalizeUpper(report.Cryptos)
			}
		}
	}
	cfg.Cryptos = normalizeUpper(cfg.Cryptos)
	if len(cfg.Cryptos) == 0 {
		return Audit{}, errors.New("cutover audit requires cryptos from CUTOVER_AUDIT_CRYPTOS, plan cryptos, or deploy-check reports")
	}
	coverage := map[string]Coverage{}
	for _, crypto := range cfg.Cryptos {
		coverage[crypto] = Coverage{}
	}
	workers := map[string]bool{}
	workerAddresses := map[string]bool{}
	orderMatrix := false
	for _, worker := range cfg.ExpectedWorkers {
		workers[worker] = false
		workerAddresses[worker] = false
	}
	missing := make([]string, 0)
	importSync, importMissing := verifyImportReports(cfg.ImportReportFiles, cfg.RequireImportSync)
	missing = append(missing, importMissing...)
	preflight, preflightMissing := verifyPreflightReports(cfg.PreflightReportFiles, cfg.RequirePreflight)
	missing = append(missing, preflightMissing...)
	for i, report := range reports {
		path := cfg.ReportFiles[i]
		if report.Status != "ok" {
			if report.Error != "" {
				missing = append(missing, fmt.Sprintf("report %s status=%s error=%s", path, report.Status, report.Error))
			} else {
				missing = append(missing, fmt.Sprintf("report %s status=%s", path, report.Status))
			}
		}
		consumeReport(report, coverage, workers, workerAddresses, &orderMatrix)
	}
	if cfg.RequireOrderMatrix && !orderMatrix {
		missing = append(missing, "missing order_status_matrix")
	}
	for _, crypto := range cfg.Cryptos {
		row := coverage[crypto]
		if cfg.RequireMainStatus && !row.MainStatus {
			missing = append(missing, "crypto "+crypto+": missing main_status")
		}
		if cfg.RequireOrderMatrix && !row.OrderStatusMatrix {
			missing = append(missing, "crypto "+crypto+": missing order_status_matrix")
		}
		if cfg.RequirePaymentOrder && !row.PaymentOrder {
			missing = append(missing, "crypto "+crypto+": missing payment_order")
		}
		if cfg.RequirePayoutOrder && !row.PayoutOrder {
			missing = append(missing, "crypto "+crypto+": missing payout_order")
		}
		if cfg.RequirePayoutTxID && !row.PayoutTxID {
			missing = append(missing, "crypto "+crypto+": missing payout txid")
		}
	}
	if cfg.RequireWorkerReady {
		for _, worker := range cfg.ExpectedWorkers {
			if !workers[worker] {
				missing = append(missing, "worker "+worker+": missing readyz")
			}
		}
	}
	if cfg.RequireWorkerAddress {
		for _, worker := range cfg.ExpectedWorkers {
			if !workerAddresses[worker] {
				missing = append(missing, "worker "+worker+": missing worker_address")
			}
		}
	}
	status := "pass"
	if len(missing) > 0 {
		status = "fail"
	}
	audit := Audit{
		Status:           status,
		GeneratedAt:      time.Now(),
		PlanFile:         cfg.PlanFile,
		ReportFiles:      append([]string(nil), cfg.ReportFiles...),
		ImportReports:    append([]string(nil), cfg.ImportReportFiles...),
		PreflightReports: append([]string(nil), cfg.PreflightReportFiles...),
		RequiredCryptos:  append([]string(nil), cfg.Cryptos...),
		ExpectedWorkers:  append([]string(nil), cfg.ExpectedWorkers...),
		Gates: Gates{
			ImportSync:        cfg.RequireImportSync,
			Preflight:         cfg.RequirePreflight,
			MainStatus:        cfg.RequireMainStatus,
			OrderStatusMatrix: cfg.RequireOrderMatrix,
			PaymentOrder:      cfg.RequirePaymentOrder,
			PayoutOrder:       cfg.RequirePayoutOrder,
			PayoutTxID:        cfg.RequirePayoutTxID,
			WorkerReady:       cfg.RequireWorkerReady,
			WorkerAddress:     cfg.RequireWorkerAddress,
		},
		ImportSync:      importSync,
		Preflight:       preflight,
		OrderMatrix:     orderMatrix,
		Coverage:        coverage,
		Workers:         workers,
		WorkerAddresses: workerAddresses,
		ReportStatuses:  reportStatuses,
		Missing:         missing,
	}
	return audit, nil
}

func WriteAudit(path string, audit Audit) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create cutover audit dir %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cutover audit: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func readReport(path string) (deploycheck.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return deploycheck.Report{}, fmt.Errorf("read deploy-check report %s: %w", path, err)
	}
	var report deploycheck.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return deploycheck.Report{}, fmt.Errorf("decode deploy-check report %s: %w", path, err)
	}
	return report, nil
}

func verifyImportReports(paths []string, required bool) (bool, []string) {
	if len(paths) == 0 {
		if required {
			return false, []string{"missing import report"}
		}
		return false, nil
	}
	missing := make([]string, 0)
	ok := true
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			ok = false
			missing = append(missing, "import report "+path+": "+err.Error())
			continue
		}
		var report importReport
		if err := json.Unmarshal(data, &report); err != nil {
			ok = false
			missing = append(missing, "import report "+path+": "+err.Error())
			continue
		}
		if report.Mode != "mariadb-upsert" {
			ok = false
			missing = append(missing, "import report "+path+": mode is not mariadb-upsert")
		}
		if report.Rows <= 0 {
			ok = false
			missing = append(missing, "import report "+path+": imported rows is zero")
		}
		if report.OrderIndexRows <= 0 {
			ok = false
			missing = append(missing, "import report "+path+": order_index_rows is zero")
		}
	}
	return ok, missing
}

func verifyPreflightReports(paths []string, required bool) (bool, []string) {
	if len(paths) == 0 {
		if required {
			return false, []string{"missing preflight report"}
		}
		return false, nil
	}
	missing := make([]string, 0)
	ok := true
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			ok = false
			missing = append(missing, "preflight report "+path+": "+err.Error())
			continue
		}
		var report preflightReport
		if err := json.Unmarshal(data, &report); err != nil {
			ok = false
			missing = append(missing, "preflight report "+path+": "+err.Error())
			continue
		}
		if report.Status != "pass" {
			ok = false
			reason := strings.Join(report.Blockers, "; ")
			if reason == "" {
				reason = "status=" + report.Status
			}
			missing = append(missing, "preflight report "+path+": "+reason)
		}
		if !report.DatabaseURLConfigured {
			ok = false
			missing = append(missing, "preflight report "+path+": database URL was not configured")
		}
		if len(report.MissingTables) > 0 {
			ok = false
			missing = append(missing, "preflight report "+path+": missing tables "+strings.Join(report.MissingTables, ","))
		}
		if len(report.MissingIndexes) > 0 {
			ok = false
			missing = append(missing, "preflight report "+path+": missing indexes "+strings.Join(report.MissingIndexes, ","))
		}
		if report.EnabledWalletCount <= 0 {
			ok = false
			missing = append(missing, "preflight report "+path+": enabled_wallet_count is zero")
		}
	}
	return ok, missing
}

func consumeReport(report deploycheck.Report, coverage map[string]Coverage, workers map[string]bool, workerAddresses map[string]bool, orderMatrix *bool) {
	for _, check := range report.Checks {
		if check.Status != "ok" {
			continue
		}
		switch check.Name {
		case "main_status":
			markCoverage(coverage, check.Details["crypto"], func(row *Coverage) { row.MainStatus = true })
		case "order_status_matrix":
			if orderMatrix != nil {
				*orderMatrix = true
			}
			for _, crypto := range splitCSVUpper(check.Details["cryptos"]) {
				markCoverage(coverage, crypto, func(row *Coverage) { row.OrderStatusMatrix = true })
			}
		case "payment_order":
			markCoverage(coverage, check.Details["crypto"], func(row *Coverage) { row.PaymentOrder = true })
		case "payout_order":
			markCoverage(coverage, check.Details["crypto"], func(row *Coverage) {
				row.PayoutOrder = true
				if strings.TrimSpace(check.Details["txids"]) != "" {
					row.PayoutTxID = true
				}
			})
		case "payout_dispatch", "payout_status":
			markCoverage(coverage, check.Details["crypto"], func(row *Coverage) {
				if strings.TrimSpace(check.Details["txids"]) != "" {
					row.PayoutTxID = true
				}
			})
		case "worker_address":
			if name := workerNameFromAddressCheck(check); name != "" {
				workerAddresses[name] = true
			}
		default:
			if strings.HasPrefix(check.Name, "worker_readyz") {
				if name := workerNameFromCheck(check.Name); name != "" {
					workers[name] = true
				}
			}
		}
	}
}

func markCoverage(coverage map[string]Coverage, crypto string, mark func(*Coverage)) {
	crypto = strings.ToUpper(strings.TrimSpace(crypto))
	if crypto == "" {
		return
	}
	row := coverage[crypto]
	mark(&row)
	coverage[crypto] = row
}

func workerNameFromCheck(name string) string {
	if name == "worker_readyz" {
		return "worker"
	}
	_, after, ok := strings.Cut(name, "name=")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(after))
}

func workerNameFromAddressCheck(check deploycheck.ReportCheck) string {
	if value := strings.TrimSpace(check.Details["worker"]); value != "" {
		return strings.ToLower(value)
	}
	if value := strings.TrimSpace(check.Details["name"]); value != "" {
		return strings.ToLower(value)
	}
	return ""
}

func planWorkerNames(plan planFile) []string {
	names := make([]string, 0)
	if strings.TrimSpace(plan.WorkerURL) != "" {
		names = append(names, "worker")
	}
	raw := bytes.TrimSpace(plan.WorkerURLs)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return normalizeLower(names)
	}
	switch raw[0] {
	case '"':
		var value string
		if json.Unmarshal(raw, &value) == nil {
			for _, item := range splitCSV(value) {
				name := item
				if before, _, ok := strings.Cut(item, "="); ok {
					name = before
				}
				names = append(names, name)
			}
		}
	case '{':
		values := map[string]string{}
		if json.Unmarshal(raw, &values) == nil {
			for name, urlValue := range values {
				if strings.TrimSpace(urlValue) != "" {
					names = append(names, name)
				}
			}
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			for i, item := range items {
				item = bytes.TrimSpace(item)
				if len(item) == 0 || bytes.Equal(item, []byte("null")) {
					continue
				}
				if item[0] == '"' {
					var value string
					if json.Unmarshal(item, &value) == nil {
						parts := splitCSV(value)
						for _, part := range parts {
							name := part
							if before, _, ok := strings.Cut(part, "="); ok {
								name = before
							}
							names = append(names, name)
						}
					}
					continue
				}
				var named planNamedURL
				if json.Unmarshal(item, &named) == nil && strings.TrimSpace(named.URL) != "" {
					name := strings.TrimSpace(named.Name)
					if name == "" {
						name = fmt.Sprintf("worker%d", i+1)
					}
					names = append(names, name)
				}
			}
		}
	}
	return normalizeLower(names)
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitCSVUpper(value string) []string {
	return normalizeUpper(splitCSV(value))
}

func normalizeUpper(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeLower(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func boolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
